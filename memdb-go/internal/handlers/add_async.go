package handlers

// add_async.go — async-mode memory add pipeline.
//
// Async is "accept fast, process later":
//  1. Extract sliding-window text (CPU-only, no ONNX, no dedup)
//  2. Insert lightweight WM nodes with zero embedding (temporary staging)
//  3. XADD mem_read → Go Worker: LLM enhance → embed → insert LTM → delete WM
//  4. XADD pref_add → Go Worker: extract preferences from messages
//
// This mirrors Python's async path: raw WM insert + schedule background tasks.
// Target response time: <50ms (vs ~1s for sync fast pipeline).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
)

// zeroVecDim is the dimension of the zero vector used for WM-only inserts (must match ONNX model dim).
const zeroVecDim = 1024

// zeroVec1024 is a pre-allocated zero vector string for WM-only inserts.
// WM nodes are temporary — Worker deletes them after LLM enhancement.
// Zero vector won't match any real vector search (cosine = 0).
var zeroVec1024 = func() string {
	v := make([]float32, zeroVecDim)
	return db.FormatVector(v)
}()

// nativeAsyncAddForCube runs the async pipeline for a single cube.
// Returns immediately with WM items; background tasks handle LTM transfer.
func (h *Handler) nativeAsyncAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	userID := *req.UserID
	sessionID := stringOrEmpty(req.SessionID)
	info := mapOrEmpty(req.Info)
	now := nowTimestamp()

	// --- Step 1: extract sliding-window text (CPU-only) ---
	memories := extractFastMemories(req.Messages)
	if len(memories) == 0 {
		// No text to process — still submit pref_add if messages present
		h.submitPrefAdd(ctx, req, cubeID)
		return nil, nil
	}

	// --- Step 2: build + insert WM-only nodes (zero embedding) ---
	var nodes []db.MemoryInsertNode
	var items []addResponseItem

	for _, mem := range memories {
		wmID := uuid.New().String()

		memInfo := make(map[string]any, len(info)+1)
		for k, v := range info {
			memInfo[k] = v
		}

		wmJSON, err := marshalProps(buildMemoryProperties(
			wmID, mem.Text, "WorkingMemory", cubeID, stringOrEmpty(req.AgentID),
			sessionID, now, memInfo, req.CustomTags, mem.Sources, "",
		))
		if err != nil {
			return nil, fmt.Errorf("async: marshal wm props: %w", err)
		}

		nodes = append(nodes, db.MemoryInsertNode{
			ID:             wmID,
			PropertiesJSON: wmJSON,
			EmbeddingVec:   zeroVec1024,
		})
		items = append(items, addResponseItem{
			Memory:     mem.Text,
			MemoryID:   wmID,
			MemoryType: "WorkingMemory",
			CubeID:     cubeID,
		})
	}

	if err := h.postgres.InsertMemoryNodes(ctx, nodes); err != nil {
		return nil, fmt.Errorf("async: insert wm nodes: %w", err)
	}

	// --- Step 3: XADD mem_read (WM→LTM transfer via Worker) ---
	wmIDs := make([]string, len(items))
	for i, it := range items {
		wmIDs[i] = it.MemoryID
	}
	h.submitStreamTask(ctx, userID, cubeID, scheduler.LabelMemRead, wmIDs, req)

	// --- Step 4: XADD pref_add ---
	h.submitPrefAdd(ctx, req, cubeID)

	h.logger.Info("async add: queued",
		slog.String("cube_id", cubeID),
		slog.Int("wm_nodes", len(items)),
	)

	return items, nil
}

// submitStreamTask XADDs a task to Redis Streams and registers it in TaskStatusTracker.
func (h *Handler) submitStreamTask(ctx context.Context, userID, cubeID, label string, ids []string, req *fullAddRequest) {
	rdb := h.redis.Client()
	contentJSON, _ := json.Marshal(ids)
	itemID := uuid.New().String()
	taskID := stringOrEmpty(req.TaskID)
	streamKey := fmt.Sprintf("%s:%s:%s:%s", scheduler.StreamKeyPrefix, userID, cubeID, label)

	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]any{
			"item_id":   itemID,
			"user_id":   userID,
			"cube_id":   cubeID,
			"label":     label,
			"content":   string(contentJSON),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"user_name": cubeID,
			"task_id":   taskID,
		},
	}).Err(); err != nil {
		h.logger.Error("async: xadd failed", slog.String("label", label), slog.Any("error", err))
		return
	}
	h.trackSubmitted(ctx, itemID, userID, cubeID, label)
}

// submitPrefAdd XADDs a pref_add task if messages are present.
func (h *Handler) submitPrefAdd(ctx context.Context, req *fullAddRequest, cubeID string) {
	if len(req.Messages) == 0 {
		return
	}
	rdb := h.redis.Client()
	userID := *req.UserID
	messagesJSON, _ := json.Marshal([][]chatMessage{req.Messages})
	itemID := uuid.New().String()
	taskID := stringOrEmpty(req.TaskID)
	streamKey := fmt.Sprintf("%s:%s:%s:%s", scheduler.StreamKeyPrefix, userID, cubeID, scheduler.LabelPrefAdd)

	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]any{
			"item_id":   itemID,
			"user_id":   userID,
			"cube_id":   cubeID,
			"label":     scheduler.LabelPrefAdd,
			"content":   string(messagesJSON),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"user_name": cubeID,
			"task_id":   taskID,
		},
	}).Err(); err != nil {
		h.logger.Error("async: xadd pref_add failed", slog.Any("error", err))
		return
	}
	h.trackSubmitted(ctx, itemID, userID, cubeID, scheduler.LabelPrefAdd)
}

// trackSubmitted registers a task as "waiting" in the TaskStatusTracker
// so /scheduler/wait can poll for completion.
func (h *Handler) trackSubmitted(ctx context.Context, itemID, userID, cubeID, label string) {
	if h.tracker == nil {
		return
	}
	h.tracker.TaskSubmitted(ctx, scheduler.ScheduleMessage{
		ItemID: itemID,
		UserID: userID,
		CubeID: cubeID,
		Label:  label,
	})
}
