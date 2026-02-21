package handlers

// scheduler.go — native Go handlers for /product/scheduler/* endpoints.
//
// These replace the Python scheduler status proxy calls by querying Redis
// Streams directly for the Go consumer group (memdb_go_scheduler).

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

const goConsumerGroup = "memdb_go_scheduler"

// NativeSchedulerStatus handles GET /product/scheduler/status
// Returns the Go scheduler's consumer group status across all watched streams.
func (h *Handler) NativeSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if h.redis == nil {
		h.ProxyToProduct(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	info := h.buildSchedulerStatus(ctx)
	h.writeJSON(w, http.StatusOK, info)
}

// NativeSchedulerAllStatus handles GET /product/scheduler/allstatus
// Returns extended status including DLQ info.
func (h *Handler) NativeSchedulerAllStatus(w http.ResponseWriter, r *http.Request) {
	if h.redis == nil {
		h.ProxyToProduct(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	info := h.buildSchedulerStatus(ctx)

	// Add DLQ info.
	dlqLen, _ := h.redis.Client().XLen(ctx, "scheduler:dlq:v1").Result()
	info["dlq_pending"] = dlqLen
	info["dlq_stream"] = "scheduler:dlq:v1"

	h.writeJSON(w, http.StatusOK, info)
}

// NativeSchedulerTaskQueueStatus handles GET /product/scheduler/task_queue_status
// Returns simplified task queue stats.
func (h *Handler) NativeSchedulerTaskQueueStatus(w http.ResponseWriter, r *http.Request) {
	if h.redis == nil {
		h.ProxyToProduct(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	streams, total, pending := h.countSchedulerStreams(ctx)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"consumer_group": goConsumerGroup,
		"stream_count":   streams,
		"total_messages": total,
		"pending":        pending,
		"status":         "running",
	})
}

// buildSchedulerStatus collects consumer group info from all scheduler streams.
func (h *Handler) buildSchedulerStatus(ctx context.Context) map[string]any {
	streamCount, totalMessages, totalPending := h.countSchedulerStreams(ctx)

	return map[string]any{
		"consumer_group": goConsumerGroup,
		"implementation": "go_native",
		"stream_count":   streamCount,
		"total_messages": totalMessages,
		"total_pending":  totalPending,
		"status":         "running",
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
	}
}

// countSchedulerStreams scans for scheduler stream keys and aggregates counts.
func (h *Handler) countSchedulerStreams(ctx context.Context) (streams, totalMessages, totalPending int64) {
	const scanPattern = "scheduler:messages:stream:v2.0:*"
	const batchSize = 100

	rdb := h.redis.Client()
	var cursor uint64
	var keys []string

	for {
		batch, next, err := rdb.Scan(ctx, cursor, scanPattern, batchSize).Result()
		if err != nil {
			break
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}

	// Deduplicate base stream keys (drop label suffix for counting).
	seenBases := make(map[string]struct{})
	var baseKeys []string
	for _, k := range keys {
		base := streamBase(k)
		if _, seen := seenBases[base]; !seen {
			seenBases[base] = struct{}{}
			baseKeys = append(baseKeys, k)
		}
	}
	sort.Strings(baseKeys)
	streams = int64(len(baseKeys))

	for _, k := range baseKeys {
		if n, err := rdb.XLen(ctx, k).Result(); err == nil {
			totalMessages += n
		}
		// Count pending for our consumer group.
		groups, err := rdb.XInfoGroups(ctx, k).Result()
		if err != nil {
			continue
		}
		for _, g := range groups {
			if g.Name == goConsumerGroup {
				totalPending += g.Pending
				break
			}
		}
	}
	return streams, totalMessages, totalPending
}

// streamBase strips the label suffix from a scheduler stream key.
// "scheduler:messages:stream:v2.0:user:cube:mem_update" → "scheduler:messages:stream:v2.0:user:cube"
func streamBase(key string) string {
	const prefix = "scheduler:messages:stream:v2.0:"
	if !strings.HasPrefix(key, prefix) {
		return key
	}
	rest := key[len(prefix):]
	// format: userID:cubeID:label — drop the last colon-separated segment
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return key
	}
	return prefix + rest[:idx]
}
