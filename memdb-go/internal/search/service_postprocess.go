// Package search — service_postprocess.go: rerank / filter / dedup pipeline
// (steps 6–11) plus iterative expansion, temporal decay, relativity.
package search

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"time"

	rerankpkg "github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
)

// postProcessResults runs steps 6–11 of the search pipeline:
// cosine rerank → cross-encoder rerank → LLM rerank (opt-in) → iterative expansion →
// temporal decay → relativity threshold → pref quality filter → dedup →
// cross-source dedup → trim.
// Returns the processed slices plus timing durations for ce_rerank, llm_rerank
// and iterative steps.
func (s *SearchService) postProcessResults(
	ctx context.Context,
	queryVec []float32,
	textEmbByID, skillEmbByID, toolEmbByID map[string][]float32,
	text, skill, tool, pref []map[string]any,
	p SearchParams,
) (retText, retSkill, retTool, retPref []map[string]any, llmRerankDur, iterativeDur, ceRerankDur time.Duration) {
	// Steps 6 → 6.15: rerank pipeline, split in two phases.
	//
	// Phase 1 — prefix chain [Cosine, CrossEncoder?]: rescore items into a
	// stable post-cosine/CE distribution. This is the input the rerank
	// gate (rerankStrategy) MUST evaluate against — pre-R3 main called the
	// gate after cosine + CE, and the thresholds (defaultRerankTopCosineThreshold
	// = 0.85, defaultRerankClusteredSpread = 0.10, defaultRerankWideSpread = 0.30,
	// all env-overridable as of M12.7) are tuned for the post-rescore score
	// distribution, not the raw PolarDB-compressed band.
	//
	// Phase 2 — suffix chain [LLMJudge?, Staged]: gated rerankers that act
	// on the rescored items. LLMJudge inclusion is decided by the gate; its
	// Cap (TopK) comes from the same decision. Staged self-skips when its
	// env flag is unset.
	//
	// skill and tool stay cosine-only — CE / LLM / staged are text-only by
	// design (skill/tool are too low-value to warrant the extra cost).
	prefix := buildTextRerankPrefix(s, queryVec, textEmbByID, len(text), p)
	prefixResult := runRerankChainWithTimings(ctx, prefix, p.Query, text)
	text = prefixResult.items

	// Gate evaluates POST-cosine/CE scores — see comment above.
	llmDecision := rerankStrategy(ctx, text)
	llmEnabled := p.LLMRerank && s.LLMReranker.APIURL != "" && llmDecision.ShouldRerank

	suffix := buildTextRerankSuffix(s, llmEnabled, llmDecision.TopK, textEmbByID)
	suffixResult := runRerankChainWithTimings(ctx, suffix, p.Query, text)
	text = suffixResult.items

	// Per-strategy durations from both phases — strategy names are stable
	// constants matching the rerank package (cosine, cross_encoder,
	// llm_judge, staged). Missing entries (strategy not in chain) default
	// to zero. Operators reading the slog "search pipeline timing" line
	// see CE cost (prefix) separated from LLM cost (suffix) — no more
	// double-counting against chainDur as in the pre-fix R3 path.
	if d, ok := prefixResult.durations["cross_encoder"]; ok {
		ceRerankDur = d
	}
	if d, ok := suffixResult.durations["llm_judge"]; ok {
		llmRerankDur = d
	}

	// Forensic 2026-05-01: per-strategy rerank histograms. Emit every
	// strategy that fired in either chain (cosine always; cross_encoder /
	// llm_judge / mmr / staged conditionally). Skips strategies that
	// weren't in the chain — operators count series via _count.
	for strat, d := range prefixResult.durations {
		recordRerankDuration(ctx, strat, d.Seconds())
	}
	for strat, d := range suffixResult.durations {
		recordRerankDuration(ctx, strat, d.Seconds())
	}

	// Skill / tool: cosine-only.
	skill = ReRankByCosine(queryVec, skill, skillEmbByID)
	tool = ReRankByCosine(queryVec, tool, toolEmbByID)

	// Step 6.2: Iterative multi-stage retrieval expansion
	t0 := time.Now()
	text = s.runIterativeExpansion(ctx, queryVec, text, p)
	iterativeDur = time.Since(t0)

	// Step 6.5: Temporal decay
	text, skill, tool = s.applyTemporalDecay(text, skill, tool, p)

	// Step 7: Relativity threshold (forensic 2026-05-01: count drops).
	preThr := len(text) + len(skill) + len(tool) + len(pref)
	text, skill, tool, pref = s.applyRelativity(text, skill, tool, pref, p)
	postThr := len(text) + len(skill) + len(tool) + len(pref)
	recordDedupDrop(ctx, "threshold_filter", preThr-postThr)

	// Step 8: Pref quality filter
	pref = FilterPrefByQuality(pref)

	// Step 9: Dedup per type (forensic 2026-05-01: count drops by mode).
	text, skill, tool, pref = s.dedupResults(ctx, queryVec, textEmbByID, text, skill, tool, pref, p)

	// Step 10: Cross-source dedup (forensic 2026-05-01: count drops).
	preX := len(skill) + len(tool) + len(pref)
	skill, tool, pref = CrossSourceDedupByText(text, skill, tool, pref)
	postX := len(skill) + len(tool) + len(pref)
	recordDedupDrop(ctx, "cross_source", preX-postX)

	// Step 10.5: D10 post-retrieval answer enhancement (env-gated by
	// MEMDB_SEARCH_ENHANCE=true; default off). Runs after dedup but
	// before trim so we synthesise on the actual top candidates.
	// Reuses LLMReranker proxy credentials.
	text = applyAnswerEnhancement(ctx, s.logger, p.Query, text, AnswerEnhanceConfig{
		APIURL: s.LLMReranker.APIURL,
		APIKey: s.LLMReranker.APIKey,
		Model:  s.LLMReranker.Model,
		Locale: p.Locale,
	}, s.embedder)

	// Step 11: Trim each type to its budget
	text = TrimSlice(text, p.TopK)
	skill = TrimSlice(skill, p.SkillTopK)
	tool = TrimSlice(tool, p.ToolTopK)
	pref = TrimSlice(pref, p.PrefTopK)

	StripEmbeddings(text)
	StripEmbeddings(skill)
	StripEmbeddings(tool)
	StripEmbeddings(pref)

	return text, skill, tool, pref, llmRerankDur, iterativeDur, ceRerankDur
}

// buildTextRerankPrefix composes the pre-gate rerank chain applied to
// text_mem: [Cosine, CrossEncoder?]. Cosine always runs (cheap,
// in-process). CrossEncoder is included only when a non-nil RerankClient
// is configured AND len(text) > 1 (parity with the legacy postprocess
// guard — single-item batches don't benefit from CE rerank).
//
// The prefix output feeds rerankStrategy() so the LLM-judge gate sees
// post-cosine/CE scores — same ordering as pre-R3 main.
//
// Q3: CrossEncoder is constructed with QueryMeta fields so the metadata
// boost post-step (applyMetadataBoost inside CE.Rerank) can match items
// by user_id / session_id / tags. SearchParams.UserName serves as the
// user_id key (it is the cube/user identifier used throughout the stack).
// AgentID maps to session_id for session-scoped boosts.
func buildTextRerankPrefix(s *SearchService, queryVec []float32, embByID map[string][]float32, textLen int, p SearchParams) rerankpkg.Chain {
	chain := rerankpkg.Chain{
		rerankpkg.Cosine{QueryVec: queryVec, EmbeddingsByID: embByID},
	}
	// M14 A1: Stage-0 pre-CE diversity prefilter. Runs after cosine sort but
	// before CrossEncoder so CE receives a diversity-aware top-K instead of
	// plain cosine order. Disabled by default (MEMDB_RERANK_MATH_PREFILTER=0).
	if mpf, ok := rerankpkg.NewMathPrefilterFromEnv(embByID, queryVec); ok {
		chain = append(chain, mpf)
	}
	if s.RerankClient.Available() && textLen > 1 {
		chain = append(chain, rerankpkg.CrossEncoder{
			Client:         s.RerankClient,
			OnLiveCall:     ceLiveHook,
			OnPrecompute:   cePrecomputeHook,
			OnMathFallback: ceMathFallbackHook,
			QueryUserID:    p.UserName,
			QuerySessionID: p.AgentID,
			QueryTags:      p.Tags,
			BoostWeights:   rerankpkg.DefaultBoostWeights(),
			// Math-fallback inputs (go-search PRs #10 + #14 pattern).
			// embByID holds the same vectors the cosine and MathPrefilter
			// stages already computed; reusing them adds zero embed-server
			// load and gives the fallback path real cosine signal.
			QueryVec:       queryVec,
			EmbeddingsByID: embByID,
		})
	}
	return chain
}

// buildTextRerankSuffix composes the post-gate rerank chain applied to
// text_mem: [LLMJudge?, MMR, Staged]. LLMJudge is included only when the
// rerank gate (rerankStrategy on prefix output) said ShouldRerank.
// MMR diversification runs after CE+LLMJudge and before the final Staged
// cut — filters near-duplicate memories (cosine ≥ MEMDB_MMR_BAR, default 0.8)
// so the user sees diverse results even when the corpus has many paraphrases.
// Staged is always included; the strategy self-skips when MEMDB_SEARCH_STAGED
// is unset.
//
// Adding a new reranker is strictly additive: write a Reranker, append
// here, never touch postProcessResults again.
func buildTextRerankSuffix(s *SearchService, llmEnabled bool, llmCap int, embByID map[string][]float32) rerankpkg.Chain {
	chain := rerankpkg.Chain{}
	if llmEnabled {
		chain = append(chain, rerankpkg.LLMJudge{
			Config: rerankpkg.LLMConfig{
				APIURL: s.LLMReranker.APIURL,
				APIKey: s.LLMReranker.APIKey,
				Model:  s.LLMReranker.Model,
			},
			Cap: llmCap,
		})
	}
	chain = append(chain, rerankpkg.NewMMRFromEnv(embByID))
	chain = append(chain, rerankpkg.Staged{
		Config: rerankpkg.LLMConfig{
			APIURL: s.LLMReranker.APIURL,
			APIKey: s.LLMReranker.APIKey,
			Model:  s.LLMReranker.Model,
		},
		Logger:        s.logger,
		ShortlistSize: stagedShortlistSize(),
		MaxInputSize:  stagedMaxInputSize(),
		OnStage:       stagedStageHook,
		OnJustified:   stagedJustifiedHook,
		// M12.6: thread the cross-encoder client through so the
		// default `ce` backend can replace the two LLM calls per
		// query. Backend selection is env-driven (MEMDB_STAGED_BACKEND);
		// when CE is requested but the client is unavailable the
		// strategy logs a WARN and falls back to LLMBackend (NOT
		// silent — see staged.go::Rerank).
		RerankClient: s.RerankClient,
	})
	return chain
}

// rerankPhaseResult captures the unadapted slice + per-strategy timings
// for one chain invocation. Used by postProcessResults to populate
// ceRerankDur / llmRerankDur from the appropriate phase.
type rerankPhaseResult struct {
	items     []map[string]any
	durations map[string]time.Duration
}

// runRerankChainWithTimings wraps the chain call in the slice adaptation
// and propagates per-strategy timings. Returned slice shares maps with
// the input — strategies mutate metadata in place, the slice ordering
// reflects the chain's final output. Empty input short-circuits with a
// zero-duration map.
func runRerankChainWithTimings(ctx context.Context, chain rerankpkg.Chain, query string, items []map[string]any) rerankPhaseResult {
	if len(items) == 0 {
		return rerankPhaseResult{items: items, durations: map[string]time.Duration{}}
	}
	adapted := adaptItems(items)
	res, _ := chain.RerankWithTimings(ctx, query, adapted)
	if res == nil {
		return rerankPhaseResult{items: items, durations: map[string]time.Duration{}}
	}
	return rerankPhaseResult{items: unadaptItems(res.Items), durations: res.Durations}
}

// runIterativeExpansion applies iterative multi-stage retrieval if configured.
func (s *SearchService) runIterativeExpansion(ctx context.Context, queryVec []float32, textFormatted []map[string]any, p SearchParams) []map[string]any {
	numStages := p.NumStages
	if numStages <= 0 || s.Iterative.APIURL == "" || s.embedder == nil || s.postgres == nil {
		return textFormatted
	}
	embedFn := func(subCtx context.Context, subQuery string) ([]map[string]any, error) {
		vecs, err := s.embedder.Embed(subCtx, []string{subQuery})
		if err != nil || len(vecs) == 0 {
			return nil, err
		}
		subVec := vecs[0]
		results, err := s.postgres.VectorSearch(subCtx, subVec, p.CubeID, p.UserName, TextScopes, p.AgentID, p.TopK*InflateFactor)
		if err != nil {
			return nil, err
		}
		merged := MergeVectorAndFulltext(results, nil)
		formatted, embByID := FormatMergedItems(merged, true)
		formatted = ReRankByCosine(subVec, formatted, embByID)
		return formatted, nil
	}
	extCfg := s.Iterative
	extCfg.NumStages = numStages
	result := IterativeExpand(ctx, p.Query, textFormatted, embedFn, extCfg)
	s.logger.Debug("iterative expansion complete",
		slog.String("query", p.Query),
		slog.Int("total_items", len(result)),
	)
	return result
}

// applyTemporalDecay applies temporal decay to all formatted result slices.
//
// Alpha selection order:
//  1. Explicit p.DecayAlpha (>0) — overrides everything (per-request tuning).
//  2. MEMDB_D1_HALF_LIFE_DAYS env set → alpha = ln(2)/halfLife.
//  3. DefaultDecayAlpha (0.0039, ~180d half-life).
//
// p.DecayAlpha = -1 disables decay entirely (profile opt-out).
func (s *SearchService) applyTemporalDecay(text, skill, tool []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any) {
	decayAlpha := p.DecayAlpha
	if decayAlpha == 0 {
		if os.Getenv("MEMDB_D1_HALF_LIFE_DAYS") != "" {
			decayAlpha = d1DecayAlpha()
		} else {
			decayAlpha = DefaultDecayAlpha
		}
	}
	if decayAlpha <= 0 {
		return text, skill, tool
	}
	now := time.Now()
	return ApplyTemporalDecay(text, now, decayAlpha),
		ApplyTemporalDecay(skill, now, decayAlpha),
		ApplyTemporalDecay(tool, now, decayAlpha)
}

// applyRelativity filters all result slices by the relativity threshold.
func (s *SearchService) applyRelativity(text, skill, tool, pref []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any, []map[string]any) {
	if p.Relativity <= 0 {
		return text, skill, tool, pref
	}
	text = FilterByRelativity(text, p.Relativity)
	skill = FilterByRelativity(skill, p.Relativity)
	tool = FilterByRelativity(tool, p.Relativity)
	// M15: pref threshold offset env-tunable via
	// MEMDB_M15_PREF_THRESHOLD_OFFSET (default 0.10).
	prefThreshold := p.Relativity - prefThresholdOffset()
	if prefThreshold > 0 {
		pref = FilterByRelativity(pref, prefThreshold)
	}
	return text, skill, tool, pref
}

// dedupResults applies the requested dedup strategy to all result slices.
//
// Forensic 2026-05-01: emits memdb_search_dedup_drop_total counters per
// reason. sim_threshold counts items dropped by DedupSim (cosine guard);
// mmr_high_sim counts items dropped by DedupMMR (combined relevance +
// diversity prune); exact_text counts items dropped by DedupByText. ctx
// is threaded through so the OTel observation lands in the request span.
func (s *SearchService) dedupResults(ctx context.Context, queryVec []float32, textEmbByID map[string][]float32, text, skill, tool, pref []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any, []map[string]any) {
	switch p.Dedup {
	case DedupModeSim:
		textItems := ToSearchItems(text, textEmbByID, "text")
		preCount := len(textItems)
		textItems = DedupSim(textItems, p.TopK)
		recordDedupDrop(ctx, "sim_threshold", preCount-len(textItems))
		text = FromSearchItems(textItems)
		skill = dedupByTextWithMetric(ctx, skill)
		tool = dedupByTextWithMetric(ctx, tool)
		pref = dedupByTextWithMetric(ctx, pref)

	case DedupModeMMR:
		textItems := ToSearchItems(text, textEmbByID, "text")
		prefItems := ToSearchItems(pref, nil, "preference")
		combined := slices.Concat(textItems, prefItems)
		if len(combined) > 0 {
			mmrLambda := p.MMRLambda
			if mmrLambda <= 0 || mmrLambda > 1 {
				mmrLambda = DefaultMMRLambda
			}
			preCount := len(combined)
			dedupedText, dedupedPref := DedupMMR(combined, p.TopK, p.PrefTopK, queryVec, mmrLambda)
			recordDedupDrop(ctx, "mmr_high_sim", preCount-(len(dedupedText)+len(dedupedPref)))
			text = FromSearchItems(dedupedText)
			pref = FromSearchItems(dedupedPref)
		}
		skill = dedupByTextWithMetric(ctx, skill)
		tool = dedupByTextWithMetric(ctx, tool)

	default:
		text = dedupByTextWithMetric(ctx, text)
		skill = dedupByTextWithMetric(ctx, skill)
		tool = dedupByTextWithMetric(ctx, tool)
		pref = dedupByTextWithMetric(ctx, pref)
	}
	return text, skill, tool, pref
}

// dedupByTextWithMetric wraps DedupByText with a drop counter. Helper
// signatures stay pure — the metric is captured at the only call site
// inside the search package.
func dedupByTextWithMetric(ctx context.Context, items []map[string]any) []map[string]any {
	pre := len(items)
	out := DedupByText(items)
	recordDedupDrop(ctx, "exact_text", pre-len(out))
	return out
}
