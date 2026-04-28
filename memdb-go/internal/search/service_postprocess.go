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
	// Steps 6 → 6.15: rerank pipeline.
	//
	// All four rerank strategies (cosine + cross-encoder lookup/live + LLM
	// judge + D5 staged) are composed into a single rerank.Chain on text.
	// skill and tool get cosine only — CE / LLM / staged are text-only by
	// design (skill/tool are too low-value to warrant the extra cost).
	//
	// LLM-judge gating: rerankStrategy() decides whether to run AND the
	// per-strategy Cap. The strategy is included in the chain only when
	// the gate says ShouldRerank — preserves rerank_gate_test semantics.
	llmDecision := rerankStrategy(text)
	llmEnabled := p.LLMRerank && s.LLMReranker.APIURL != "" && llmDecision.ShouldRerank

	chain := buildTextRerankChain(s, queryVec, textEmbByID, len(text), llmEnabled, llmDecision.TopK)
	t0 := time.Now()
	text = runRerankChain(ctx, chain, p.Query, text)
	// Approximate duration split: CE + LLM rerank durations are no longer
	// individually broken out by the chain. Pipeline totals these together
	// under the iterative timing block downstream. We keep the legacy
	// return values populated so the SearchService.Search log line still
	// builds — ceRerankDur captures the full chain duration when a CE
	// client is available, llmRerankDur captures the same when LLM judge
	// fired. This is parity with the old timing in the common path
	// (CE+LLM run sequentially anyway).
	chainDur := time.Since(t0)
	if s.RerankClient.Available() && len(text) > 1 {
		ceRerankDur = chainDur
	}
	if llmEnabled {
		llmRerankDur = chainDur
	}

	// Skill / tool: cosine-only.
	skill = ReRankByCosine(queryVec, skill, skillEmbByID)
	tool = ReRankByCosine(queryVec, tool, toolEmbByID)

	// Step 6.2: Iterative multi-stage retrieval expansion
	t0 = time.Now()
	text = s.runIterativeExpansion(ctx, queryVec, text, p)
	iterativeDur = time.Since(t0)

	// Step 6.5: Temporal decay
	text, skill, tool = s.applyTemporalDecay(text, skill, tool, p)

	// Step 7: Relativity threshold
	text, skill, tool, pref = s.applyRelativity(text, skill, tool, pref, p)

	// Step 8: Pref quality filter
	pref = FilterPrefByQuality(pref)

	// Step 9: Dedup per type
	text, skill, tool, pref = s.dedupResults(queryVec, textEmbByID, text, skill, tool, pref, p)

	// Step 10: Cross-source dedup
	skill, tool, pref = CrossSourceDedupByText(text, skill, tool, pref)

	// Step 10.5: D10 post-retrieval answer enhancement (env-gated by
	// MEMDB_SEARCH_ENHANCE=true; default off). Runs after dedup but
	// before trim so we synthesise on the actual top candidates.
	// Reuses LLMReranker proxy credentials.
	text = applyAnswerEnhancement(ctx, s.logger, p.Query, text, AnswerEnhanceConfig{
		APIURL: s.LLMReranker.APIURL,
		APIKey: s.LLMReranker.APIKey,
		Model:  s.LLMReranker.Model,
	})

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

// buildTextRerankChain composes the rerank.Chain applied to text_mem.
// Cosine always runs (cheap, in-process). CrossEncoder is included only
// when a non-nil RerankClient is configured AND len(text) > 1 (parity
// with the legacy postprocess guard — single-item batches don't benefit
// from CE rerank). LLMJudge is included only when the rerank gate
// (rerankStrategy) said ShouldRerank — keeps the existing gate
// semantics. Staged is included always; the strategy self-skips when
// MEMDB_SEARCH_STAGED is unset.
//
// Adding a new reranker is strictly additive: write a Reranker, append
// here, never touch postProcessResults again.
func buildTextRerankChain(s *SearchService, queryVec []float32, embByID map[string][]float32, textLen int, llmEnabled bool, llmCap int) rerankpkg.Chain {
	chain := rerankpkg.Chain{
		rerankpkg.Cosine{QueryVec: queryVec, EmbeddingsByID: embByID},
	}
	if s.RerankClient.Available() && textLen > 1 {
		chain = append(chain, rerankpkg.CrossEncoder{
			Client:       s.RerankClient,
			OnLiveCall:   ceLiveHook,
			OnPrecompute: cePrecomputeHook,
		})
	}
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
	})
	return chain
}

// runRerankChain wraps the chain call in the slice adaptation. Returned
// slice shares maps with the input — strategies mutate metadata in place,
// the slice ordering reflects the chain's final output.
func runRerankChain(ctx context.Context, chain rerankpkg.Chain, query string, items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return items
	}
	adapted := adaptItems(items)
	out, _ := chain.Rerank(ctx, query, adapted)
	return unadaptItems(out)
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
	prefThreshold := p.Relativity - 0.10
	if prefThreshold > 0 {
		pref = FilterByRelativity(pref, prefThreshold)
	}
	return text, skill, tool, pref
}

// dedupResults applies the requested dedup strategy to all result slices.
func (s *SearchService) dedupResults(queryVec []float32, textEmbByID map[string][]float32, text, skill, tool, pref []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any, []map[string]any) {
	switch p.Dedup {
	case DedupModeSim:
		textItems := ToSearchItems(text, textEmbByID, "text")
		textItems = DedupSim(textItems, p.TopK)
		text = FromSearchItems(textItems)
		skill = DedupByText(skill)
		tool = DedupByText(tool)
		pref = DedupByText(pref)

	case DedupModeMMR:
		textItems := ToSearchItems(text, textEmbByID, "text")
		prefItems := ToSearchItems(pref, nil, "preference")
		combined := slices.Concat(textItems, prefItems)
		if len(combined) > 0 {
			mmrLambda := p.MMRLambda
			if mmrLambda <= 0 || mmrLambda > 1 {
				mmrLambda = DefaultMMRLambda
			}
			dedupedText, dedupedPref := DedupMMR(combined, p.TopK, p.PrefTopK, queryVec, mmrLambda)
			text = FromSearchItems(dedupedText)
			pref = FromSearchItems(dedupedPref)
		}
		skill = DedupByText(skill)
		tool = DedupByText(tool)

	default:
		text = DedupByText(text)
		skill = DedupByText(skill)
		tool = DedupByText(tool)
		pref = DedupByText(pref)
	}
	return text, skill, tool, pref
}
