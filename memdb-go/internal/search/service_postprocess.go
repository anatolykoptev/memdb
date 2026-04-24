// Package search — service_postprocess.go: rerank / filter / dedup pipeline
// (steps 6–11) plus iterative expansion, temporal decay, relativity.
package search

import (
	"context"
	"log/slog"
	"slices"
	"time"
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
	// Step 6: Cosine rerank
	text = ReRankByCosine(queryVec, text, textEmbByID)
	skill = ReRankByCosine(queryVec, skill, skillEmbByID)
	tool = ReRankByCosine(queryVec, tool, toolEmbByID)

	// Step 6.05: Cross-encoder rerank (best-effort, runs before LLM rerank).
	// Only applied to text_mem — skill/tool/pref are too low-value to warrant
	// the extra HTTP round-trip. Zero-value config.URL disables.
	if s.RerankClient.Available() && len(text) > 1 {
		t0 := time.Now()
		text = rerankMemoryItems(ctx, s.RerankClient, p.Query, text)
		ceRerankDur = time.Since(t0)
	}

	// Step 6.1: LLM rerank of text_mem (adaptive strategy)
	if decision := rerankStrategy(text); p.LLMRerank && s.LLMReranker.APIURL != "" && decision.ShouldRerank {
		t0 := time.Now()
		rerankInput := text
		if decision.TopK > 0 && decision.TopK < len(text) {
			rerankInput = text[:decision.TopK]
		}
		reranked := LLMRerank(ctx, p.Query, rerankInput, s.LLMReranker)
		if decision.TopK > 0 && decision.TopK < len(text) {
			text = append(reranked, text[decision.TopK:]...)
		} else {
			text = reranked
		}
		llmRerankDur = time.Since(t0)
	}

	// Step 6.2: Iterative multi-stage retrieval expansion
	t0 := time.Now()
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
func (s *SearchService) applyTemporalDecay(text, skill, tool []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any) {
	decayAlpha := p.DecayAlpha
	if decayAlpha == 0 {
		decayAlpha = DefaultDecayAlpha
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
