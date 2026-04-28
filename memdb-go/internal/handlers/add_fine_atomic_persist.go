// Package handlers — F8 atomic-fact persistence path.
//
// applyAtomicAndPersist mirrors applyAndPersistFineFacts, but per-fact:
// each ExtractedFact gets its own factInfo bag (kind=atomic_fact +
// attributed_to + linked_memory_ids + event_dates), then is inserted
// alongside its WorkingMemory twin. Reuses the embed/entity-link/cleanup
// helpers without modifying the legacy build path.
//
// Why a sibling instead of editing applyFineActionsCtx: the legacy
// buildAddNodes signature is pinned by phase35_test.go and the legacy
// applyFineActionsCtx multiplexes ADD/UPDATE/DELETE — atomic facts are
// pure ADD (no UPDATE/DELETE per spec) so the routing logic is moot.
// Splitting keeps the legacy path byte-identical when MEMDB_ATOMIC_FACTS
// is unset.
package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// applyAtomicAndPersist takes embedded atomic facts + a parallel slice of
// per-fact info maps (from applyAtomicInfoToFacts) and writes one
// WorkingMemory + one LongTermMemory row per fact, with kind=atomic_fact
// stamped into properties.kind. Returns the per-fact addResponseItem list
// the caller surfaces in the API response.
func (h *Handler) applyAtomicAndPersist(
	ctx context.Context,
	embedded []embeddedFact,
	perFactInfo []map[string]any,
	fc factContext,
) ([]addResponseItem, error) {
	if len(embedded) != len(perFactInfo) {
		return nil, fmt.Errorf("atomic persist: length mismatch (embedded=%d info=%d)", len(embedded), len(perFactInfo))
	}
	var allNodes []db.MemoryInsertNode
	var items []addResponseItem
	var vsetInserts []wmVSetInsert

	for i := range embedded {
		ef := embedded[i]
		f := ef.fact
		if ef.embVec == "" || f.Memory == "" {
			continue
		}

		// Per-fact info bag: shared fc.Info + per-fact atomic extras + content_hash.
		factInfo := make(map[string]any, len(fc.Info)+len(perFactInfo[i])+1)
		for k, v := range fc.Info {
			factInfo[k] = v
		}
		for k, v := range perFactInfo[i] {
			factInfo[k] = v
		}
		if f.ContentHash != "" {
			factInfo["content_hash"] = f.ContentHash
		}

		createdAt := fc.Now
		if f.ValidAt != "" {
			createdAt = f.ValidAt
		}

		wmID := uuid.New().String()
		ltID := uuid.New().String()
		background := workingBinding(wmID)
		allTags := append([]string{}, fc.CustomTags...)
		allTags = append(allTags, f.Tags...)

		wmProps := buildNodeProps(memoryNodeProps{
			ID: wmID, Memory: f.Memory, MemoryType: "WorkingMemory",
			UserName: fc.CubeID, UserID: fc.UserID, AgentID: fc.AgentID, SessionID: fc.SessionID,
			Mode: modeFine, Now: fc.Now, CreatedAt: createdAt,
			Info: factInfo, CustomTags: allTags, Sources: fc.Sources, Background: "",
			RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
			Key: fc.Key,
		})
		ltProps := buildNodeProps(memoryNodeProps{
			ID: ltID, Memory: f.Memory, MemoryType: f.Type,
			UserName: fc.CubeID, UserID: fc.UserID, AgentID: fc.AgentID, SessionID: fc.SessionID,
			Mode: modeFine, Now: fc.Now, CreatedAt: createdAt,
			Info: factInfo, CustomTags: allTags, Sources: fc.Sources, Background: background,
			RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
			Key: fc.Key,
		})
		// Critical: lift atomic-fact discriminator keys to TOP-LEVEL properties.
		// Migration 0022's GENERATED `kind` column reads properties->>'kind'
		// (top-level); the GIN index on linked_memory_ids and the partial
		// index on attributed_to also expect top-level paths. Leaving these
		// nested under properties.info silently degrades to kind='paragraph_legacy'
		// for every atomic row, which gates F8/F12 entirely. We keep the values
		// inside `info` too (legacy callers and JSONB-property tests still
		// assert that shape) — duplication is intentional.
		liftAtomicDiscriminators(wmProps, factInfo)
		liftAtomicDiscriminators(ltProps, factInfo)
		wmJSON, err1 := marshalProps(wmProps)
		ltJSON, err2 := marshalProps(ltProps)
		if err1 != nil || err2 != nil {
			h.logger.Debug("atomic persist: marshal failed",
				slog.Any("err1", err1), slog.Any("err2", err2))
			continue
		}
		allNodes = append(allNodes,
			db.MemoryInsertNode{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: ef.embVec},
			db.MemoryInsertNode{ID: ltID, PropertiesJSON: ltJSON, EmbeddingVec: ef.embVec},
		)
		items = append(items, addResponseItem{
			Memory: f.Memory, MemoryID: ltID, MemoryType: f.Type, CubeID: fc.CubeID,
		})
		embedded[i].ltmID = ltID
		if len(ef.embedding) > 0 {
			vsetInserts = append(vsetInserts, wmVSetInsert{
				id: wmID, memory: f.Memory, embedding: ef.embedding,
			})
		}
	}

	if len(allNodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			return nil, fmt.Errorf("atomic persist: insert nodes: %w", err)
		}
		if h.wmCache != nil {
			ts := nowUnix()
			for _, vi := range vsetInserts {
				if err := h.wmCache.VAdd(ctx, fc.CubeID, vi.id, vi.memory, vi.embedding, ts); err != nil {
					h.logger.Debug("atomic persist: vset write failed",
						slog.String("id", vi.id), slog.Any("error", err))
				}
			}
		}
		h.linkEntitiesAsync(embedded, fc.CubeID, fc.Now)
	}
	h.cleanupWorkingMemory(ctx, fc.CubeID)
	return items, nil
}

// runAtomicFineForCube is the F8 sibling of nativeFineAddForCube. Lives
// here (not add_fine.go) because it diverges in extract+persist — classify,
// embed, and fanout stages are reused as-is. Same per-stage timing labels.
func (h *Handler) runAtomicFineForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	if len(req.Messages) == 0 {
		return nil, nil
	}
	now := nowTimestamp()
	sessionID := stringOrEmpty(req.SessionID)
	conversation := formatConversation(req.Messages, now)

	// Stage 1: classify (same gate as legacy).
	t := time.Now()
	sig := classifyContent(req.Messages, conversation)
	recordStageDuration(ctx, "classify", t)
	if sig.Skip {
		h.logger.Debug("atomic fine add: skipped extraction",
			slog.String("reason", sig.SkipReason), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Stage 2: atomic extraction. We need both the AtomicFact slice (for the
	// kind/attributed_to/linked_ids info bag) and the converted ExtractedFact
	// slice (for embed/persist). Fetch both via a parallel call.
	t = time.Now()
	atomicFacts, extracted, candOK, err := h.runAtomicFineExtractionFull(ctx, conversation, cubeID, req, &sig)
	recordStageDuration(ctx, "extract", t)
	if err != nil {
		return nil, err
	}
	if !candOK || len(extracted) == 0 {
		return nil, nil
	}

	// Stage 3: embed. Reuse the legacy batched embed path — it already
	// short-circuits on ContentHash dedup and DELETE actions (none apply
	// in the atomic path).
	t = time.Now()
	extracted = h.filterAddsByContentHash(ctx, extracted, cubeID)
	embedded := h.embedFacts(ctx, extracted)
	recordStageDuration(ctx, "embed", t)

	// Stage 4: per-fact info bag + persist via the atomic sibling.
	t = time.Now()
	perFactInfo := applyAtomicInfoToFacts(atomicFacts, extracted)
	fc := factContext{
		CubeID: cubeID, UserID: *req.UserID, AgentID: stringOrEmpty(req.AgentID),
		SessionID: sessionID, Now: now, Info: mapOrEmpty(req.Info),
		CustomTags: req.CustomTags,
		Sources:    buildSourcesFromMessages(req.Messages),
		Key:        stringOrEmpty(req.Key),
	}
	items, err := h.applyAtomicAndPersist(ctx, embedded, perFactInfo, fc)
	recordStageDuration(ctx, "apply", t)
	if err != nil {
		return nil, err
	}

	// Stage 5: background extractors (skill / profile / structural edges) —
	// shared with the legacy path so behavioural parity is preserved.
	t = time.Now()
	h.triggerBackgroundExtractors(extractorTriggerInput{
		CubeID: cubeID, UserID: *req.UserID, SessionID: sessionID,
		Conversation: conversation, Now: now,
		FactCount: len(extracted), MessageCount: len(req.Messages),
	})
	// Stage 5b: F12 linked_memory_ids resolver — fire-and-forget. Runs only
	// for atomic facts (the legacy paragraph path doesn't carry the
	// linked_memory_ids slot). Lives after triggerBackgroundExtractors so
	// existing fanout latency stays unchanged when MEMDB_F12_LINKED is off.
	h.triggerLinkedIDsResolver(atomicFacts, embedded, cubeID, *req.UserID, stringOrEmpty(req.AgentID))
	recordStageDuration(ctx, "fanout", t)
	return items, nil
}

// runAtomicFineExtractionFull is runAtomicFineExtraction widened to also
// return the original AtomicFact slice (so we can build per-fact info bags
// later). Kept thin: it calls into the same getAtomicExtractor/fetchFineCandidates
// path runAtomicFineExtraction uses.
func (h *Handler) runAtomicFineExtractionFull(
	ctx context.Context, conversation, cubeID string,
	req *fullAddRequest, sig *ContentSignal,
) ([]llm.AtomicFact, []llm.ExtractedFact, bool, error) {
	ext := h.getAtomicExtractor()
	if ext == nil {
		return nil, nil, true, fmt.Errorf("atomic fine extract: nil extractor")
	}

	candidates, topScore := h.fetchFineCandidates(ctx, conversation, cubeID, stringOrEmpty(req.AgentID))
	if topScore > nearDuplicateThreshold {
		h.logger.Debug("atomic fine add: skipped — near-duplicate",
			slog.Float64("top_score", topScore), slog.String("cube_id", cubeID))
		return nil, nil, false, nil
	}

	obs := observationDateFromRequest(req)
	atomicFacts, err := ext.ExtractAtomicFacts(ctx, conversation, candidates, obs)
	if err != nil {
		recordAtomicExtractOutcome(ctx, atomicOutcomeLLMError)
		return nil, nil, true, fmt.Errorf("atomic fine add: extract: %w", err)
	}
	if len(atomicFacts) == 0 {
		recordAtomicExtractOutcome(ctx, atomicOutcomeEmpty)
		h.logger.Debug("atomic fine add: no facts extracted",
			slog.String("cube_id", cubeID))
		return nil, nil, true, nil
	}

	recordAtomicExtractOutcome(ctx, atomicOutcomeSuccess)
	recordAtomicFactsPerChunk(ctx, len(atomicFacts))

	extracted := make([]llm.ExtractedFact, 0, len(atomicFacts))
	for _, f := range atomicFacts {
		if !llm.HasProperNoun(f.Text) {
			h.logger.Debug("atomic fine add: fact has no proper noun",
				slog.String("text", f.Text), slog.String("cube_id", cubeID))
		}
		recordAtomicFactWordCount(ctx, llm.CountWords(f.Text))
		extracted = append(extracted, atomicToExtracted(f))
	}
	_ = sig
	h.logger.Debug("atomic fine add: extracted facts",
		slog.Int("count", len(extracted)), slog.String("cube_id", cubeID))
	return atomicFacts, extracted, true, nil
}
