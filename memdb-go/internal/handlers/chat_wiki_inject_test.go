package handlers

import (
	"strings"
	"testing"
)

func TestWikiInjectEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"True", true},
		{"YES", true},
		{"on", true},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiSearchInjectEnv, tc.val)
			if got := wikiInjectEnabled(); got != tc.want {
				t.Errorf("wikiInjectEnabled(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestWikiInjectTopK(t *testing.T) {
	cases := []struct {
		val  string
		want int
	}{
		{"", defaultWikiInjectTopK},
		{"abc", defaultWikiInjectTopK},
		{"0", wikiInjectTopKMin},
		{"-5", wikiInjectTopKMin},
		{"1", 1},
		{"5", 5},
		{"10", 10},
		{"99", wikiInjectTopKMax},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiInjectTopKEnv, tc.val)
			if got := wikiInjectTopK(); got != tc.want {
				t.Errorf("wikiInjectTopK(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

func TestWikiInjectMaxBodyTokens(t *testing.T) {
	cases := []struct {
		val  string
		want int
	}{
		{"", defaultWikiInjectMaxBodyTokens},
		{"junk", defaultWikiInjectMaxBodyTokens},
		{"50", wikiInjectMaxBodyTokensMin},
		{"100", 100},
		{"800", 800},
		{"4000", 4000},
		{"99999", wikiInjectMaxBodyTokensMax},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiInjectMaxBodyTokensEnv, tc.val)
			if got := wikiInjectMaxBodyTokens(); got != tc.want {
				t.Errorf("wikiInjectMaxBodyTokens(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

func TestRenderWikiInjectBlock_Empty(t *testing.T) {
	if got := renderWikiInjectBlock(nil, 800); got != "" {
		t.Errorf("expected empty string for nil pages, got %q", got)
	}
	if got := renderWikiInjectBlock([]dbWikiPage{}, 800); got != "" {
		t.Errorf("expected empty string for empty pages, got %q", got)
	}
}

func TestRenderWikiInjectBlock_SinglePage(t *testing.T) {
	pages := []dbWikiPage{
		{slug: "auto/L1/abc123", title: "User runs three businesses", body: "# User runs three businesses\n\nFood truck, dog grooming, podcast."},
	}
	got := renderWikiInjectBlock(pages, 800)
	if !strings.HasPrefix(got, "## Wiki Synthesis\n\n") {
		t.Errorf("missing header, got: %q", got)
	}
	if !strings.Contains(got, "### User runs three businesses") {
		t.Errorf("missing title heading, got: %q", got)
	}
	if !strings.Contains(got, "Food truck, dog grooming, podcast.") {
		t.Errorf("missing body, got: %q", got)
	}
	if strings.Contains(got, "---") {
		t.Errorf("single page should not include separator, got: %q", got)
	}
}

func TestRenderWikiInjectBlock_MultiPage(t *testing.T) {
	pages := []dbWikiPage{
		{slug: "auto/L1/aaa", title: "Topic A", body: "Body A."},
		{slug: "auto/L1/bbb", title: "Topic B", body: "Body B."},
		{slug: "auto/L1/ccc", title: "Topic C", body: "Body C."},
	}
	got := renderWikiInjectBlock(pages, 800)
	for _, want := range []string{"### Topic A", "### Topic B", "### Topic C", "Body A.", "Body B.", "Body C."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %q", want, got)
		}
	}
	if n := strings.Count(got, "\n---\n"); n != 2 {
		t.Errorf("expected 2 separators between 3 pages, got %d", n)
	}
}

func TestRenderWikiInjectBlock_TitleFallback(t *testing.T) {
	pages := []dbWikiPage{
		{slug: "auto/L1/zzz", title: "   ", body: "Body."},
	}
	got := renderWikiInjectBlock(pages, 800)
	if !strings.Contains(got, "### auto/L1/zzz") {
		t.Errorf("expected slug fallback when title blank, got: %q", got)
	}
}

func TestTruncateMarkdown_BelowBudget(t *testing.T) {
	body := "Short body."
	if got := truncateMarkdown(body, 800); got != body {
		t.Errorf("body under budget should be unchanged: got %q", got)
	}
}

func TestTruncateMarkdown_ZeroBudget(t *testing.T) {
	body := "Some content."
	if got := truncateMarkdown(body, 0); got != body {
		t.Errorf("zero/negative budget should return original: got %q", got)
	}
}

func TestTruncateMarkdown_ParaBoundary(t *testing.T) {
	// 200 chars of "X" then \n\n then 200 chars of "Y" → maxTokens 30 (120 chars)
	// would land mid-X, but the cut window is the whole "X" prefix; LastIndex
	// of "\n\n" inside that window is -1, so it falls through to LastIndex("\n")
	// which is also -1, so to "." which is also -1, so hard-cut.
	// Build a case that DOES have a paragraph boundary in the cut window.
	body := strings.Repeat("X", 100) + "\n\n" + strings.Repeat("Y", 200)
	// maxTokens=40 → maxChars=160; cut[:160] contains the \n\n at position 100.
	got := truncateMarkdown(body, 40)
	if !strings.HasSuffix(got, "\n\n…") {
		t.Errorf("expected paragraph-boundary suffix, got: %q", got)
	}
	if strings.Contains(got, "Y") {
		t.Errorf("paragraph boundary should drop the Y block, got: %q", got)
	}
}

func TestTruncateMarkdown_LineBoundary(t *testing.T) {
	// No \n\n in the cut window, but a \n exists.
	body := strings.Repeat("X", 80) + "\n" + strings.Repeat("Y", 200)
	got := truncateMarkdown(body, 30) // maxChars=120; cut[:120] has \n at 80
	if !strings.HasSuffix(got, "\n…") {
		t.Errorf("expected line-boundary suffix, got: %q", got)
	}
}

func TestTruncateMarkdown_SentenceBoundary(t *testing.T) {
	// No \n at all, but a sentence terminator.
	body := strings.Repeat("X", 80) + ". " + strings.Repeat("Y", 200)
	got := truncateMarkdown(body, 30) // maxChars=120; cut[:120] has "." at 80
	if !strings.HasSuffix(got, ". …") {
		t.Errorf("expected sentence-boundary suffix, got: %q", got)
	}
}

func TestTruncateMarkdown_HardCut(t *testing.T) {
	// No boundary inside the budget window — hard char cut with ellipsis.
	body := strings.Repeat("X", 5000)
	got := truncateMarkdown(body, 100) // maxChars=400
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected hard-cut ellipsis, got tail: %q", got[len(got)-10:])
	}
	if len(got) != 400+len("…") {
		t.Errorf("expected len 400+ellipsis, got %d", len(got))
	}
}
