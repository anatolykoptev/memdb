package scheduler

// reorganizer_mem_read_actions.go — ADD/UPDATE/DELETE action execution for the
// fine mem_read pipeline: builds LTM insert nodes, updates/deletes existing
// memories, and wires up EXTRACTED_FROM edges.

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// applyMemoryActions applies ADD/UPDATE/DELETE actions from the embedded facts.
// Returns new LTM nodes to insert and outcome counters.
func (r *Reorganizer) applyMemoryActions(
	ctx context.Context,
	embedded []embeddedMemReadFact,
	userID, cubeID, agentID, sessionID, now string,
	log *slog.Logger,
) ([]db.MemoryInsertNode, actionCounts) {
	var allNodes []db.MemoryInsertNode
	var counts actionCounts
	for i := range embedded {
		ef := &embedded[i]
		switch ef.fact.Action {
		case llm.MemSkip:
			continue
		case llm.MemDelete:
			if r.applyMemDelete(ctx, ef.fact.TargetID, cubeID, now, log) {
				counts.deleted++
			}
		case llm.MemUpdate:
			if r.applyMemUpdate(ctx, ef, now, log) {
				counts.updated++
			}
		default: // llm.MemAdd
			node, ltmID, ok := buildLTMNode(ef, userID, cubeID, agentID, sessionID, now)
			if ok {
				allNodes = append(allNodes, node)
				ef.ltmID = ltmID
				counts.inserted++
			}
		}
	}
	return allNodes, counts
}

// applyMemDelete invalidates edges and deletes a memory node. Returns true if deleted.
func (r *Reorganizer) applyMemDelete(ctx context.Context, targetID, cubeID, now string, log *slog.Logger) bool {
	if targetID == "" {
		return false
	}
	if err := r.postgres.InvalidateEdgesByMemoryID(ctx, targetID, now); err != nil {
		log.Debug("mem_read: invalidate edges failed", slog.String("id", targetID), slog.Any("error", err))
	}
	if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, targetID, now); err != nil {
		log.Debug("mem_read: invalidate entity edges failed", slog.String("id", targetID), slog.Any("error", err))
	}
	if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{targetID}, cubeID); err != nil {
		log.Debug("mem_read: delete contradicted memory failed", slog.String("id", targetID), slog.Any("error", err))
	}
	if r.wmCache != nil {
		_ = r.wmCache.VRem(ctx, cubeID, targetID)
	}
	return true
}

// applyMemUpdate invalidates edges and updates the memory node in place. Returns true if updated.
func (r *Reorganizer) applyMemUpdate(ctx context.Context, ef *embeddedMemReadFact, now string, log *slog.Logger) bool {
	f := ef.fact
	if f.TargetID == "" || ef.embVec == "" {
		return false
	}
	if err := r.postgres.InvalidateEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
		log.Debug("mem_read: invalidate edges on update failed", slog.String("id", f.TargetID), slog.Any("error", err))
	}
	if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
		log.Debug("mem_read: invalidate entity edges on update failed", slog.String("id", f.TargetID), slog.Any("error", err))
	}
	if err := r.postgres.UpdateMemoryNodeFull(ctx, f.TargetID, f.Memory, ef.embVec, now); err != nil {
		log.Debug("mem_read: update node failed", slog.String("id", f.TargetID), slog.Any("error", err))
		return false
	}
	ef.ltmID = f.TargetID
	return true
}

// buildLTMNode constructs a MemoryInsertNode for a MemAdd fact.
// Returns (node, ltmID, ok).
func buildLTMNode(ef *embeddedMemReadFact, userID, cubeID, agentID, sessionID, now string) (db.MemoryInsertNode, string, bool) {
	f := ef.fact
	if ef.embVec == "" {
		return db.MemoryInsertNode{}, "", false
	}
	createdAt := now
	if f.ValidAt != "" {
		createdAt = f.ValidAt
	}
	memType := f.Type
	if memType != "UserMemory" {
		memType = "LongTermMemory"
	}
	ltID := uuid.New().String()

	factInfo := map[string]any{}
	if f.ContentHash != "" {
		factInfo["content_hash"] = f.ContentHash
	}
	if f.Confidence > 0 {
		factInfo["confidence"] = f.Confidence
	}
	if f.ValidAt != "" {
		factInfo["valid_at"] = f.ValidAt
	}

	props := map[string]any{
		"id": ltID, "memory": f.Memory, "memory_type": memType,
		"user_name": cubeID, "user_id": userID, "agent_id": agentID, "session_id": sessionID,
		"status": "activated", "created_at": createdAt, "updated_at": now,
		"tags":       append([]string{"mode:mem_read"}, f.Tags...),
		"background": "", "delete_time": "", "delete_record_id": "",
		"confidence": f.Confidence, "type": "fact", "info": factInfo,
		"graph_id": uuid.New().String(), "importance_score": 1.0, "retrieval_count": 0,
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return db.MemoryInsertNode{}, "", false
	}
	return db.MemoryInsertNode{ID: ltID, PropertiesJSON: propsJSON, EmbeddingVec: ef.embVec}, ltID, true
}

// insertAndLinkLTMNodes inserts allNodes and creates EXTRACTED_FROM edges to WM source nodes.
func (r *Reorganizer) insertAndLinkLTMNodes(ctx context.Context, allNodes []db.MemoryInsertNode, processedWMIDs []string, now string, log *slog.Logger) {
	if len(allNodes) == 0 {
		return
	}
	if err := r.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
		log.Warn("mem_read: InsertMemoryNodes failed", slog.Any("error", err))
		return
	}
	for _, ltmNode := range allNodes {
		for _, wmID := range processedWMIDs {
			if err := r.postgres.CreateMemoryEdge(ctx, ltmNode.ID, wmID, db.EdgeExtractedFrom, now, ""); err != nil {
				log.Debug("mem_read: create extracted_from edge failed (non-fatal)",
					slog.String("ltm_id", ltmNode.ID), slog.String("wm_id", wmID), slog.Any("error", err))
			}
		}
	}
}
