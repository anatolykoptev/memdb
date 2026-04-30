package handlers

// add_windowing.go — sliding-window extraction of fast-mode memories from messages.
// Responsibility: split a flat list of chat messages into overlapping text windows,
// classify each window as UserMemory or LongTermMemory. No I/O, no LLM calls.

import (
	"fmt"
	"strings"
	"time"
)

const (
	roleUser        = "user"
	memTypeLongTerm = "LongTermMemory"
	memTypeUser     = "UserMemory"
	modeFast        = "fast"
	modeFine        = "fine"
	modeAsync       = "async"
	modeRaw         = "raw"

	// fastMsgKind is the value stamped into properties.kind for every
	// per-message fast row. Sibling discriminator to atomicFactKind on the
	// atomic path. Without this, fast rows fall through to migration 0022's
	// COALESCE default 'paragraph_legacy' and search filters like
	// kind='fast_msg' silently exclude them.
	fastMsgKind = "fast_msg"
)

// extractedMemory is a single memory unit produced by the fast-add extractor.
// Two extractor flavours feed this struct:
//   - extractFastMemoriesPerMessage  → 1 row per message, Sources len = 1
//   - extractFastMemories (windowed) → 1 row per ~windowChars-budget window
//
// The downstream pipeline (buildFastNodes / WM+LTM pair) treats both
// uniformly; the only difference is granularity. Window mode is preserved
// for legacy A/B and the WindowChars override path.
type extractedMemory struct {
	Text       string
	Sources    []map[string]any
	MemoryType string // "LongTermMemory" or "UserMemory"
}

// Allowed range for per-request window-size overrides. Values outside this
// range fall back to the default windowChars constant.
const (
	windowCharsMin = 128
	windowCharsMax = 16384
)

// windowSizeFor returns the window-char size for an add request.
// Honours req.WindowChars when set and within [windowCharsMin, windowCharsMax];
// otherwise returns the default windowChars constant. Out-of-range, zero, and
// negative values fall back silently — this is a tuning hint, not a contract.
func windowSizeFor(req *fullAddRequest) int {
	if req == nil || req.WindowChars == nil {
		return windowChars
	}
	v := *req.WindowChars
	if v < windowCharsMin || v > windowCharsMax {
		return windowChars
	}
	return v
}

// extractFastMemories splits messages into sliding windows of ~windowSize characters.
// Each window becomes one memory candidate. Windows containing only user messages
// are classified as UserMemory; mixed windows become LongTermMemory.
// windowSize ≤ 0 falls back to the default windowChars constant (defence-in-depth;
// callers should funnel through windowSizeFor which already guards this).
func extractFastMemories(messages []chatMessage, windowSize int) []extractedMemory {
	if len(messages) == 0 {
		return nil
	}
	if windowSize <= 0 {
		windowSize = windowChars
	}

	formatted := formatMessages(messages)

	var results []extractedMemory
	start := 0

	for start < len(formatted) {
		window, end := buildWindow(formatted, start, windowSize)
		if window == nil {
			break
		}
		results = append(results, *window)

		if end >= len(formatted) {
			break
		}
		start = advanceStart(formatted, start, end)
	}

	return results
}

// extractFastMemoriesPerMessage emits one extractedMemory per non-empty
// message — same granularity as raw mode but riding the fast pipeline
// (batched embed + WM/LTM pair + VSET hot cache).
//
// Why this exists: the windowed extractor (extractFastMemories) bundles
// 18+ messages into ~3 windows, dissolving per-message facts ("Melanie has
// 3 kids") into average-pooled embeddings. cat-1 simple-fact retrieval
// hit@k drops to zero on the windowed path. Per-message rows preserve the
// fact in its own embedding, fixable by the same retrieve→rerank stack
// that already works for raw.
//
// MemoryType: same heuristic as the windowed path — assistant role lifts
// the row to LongTermMemory, otherwise UserMemory. Single-msg windows
// can't be "mixed", so the userOnly check collapses to role inspection.
func extractFastMemoriesPerMessage(messages []chatMessage) []extractedMemory {
	if len(messages) == 0 {
		return nil
	}
	out := make([]extractedMemory, 0, len(messages))
	for _, msg := range messages {
		trimmed := strings.TrimSpace(msg.Content)
		if trimmed == "" {
			continue
		}
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = time.Now().UTC().Format("2006-01-02T15:04:05")
		}
		src := map[string]any{
			"role":      msg.Role,
			"content":   msg.Content,
			"chat_time": chatTime,
		}
		if msg.UUID != "" {
			src["uuid"] = msg.UUID
		}
		if msg.AgentID != "" {
			src["agent_id"] = msg.AgentID
		}
		memType := memTypeLongTerm
		if msg.Role == roleUser {
			memType = memTypeUser
		}
		out = append(out, extractedMemory{
			Text:       fmt.Sprintf("%s: [%s]: %s", msg.Role, chatTime, msg.Content),
			Sources:    []map[string]any{src},
			MemoryType: memType,
		})
	}
	return out
}

// formattedMsg is an intermediate representation of a single message.
type formattedMsg struct {
	text   string
	role   string
	source map[string]any
}

// formatMessages converts raw chatMessages into formattedMsgs with pre-built source maps.
// Per-message uuid and agent_id propagate into the source map when supplied so
// downstream consumers (and future per-msg dedup) can address the original
// message. Mirror of buildSourcesFromMessages in add_props.go — keep both
// in sync when extending chatMessage with new optional passthrough fields.
func formatMessages(messages []chatMessage) []formattedMsg {
	out := make([]formattedMsg, 0, len(messages))
	for _, msg := range messages {
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = time.Now().UTC().Format("2006-01-02T15:04:05")
		}
		src := map[string]any{
			"role":      msg.Role,
			"content":   msg.Content,
			"chat_time": chatTime,
		}
		if msg.UUID != "" {
			src["uuid"] = msg.UUID
		}
		if msg.AgentID != "" {
			src["agent_id"] = msg.AgentID
		}
		out = append(out, formattedMsg{
			text:   fmt.Sprintf("%s: [%s]: %s", msg.Role, chatTime, msg.Content),
			role:   msg.Role,
			source: src,
		})
	}
	return out
}

// buildWindow accumulates messages starting at start until the window exceeds windowSize.
// Returns the assembled extractedMemory and the exclusive end index.
func buildWindow(msgs []formattedMsg, start, windowSize int) (*extractedMemory, int) {
	var sb strings.Builder
	var sources []map[string]any
	userOnly := true
	end := start

	for end < len(msgs) {
		line := msgs[end].text + "\n"
		if sb.Len()+len(line) > windowSize && sb.Len() > 0 {
			break
		}
		sb.WriteString(line)
		sources = append(sources, msgs[end].source)
		if msgs[end].role != roleUser {
			userOnly = false
		}
		end++
	}

	if sb.Len() == 0 {
		return nil, end
	}

	memType := memTypeLongTerm
	if userOnly {
		memType = memTypeUser
	}

	return &extractedMemory{
		Text:       strings.TrimSpace(sb.String()),
		Sources:    sources,
		MemoryType: memType,
	}, end
}

// advanceStart moves the window start forward so the next window overlaps by ~overlapChars.
func advanceStart(msgs []formattedMsg, start, end int) int {
	// Calculate total chars in current window to find the non-overlap point.
	var total int
	for i := start; i < end; i++ {
		total += len(msgs[i].text) + 1 // +1 for \n
	}
	overlapTarget := total - overlapChars

	charCount := 0
	newStart := start
	for newStart < end {
		charCount += len(msgs[newStart].text) + 1
		newStart++
		if charCount >= overlapTarget {
			break
		}
	}
	if newStart == start {
		newStart = start + 1 // always make forward progress
	}
	return newStart
}
