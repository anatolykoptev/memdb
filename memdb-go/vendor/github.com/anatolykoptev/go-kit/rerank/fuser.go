package rerank

import (
	"errors"
	"fmt"
)

// RankFuser merges ranked-only lists ([]string) into a single fused list.
// Implementations: NewRRF, NewWeightedRRF.
type RankFuser interface {
	Fuse(lists ...[]string) []Fused
}

// ScoreFuser merges score-aware lists (ScoredIDList) into a single fused list.
// Implementations: NewDBSF, NewLinearMinMax.
type ScoreFuser interface {
	Fuse(lists ...ScoredIDList) []Fused
}

// FuserOption configures a Fuser at construction.
type FuserOption func(*fuserConfig)

// fuserConfig is the shared option struct for all Fuser constructors.
type fuserConfig struct {
	topK int // 0 = uncapped
}

// WithTopK caps fused output at top n results (default 0 = uncapped).
// Matches Qdrant `limit` and Elasticsearch `size` conventions.
//
// n must be ≥ 0; constructors return an error on negative values.
func WithTopK(n int) FuserOption {
	return func(c *fuserConfig) { c.topK = n }
}

// applyOptions builds a fuserConfig from opts and validates it.
// Errors are returned to the caller (constructor) rather than panicking,
// since constructors are intended for config-driven systems.
func applyOptions(opts []FuserOption) (fuserConfig, error) {
	var cfg fuserConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.topK < 0 {
		return cfg, fmt.Errorf("rerank: WithTopK(%d) must be ≥ 0", cfg.topK)
	}
	return cfg, nil
}

// capTopK trims out to the top n entries. Returns out unchanged if n <= 0
// (uncapped) or len(out) <= n. Caller must have already sorted out.
func capTopK(out []Fused, n int) []Fused {
	if n <= 0 || len(out) <= n {
		return out
	}
	return out[:n]
}

// validateNonNegativeWeights returns an error if any weight is < 0.
// Empty weights are accepted here; callers requiring non-empty validate that
// separately.
func validateNonNegativeWeights(name string, weights []float64) error {
	for i, w := range weights {
		if w < 0 {
			return fmt.Errorf("rerank.%s: weights[%d]=%g, weights must be ≥ 0; remove the retriever rather than negating it", name, i, w)
		}
	}
	return nil
}

// rrfFuser implements RankFuser via the package-level RRF function.
type rrfFuser struct {
	k   int
	cfg fuserConfig
}

// Fuse delegates to RRF and applies TopK capping.
func (f *rrfFuser) Fuse(lists ...[]string) []Fused {
	return capTopK(RRF(f.k, lists...), f.cfg.topK)
}

// NewRRF returns a RankFuser using Reciprocal Rank Fusion (Cormack-Clarke 2009).
// k smooths large rank differences (default DefaultRRFK=60). Negative k is
// rejected; k=0 falls back to DefaultRRFK at Fuse time, matching the
// package-level RRF function.
//
// Returns an error on invalid configuration; safe to use in config-driven
// systems (env, config files, database).
func NewRRF(k int, opts ...FuserOption) (RankFuser, error) {
	if k < 0 {
		return nil, fmt.Errorf("rerank.NewRRF: k=%d must be ≥ 0 (k=0 falls back to DefaultRRFK)", k)
	}
	cfg, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	return &rrfFuser{k: k, cfg: cfg}, nil
}

// weightedRRFFuser implements RankFuser via the package-level WeightedRRF
// function with stashed weights.
type weightedRRFFuser struct {
	k       int
	weights []float64
	cfg     fuserConfig
}

// Fuse delegates to WeightedRRF. Length mismatch between stashed weights and
// lists triggers the same panic as the package-level function — runtime
// per-call invariants stay panics; constructor-time invariants are errors.
func (f *weightedRRFFuser) Fuse(lists ...[]string) []Fused {
	return capTopK(WeightedRRF(f.k, f.weights, lists...), f.cfg.topK)
}

// NewWeightedRRF returns a RankFuser with per-list weights.
// Weights must be non-negative and len-equal to the number of lists at
// Fuse time (length mismatch panics, matching the package-level function).
//
// Returns an error on invalid configuration: negative k, empty weights, or
// any negative weight.
func NewWeightedRRF(k int, weights []float64, opts ...FuserOption) (RankFuser, error) {
	if k < 0 {
		return nil, fmt.Errorf("rerank.NewWeightedRRF: k=%d must be ≥ 0 (k=0 falls back to DefaultRRFK)", k)
	}
	if len(weights) == 0 {
		return nil, errors.New("rerank.NewWeightedRRF: weights must be non-empty")
	}
	if err := validateNonNegativeWeights("NewWeightedRRF", weights); err != nil {
		return nil, err
	}
	cfg, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	// Defensive copy so caller mutations after construction can't change
	// runtime behavior.
	w := make([]float64, len(weights))
	copy(w, weights)
	return &weightedRRFFuser{k: k, weights: w, cfg: cfg}, nil
}

// dbsfFuser implements ScoreFuser via the package-level DBSF function.
type dbsfFuser struct {
	cfg fuserConfig
}

// Fuse delegates to DBSF and applies TopK capping.
func (f *dbsfFuser) Fuse(lists ...ScoredIDList) []Fused {
	return capTopK(DBSF(lists...), f.cfg.topK)
}

// NewDBSF returns a ScoreFuser using Distribution-Based Score Fusion (Qdrant
// convention). Per list: z-score normalize (population stddev), clip to ±3σ,
// sum across lists.
//
// Recommended ≥10 items per list for stable σ; below that prefer RRF.
//
// Returns an error only on invalid options (e.g. WithTopK(-1)).
func NewDBSF(opts ...FuserOption) (ScoreFuser, error) {
	cfg, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	return &dbsfFuser{cfg: cfg}, nil
}

// linearMinMaxFuser implements ScoreFuser via the package-level LinearMinMax
// function with stashed weights.
type linearMinMaxFuser struct {
	weights []float64
	cfg     fuserConfig
}

// Fuse delegates to LinearMinMax.
func (f *linearMinMaxFuser) Fuse(lists ...ScoredIDList) []Fused {
	return capTopK(LinearMinMax(f.weights, lists...), f.cfg.topK)
}

// NewLinearMinMax returns a ScoreFuser using MinMax-normalized weighted sum.
// Matches Elasticsearch Linear Retriever / Weaviate relativeScoreFusion.
// Weights must be non-negative.
//
// Returns an error on invalid configuration: empty weights or any negative
// weight.
func NewLinearMinMax(weights []float64, opts ...FuserOption) (ScoreFuser, error) {
	if len(weights) == 0 {
		return nil, errors.New("rerank.NewLinearMinMax: weights must be non-empty")
	}
	if err := validateNonNegativeWeights("NewLinearMinMax", weights); err != nil {
		return nil, err
	}
	cfg, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	// Defensive copy.
	w := make([]float64, len(weights))
	copy(w, weights)
	return &linearMinMaxFuser{weights: w, cfg: cfg}, nil
}
