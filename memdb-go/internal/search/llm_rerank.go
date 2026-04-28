package search

// llm_rerank.go — backward-compatible wrappers around the rerank.LLMJudge
// strategy. The implementation moved to internal/search/rerank/llm.go in
// M11/R3. Public callers (tests, profiles.go::SearchParams.LLMRerank flag)
// keep working unchanged.

import (
	"context"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
)

// LLMRerankConfig holds the parameters for the LLM reranker. Mirrors
// rerank.LLMConfig field-for-field — declared locally so SearchService
// and profiles.go don't need to import the rerank subpackage.
type LLMRerankConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// LLMRerank re-scores top-K candidates via an LLM and re-sorts. Thin
// wrapper around rerank.LLMJudge; preserves the original signature so
// existing tests in llm_rerank_test.go pass UNCHANGED.
func LLMRerank(ctx context.Context, query string, items []map[string]any, cfg LLMRerankConfig) []map[string]any {
	if len(items) == 0 {
		return items
	}
	adapted := adaptItems(items)
	out, _ := rerank.LLMJudge{
		Config: rerank.LLMConfig{APIURL: cfg.APIURL, APIKey: cfg.APIKey, Model: cfg.Model},
		Cap:    0, // legacy entrypoint passes the whole slice; cap handled by maxCandidates inside.
	}.Rerank(ctx, query, adapted)
	return unadaptItems(out)
}
