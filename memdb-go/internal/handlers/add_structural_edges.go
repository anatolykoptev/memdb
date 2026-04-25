package handlers

// add_structural_edges.go — M8 Stream 10: cheap structural edges at ingest.
//
// Why: D2 multi-hop retrieval (cat-2 LoCoMo F1 = 0.091 in M7 Stage 2) suffered
// because memory_edges was sparsely populated — edges were only created when
// the D3 LLM reorganizer eventually fired. This file emits three classes of
// structural edges synchronously after each /add call lands, with no LLM and
// no extra embeddings:
//
//   SAME_SESSION         — every new memory ↔ every existing memory in the
//                          same (cube_id, session_id). Capped at 20 partners
//                          per new memory to stop N² fan-out on long sessions.
//   TIMELINE_NEXT        — each memory linked to its immediate predecessor
//                          (by chat_time / created_at ASC) within the session.
//                          Stores dt_seconds in rationale for D2 weighting.
//   SIMILAR_COSINE_HIGH  — top-5 existing memories whose cosine similarity to
//                          the new memory is in (0.85, dedupThreshold). Skipped
//                          when the new memory was already filtered as a
//                          duplicate (≥ dedupThreshold) — duplicates are not
//                          "similar", they are "the same".
//
// The orchestrator (emitStructuralEdges) is a fire-and-forget tail: it logs
// failures but never propagates them, because /add must not regress because
// the structural-edge insert tripped on an already-degraded edges table.
//
// Pattern lifted from go-code commit de40df1 (parser-time INHERITS/IMPLEMENTS
// edges) — emit free, deterministic edges at the moment data lands.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	// sameSessionMaxPartners caps the SAME_SESSION fan-out so a 200-message
	// session does not produce 200 edges per new memory.
	sameSessionMaxPartners = 20

	// maxFetchLimit is the upper bound for the GetSessionMemoryNeighborsRecent
	// fetch. Guards against sameSessionMaxPartners growing without a matching
	// review of memory pressure.
	maxFetchLimit = 100

	// similarCosineMinScore is the lower bound for SIMILAR_COSINE_HIGH —
	// below this threshold the pair is uninteresting topically.
	similarCosineMinScore = 0.85

	// similarCosineMaxPartners caps the per-memory SIMILAR fan-out (top-K).
	similarCosineMaxPartners = 5
)

// newMemoryRef is the minimal description of a memory that has just been
// persisted by the add pipeline. The structural emitter consumes a slice of
// these — slimmer than the full db.MemoryInsertNode so unit tests don't
// need to fabricate JSON properties.
type newMemoryRef struct {
	ID        string    // property UUID (memory_edges.from_id / to_id)
	CreatedAt string    // ISO timestamp of the new row, used as TIMELINE_NEXT anchor
	Embedding []float32 // optional — only consulted by SIMILAR_COSINE_HIGH path
}

// emitStructuralEdges is the post-InsertMemoryNodes tail. Best-effort: any
// error is logged and swallowed because edge emission is auxiliary to the
// add pipeline's primary contract (the memory rows are already written).
//
// Skip-when-no-session: SAME_SESSION and TIMELINE_NEXT both require a
// session; SIMILAR_COSINE_HIGH does not. If sessionID is empty we still
// run the cosine pass (it's the only edge family that makes sense without
// a session anchor).
func (h *Handler) emitStructuralEdges(ctx context.Context, fac fastAddContext, newMems []newMemoryRef) {
	if h.postgres == nil || len(newMems) == 0 {
		return
	}

	// Step 1 — fetch session pool (if any). ORDER BY DESC so the first rows
	// are the most-recent prior turns — critical for TIMELINE_NEXT to link to
	// the immediate predecessor, not the oldest turn in a long session.
	// Fetch slightly more than sameSessionMaxPartners so buildSameSessionEdges
	// has pool headroom; hard cap at maxFetchLimit to bound the read.
	var neighbors []db.SessionMemoryNeighbor
	if fac.sessionID != "" {
		fetchLimit := sameSessionMaxPartners*2 + len(newMems)
		if fetchLimit > maxFetchLimit {
			fetchLimit = maxFetchLimit
		}
		nb, err := h.postgres.GetSessionMemoryNeighborsRecent(ctx, fac.cubeID, fac.sessionID, fetchLimit)
		if err != nil {
			h.logger.Debug("structural edges: session neighbors fetch failed",
				slog.String("session_id", fac.sessionID), slog.Any("error", err))
			// Continue — we can still emit SIMILAR_COSINE_HIGH.
		} else {
			neighbors = excludeNewMemoryIDs(nb, newMems)
		}
	}

	// Step 2 — build edge candidates from pure helpers (unit-testable).
	var edges []db.MemoryEdgeRow
	if fac.sessionID != "" {
		ss, capped := buildSameSessionEdges(newMems, neighbors, sameSessionMaxPartners)
		edges = append(edges, ss...)
		if capped > 0 {
			addMx().SameSessionCapped.Add(ctx, int64(capped))
		}
		edges = append(edges, buildTimelineNextEdges(newMems, neighbors)...)
	}
	edges = append(edges, h.buildSimilarCosineEdgesForBatch(ctx, fac, newMems)...)

	// Step 3 — single bulk insert.
	if len(edges) == 0 {
		return
	}
	if err := h.postgres.BulkInsertMemoryEdges(ctx, edges, fac.now); err != nil {
		h.logger.Warn("structural edges: bulk insert failed",
			slog.String("cube_id", fac.cubeID),
			slog.Int("edges", len(edges)),
			slog.Any("error", err))
		return
	}
	recordStructuralEdgeCounts(ctx, edges)
}

// excludeNewMemoryIDs filters out neighbours whose ID matches a brand-new
// memory we just inserted. Without this, a multi-memory /add (fast mode emits
// one LTM per window) would generate self-edges within the call.
func excludeNewMemoryIDs(neighbors []db.SessionMemoryNeighbor, newMems []newMemoryRef) []db.SessionMemoryNeighbor {
	if len(newMems) == 0 || len(neighbors) == 0 {
		return neighbors
	}
	skip := make(map[string]struct{}, len(newMems))
	for _, m := range newMems {
		skip[m.ID] = struct{}{}
	}
	out := neighbors[:0:len(neighbors)]
	for _, n := range neighbors {
		if _, drop := skip[n.ID]; drop {
			continue
		}
		out = append(out, n)
	}
	return out
}

// buildSameSessionEdges emits one SAME_SESSION edge per (newMem, partner) pair
// where partner is an EXISTING memory in the same session. The "older" set
// (neighbors) is treated as the partner pool — we do NOT cross-link new
// memories among themselves here because TIMELINE_NEXT already chains the
// freshly-inserted batch in chronological order; SAME_SESSION is the
// "connect to the past" pass.
//
// Returns (edges, cappedCount). cappedCount is the number of partner slots
// trimmed by sameSessionMaxPartners — feeds the same_session_capped_total
// metric so we can spot pathologically-long sessions in production.
func buildSameSessionEdges(newMems []newMemoryRef, neighbors []db.SessionMemoryNeighbor, maxPartners int) ([]db.MemoryEdgeRow, int) {
	if len(newMems) == 0 || len(neighbors) == 0 {
		return nil, 0
	}
	pool := neighbors
	capped := 0
	if len(pool) > maxPartners {
		capped = (len(pool) - maxPartners) * len(newMems)
		pool = pool[:maxPartners]
	}
	edges := make([]db.MemoryEdgeRow, 0, len(newMems)*len(pool))
	for _, m := range newMems {
		for _, p := range pool {
			edges = append(edges, db.MemoryEdgeRow{
				FromID:     m.ID,
				ToID:       p.ID,
				Relation:   db.EdgeSameSession,
				Confidence: 1.0,
			})
		}
	}
	return edges, capped
}

// buildTimelineNextEdges chains memories in created_at ASC order. Considers
// both existing neighbors and freshly-added memories so a multi-window /add
// produces an internal chain plus a link to the previous turn.
//
// Edge direction: from_id = current, to_id = previous. D2's recursive CTE
// traverses this in either direction (predicate filters edge_type, not
// orientation), so the asymmetric write is fine.
func buildTimelineNextEdges(newMems []newMemoryRef, neighbors []db.SessionMemoryNeighbor) []db.MemoryEdgeRow {
	if len(newMems) == 0 {
		return nil
	}
	// Combine into a single timeline keyed by (created_at, id) so ties are stable.
	type tlEntry struct {
		id        string
		createdAt string
		isNew     bool
	}
	all := make([]tlEntry, 0, len(newMems)+len(neighbors))
	for _, n := range neighbors {
		all = append(all, tlEntry{id: n.ID, createdAt: n.CreatedAt})
	}
	for _, m := range newMems {
		all = append(all, tlEntry{id: m.ID, createdAt: m.CreatedAt, isNew: true})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].createdAt != all[j].createdAt {
			return all[i].createdAt < all[j].createdAt
		}
		return all[i].id < all[j].id
	})

	edges := make([]db.MemoryEdgeRow, 0, len(newMems))
	for i := 1; i < len(all); i++ {
		cur := all[i]
		// Only emit when at least one endpoint is freshly inserted; avoid
		// re-creating chains between two purely-existing memories that some
		// earlier ingest already linked (idempotent INSERT would no-op them
		// anyway, but skipping saves bandwidth).
		if !cur.isNew && !all[i-1].isNew {
			continue
		}
		dt := timelineDeltaSeconds(all[i-1].createdAt, cur.createdAt)
		edges = append(edges, db.MemoryEdgeRow{
			FromID:     cur.id,
			ToID:       all[i-1].id,
			Relation:   db.EdgeTimelineNext,
			Confidence: 1.0,
			Rationale:  encodeTimelineRationale(dt),
		})
	}
	return edges
}

// timelineDeltaSeconds parses two ISO timestamps (per nowTimestamp format) and
// returns the absolute seconds between them. Returns 0 if either parse fails —
// rationale is best-effort metadata, never critical-path.
func timelineDeltaSeconds(prev, cur string) int64 {
	const layout = "2006-01-02T15:04:05.000000"
	pt, perr := time.Parse(layout, prev)
	ct, cerr := time.Parse(layout, cur)
	if perr != nil || cerr != nil {
		// Try the source-format fallback (no microseconds) used by extractFastMemories.
		const layout2 = "2006-01-02T15:04:05"
		if perr != nil {
			pt, perr = time.Parse(layout2, prev)
		}
		if cerr != nil {
			ct, cerr = time.Parse(layout2, cur)
		}
		if perr != nil || cerr != nil {
			return 0
		}
	}
	d := ct.Sub(pt).Milliseconds() / 1000
	if d < 0 {
		d = -d
	}
	return d
}

// encodeTimelineRationale serialises the dt_seconds payload. Compact JSON so
// the rationale column stays small — D2 reads it via ->> 'dt_seconds'.
func encodeTimelineRationale(dtSeconds int64) string {
	b, err := json.Marshal(map[string]int64{"dt_seconds": dtSeconds})
	if err != nil {
		return fmt.Sprintf(`{"dt_seconds":%d}`, dtSeconds)
	}
	return string(b)
}

// recordStructuralEdgeCounts increments memdb.add.structural_edges_total per
// relation type — one Add call per type so dashboards can break down by
// relation without sampling every edge.
func recordStructuralEdgeCounts(ctx context.Context, edges []db.MemoryEdgeRow) {
	if len(edges) == 0 {
		return
	}
	counts := make(map[string]int64, 3)
	for _, e := range edges {
		counts[e.Relation]++
	}
	for relation, n := range counts {
		addMx().StructuralEdges.Add(ctx, n,
			metric.WithAttributes(attribute.String("type", relation)))
	}
}
