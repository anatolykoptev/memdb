package handlers

// chat_prompt_parity_test.go — bytewise output parity for the 4 factual QA
// prompt templates after the single-source-of-truth refactor.
//
// Each golden file under testdata/ was captured from the original hardcoded
// constants (before the refactor) via `git show HEAD:...`. TestFactualPromptParity
// verifies that the new builder-assembled strings are byte-for-byte identical
// to the originals. If this test fails, either the builder logic has a bug or
// the golden file is stale (update it only when intentionally changing a rule).
//
// How to update goldens after an intentional rule change:
//  1. Edit the relevant constant(s) in chat_prompt_tpl.go.
//  2. Run: go test ./internal/handlers/... -run TestFactualPromptParity -update
//     (the -update flag is handled by the -update check below).
//  3. Commit the updated golden files alongside the rule change.

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "overwrite golden files with current output")

func TestFactualPromptParity(t *testing.T) {
	cases := []struct {
		name   string
		got    string
		golden string
	}{
		{
			name:   "high_confidence_EN",
			got:    factualQAPromptHighConfidenceEN,
			golden: "factual_high_en_golden.golden",
		},
		{
			name:   "low_confidence_EN",
			got:    factualQAPromptLowConfidenceEN,
			golden: "factual_low_en_golden.golden",
		},
		{
			name:   "high_confidence_ZH",
			got:    factualQAPromptHighConfidenceZH,
			golden: "factual_high_zh_golden.golden",
		},
		{
			name:   "low_confidence_ZH",
			got:    factualQAPromptLowConfidenceZH,
			golden: "factual_low_zh_golden.golden",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join("testdata", c.golden)

			if *updateGolden {
				if err := os.WriteFile(path, []byte(c.got), 0o644); err != nil {
					t.Fatalf("update golden: %v", err)
				}
				t.Logf("updated %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
			}

			if c.got != string(want) {
				t.Errorf("builder output differs from golden %s\ngot  len=%d\nwant len=%d\nfirst diff byte: %d",
					c.golden, len(c.got), len(want), firstDiffByte(c.got, string(want)))
			}
		})
	}
}

// firstDiffByte returns the index of the first byte that differs between a and b,
// or min(len(a), len(b)) when one is a prefix of the other.
func firstDiffByte(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
