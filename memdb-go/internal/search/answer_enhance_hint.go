package search

// answer_enhance_hint.go — formatter that turns a classifier top-N result
// into the short hint block appended to the D10 base extractor prompt.
//
// Concern split: classifier produces a [(category, confidence), ...] slice
// (math + embeddings). This file is pure prompt-shape: take that slice,
// decide whether to emit anything, render the textual block. Keeping the
// formatter here makes the prompt-shape contract obvious and isolates it
// from the classifier's math.

import (
	"fmt"
	"strings"
)

// categoryHintBlock returns a short hint block to append to the base
// extractor prompt, or "" when no hint should be emitted.
//
// Suppression rules (return ""):
//   - top is empty
//   - top-1 confidence < threshold
//   - top-1 category is open_domain (no useful shape constraint)
//   - top-1 category has no hint string registered
//
// The block format is fixed:
//
//	=== Classifier hint ===
//	Likely question type: <top1.cat> (confidence <c1>)
//	  - <hint string for top1>
//	Secondary: <top2.cat> (confidence <c2>)   [only when top2 exists and has a hint]
//	If hint conflicts with the SHORTEST surface form rule above, prefer the surface form rule.
//
// The trailing line is critical — it tells the LLM to defer to the base
// prompt's discipline whenever the hint pulls toward a verbose shape.
func categoryHintBlock(top []CategoryConfidence, threshold float64) string {
	if len(top) == 0 {
		return ""
	}
	t1 := top[0]
	if t1.Confidence < threshold {
		return ""
	}
	if t1.Category == QueryCategoryOpenDomain {
		return ""
	}
	hint1, ok := categoryHintStrings[t1.Category]
	if !ok || hint1 == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n=== Classifier hint ===\n")
	fmt.Fprintf(&b, "Likely question type: %s (confidence %.2f)\n", t1.Category, t1.Confidence)
	fmt.Fprintf(&b, "  - %s\n", hint1)
	if len(top) > 1 {
		t2 := top[1]
		// Only mention secondary if it has a category-specific hint AND
		// is distinct from the primary. Skip when secondary is the empty
		// open_domain branch.
		if t2.Category != t1.Category && t2.Category != QueryCategoryOpenDomain {
			if _, ok := categoryHintStrings[t2.Category]; ok {
				fmt.Fprintf(&b, "Secondary: %s (confidence %.2f)\n", t2.Category, t2.Confidence)
			}
		}
	}
	b.WriteString("If hint conflicts with the SHORTEST surface form rule above, prefer the surface form rule.")
	return b.String()
}
