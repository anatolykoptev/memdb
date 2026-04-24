package scheduler

// reorganizer_mem_read_candidates.go — two-tier dedup candidate fetch for mem_read:
// VSET hot-cache (wmCache) first, then Postgres pgvector.

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// memReadCandidateHeadChars is the character limit for the conversation head
// when embedding for dedup candidate lookup.
const memReadCandidateHeadChars = 512

// fetchMemReadCandidates fetches dedup candidates for the mem_read pipeline (two-tier).
func (r *Reorganizer) fetchMemReadCandidates(ctx context.Context, conversation, cubeID, agentID string, log *slog.Logger) []llm.Candidate {
	const candidateLimit = 10
	head := conversation
	if len(head) > memReadCandidateHeadChars {
		head = head[:memReadCandidateHeadChars]
	}
	convEmbs, err := r.embedder.Embed(ctx, []string{head})
	if err != nil || len(convEmbs) == 0 || len(convEmbs[0]) == 0 {
		log.Debug("mem_read: embed for candidates failed", slog.Any("error", err))
		return nil
	}
	embedding := convEmbs[0]
	seen := make(map[string]struct{})
	out := make([]llm.Candidate, 0, candidateLimit)

	r.appendVSetCandidates(ctx, cubeID, embedding, candidateLimit, &out, seen, log)
	r.appendPGCandidates(ctx, cubeID, agentID, embedding, candidateLimit, &out, seen, log)
	return out
}

// appendVSetCandidates adds VSET hot-cache candidates to the output slice.
func (r *Reorganizer) appendVSetCandidates(ctx context.Context, cubeID string, embedding []float32, limit int, out *[]llm.Candidate, seen map[string]struct{}, log *slog.Logger) {
	if r.wmCache == nil {
		return
	}
	results, err := r.wmCache.VSim(ctx, cubeID, embedding, limit)
	if err != nil {
		log.Debug("mem_read: vset vsim failed", slog.Any("error", err))
		return
	}
	for _, vr := range results {
		if vr.ID != "" && vr.Memory != "" {
			*out = append(*out, llm.Candidate{ID: vr.ID, Memory: vr.Memory})
			seen[vr.ID] = struct{}{}
		}
	}
}

// appendPGCandidates adds Postgres pgvector candidates to the output slice.
func (r *Reorganizer) appendPGCandidates(ctx context.Context, cubeID, agentID string, embedding []float32, limit int, out *[]llm.Candidate, seen map[string]struct{}, log *slog.Logger) {
	results, err := r.postgres.VectorSearch(ctx, embedding, cubeID, cubeID,
		[]string{"LongTermMemory", "UserMemory"}, agentID, limit)
	if err != nil {
		log.Debug("mem_read: postgres vector search failed", slog.Any("error", err))
		return
	}
	for _, vr := range results {
		id, mem := extractIDAndMemory(vr.Properties)
		if id == "" || mem == "" || isDup(id, seen) {
			continue
		}
		*out = append(*out, llm.Candidate{ID: id, Memory: mem})
		seen[id] = struct{}{}
	}
}

// isDup returns true if id is already in the seen set.
func isDup(id string, seen map[string]struct{}) bool {
	_, dup := seen[id]
	return dup
}
