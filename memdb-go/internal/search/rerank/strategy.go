// Package rerank — strategy interface + Chain orchestrator.
//
// M11/R3 carved out of internal/search to break a 5-way inline branch in
// postProcessResults. Each rerank touch-point (cosine, cross-encoder lookup
// + live, LLM judge, staged retrieval) becomes a typed strategy implementing
// Reranker. postProcessResults composes them via Chain so adding a new
// reranker (Q3 metadata boost, Q4 MMR diversification, F5 dialogue-pair) is
// strictly additive: write a Reranker, append to the chain.
//
// Boundary: this package MUST NOT import internal/search (would cycle).
// The minimal Item interface lets the parent search pkg supply an adapter
// over its existing map[string]any item shape.
package rerank

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Item is the minimal shape a Reranker needs to read+mutate a search hit.
// The parent search package supplies an adapter wrapping its existing
// map[string]any item type — see internal/search/rerank_adapter.go.
//
// Score is the active relevance score (canonically metadata.relativity
// in the legacy map shape). SetMeta lets a strategy stamp flags like
// cross_encoder_reranked / llm_reranked without leaking the underlying
// map type into the strategy code.
type Item interface {
	ID() string
	Score() float64
	SetScore(v float64)
	SetMeta(key string, val any)
	GetMeta(key string) (any, bool)
	// EmbeddingText returns the text used for cross-encoder / LLM judge
	// scoring (typically the "memory" field).
	EmbeddingText() string
}

// Reranker reorders a slice of items in light of a query. Implementations
// SHOULD return the input slice (possibly reordered or with mutated items)
// rather than allocate a fresh one — the search pipeline pre-allocates
// the underlying maps and downstream stages share state with upstream.
//
// Errors are logged via the chain's metric and the chain falls through to
// the next strategy with the unchanged slice — rerankers must NEVER return
// nil items on a non-fatal failure.
type Reranker interface {
	Name() string
	Rerank(ctx context.Context, query string, items []Item) ([]Item, error)
}

// Chain composes rerankers in order. Each receives the output of the
// previous. A per-strategy outcome counter is emitted (success | skipped |
// error). On error the chain logs and proceeds with the input unchanged.
type Chain []Reranker

// Rerank runs every strategy in order, returning the final slice. On a
// strategy error the previous slice is forwarded unchanged so the chain
// degrades gracefully — no rerank step is allowed to fail the whole search.
func (c Chain) Rerank(ctx context.Context, query string, items []Item) ([]Item, error) {
	for _, r := range c {
		out, err := r.Rerank(ctx, query, items)
		if err != nil {
			recordOutcome(ctx, r.Name(), "error")
			// Forward the input on error — chain must be best-effort.
			continue
		}
		if out == nil {
			recordOutcome(ctx, r.Name(), "skipped")
			continue
		}
		items = out
	}
	return items, nil
}

// Name returns "chain" so the Chain itself can be used as a Reranker. Useful
// if a future caller wants to nest Chains. Outcome metrics are still emitted
// per inner strategy.
func (c Chain) Name() string { return "chain" }

// SkippedOutcome is the metric label emitted when a Reranker returns nil.
// Re-exported as a const so strategies can self-report skip reasons via
// RecordSkipped without typoing the label.
const (
	OutcomeSuccess = "success"
	OutcomeSkipped = "skipped"
	OutcomeError   = "error"
)

// metric singleton — pre-registers all known strategy/outcome pairs so
// dashboards see series from container start.
var (
	rerankCounter metric.Int64Counter
)

// known names pre-registered at zero. Adding a new strategy MUST extend
// this list to keep the dashboard pre-populated.
var preregNames = []string{"cosine", "cross_encoder", "llm_judge", "staged"}

func init() {
	m := otel.Meter("memdb-go/search/rerank")
	c, _ := m.Int64Counter("memdb.search.rerank_strategy_total",
		metric.WithDescription("Per-strategy rerank outcome (name in cosine|cross_encoder|llm_judge|staged, outcome in success|skipped|error)"))
	rerankCounter = c
	ctx := context.Background()
	for _, n := range preregNames {
		for _, oc := range []string{OutcomeSuccess, OutcomeSkipped, OutcomeError} {
			c.Add(ctx, 0, metric.WithAttributes(
				attribute.String("name", n),
				attribute.String("outcome", oc),
			))
		}
	}
}

// recordOutcome bumps the per-strategy counter. Strategies use
// RecordSuccess / RecordSkipped helpers below; the chain uses
// recordOutcome for the error path.
func recordOutcome(ctx context.Context, name, outcome string) {
	if rerankCounter == nil {
		return
	}
	rerankCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("name", name),
		attribute.String("outcome", outcome),
	))
}

// RecordSuccess bumps the per-strategy success counter. Strategies should
// call this when their work landed (returned a reordered slice). Skipped
// and error are handled by the chain.
func RecordSuccess(ctx context.Context, name string) {
	recordOutcome(ctx, name, OutcomeSuccess)
}

// RecordSkipped bumps the per-strategy skipped counter. Strategies call
// this when they elected not to run (env-disabled, too few items, missing
// config) but did not error.
func RecordSkipped(ctx context.Context, name string) {
	recordOutcome(ctx, name, OutcomeSkipped)
}
