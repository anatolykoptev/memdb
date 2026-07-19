package llm

import (
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
)

// SelectionStrategy controls the order in which eligible chain endpoints are tried.
type SelectionStrategy int

const (
	// SelectionPriority tries endpoints in configured order (primary first, then
	// fallbacks). This is the default — identical to pre-feature behaviour.
	SelectionPriority SelectionStrategy = iota
	// SelectionRandom shuffles eligible (healthy + non-cooled) endpoints on each
	// request before the try-loop. No single endpoint is always hammered first;
	// load is distributed across the pool.
	SelectionRandom
	// SelectionWeighted performs a weighted random shuffle of eligible endpoints
	// using Efraimidis-Spirakis: key = rand^(1/w), sorted descending. Models with
	// weight 0 are structurally excluded from the try-order (not just deprioritized).
	// Models absent from the weights map default to weight 1. Composes with
	// eligibleEndpoints (health + cooldown filter) the same way SelectionRandom does.
	SelectionWeighted
)

// parseSelectionStrategy converts an env-var string value to a SelectionStrategy.
// Unknown / empty values log a warning and fall back to SelectionPriority.
func parseSelectionStrategy(s string) SelectionStrategy {
	switch s {
	case "random":
		return SelectionRandom
	case "weighted":
		return SelectionWeighted
	case "priority", "":
		return SelectionPriority
	default:
		slog.Warn("llm: unknown LLM_SELECTION_STRATEGY value, using priority", "value", s)
		return SelectionPriority
	}
}

// shuffleEndpoints returns a shuffled COPY of eps using r (or the global rand
// source when r is nil). Never mutates the input slice.
func shuffleEndpoints(eps []Endpoint, r *rand.Rand) []Endpoint {
	out := make([]Endpoint, len(eps))
	copy(out, eps)
	if r != nil {
		r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	} else {
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	}
	return out
}

// eligibleEndpoints filters eps to those not currently in cooldown.
// This is Guard A of the cooled-model exclusion invariant: it builds the
// non-cooled subset before shuffling, so a cooled model is never placed into
// the try-order at all. Guard B (the per-ep cooling() check in the loop body
// of executeInner) is a race-safety backstop for the concurrent-cooldown
// window; it does not cover this point-in-time filtering.
// Called only when skipCooled=true (≥1 healthy endpoint exists) and strategy
// is SelectionRandom or SelectionWeighted.
func eligibleEndpoints(all []Endpoint, cd *modelCooldown) []Endpoint {
	out := make([]Endpoint, 0, len(all))
	for _, ep := range all {
		if !cd.cooling(ep.Model) {
			out = append(out, ep)
		}
	}
	return out
}

// weightedShuffleEndpoints returns a weighted-ordered COPY of eps using
// Efraimidis-Spirakis: for each endpoint with weight w > 0, a key
// key = rand^(1/w) is drawn and endpoints are sorted descending by key.
// Endpoints with weight == 0 are excluded entirely (structural exclusion,
// not merely deprioritized). Endpoints whose Model is not in the weights
// map get default weight 1. Returns empty slice when all endpoints are
// weight-0 (caller's race guard fires). Never mutates the input slice.
// When r is nil, uses the global math/rand source (same pattern as shuffleEndpoints).
func weightedShuffleEndpoints(eps []Endpoint, weights map[string]int, r *rand.Rand) []Endpoint {
	type keyed struct {
		ep  Endpoint
		key float64
	}

	candidates := make([]keyed, 0, len(eps))
	for _, ep := range eps {
		w := 1 // default weight for unlisted models
		if weights != nil {
			if ww, ok := weights[ep.Model]; ok {
				w = ww
			}
		}
		if w == 0 {
			// Structural exclusion: weight-0 model never appears in output.
			continue
		}

		var rf float64
		if r != nil {
			rf = r.Float64()
		} else {
			rf = rand.Float64() //nolint:gosec // math/rand intentional; not security-critical
		}
		// Guard against Float64() returning exactly 0 (extremely rare): math.Pow(0, 1/w)
		// returns 0 for any positive w, which would sort this endpoint last rather
		// than at a random position. Replace with a tiny non-zero value instead.
		if rf == 0 {
			rf = 1e-15
		}
		key := math.Pow(rf, 1.0/float64(w))
		candidates = append(candidates, keyed{ep: ep, key: key})
	}

	// Sort descending by key.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].key > candidates[j].key
	})

	out := make([]Endpoint, len(candidates))
	for i, c := range candidates {
		out[i] = c.ep
	}
	return out
}

// parseModelWeights parses a comma-separated "model:weight" string into a map.
// Pairs where the model name is empty, the colon is missing, the weight is not
// a non-negative integer, or the weight is negative are skipped with a warning.
// Weight 0 is valid (exclusion signal). Returns nil when s is empty.
func parseModelWeights(s string) map[string]int {
	if s == "" {
		return nil
	}
	m := make(map[string]int)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		idx := strings.IndexByte(pair, ':')
		if idx < 0 {
			slog.Warn("llm: invalid LLM_MODEL_WEIGHTS pair, skipping", "pair", pair)
			continue
		}
		model := pair[:idx]
		weightStr := pair[idx+1:]
		if model == "" {
			slog.Warn("llm: invalid LLM_MODEL_WEIGHTS pair, skipping", "pair", pair)
			continue
		}
		w, err := strconv.Atoi(weightStr)
		if err != nil {
			slog.Warn("llm: invalid LLM_MODEL_WEIGHTS pair, skipping", "pair", pair)
			continue
		}
		if w < 0 {
			slog.Warn("llm: invalid LLM_MODEL_WEIGHTS pair, skipping", "pair", pair)
			continue
		}
		m[model] = w
	}
	return m
}

// parseCSV splits a comma-separated string into trimmed, non-empty tokens.
// Used by NewClient to parse LLM_REASONING_EFFORT_MODELS and similar list env vars.
func parseCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
