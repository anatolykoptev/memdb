// Package scheduler implements a Redis Streams consumer for MemDB background tasks.
// It replaces the Python scheduler's RabbitMQ dependency with Redis Streams.
//
// Stream key format: scheduler:messages:stream:v2.0:{user_id}:{cube_id}:{label}
// Consumer group:    memdb_go_scheduler  (independent from Python's scheduler_group)
//
// Labels handled natively in Go:
//   - "add"          → XACK and drop (add already executed by Go pipeline)
//   - "mem_organize" → run Memory Reorganizer for the cube
//   - "mem_update"   → RefreshWorkingMemory: embed query → SearchLTMByVector → VAdd
//   - "query"        → pre-emptive RefreshWorkingMemory (before Python relay)
//   - "mem_read"     → ProcessRawMemory: parseMemReadIDs → llmEnhance → insert LTM → delete WM
//   - "mem_feedback" → full: ProcessFeedback → LLM keep/update/remove → UpdateMemoryNodeFull / delete
//
// Labels XACK'd (Python's scheduler_group handles):
//   - "answer", "mem_archive"
package scheduler

import (
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// StreamKeyPrefix is the Redis key prefix for all scheduler streams.
	// Must match the Python scheduler's DEFAULT_STREAM_KEY_PREFIX.
	StreamKeyPrefix = "scheduler:messages:stream:v2.0"

	// ConsumerGroup is the Go worker's consumer group name.
	// Intentionally different from Python's "scheduler_group" so both can co-exist.
	ConsumerGroup = "memdb_go_scheduler"

	// retryZSetKey is the Redis Sorted Set used for delayed retry scheduling.
	// Score = Unix timestamp (seconds) when the task should be retried.
	// Inspired by MemOS RedisStreamScheduler redis_scheduled_tasks_zset_key.
	retryZSetKey = "scheduler:retry:v1"

	// defaultMaxRetries is the number of retry attempts before a task goes to DLQ.
	defaultMaxRetries = 3

	// retryBaseDelay is the initial backoff delay. Doubles on each attempt.
	// Attempt 1: 5s, Attempt 2: 10s, Attempt 3: 20s → DLQ.
	retryBaseDelay = 5 * time.Second

	// retryMaxDelay caps the exponential backoff.
	retryMaxDelay = 5 * time.Minute

	// retryPollInterval is how often retryLoop checks the ZSet for due tasks.
	retryPollInterval = 5 * time.Second

	// --- Labels handled natively in Go ---

	// LabelAdd: add task — already executed by Go add pipeline; XACK and drop.
	LabelAdd = "add"

	// LabelMemOrganize: triggers Memory Reorganizer (FindNearDuplicates → LLM merge).
	LabelMemOrganize = "mem_organize"

	// --- Labels delegated to Python (XACK our group, Python's scheduler_group handles) ---
	//
	// LabelMemRead: raw WorkingMemory IDs → LLM enhancement → LTM insert → delete WM.
	// Go-native: parseMemReadIDs (JSON|CSV) → ProcessRawMemory → llmEnhance → embed → insert LTM → delete WM + VSET evict.
	// Mirrors Python's fine_transfer_simple_mem in GeneralScheduler.
	// Note: when Go add pipeline is the only path, this label will stop being generated.
	LabelMemRead = "mem_read"

	// LabelMemUpdate: WorkingMemory refresh by query history.
	// Go-native: embed query → SearchLTMByVector (cosine ≥ 0.60, top-10) → VAdd in VSET.
	// Mirrors Python's process_session_turn in GeneralScheduler.
	LabelMemUpdate = "mem_update"

	// LabelPrefAdd: preference extraction from conversation → store as UserMemory in Postgres.
	// Go-native: parsePrefConversation → ExtractAndStorePreferences → llmExtractPreferences → embed → insert UserMemory.
	LabelPrefAdd = "pref_add"

	// LabelQuery: logs user query as addMessage event; re-submits as mem_update.
	// Pure logging + routing — no memory state change.
	LabelQuery = "query"

	// LabelAnswer: logs assistant answer as addMessage event.
	// Pure logging — no memory state change.
	LabelAnswer = "answer"

	// LabelMemFeedback: processes user feedback → update/remove specific memories.
	// Go-native (full): parseFeedbackPayload → ProcessFeedback → llmAnalyzeFeedback
	// (keep/update/remove) → UpdateMemoryNodeFull / DeleteByPropertyIDs + VSET evict.
	// Falls back to RunTargeted near-duplicate consolidation on LLM error.
	LabelMemFeedback = "mem_feedback"

	// LabelMemArchive: defined in Python task_schemas but not registered in GeneralScheduler.
	// Reserved for future use.
	LabelMemArchive = "mem_archive"
)

// ScheduleMessage is a task delivered via a Redis Stream entry.
type ScheduleMessage struct {
	ItemID     string
	UserID     string
	CubeID     string // stored as user_name in Memory properties
	Label      string
	Content    string
	Timestamp  time.Time
	UserName   string
	TaskID     string
	MsgID      string // Redis stream entry ID — required for XACK
	StreamKey  string
	RetryCount int // number of attempts already made (0 = first attempt)
	MaxRetries int // max retries before DLQ (0 = use defaultMaxRetries)
	HighPriority bool // true → routed to highMsgCh, processed before low-priority tasks
}

// isHighPriority returns true for user-triggered, latency-sensitive labels.
// High-priority messages are processed before background tasks.
//
// HIGH: mem_update, query, mem_feedback — user is waiting for a response.
// LOW:  mem_organize, mem_read, pref_add, add, answer — background work.
func isHighPriority(label string) bool {
	switch label {
	case LabelMemUpdate, LabelQuery, LabelMemFeedback:
		return true
	}
	return false
}

// maxRetries returns the effective max retries for this message.
func (m ScheduleMessage) maxRetries() int {
	if m.MaxRetries > 0 {
		return m.MaxRetries
	}
	return defaultMaxRetries
}

// retryDelay returns the backoff delay for the next retry attempt.
// Uses exponential backoff: baseDelay * 2^retryCount, capped at retryMaxDelay.
func (m ScheduleMessage) retryDelay() time.Duration {
	delay := retryBaseDelay
	for i := 0; i < m.RetryCount; i++ {
		delay *= 2
		if delay > retryMaxDelay {
			return retryMaxDelay
		}
	}
	return delay
}

// fromXMessage parses a go-redis XMessage into a ScheduleMessage.
// Returns an error only if mandatory fields are missing.
func fromXMessage(streamKey string, msg redis.XMessage) (ScheduleMessage, error) {
	get := func(key string) string {
		if v, ok := msg.Values[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	cubeID := get("cube_id")
	if cubeID == "" {
		return ScheduleMessage{}, fmt.Errorf("missing cube_id in stream entry %s", msg.ID)
	}
	label := get("label")
	if label == "" {
		return ScheduleMessage{}, fmt.Errorf("missing label in stream entry %s", msg.ID)
	}

	var ts time.Time
	if tsStr := get("timestamp"); tsStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			ts = t
		} else if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			ts = t
		}
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	return ScheduleMessage{
		ItemID:       get("item_id"),
		UserID:       get("user_id"),
		CubeID:       cubeID,
		Label:        label,
		Content:      get("content"),
		Timestamp:    ts,
		UserName:     get("user_name"),
		TaskID:       get("task_id"),
		MsgID:        msg.ID,
		StreamKey:    streamKey,
		HighPriority: isHighPriority(label),
	}, nil
}
