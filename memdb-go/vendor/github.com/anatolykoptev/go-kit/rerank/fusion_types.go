package rerank

// ScoredID is an (id, score) pair from a single retriever, used as input to
// score-level fusion helpers (DBSF, LinearMinMax). Distinct from the existing
// Scored type, which carries a full Doc + OrigRank for the rerank pipeline.
//
// A ScoredIDList is one retriever's output, conventionally sorted desc by
// Score (highest first), but fusion does not depend on order — only on the
// score values per ID.
type ScoredID struct {
	ID    string
	Score float64
}

// ScoredIDList is one retriever's output: ordered (id, score) pairs.
// Fusion helpers consume it score-only; ordering is a caller convention.
type ScoredIDList []ScoredID
