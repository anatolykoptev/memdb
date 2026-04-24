package scheduler

// relation_detector.go — D3 port of Python relation_reason_detector.py.
//
// Given a pair of memory IDs + text, asks the LLM to classify the directed
// relationship A → B from {CAUSES, CONTRADICTS, SUPPORTS, RELATED, NONE} and
// writes a memory_edges row (with confidence + rationale) for any non-NONE
// outcome. Skips writes on confidence < minRelationConfidence to keep the
// graph clean — low-confidence edges distort D2 multi-hop retrieval.
//
// Called from RunTreeReorgForCube after episodic+semantic promotion — pair
// selection is the parent-tier nodes so edges link theme-level memories.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	// minRelationConfidence is the floor for writing a relation edge. Below this
	// the LLM's self-reported certainty is too low to be worth the graph bloat.
	minRelationConfidence = 0.55

	// relationDetectorTimeout is the per-pair LLM deadline.
	relationDetectorTimeout = 30 * time.Second

	// relationDetectorMaxTokens — JSON response is 3 short fields, so budget is tight.
	relationDetectorMaxTokens = 200

	// relationRationaleMaxLen truncates the rationale to keep the edge row bounded.
	relationRationaleMaxLen = 200
)

// relationResult is the JSON shape expected from the LLM.
type relationResult struct {
	Relation   string  `json:"relation"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

// DetectRelationPair asks the LLM to classify the A → B relation and writes an
// edge if the result is non-NONE and above the confidence threshold.
// Returns the relation + confidence actually written ("" if nothing written).
//
// Non-fatal: all errors are returned but TreeManager calls this best-effort —
// a failed relation detection must not abort the reorg cycle.
func (r *Reorganizer) DetectRelationPair(ctx context.Context, fromID, fromText, toID, toText string) (string, float64, error) {
	if fromID == "" || toID == "" || fromID == toID {
		return "", 0, nil
	}
	if strings.TrimSpace(fromText) == "" || strings.TrimSpace(toText) == "" {
		return "", 0, nil
	}

	user := fmt.Sprintf("Memory A (id=%s): %s\n\nMemory B (id=%s): %s", fromID, fromText, toID, toText)

	callCtx, cancel := context.WithTimeout(ctx, relationDetectorTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, []map[string]string{
		{"role": "system", "content": relationDetectorSystemPrompt},
		{"role": "user", "content": user},
	}, relationDetectorMaxTokens)
	if err != nil {
		return "", 0, fmt.Errorf("relation detector llm: %w", err)
	}
	raw = string(llm.StripJSONFence([]byte(raw)))

	var parsed relationResult
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", 0, fmt.Errorf("parse relation json (%s): %w", truncate(raw, 200), err)
	}

	rel := normalizeRelation(parsed.Relation)
	if rel == "" {
		return "", parsed.Confidence, nil
	}
	if parsed.Confidence < minRelationConfidence {
		return "", parsed.Confidence, nil
	}

	rationale := strings.TrimSpace(parsed.Rationale)
	if len(rationale) > relationRationaleMaxLen {
		rationale = rationale[:relationRationaleMaxLen]
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.postgres.CreateMemoryEdgeWithConfidence(ctx, fromID, toID, rel, now, "", parsed.Confidence, rationale); err != nil {
		return "", parsed.Confidence, fmt.Errorf("write relation edge: %w", err)
	}
	r.logger.Debug("tree reorg: relation edge written",
		slog.String("from", fromID),
		slog.String("to", toID),
		slog.String("relation", rel),
		slog.Float64("confidence", parsed.Confidence),
	)
	return rel, parsed.Confidence, nil
}

// normalizeRelation maps the LLM's vocabulary to our edge relation constants.
// Returns "" for NONE or unrecognised strings (safer than fabricating RELATED).
func normalizeRelation(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "CAUSES", "CAUSE":
		return db.EdgeCauses
	case "CONTRADICTS", "CONTRADICT":
		return db.EdgeContradicts
	case "SUPPORTS", "SUPPORT":
		return db.EdgeSupports
	case "RELATED", "RELATE":
		return db.EdgeRelated
	default:
		return ""
	}
}
