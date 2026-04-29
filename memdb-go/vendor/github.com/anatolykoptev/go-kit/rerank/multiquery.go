package rerank

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// Compile-time check: MultiQuery implements Reranker.
var _ Reranker = MultiQuery{}

// ErrEmptyQueries is returned by RerankMulti when queries is empty.
var ErrEmptyQueries = errors.New("rerank: MultiQuery requires at least one query")

// ErrAllQueriesFailed is returned when every query fails without surfacing an
// explicit error (e.g. a fallback chain that returns StatusDegraded + nil err).
// Allows callers to use errors.Is(err, ErrAllQueriesFailed) for reliable detection.
var ErrAllQueriesFailed = errors.New("rerank: all queries failed without explicit error")

// CombineMode determines how scores from N query results are fused.
type CombineMode uint8

const (
	// CombineMax takes the maximum score across queries for each doc. Default.
	CombineMax CombineMode = iota
	// CombineAvg computes the arithmetic mean of scores across queries for each doc.
	CombineAvg
	// CombineRRF applies reciprocal rank fusion: sum(1/(k+rank+1)) per doc across queries.
	// k defaults to 60 (LangChain4j convention).
	CombineRRF
)

// String returns the canonical label for the combine mode: "max", "avg", or "rrf".
func (m CombineMode) String() string {
	switch m {
	case CombineMax:
		return "max"
	case CombineAvg:
		return "avg"
	case CombineRRF:
		return "rrf"
	default:
		return "unknown"
	}
}

// MultiQuery wraps any Reranker, scores docs against multiple query
// reformulations, and fuses scores via Combine mode. Useful for HyDE-style
// query expansion: the caller pre-generates paraphrases (via LLM), MultiQuery
// runs them in bounded-concurrency parallel and fuses results.
//
// Implements Reranker — composable with Cascade (cascade of multi-queries) and
// *Client. The primary query (queries[0]) is used for single-query interface
// methods Rerank/RerankWithResult. Use RerankMulti for the full multi-query API.
type MultiQuery struct {
	Inner       Reranker
	Combine     CombineMode
	RRFK        int // for CombineRRF; default 60 (LangChain4j convention)
	Concurrency int // max parallel inner calls; default min(N, 4)
}

// Rerank satisfies the Reranker interface using a single query. Delegates to Inner.
func (m MultiQuery) Rerank(ctx context.Context, query string, docs []Doc) []Scored {
	return m.Inner.Rerank(ctx, query, docs)
}

// RerankWithResult satisfies the Reranker interface using a single query. Delegates to Inner.
func (m MultiQuery) RerankWithResult(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) (*Result, error) {
	return m.Inner.RerankWithResult(ctx, query, docs, opts...)
}

// Available delegates to Inner. Returns false if Inner is nil.
func (m MultiQuery) Available() bool {
	return m.Inner != nil && m.Inner.Available()
}

// RerankMulti scores docs against each query in bounded-concurrency parallel,
// then combines results via the Combine mode.
//
// Error handling:
//   - Empty queries → ErrEmptyQueries (fast fail, caller bug).
//   - All queries fail → StatusDegraded, passthrough scores, returns the first error.
//   - Partial failure → Status=Ok, combined scores from successful queries only.
//
// The returned Result.Status reflects the worst outcome among queries:
// Degraded if all failed; Ok if at least one succeeded.
func (m MultiQuery) RerankMulti(ctx context.Context, queries []string, docs []Doc, opts ...RerankOpt) (*Result, error) {
	if len(queries) == 0 {
		return nil, ErrEmptyQueries
	}

	conc := m.Concurrency
	if conc <= 0 {
		conc = len(queries)
		if conc > 4 {
			conc = 4
		}
	}

	// Bounded concurrency via semaphore channel.
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	results := make([]*Result, len(queries))
	errs := make([]error, len(queries))

	for i, q := range queries {
		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done() // we already called wg.Add(1)
			// Mark remaining queries as cancelled so callers see ctx.Err().
			for j := i; j < len(queries); j++ {
				errs[j] = ctx.Err()
			}
			goto wait
		}
		go func(idx int, query string) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := m.Inner.RerankWithResult(ctx, query, docs, opts...)
			results[idx] = res
			errs[idx] = err
		}(i, q)
	}
wait:
	wg.Wait()

	// Count successes and collect the first error.
	var successCount int
	var firstErr error
	for i, err := range errs {
		if err == nil && results[i] != nil && results[i].Status != StatusDegraded {
			successCount++
		} else {
			if firstErr == nil {
				if err != nil {
					firstErr = err
				} else if results[i] != nil && results[i].Err != nil {
					firstErr = results[i].Err
				}
			}
		}
	}

	if successCount == 0 {
		if firstErr == nil {
			firstErr = ErrAllQueriesFailed
		}
		recordMultiQueryPartial("all_failed")
		return &Result{Scored: multiPassthrough(docs), Status: StatusDegraded, Err: firstErr}, firstErr
	}
	if successCount < len(queries) {
		recordMultiQueryPartial("partial")
	}

	combined := combineScores(results, m.Combine, m.RRFK, docs)
	recordMultiQueryCombine(m.Combine.String())

	return &Result{Scored: combined, Status: StatusOk}, nil
}

// combineScores merges N Result.Scored slices into a single []Scored sorted desc
// by the combined score. Doc identity is OrigRank (original index in docs slice).
// Results with nil or Degraded status are skipped in the combination.
func combineScores(results []*Result, mode CombineMode, rrfK int, docs []Doc) []Scored {
	n := len(docs)
	combined := make([]float32, n)

	switch mode {
	case CombineMax:
		// Initialise to a value below any real score so the first real score wins.
		const negInf = float32(-1e30)
		for i := range combined {
			combined[i] = negInf
		}
		for _, res := range results {
			if res == nil || res.Status == StatusDegraded {
				continue
			}
			for _, s := range res.Scored {
				if s.OrigRank >= 0 && s.OrigRank < n && s.Score > combined[s.OrigRank] {
					combined[s.OrigRank] = s.Score
				}
			}
		}
		// Replace any still-at-negInf slots (docs not present in any result) with 0.
		for i := range combined {
			if combined[i] == negInf {
				combined[i] = 0
			}
		}

	case CombineAvg:
		counts := make([]int, n)
		for _, res := range results {
			if res == nil || res.Status == StatusDegraded {
				continue
			}
			for _, s := range res.Scored {
				if s.OrigRank >= 0 && s.OrigRank < n {
					combined[s.OrigRank] += s.Score
					counts[s.OrigRank]++
				}
			}
		}
		for i := range combined {
			if counts[i] > 0 {
				combined[i] /= float32(counts[i])
			}
		}

	case CombineRRF:
		if rrfK <= 0 {
			rrfK = 60
		}
		// For each result, res.Scored is already sorted desc by score.
		// rank is 0-based; RRF contribution = 1/(k + rank + 1).
		for _, res := range results {
			if res == nil || res.Status == StatusDegraded {
				continue
			}
			for rank, s := range res.Scored {
				if s.OrigRank >= 0 && s.OrigRank < n {
					combined[s.OrigRank] += 1.0 / float32(rrfK+rank+1)
				}
			}
		}
	}

	// Build output slice sorted descending by combined score.
	out := make([]Scored, n)
	for i, d := range docs {
		out[i] = Scored{Doc: d, Score: combined[i], OrigRank: i}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// multiPassthrough returns docs in original order with Score=0 and OrigRank=i.
// Used when all queries fail and we must return a safe passthrough.
func multiPassthrough(docs []Doc) []Scored {
	out := make([]Scored, len(docs))
	for i, d := range docs {
		out[i] = Scored{Doc: d, OrigRank: i}
	}
	return out
}
