package handlers

// add_window_chars_test.go — tests for the per-request window_chars override
// (M7 Stream C, Option A). Covers the windowSizeFor helper and verifies that
// a smaller windowSize fed into extractFastMemories produces strictly more
// memory windows than the default 4096-char setting.

import (
	"strings"
	"testing"
)

// --- windowSizeFor ---

func TestWindowSizeFor_Default(t *testing.T) {
	tests := []struct {
		name string
		req  *fullAddRequest
		want int
	}{
		{name: "nil request", req: nil, want: windowChars},
		{name: "nil pointer", req: &fullAddRequest{WindowChars: nil}, want: windowChars},
		{name: "in range — small", req: &fullAddRequest{WindowChars: intPtr(512)}, want: 512},
		{name: "in range — boundary min", req: &fullAddRequest{WindowChars: intPtr(windowCharsMin)}, want: windowCharsMin},
		{name: "in range — boundary max", req: &fullAddRequest{WindowChars: intPtr(windowCharsMax)}, want: windowCharsMax},
		{name: "in range — explicit default", req: &fullAddRequest{WindowChars: intPtr(windowChars)}, want: windowChars},
		{name: "out of range — under min", req: &fullAddRequest{WindowChars: intPtr(windowCharsMin - 1)}, want: windowChars},
		{name: "out of range — over max", req: &fullAddRequest{WindowChars: intPtr(windowCharsMax + 1)}, want: windowChars},
		{name: "out of range — zero", req: &fullAddRequest{WindowChars: intPtr(0)}, want: windowChars},
		{name: "out of range — negative", req: &fullAddRequest{WindowChars: intPtr(-100)}, want: windowChars},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowSizeFor(tt.req)
			if got != tt.want {
				t.Errorf("windowSizeFor() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- extractFastMemories with custom windowSize ---

// TestExtractFastMemories_RespectsWindowSize confirms that a smaller window
// produces strictly more memories than the default 4096 on the same long
// session. The windowing algorithm cannot split a single message — it
// accumulates whole messages until the budget is exceeded — so the test uses
// many small messages to make the window boundary the dominant variable.
func TestExtractFastMemories_RespectsWindowSize(t *testing.T) {
	// 60 short messages × ~80 chars body each → ~5–6 KB of formatted text
	// (formatMessages adds "role: [chat_time]: " prefix per line).
	const numMessages = 60
	msgs := make([]chatMessage, numMessages)
	for i := range msgs {
		msgs[i] = chatMessage{
			Role: "user",
			// Short, distinct content so each message is well under any tested window.
			Content:  strings.Repeat("alpha ", 12), // 72 chars
			ChatTime: "2026-02-16T10:00:00",
		}
	}

	defaultResults := extractFastMemories(msgs, windowChars)
	smallResults := extractFastMemories(msgs, 512)

	if len(defaultResults) == 0 {
		t.Fatalf("default windowChars produced 0 memories; expected ≥1")
	}
	if len(smallResults) <= len(defaultResults) {
		t.Errorf("smaller windowSize=512 produced %d memories, expected strictly more than default windowChars=%d which produced %d",
			len(smallResults), windowChars, len(defaultResults))
	}
	// Sanity check: 512-char window over ~5–6 KB of formatted text should
	// yield several windows. Accept ≥6 as a loose floor.
	if len(smallResults) < 6 {
		t.Errorf("expected ≥6 memories for windowSize=512, got %d", len(smallResults))
	}
}

// TestExtractFastMemories_ZeroWindowSize verifies the defence-in-depth guard:
// passing 0 (or negative) directly to extractFastMemories must default to
// windowChars rather than entering an infinite loop.
func TestExtractFastMemories_ZeroWindowSize(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "hello world", ChatTime: "2026-02-16T10:00:00"},
	}

	zeroResults := extractFastMemories(msgs, 0)
	negResults := extractFastMemories(msgs, -100)
	defaultResults := extractFastMemories(msgs, windowChars)

	if len(zeroResults) != len(defaultResults) {
		t.Errorf("windowSize=0 produced %d memories; expected same as default %d",
			len(zeroResults), len(defaultResults))
	}
	if len(negResults) != len(defaultResults) {
		t.Errorf("windowSize=-100 produced %d memories; expected same as default %d",
			len(negResults), len(defaultResults))
	}
}

// --- helpers ---

func intPtr(v int) *int { return &v }
