package handlers

// add_fine_candidates.go — candidate fetch + LLM extraction + content-hash
// dedup. Embedding lives in add_fine_nodes.go. Split from add_fine.go (M11 R1).

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// candidateConvHeadChars is the prefix length embedded for candidate lookup.
const candidateConvHeadChars = 512

// embeddedFact pairs an ExtractedFact with its computed embedding.
type embeddedFact struct {
	fact      llm.ExtractedFact
	embedding []float32 // for VSET VAdd
	embVec    string    // pgvector literal for postgres insert
	ltmID     string    // assigned in applyFineActions (ADD=new ltID, UPDATE=targetID)
}

// wmVSetInsert is a WorkingMemory node queued for the VSET hot cache.
type wmVSetInsert struct {
	id, memory string
	embedding  []float32
}

// fetchFineCandidates returns top-10 existing memories as LLM dedup candidates.
// Two-tier: VSET hot cache (Redis HNSW) → postgres pgvector fallback. Merged
// + deduped by ID. Returns top similarity score for near-duplicate gating.
func (h *Handler) fetchFineCandidates(ctx context.Context, conversation, cubeID, agentID string) ([]llm.Candidate, float64) { //nolint:gocognit,cyclop
	head := conversation[:min(candidateConvHeadChars, len(conversation))]
	convEmbs, err := h.embedder.Embed(ctx, []string{head})
	if err != nil || len(convEmbs) == 0 || len(convEmbs[0]) == 0 {
		return nil, 0
	}
	embedding := convEmbs[0]
	seen := make(map[string]struct{})
	out := make([]llm.Candidate, 0, 10)
	var topScore float64

	// Tier 1: VSET hot cache (WorkingMemory, HNSW in-memory).
	if h.wmCache != nil {
		if vsetResults, err := h.wmCache.VSim(ctx, cubeID, embedding, 10); err != nil {
			h.logger.Debug("fine add: vset vsim failed, falling back",
				slog.String("cube_id", cubeID), slog.Any("error", err))
		} else {
			for _, r := range vsetResults {
				if r.Score > topScore {
					topScore = r.Score
				}
				if r.ID != "" && r.Memory != "" {
					out = append(out, llm.Candidate{ID: r.ID, Memory: r.Memory})
					seen[r.ID] = struct{}{}
				}
			}
			h.logger.Debug("fine add: vset candidates",
				slog.Int("count", len(out)), slog.String("cube_id", cubeID))
		}
	}
	// Tier 2: Postgres pgvector (LongTermMemory + UserMemory).
	results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, cubeID,
		[]string{"LongTermMemory", "UserMemory"}, agentID, 10)
	if err != nil {
		h.logger.Debug("fine add: postgres vector search failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
		return out, topScore
	}
	for _, r := range results {
		if r.Score > topScore {
			topScore = r.Score
		}
		id, mem := extractIDAndMemory(r.Properties)
		if id == "" || mem == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		out = append(out, llm.Candidate{ID: id, Memory: mem})
		seen[id] = struct{}{}
	}
	return out, topScore
}

// filterAddsByContentHash removes ADD facts whose content_hash already exists.
// Batch-checks all ADD hashes in one round-trip. Non-fatal on error.
func (h *Handler) filterAddsByContentHash(ctx context.Context, facts []llm.ExtractedFact, cubeID string) []llm.ExtractedFact {
	type entry struct {
		idx  int
		hash string
	}
	var addEntries []entry
	hashes := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action != llm.MemAdd && f.Action != "" {
			continue
		}
		if f.Memory == "" {
			continue
		}
		h := textHash(f.Memory)
		addEntries = append(addEntries, entry{idx: i, hash: h})
		hashes = append(hashes, h)
	}
	if len(hashes) == 0 {
		return facts
	}
	existing, err := h.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		h.logger.Debug("fine add: batch hash check failed (skipping hash dedup)", slog.Any("error", err))
		return facts
	}
	skipped := 0
	for _, e := range addEntries {
		if existing[e.hash] {
			facts[e.idx].Action = llm.MemSkip
			skipped++
		} else if facts[e.idx].ContentHash == "" {
			facts[e.idx].ContentHash = e.hash
		}
	}
	if skipped > 0 {
		h.logger.Debug("fine add: skipped exact duplicates by content_hash", slog.Int("skipped", skipped))
	}
	return facts
}

// runFineExtraction does candidate-fetch + LLM extract+dedup + hallucination
// filter. candOK=false means near-duplicate rejection (caller bails, no err).
// sig is mutated in place with the merge-suggestion hint when applicable.
func (h *Handler) runFineExtraction(
	ctx context.Context, conversation, cubeID string,
	req *fullAddRequest, sig *ContentSignal,
) ([]llm.ExtractedFact, bool, error) {
	candidates, topScore := h.fetchFineCandidates(ctx, conversation, cubeID, stringOrEmpty(req.AgentID))
	if topScore > nearDuplicateThreshold {
		h.logger.Debug("fine add: skipped — near-duplicate",
			slog.Float64("top_score", topScore), slog.String("cube_id", cubeID))
		return nil, false, nil
	}
	if topScore > mergeSuggestionThreshold {
		sig.Hints = append(sig.Hints, "High-similarity existing memory found — prefer UPDATE over ADD if semantically equivalent")
	}
	// Date-aware hint: emit `[mention YYYY-MM-DD]` on time-anchored facts (M9 S4).
	hints := append(dateAwareExtractHints(), sig.Hints...)
	// LoCoMo fidelity: anchor D6 resolution against the in-conversation date
	// (latest message's chat_time) instead of today's wall-clock. Empty string
	// falls back to time.Now() inside ExtractAndDedupAt for non-LoCoMo callers.
	obsDate := h.resolveObservationDate(ctx, req.Messages)
	facts, err := h.llmExtractor.ExtractAndDedupAt(ctx, conversation, candidates, obsDate, hints...)
	if err != nil {
		recordDateAwareExtractOutcome(ctx, dateAwareExtractOutcomeError)
		return nil, true, fmt.Errorf("fine add: extract and dedup: %w", err)
	}
	recordDateAwareExtractOutcome(ctx, "")
	if len(facts) == 0 {
		h.logger.Debug("fine add: no facts extracted", slog.String("cube_id", cubeID))
		return nil, true, nil
	}
	h.logger.Debug("fine add: extracted facts",
		slog.Int("count", len(facts)), slog.String("model", h.llmExtractor.Model()))
	facts = h.filterHallucinatedFacts(ctx, conversation, facts)
	if len(facts) == 0 {
		h.logger.Debug("fine add: all facts filtered as hallucinations", slog.String("cube_id", cubeID))
		return nil, true, nil
	}
	return facts, true, nil
}
