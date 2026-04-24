package scheduler

// reorganizer_mem_read_dedup.go — content-hash dedup and batch-embed stage
// for the mem_read fine pipeline.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// embeddedMemReadFact pairs an ExtractedFact with its embedding and assigned LTM ID.
type embeddedMemReadFact struct {
	fact      llm.ExtractedFact
	embedding []float32
	embVec    string
	ltmID     string
}

// filterAddsByContentHash removes ADD facts whose content_hash already exists in the DB.
func (r *Reorganizer) filterAddsByContentHash(ctx context.Context, facts []llm.ExtractedFact, cubeID string, log *slog.Logger) []llm.ExtractedFact {
	type entry struct {
		idx  int
		hash string
	}
	var addEntries []entry
	hashes := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action != llm.MemAdd {
			continue
		}
		if f.Memory == "" {
			continue
		}
		h := memReadTextHash(f.Memory)
		addEntries = append(addEntries, entry{idx: i, hash: h})
		hashes = append(hashes, h)
	}
	if len(hashes) == 0 {
		return facts
	}

	existing, err := r.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		log.Debug("mem_read: batch hash check failed (skipping hash dedup)", slog.Any("error", err))
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
		log.Debug("mem_read: skipped exact duplicates by content_hash", slog.Int("skipped", skipped))
	}
	return facts
}

// embedFacts embeds all ADD/UPDATE facts in a single batched ONNX inference call.
func (r *Reorganizer) embedFacts(ctx context.Context, facts []llm.ExtractedFact, log *slog.Logger) []embeddedMemReadFact {
	out := make([]embeddedMemReadFact, len(facts))
	for i, f := range facts {
		out[i].fact = f
	}

	indices := make([]int, 0, len(facts))
	embTexts := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action == llm.MemDelete || f.Action == llm.MemSkip || f.Memory == "" {
			continue
		}
		indices = append(indices, i)
		embTexts = append(embTexts, f.Memory)
	}
	if len(embTexts) == 0 {
		return out
	}

	embs, err := r.embedder.Embed(ctx, embTexts)
	if err != nil {
		log.Debug("mem_read: batch embed failed", slog.Any("error", err))
		return out
	}

	for j, idx := range indices {
		if j >= len(embs) || len(embs[j]) == 0 {
			continue
		}
		out[idx].embedding = embs[j]
		out[idx].embVec = db.FormatVector(embs[j])
	}
	return out
}

// memReadTextHash computes a 16-byte SHA-256 content hash of the normalized text.
func memReadTextHash(text string) string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:16])
}
