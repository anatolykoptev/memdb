package llm

import (
	"fmt"
	"strings"
	"time"
)

// FormatChatTime returns the canonical RFC3339-UTC representation used
// for Message.ChatTime. Aligned with MemDB ingestion (the indexed
// observation_date is the first 10 chars).
//
//	m.ChatTime = llm.FormatChatTime(time.Now())
//
// Zero time returns "" so callers can pipe time.Time through this
// helper unconditionally — empty ChatTime keeps the message
// unannotated.
func FormatChatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ParseChatTime parses an RFC3339 string written by FormatChatTime.
// Returns the zero time if s is empty or unparseable so callers can
// always trust the returned value's IsZero check.
func ParseChatTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// applyMessageTimestamps mutates messages in place: for each message
// with a non-empty ChatTime and string-typed Content, it prepends
// "[YYYY-MM-DD HH:MM UTC] " to the content. Multimodal messages
// (Content is []ContentPart) and messages without ChatTime are left
// untouched.
//
// The bracketed shorthand is shorter than the wire ChatTime (RFC3339)
// to keep tokens down — minute-precision is plenty for agents
// reasoning about message recency.
func applyMessageTimestamps(messages []Message) {
	for i := range messages {
		ct := messages[i].ChatTime
		if ct == "" {
			continue
		}
		text, ok := messages[i].Content.(string)
		if !ok {
			continue
		}
		t := ParseChatTime(ct)
		if t.IsZero() {
			continue
		}
		var b strings.Builder
		b.Grow(len(text) + 24)
		fmt.Fprintf(&b, "[%s UTC] ", t.UTC().Format("2006-01-02 15:04"))
		b.WriteString(text)
		messages[i].Content = b.String()
	}
}
