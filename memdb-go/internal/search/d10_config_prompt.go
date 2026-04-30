package search

// d10_config_prompt.go — D10Config.PromptForQuery routing logic. Lives in
// its own file so the env-loader concerns (d10_config.go) and the prompt
// routing concerns (this file) can evolve independently.

import "context"

// PromptForQuery returns the system prompt the extractor should send to the
// LLM, plus a `hinted` flag for metric labelling. The flag is only ever true
// in PromptModeProbabilistic when the classifier supplied a usable hint.
//
// Strict and Soft modes deliberately ignore `emb` so they incur ZERO embed
// cost — operators flipping to MEMDB_D10_PROMPT_MODE=strict for an A/B run
// see strictly fewer requests to embed-server.
func (c D10Config) PromptForQuery(ctx context.Context, query string, emb classifierEmbedder) (string, bool) {
	switch c.Mode {
	case PromptModeStrict:
		return answerEnhancePromptStrict, false
	case PromptModeSoft:
		return answerEnhancePromptSoft, false
	default:
		// PromptModeProbabilistic and any unknown value (defensive — the
		// loader normalises Mode but a future code path could mis-set it).
		// Reuse the existing classifier-driven assembler so the
		// behaviour is byte-identical to PR #252.
		return c.probabilisticPromptForQuery(ctx, query, emb)
	}
}

// probabilisticPromptForQuery wraps buildAnswerEnhanceSystemPrompt with the
// classifier-threshold value from this config snapshot. Suppresses the hint
// when the embedder is nil OR the classifier reports below the snapshot
// threshold OR top-1 is open_domain.
func (c D10Config) probabilisticPromptForQuery(ctx context.Context, query string, emb classifierEmbedder) (string, bool) {
	if emb == nil || !d10ClassifierEnabled() {
		return answerEnhancePromptProbabilistic, false
	}
	classifier := classifierForEmbedder(emb)
	top, err := classifier.ClassifyTopN(ctx, query, 2)
	if err != nil || len(top) == 0 {
		return answerEnhancePromptProbabilistic, false
	}
	hint := categoryHintBlock(top, c.ClassifierThreshold)
	if hint == "" {
		return answerEnhancePromptProbabilistic, false
	}
	return answerEnhancePromptProbabilistic + hint, true
}
