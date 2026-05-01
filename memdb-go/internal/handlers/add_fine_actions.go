package handlers

// add_fine_actions.go — route embeddedFacts to ADD/UPDATE/DELETE handlers
// + side effects (edge invalidation, ce_score cleanup, VSET eviction). Leaf
// node-builders live in add_fine_nodes.go. Split from add_fine.go (M11 R1).

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// factContext bundles the per-request invariants every fine-mode helper
// needs when turning extracted facts into memory nodes. Keeps the helper
// signatures to ≤ 4 params. Passed by value (small + immutable per call).
type factContext struct {
	CubeID     string
	UserID     string
	AgentID    string
	SessionID  string
	Now        string
	Info       map[string]any
	CustomTags []string
	Sources    []map[string]any
	Key        string

	// ObservationDate is the in-conversation date (YYYY-MM-DD) of the latest
	// message in the source batch. Distinct from Now (server wall-clock at
	// ingest). Empty when no message carried a chat_time. M12.1.
	ObservationDate string
}

// applyFineActionsCtx routes each embeddedFact through the matching action
// and returns nodes to insert + response items + VSET cache writes. UPDATE
// and DELETE are applied immediately via postgres. Used by both the fine-add
// orchestrator and the buffer-flush pipeline.
func (h *Handler) applyFineActionsCtx(
	ctx context.Context,
	embedded []embeddedFact,
	fc factContext,
) ([]db.MemoryInsertNode, []addResponseItem, []wmVSetInsert) {
	var allNodes []db.MemoryInsertNode
	var items []addResponseItem
	var vsetInserts []wmVSetInsert
	for i := range embedded {
		ef := embedded[i]
		f := ef.fact
		switch f.Action {
		case llm.MemSkip:
			continue
		case llm.MemDelete:
			h.applyDeleteAction(ctx, f.TargetID, fc.CubeID)
			h.evictFromVSet(ctx, fc.CubeID, f.TargetID, "fine add: vset vrem failed")
		case llm.MemUpdate:
			h.applyUpdateAction(ctx, updateActionArgs{
				TargetID: f.TargetID, Memory: f.Memory,
				EmbVec: ef.embVec, Now: fc.Now,
			})
			embedded[i].ltmID = f.TargetID
			if node, vsi, ok := buildUpdateWMNode(f, ef,
				fc.CubeID, fc.UserID, fc.AgentID, fc.SessionID, fc.Now,
				fc.Info, fc.CustomTags, fc.Sources, fc.Key, fc.ObservationDate); ok {
				allNodes = append(allNodes, node)
				vsetInserts = append(vsetInserts, vsi)
			}
		default: // llm.MemAdd
			nodes, item := buildAddNodes(f, ef.embVec, ef.embedding,
				fc.CubeID, fc.UserID, fc.AgentID, fc.SessionID, fc.Now,
				fc.Info, fc.CustomTags, fc.Sources, fc.Key, fc.ObservationDate)
			allNodes = append(allNodes, nodes...)
			if item != nil {
				items = append(items, *item)
				if len(nodes) >= 2 {
					embedded[i].ltmID = nodes[1].ID
				}
				if len(ef.embedding) > 0 && len(nodes) > 0 {
					vsetInserts = append(vsetInserts, wmVSetInsert{
						id: nodes[0].ID, memory: f.Memory, embedding: ef.embedding,
					})
				}
			}
		}
	}
	return allNodes, items, vsetInserts
}

// applyAndPersistFineFacts runs the apply + insert + VSET + entity-link
// block for the orchestrator. fc carries cubeID + now.
func (h *Handler) applyAndPersistFineFacts(
	ctx context.Context,
	embedded []embeddedFact,
	fc factContext,
) ([]addResponseItem, error) {
	allNodes, items, vsetInserts := h.applyFineActionsCtx(ctx, embedded, fc)
	if len(allNodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			return nil, fmt.Errorf("fine add: insert nodes: %w", err)
		}
		if h.wmCache != nil {
			ts := nowUnix()
			for _, vi := range vsetInserts {
				if err := h.wmCache.VAdd(ctx, fc.CubeID, vi.id, vi.memory, vi.embedding, ts); err != nil {
					h.logger.Debug("fine add: vset write failed",
						slog.String("id", vi.id), slog.Any("error", err))
				}
			}
		}
		h.linkEntitiesAsync(ctx, embedded, fc.CubeID, fc.Now)
	}
	h.cleanupWorkingMemory(ctx, fc.CubeID)
	return items, nil
}

// evictFromVSet removes a memory ID from the VSET hot cache (non-fatal).
func (h *Handler) evictFromVSet(ctx context.Context, cubeID, id, logMsg string) {
	if h.wmCache == nil || id == "" {
		return
	}
	if err := h.wmCache.VRem(ctx, cubeID, id); err != nil {
		h.logger.Debug(logMsg, slog.String("id", id), slog.Any("error", err))
	}
}

// applyDeleteAction hard-deletes a memory contradicted by a newer fact.
// Bi-temporal: stamps invalid_at on outgoing edges before delete; cascade
// clears ce_score_topk on neighbours.
func (h *Handler) applyDeleteAction(ctx context.Context, targetID, cubeID string) {
	if targetID == "" {
		return
	}
	now := nowTimestamp()
	if err := h.postgres.InvalidateEdgesByMemoryID(ctx, targetID, now); err != nil {
		h.logger.Debug("fine add: invalidate memory edges failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if err := h.postgres.InvalidateEntityEdgesByMemoryID(ctx, targetID, now); err != nil {
		h.logger.Debug("fine add: invalidate entity edges failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if _, err := h.postgres.DeleteByPropertyIDs(ctx, []string{targetID}, cubeID); err != nil {
		h.logger.Debug("fine add: delete contradicted memory failed",
			slog.String("id", targetID), slog.Any("error", err))
	} else {
		h.logger.Debug("fine add: deleted contradicted memory", slog.String("id", targetID))
	}
	if err := h.postgres.ClearCEScoresTopKForNeighbor(ctx, targetID); err != nil {
		h.logger.Debug("fine add: clear ce_score_topk cascade on delete failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
}

// updateActionArgs bundles the parameters applyUpdateAction needs. Extracted
// to satisfy the >4-param style rule (CLAUDE.md "pass a struct"); callers
// build it inline at the apply site so the data flow stays obvious.
type updateActionArgs struct {
	TargetID string
	Memory   string
	EmbVec   string
	Now      string
}

// applyUpdateAction merges a fact into an existing memory and re-embeds it.
// Bi-temporal: invalidates old edges (re-created by linkEntitiesAsync with
// the new valid_at). Clears local + neighbour ce_score_topk caches.
func (h *Handler) applyUpdateAction(ctx context.Context, a updateActionArgs) {
	if a.TargetID == "" || a.Memory == "" || a.EmbVec == "" {
		return
	}
	if err := h.postgres.InvalidateEdgesByMemoryID(ctx, a.TargetID, a.Now); err != nil {
		h.logger.Debug("fine add: invalidate memory edges on update failed (non-fatal)",
			slog.String("id", a.TargetID), slog.Any("error", err))
	}
	if err := h.postgres.InvalidateEntityEdgesByMemoryID(ctx, a.TargetID, a.Now); err != nil {
		h.logger.Debug("fine add: invalidate entity edges on update failed (non-fatal)",
			slog.String("id", a.TargetID), slog.Any("error", err))
	}
	if err := h.postgres.UpdateMemoryNodeFull(ctx, a.TargetID, a.Memory, a.EmbVec, a.Now); err != nil {
		h.logger.Debug("fine add: update node failed",
			slog.String("id", a.TargetID), slog.Any("error", err))
	} else {
		h.logger.Debug("fine add: merged update", slog.String("target_id", a.TargetID))
	}
	if err := h.postgres.ClearCEScoresTopK(ctx, a.TargetID); err != nil {
		h.logger.Debug("fine add: clear ce_score_topk on update failed (non-fatal)",
			slog.String("id", a.TargetID), slog.Any("error", err))
	}
	if err := h.postgres.ClearCEScoresTopKForNeighbor(ctx, a.TargetID); err != nil {
		h.logger.Debug("fine add: clear ce_score_topk cascade on update failed (non-fatal)",
			slog.String("id", a.TargetID), slog.Any("error", err))
	}
}
