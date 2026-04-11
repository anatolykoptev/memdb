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
)

// extractedMemory is a single memory window produced by the sliding-window algorithm.
type extractedMemory struct {
	Text       string
	Sources    []map[string]any
	MemoryType string // "LongTermMemory" or "UserMemory"
}

// extractFastMemories splits messages into sliding windows of ~windowChars characters.
// Each window becomes one memory candidate. Windows containing only user messages
// are classified as UserMemory; mixed windows become LongTermMemory.
func extractFastMemories(messages []chatMessage) []extractedMemory {
	if len(messages) == 0 {
		return nil
	}

	formatted := formatMessages(messages)

	var results []extractedMemory
	start := 0

	for start < len(formatted) {
		window, end := buildWindow(formatted, start)
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

// formattedMsg is an intermediate representation of a single message.
type formattedMsg struct {
	text   string
	role   string
	source map[string]any
}

// formatMessages converts raw chatMessages into formattedMsgs with pre-built source maps.
func formatMessages(messages []chatMessage) []formattedMsg {
	out := make([]formattedMsg, 0, len(messages))
	for _, msg := range messages {
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = time.Now().UTC().Format("2006-01-02T15:04:05")
		}
		out = append(out, formattedMsg{
			text: fmt.Sprintf("%s: [%s]: %s", msg.Role, chatTime, msg.Content),
			role: msg.Role,
			source: map[string]any{
				"role":      msg.Role,
				"content":   msg.Content,
				"chat_time": chatTime,
			},
		})
	}
	return out
}

// buildWindow accumulates messages starting at start until the window exceeds windowChars.
// Returns the assembled extractedMemory and the exclusive end index.
func buildWindow(msgs []formattedMsg, start int) (*extractedMemory, int) {
	var sb strings.Builder
	var sources []map[string]any
	userOnly := true
	end := start

	for end < len(msgs) {
		line := msgs[end].text + "\n"
		if sb.Len()+len(line) > windowChars && sb.Len() > 0 {
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
