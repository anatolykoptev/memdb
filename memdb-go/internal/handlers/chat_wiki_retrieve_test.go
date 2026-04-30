package handlers

import (
	"strings"
	"testing"
)

func TestWikiRetrievalSlotEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"YES", true},
		{"on", true},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiRetrievalSlotEnv, tc.val)
			if got := wikiRetrievalSlotEnabled(); got != tc.want {
				t.Errorf("wikiRetrievalSlotEnabled(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestWikiRetrievalTopK(t *testing.T) {
	cases := []struct {
		val  string
		want int
	}{
		{"", defaultWikiRetrievalTopK},
		{"abc", defaultWikiRetrievalTopK},
		{"0", wikiRetrievalTopKMin},
		{"5", 5},
		{"99", wikiRetrievalTopKMax},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiRetrievalTopKEnv, tc.val)
			if got := wikiRetrievalTopK(); got != tc.want {
				t.Errorf("wikiRetrievalTopK(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

func TestWikiRetrievalMaxBodyTokens(t *testing.T) {
	cases := []struct {
		val  string
		want int
	}{
		{"", defaultWikiRetrievalMaxBodyTokens},
		{"junk", defaultWikiRetrievalMaxBodyTokens},
		{"10", wikiRetrievalMaxBodyTokensMin},
		{"200", 200},
		{"99999", wikiRetrievalMaxBodyTokensMax},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiRetrievalMaxBodyTokensEnv, tc.val)
			if got := wikiRetrievalMaxBodyTokens(); got != tc.want {
				t.Errorf("wikiRetrievalMaxBodyTokens(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

func TestWikiRetrievalMinScore(t *testing.T) {
	cases := []struct {
		val  string
		want float64
	}{
		{"", defaultWikiRetrievalMinScore},
		{"oops", defaultWikiRetrievalMinScore},
		{"0.5", 0.5},
		{"-1", 0},
		{"2", 1},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(wikiRetrievalMinScoreEnv, tc.val)
			if got := wikiRetrievalMinScore(); got != tc.want {
				t.Errorf("wikiRetrievalMinScore(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestFormatWikiAsMemory_Shape(t *testing.T) {
	hit := WikiSearchHitView{
		Slug:  "auto/L1/abc123",
		Title: "User runs three businesses",
		Body:  "Food truck, dog grooming, podcast.",
		Score: 0.82,
	}
	mem := formatWikiAsMemory(hit, 200)

	if id, _ := mem["id"].(string); id != "wiki:auto/L1/abc123" {
		t.Errorf("id = %q, want wiki:auto/L1/abc123", id)
	}
	text, _ := mem["memory"].(string)
	if !strings.HasPrefix(text, "[Wiki] ") {
		t.Errorf("memory missing [Wiki] prefix: %q", text)
	}
	if !strings.Contains(text, "User runs three businesses") {
		t.Errorf("memory missing title: %q", text)
	}
	if !strings.Contains(text, "Food truck") {
		t.Errorf("memory missing body: %q", text)
	}

	md, ok := mem["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing or wrong type: %#v", mem["metadata"])
	}
	if rel, _ := md["relativity"].(float64); rel != 0.82 {
		t.Errorf("relativity = %v, want 0.82", rel)
	}
	if mt, _ := md["memory_type"].(string); mt != wikiRetrievalMemoryType {
		t.Errorf("memory_type = %q, want %s", mt, wikiRetrievalMemoryType)
	}
}

func TestFormatWikiAsMemory_TitleFallback(t *testing.T) {
	hit := WikiSearchHitView{Slug: "auto/L1/zzz", Title: "  ", Body: "Body.", Score: 0.5}
	mem := formatWikiAsMemory(hit, 200)
	text, _ := mem["memory"].(string)
	if !strings.Contains(text, "auto/L1/zzz") {
		t.Errorf("title fallback missing: %q", text)
	}
}

// Verify the wiki memory shape is compatible with the relativity / memType
// accessors used by sortByRelativity and filterMemoriesByThreshold.
// Drift in these accessors would silently break wiki ranking — this test
// is the canary.
func TestFormatWikiAsMemory_AccessorContract(t *testing.T) {
	hit := WikiSearchHitView{Slug: "s", Title: "T", Body: "B", Score: 0.77}
	mem := formatWikiAsMemory(hit, 200)
	if got := relativity(mem); got != 0.77 {
		t.Errorf("relativity(mem) = %v, want 0.77", got)
	}
	if got := memType(mem); got != wikiRetrievalMemoryType {
		t.Errorf("memType(mem) = %q, want %s", got, wikiRetrievalMemoryType)
	}
}
