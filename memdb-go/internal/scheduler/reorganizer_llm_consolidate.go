package scheduler

// reorganizer_llm_consolidate.go — LLM call and JSON result type for cluster consolidation.

import (
	"context"
	"encoding/json"
	"fmt"
)

// consolidationResult is the JSON structure returned by the LLM.
// ContradictedIDs contains memories directly contradicted by the winner — they are
// hard-deleted (not soft-merged) so they never re-surface in search results.
type consolidationResult struct {
	KeepID          string   `json:"keep_id"`
	RemoveIDs       []string `json:"remove_ids"`
	ContradictedIDs []string `json:"contradicted_ids,omitempty"`
	MergedText      string   `json:"merged_text"`
}

// llmConsolidate calls the LLM with the cluster members and returns a parsed result.
func (r *Reorganizer) llmConsolidate(ctx context.Context, cluster []memNode) (consolidationResult, error) {
	type inputItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	items := make([]inputItem, len(cluster))
	for i, n := range cluster {
		items[i] = inputItem{ID: n.ID, Text: n.Text}
	}
	memoriesJSON, _ := json.Marshal(items)

	msgs := []map[string]string{
		{"role": "system", "content": consolidationSystemPrompt},
		{"role": "user", "content": fmt.Sprintf("Memory cluster to consolidate:\n%s", memoriesJSON)},
	}

	// 2-node clusters need only a short JSON response — cap tokens to reduce cost.
	maxTok := consolidateLLMMaxTokens
	if len(cluster) == 2 {
		maxTok = consolidateLLMMaxTokensPair
	}

	callCtx, cancel := context.WithTimeout(ctx, reorganizerLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, msgs, maxTok)
	if err != nil {
		return consolidationResult{}, err
	}

	raw = stripFences(raw)
	var result consolidationResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return consolidationResult{}, fmt.Errorf("parse llm json (%s): %w", truncate(raw, consolidateErrTruncLen), err)
	}
	return result, nil
}
