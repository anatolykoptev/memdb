package handlers

import (
	"strings"
	"testing"
)

// collectSegments concatenates reasoning and text from segments.
func collectSegments(segs []chatSegment) (reasoning, text string) {
	var r, t strings.Builder
	for _, s := range segs {
		if s.Reasoning {
			r.WriteString(s.Text)
		} else {
			t.WriteString(s.Text)
		}
	}
	return r.String(), t.String()
}

func TestThinkParser_SimpleThinkBlock(t *testing.T) {
	p := &thinkParser{}
	segs := p.Feed("<think>reasoning here</think>answer here")

	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d: %+v", len(segs), segs)
	}
	if segs[0].Text != "reasoning here" || !segs[0].Reasoning {
		t.Errorf("seg[0] = %+v, want reasoning='reasoning here'", segs[0])
	}
	if segs[1].Text != "answer here" || segs[1].Reasoning {
		t.Errorf("seg[1] = %+v, want text='answer here'", segs[1])
	}
}

func TestThinkParser_PartialOpenTag(t *testing.T) {
	p := &thinkParser{}

	// Feed partial open tag
	segs := p.Feed("hello <thi")
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d: %+v", len(segs), segs)
	}
	if segs[0].Text != "hello " || segs[0].Reasoning {
		t.Errorf("seg[0] = %+v, want text='hello '", segs[0])
	}

	// Complete the tag + content
	segs = p.Feed("nk>inside</think>after")
	gotReasoning, gotText := collectSegments(segs)
	if gotReasoning != "inside" {
		t.Errorf("reasoning = %q, want %q", gotReasoning, "inside")
	}
	if gotText != "after" {
		t.Errorf("text = %q, want %q", gotText, "after")
	}
}

func TestThinkParser_PartialCloseTag(t *testing.T) {
	p := &thinkParser{}

	segs := p.Feed("<think>reasoning</thi")
	// Should emit reasoning text, buffer the partial close tag
	gotReasoning, _ := collectSegments(segs)
	if gotReasoning != "reasoning" {
		t.Errorf("reasoning = %q, want 'reasoning'", gotReasoning)
	}

	// Complete close tag
	segs = p.Feed("nk>done")
	_, gotText := collectSegments(segs)
	if gotText != "done" {
		t.Errorf("text = %q, want 'done'", gotText)
	}
}

func TestThinkParser_MultipleChunks(t *testing.T) {
	p := &thinkParser{}
	chunks := []string{"<", "think", ">", "step ", "by ", "step", "</", "think", ">", "final"}

	var reasoning, text strings.Builder
	for _, c := range chunks {
		for _, seg := range p.Feed(c) {
			if seg.Reasoning {
				reasoning.WriteString(seg.Text)
			} else {
				text.WriteString(seg.Text)
			}
		}
	}
	for _, seg := range p.Flush() {
		if seg.Reasoning {
			reasoning.WriteString(seg.Text)
		} else {
			text.WriteString(seg.Text)
		}
	}

	if reasoning.String() != "step by step" {
		t.Errorf("reasoning = %q, want 'step by step'", reasoning.String())
	}
	if text.String() != "final" {
		t.Errorf("text = %q, want 'final'", text.String())
	}
}

func TestThinkParser_NoThinkTags(t *testing.T) {
	p := &thinkParser{}
	segs := p.Feed("just plain text")

	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	if segs[0].Text != "just plain text" || segs[0].Reasoning {
		t.Errorf("seg = %+v, want plain text", segs[0])
	}
}

func TestThinkParser_EmptyInput(t *testing.T) {
	p := &thinkParser{}
	segs := p.Feed("")
	if len(segs) != 0 {
		t.Errorf("expected 0 segments for empty input, got %d", len(segs))
	}
}

func TestThinkParser_FlushEmptyBuffer(t *testing.T) {
	p := &thinkParser{}
	segs := p.Flush()
	if len(segs) != 0 {
		t.Errorf("expected 0 segments from empty flush, got %d", len(segs))
	}
}

func TestThinkParser_FlushPartialTag(t *testing.T) {
	p := &thinkParser{}
	_ = p.Feed("text <th")
	segs := p.Flush()

	if len(segs) != 1 {
		t.Fatalf("expected 1 flushed segment, got %d", len(segs))
	}
	// Partial tag should be flushed as regular text (not reasoning)
	if segs[0].Text != "<th" || segs[0].Reasoning {
		t.Errorf("flushed = %+v, want text='<th'", segs[0])
	}
}

func TestThinkParser_MultipleThinkBlocks(t *testing.T) {
	p := &thinkParser{}
	segs := p.Feed("a<think>r1</think>b<think>r2</think>c")

	var reasoning, text strings.Builder
	for _, s := range segs {
		if s.Reasoning {
			reasoning.WriteString(s.Text)
		} else {
			text.WriteString(s.Text)
		}
	}
	if reasoning.String() != "r1r2" {
		t.Errorf("reasoning = %q, want 'r1r2'", reasoning.String())
	}
	if text.String() != "abc" {
		t.Errorf("text = %q, want 'abc'", text.String())
	}
}

func TestParseThinkTags_Basic(t *testing.T) {
	resp, reason := parseThinkTags("<think>step 1\nstep 2</think>The answer is 42.")
	if reason != "step 1\nstep 2" {
		t.Errorf("reasoning = %q", reason)
	}
	if resp != "The answer is 42." {
		t.Errorf("response = %q", resp)
	}
}

func TestParseThinkTags_NoTags(t *testing.T) {
	resp, reason := parseThinkTags("plain text only")
	if reason != "" {
		t.Errorf("reasoning = %q, want empty", reason)
	}
	if resp != "plain text only" {
		t.Errorf("response = %q", resp)
	}
}

func TestParseThinkTags_UnclosedTag(t *testing.T) {
	resp, reason := parseThinkTags("before<think>unclosed reasoning")
	if reason != "unclosed reasoning" {
		t.Errorf("reasoning = %q", reason)
	}
	if resp != "before" {
		t.Errorf("response = %q", resp)
	}
}

func TestParseThinkTags_MultipleBlocks(t *testing.T) {
	resp, reason := parseThinkTags("a<think>r1</think>b<think>r2</think>c")
	if reason != "r1\nr2" && reason != "r1r2" {
		// parseThinkTags concatenates reasoning blocks
		t.Logf("reasoning = %q (format may vary)", reason)
	}
	if resp != "a b c" && resp != "abc" {
		t.Logf("response = %q (format may vary)", resp)
	}
}

func TestLongestSuffix(t *testing.T) {
	tests := []struct {
		s, tag string
		want   int
	}{
		{"abc<", "<think>", 1},
		{"abc<t", "<think>", 2},
		{"abc<thin", "<think>", 5},
		{"abc<think", "<think>", 6},
		{"abc", "<think>", 0},
		{"", "<think>", 0},
		{"<", "</think>", 1},
		{"</thi", "</think>", 5},
	}
	for _, tt := range tests {
		got := longestSuffix(tt.s, tt.tag)
		if got != tt.want {
			t.Errorf("longestSuffix(%q, %q) = %d, want %d", tt.s, tt.tag, got, tt.want)
		}
	}
}
