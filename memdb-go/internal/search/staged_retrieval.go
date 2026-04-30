package search

// staged_retrieval.go — backward-compatible wrappers around the
// rerank.Staged strategy. Implementation moved to
// internal/search/rerank/staged.go in M11/R3. The legacy entrypoint
// RunStagedRetrieval keeps its signature so staged_retrieval_test.go
// passes UNCHANGED.

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	rerankpkg "github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
)

// stagedMinInputSize re-exported for tests (staged_retrieval_test.go
// references it for the BelowMinSize case). Keep in sync with the
// rerank package internal constant.
const stagedMinInputSize = 5

// StagedRetrievalConfig carries the LLM endpoint credentials. Mirrors
// rerank.LLMConfig field-for-field; declared here so SearchService
// callers don't need to import the rerank subpackage.
type StagedRetrievalConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// stagedRetrievalEnabled reports whether the D5 staged-retrieval step
// runs. Env-gated by MEMDB_SEARCH_STAGED=true (default off).
func stagedRetrievalEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_STAGED") == "true"
}

// RunStagedRetrieval executes the D5 two-stage refine→justify pass.
// Returns items unchanged on any LLM error or env-disabled.
//
// Thin wrapper around rerank.Staged — see internal/search/rerank/staged.go
// for the algorithm. Hooks fire the existing memdb.search.d5_staged and
// memdb.search.d5_justified counters so dashboards stay green.
//
// Fast-path skip via stagedRetrievalEnabled — avoids the adaptItems /
// rerank.Staged construction churn when the env gate is off (which is the
// default). Without this, callers paid for a full no-op trip through
// rerankpkg.Staged on every search.
func RunStagedRetrieval(ctx context.Context, logger *slog.Logger, query string, items []map[string]any, cfg StagedRetrievalConfig) []map[string]any {
	if items == nil {
		return nil
	}
	if len(items) == 0 {
		return items
	}
	if !stagedRetrievalEnabled() {
		return items
	}
	adapted := adaptItems(items)
	out, _ := rerankpkg.Staged{
		Config:        rerankpkg.LLMConfig{APIURL: cfg.APIURL, APIKey: cfg.APIKey, Model: cfg.Model},
		Logger:        logger,
		ShortlistSize: stagedShortlistSize(),
		MaxInputSize:  stagedMaxInputSize(),
		OnStage:       stagedStageHook,
		OnJustified:   stagedJustifiedHook,
		OnStageSize:   stagedStageSizeHook,
	}.Rerank(ctx, query, adapted)
	return unadaptItems(out)
}

// stagedStageHook bumps the legacy memdb.search.d5_staged counter.
func stagedStageHook(ctx context.Context, stage, outcome string) {
	mx := searchMx()
	if mx == nil || mx.D5Staged == nil {
		return
	}
	mx.D5Staged.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", stage),
		attribute.String("outcome", outcome),
	))
}

// stagedJustifiedHook bumps the legacy memdb.search.d5_justified counter.
func stagedJustifiedHook(ctx context.Context, relevance string) {
	mx := searchMx()
	if mx == nil || mx.D5Justified == nil {
		return
	}
	mx.D5Justified.Add(ctx, 1, metric.WithAttributes(attribute.String("relevance", relevance)))
}

// stagedStageSizeHook records the candidate count entering each staged-retrieval stage.
func stagedStageSizeHook(ctx context.Context, stage string, size int) {
	mx := searchMx()
	if mx == nil || mx.D5StageInputSize == nil {
		return
	}
	mx.D5StageInputSize.Record(ctx, int64(size), metric.WithAttributes(attribute.String("stage", stage)))
}

// reorderByIDs places items with IDs in `order` first (in that order),
// followed by the remainder preserving original ordering. Public-shape
// helper retained for staged_retrieval_test.go::TestReorderByIDs_*.
//
// The implementation lives in rerank.staged.reorderByIDs (typed Item
// slice) but the test asserts on []map[string]any directly, so the
// wrapper adapts in/out.
func reorderByIDs(items []map[string]any, order []string) []map[string]any {
	orderIdx := make(map[string]int, len(order))
	for i, id := range order {
		orderIdx[id] = i
	}
	shortlisted := make([]map[string]any, 0, len(order))
	rest := make([]map[string]any, 0, len(items))
	for _, it := range items {
		id, _ := it["id"].(string)
		if _, ok := orderIdx[id]; ok {
			shortlisted = append(shortlisted, it)
		} else {
			rest = append(rest, it)
		}
	}
	for i := 0; i < len(shortlisted); i++ {
		for j := i + 1; j < len(shortlisted); j++ {
			idI, _ := shortlisted[i]["id"].(string)
			idJ, _ := shortlisted[j]["id"].(string)
			if orderIdx[idI] > orderIdx[idJ] {
				shortlisted[i], shortlisted[j] = shortlisted[j], shortlisted[i]
			}
		}
	}
	return append(shortlisted, rest...)
}
