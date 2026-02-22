package search

// rerank_gate.go — adaptive LLM rerank strategy based on result spread.
//
// Instead of a binary should/shouldn't, this returns a RerankDecision
// with the reason and a TopK cap for cost control.

// Thresholds for rerank decisions.
const (
	rerankTopCosineThreshold = 0.93 // skip if top result is already high-confidence
	rerankMinResults         = 4    // skip if fewer than this many results
	rerankClusteredSpread    = 0.05 // spread below this → clustered, rerank all
	rerankWideSpread         = 0.25 // spread above this → clear separation, skip
	rerankTopKCap            = 8    // cap for medium-spread reranking
)

// RerankDecision holds the adaptive rerank strategy.
type RerankDecision struct {
	ShouldRerank bool   // whether to invoke LLM rerank
	Reason       string // "too-few" | "high-confidence" | "clustered" | "medium-spread" | "wide-spread"
	TopK         int    // how many items to send to LLM reranker (0 = all)
}

// rerankStrategy analyzes result scores and returns a rerank decision.
// Uses the spread between top and bottom relativity scores to determine strategy.
func rerankStrategy(items []map[string]any) RerankDecision {
	if len(items) < rerankMinResults {
		return RerankDecision{ShouldRerank: false, Reason: "too-few"}
	}

	topRel, botRel := extractRelativityRange(items)

	// High-confidence top result — cosine ordering is sufficient.
	if topRel > rerankTopCosineThreshold {
		return RerankDecision{ShouldRerank: false, Reason: "high-confidence"}
	}

	spread := topRel - botRel

	// Clustered: all scores are close together — LLM judgment needed for all.
	if spread < rerankClusteredSpread {
		return RerankDecision{ShouldRerank: true, Reason: "clustered", TopK: 0}
	}

	// Wide spread: clear separation — cosine ordering is reliable.
	if spread > rerankWideSpread {
		return RerankDecision{ShouldRerank: false, Reason: "wide-spread"}
	}

	// Medium spread: ambiguous — rerank but cap items for cost control.
	return RerankDecision{ShouldRerank: true, Reason: "medium-spread", TopK: rerankTopKCap}
}

// shouldLLMRerank is the backward-compatible wrapper for callers that
// only need a bool.
func shouldLLMRerank(items []map[string]any) bool {
	return rerankStrategy(items).ShouldRerank
}

// extractRelativityRange returns the top and bottom relativity scores from items.
// Returns (0, 0) if metadata is missing.
func extractRelativityRange(items []map[string]any) (top, bottom float64) {
	if len(items) == 0 {
		return 0, 0
	}
	top = extractRelativity(items[0])
	bottom = extractRelativity(items[len(items)-1])
	return top, bottom
}

// extractRelativity extracts the relativity score from an item's metadata.
func extractRelativity(item map[string]any) float64 {
	meta, ok := item["metadata"].(map[string]any)
	if !ok {
		return 0
	}
	rel, ok := meta["relativity"].(float64)
	if !ok {
		return 0
	}
	return rel
}
