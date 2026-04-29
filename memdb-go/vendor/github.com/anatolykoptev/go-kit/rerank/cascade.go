package rerank

import (
	"context"
	"time"
)

// Reranker abstracts pointwise rerank implementations. Implementations MUST
// return a non-nil *Result, even on error — callers index Result.Status without
// nil-checking. Cascade defensively guards against nil for safety, but the
// contract is non-nil.
//
// Implemented by:
//   - *Client (HTTP cross-encoder)
//   - Cascade (multi-stage chain)
//   - Future: MultiQuery (G4), listwise impls (M13+)
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []Doc) []Scored
	RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error)
	Available() bool
}

// Compile-time checks that concrete types satisfy Reranker.
var _ Reranker = (*Client)(nil)
var _ Reranker = Cascade{}

// Cascade chains Reranker stages. Stage N receives only the top-N output from
// Stage N-1 (after KeepTopN cut). Use a fast/lightweight Reranker as Stage 0
// to prefilter, then a heavy/precise Reranker on the shortlist.
//
// Stage scores DO NOT propagate forward — each stage re-scores its input from
// scratch. This avoids subtle bugs from mixing score scales across models.
//
// Example:
//
//	fast := rerank.NewClient(url, rerank.WithModel("bge-base"))
//	slow := rerank.NewClient(url, rerank.WithModel("bge-reranker-v2-m3"))
//	c := rerank.Cascade{Stages: []rerank.StageConfig{
//	    {Reranker: fast, KeepTopN: 20, Label: "prefilter"},
//	    {Reranker: slow, KeepTopN: 10, Label: "deep"},
//	}}
//	res, _ := c.RerankWithResult(ctx, query, docs)
type Cascade struct {
	Stages []StageConfig
}

// StageConfig defines a single stage within a Cascade.
type StageConfig struct {
	// Reranker is the implementation used for this stage.
	Reranker Reranker
	// KeepTopN passes only the top-N results to the next stage.
	// 0 means "pass all" (no cut). Last stage's KeepTopN cuts the final output.
	KeepTopN int
	// StopBelowThreshold stops the cascade early if the highest-scored result
	// in this stage falls below the threshold. 0 disables early exit.
	// Applied to the stage's raw scores (caller wires WithNormalize on the inner
	// Reranker if normalized scores are needed for the comparison).
	StopBelowThreshold float32
	// Label is used as a Prometheus label for per-stage metrics. Required.
	Label string
}

// Rerank satisfies the Reranker interface. Drops the Result/Status — use
// RerankWithResult when you need to distinguish degraded/skipped paths.
func (c Cascade) Rerank(ctx context.Context, query string, docs []Doc) []Scored {
	res, _ := c.RerankWithResult(ctx, query, docs)
	if res == nil {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return out
	}
	return res.Scored
}

// RerankWithResult chains Stages sequentially. Returns a final Result whose
// Status reflects the last-completed stage (or StatusDegraded if any stage
// returns an error/Degraded).
//
// Empty Stages: returns StatusSkipped passthrough with no error.
// Mid-stage failure: returns StatusDegraded with the error from that stage;
// earlier stages' work is discarded.
// Stage scores DO NOT propagate forward — each stage re-scores from scratch
// on the shortlist produced by the previous stage's KeepTopN cut.
func (c Cascade) RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error) {
	if len(c.Stages) == 0 {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return &Result{
			Scored: out,
			Status: StatusSkipped,
		}, nil
	}

	cur := docs
	start := time.Now()
	var lastModel string

	for i, stage := range c.Stages {
		recordCascadeStageIn(stage.Label, len(cur))

		res, err := stage.Reranker.RerankWithResult(ctx, query, cur, opts...)
		if err != nil || res == nil || res.Status == StatusDegraded {
			recordCascadeStageOutcome(stage.Label, i, "degraded")
			// Propagate the degraded result/error; earlier stages' work is lost.
			if res == nil {
				res = &Result{
					Scored: make([]Scored, len(cur)),
					Status: StatusDegraded,
					Err:    err,
				}
				for j, d := range cur {
					res.Scored[j] = Scored{Doc: d, OrigRank: j}
				}
			}
			res.Status = StatusDegraded
			recordCascadeTotalDuration(time.Since(start))
			return res, err
		}

		// StopBelowThreshold: check the top score of this stage's output.
		if stage.StopBelowThreshold > 0 && len(res.Scored) > 0 &&
			res.Scored[0].Score < stage.StopBelowThreshold {
			recordCascadeEarlyExit(stage.Label, "below_threshold")
			recordCascadeStageOutcome(stage.Label, i, "early_exit")
			recordCascadeTotalDuration(time.Since(start))
			return res, nil
		}

		// Apply KeepTopN: cut res.Scored before passing to next stage.
		next := res.Scored
		if stage.KeepTopN > 0 && stage.KeepTopN < len(next) {
			next = next[:stage.KeepTopN]
		}
		recordCascadeStageOut(stage.Label, len(next))
		recordCascadeStageOutcome(stage.Label, i, "ok")
		lastModel = res.Model

		// Last stage: return its Result with the KeepTopN-cut Scored.
		if i == len(c.Stages)-1 {
			res.Scored = next
			if lastModel != "" {
				res.Model = lastModel
			}
			recordCascadeTotalDuration(time.Since(start))
			return res, nil
		}

		// Convert the shortlist back to []Doc for the next stage. Each stage
		// re-scores from scratch — lower-stage scores are NOT preserved.
		cur = make([]Doc, len(next))
		for j, s := range next {
			cur[j] = s.Doc
		}
	}

	// Unreachable (loop returns on last stage), but keeps the compiler happy.
	recordCascadeTotalDuration(time.Since(start))
	return &Result{Status: StatusOk, Model: lastModel}, nil
}

// Available reports true only if all stages have a non-nil, available Reranker.
// An empty Cascade returns false.
func (c Cascade) Available() bool {
	if len(c.Stages) == 0 {
		return false
	}
	for _, s := range c.Stages {
		if s.Reranker == nil || !s.Reranker.Available() {
			return false
		}
	}
	return true
}
