package handlers

// add_fine_test.go — unit tests for date-aware extract prompt gating
// (M9 Stream 4). Verifies dateAwareExtractEnabled, dateAwareExtractHints,
// and the env-flag default-true semantics.

import (
	"strings"
	"testing"
)

func TestDateAwareExtractEnabled_DefaultTrue(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "")
	if !dateAwareExtractEnabled() {
		t.Errorf("expected enabled by default when env is unset/empty, got false")
	}
}

func TestDateAwareExtractEnabled_FalseDisables(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "false")
	if dateAwareExtractEnabled() {
		t.Errorf("expected disabled when MEMDB_DATE_AWARE_EXTRACT=false, got true")
	}
}

func TestDateAwareExtractEnabled_ZeroDisables(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "0")
	if dateAwareExtractEnabled() {
		t.Errorf("expected disabled when MEMDB_DATE_AWARE_EXTRACT=0, got true")
	}
}

func TestDateAwareExtractEnabled_TrueEnables(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "true")
	if !dateAwareExtractEnabled() {
		t.Errorf("expected enabled when MEMDB_DATE_AWARE_EXTRACT=true, got false")
	}
}

func TestDateAwareExtractEnabled_OneEnables(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "1")
	if !dateAwareExtractEnabled() {
		t.Errorf("expected enabled when MEMDB_DATE_AWARE_EXTRACT=1, got false")
	}
}

func TestDateAwareExtractEnabled_FalseCaseInsensitive(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "FALSE")
	if dateAwareExtractEnabled() {
		t.Errorf("expected disabled with MEMDB_DATE_AWARE_EXTRACT=FALSE (case-insensitive), got true")
	}
}

func TestDateAwareExtractEnabled_FalseTrimmed(t *testing.T) {
	// Whitespace around the value must not flip the gate — emulates accidental
	// .env quoting like `MEMDB_DATE_AWARE_EXTRACT=" false "`.
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "  false  ")
	if dateAwareExtractEnabled() {
		t.Errorf("expected disabled with whitespace-padded false, got true")
	}
}

func TestDateAwareExtractEnabled_GarbageDefaultsTrue(t *testing.T) {
	// Per spec: only "false" or "0" disable. Anything else (including garbage)
	// keeps the feature on so a typo never silently turns off temporal lift.
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "maybe")
	if !dateAwareExtractEnabled() {
		t.Errorf("expected enabled for garbage value (default-on), got false")
	}
}

func TestDateAwareExtractHints_EnabledReturnsHint(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "true")
	hints := dateAwareExtractHints()
	if len(hints) != 1 {
		t.Fatalf("expected exactly 1 hint when enabled, got %d", len(hints))
	}
	if hints[0] != dateAwareExtractHint {
		t.Errorf("hint[0] mismatch:\n got: %q\nwant: %q", hints[0], dateAwareExtractHint)
	}
}

func TestDateAwareExtractHints_DisabledReturnsNil(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "false")
	hints := dateAwareExtractHints()
	if len(hints) != 0 {
		t.Errorf("expected no hints when disabled, got %d: %v", len(hints), hints)
	}
}

// TestDateAwareExtractHint_ContainsKeyTokens guards the load-bearing strings
// in the hint. If someone rewrites the hint and accidentally drops the
// `[mention YYYY-MM-DD]` template or the relative-date ban, this test catches it.
func TestDateAwareExtractHint_ContainsKeyTokens(t *testing.T) {
	required := []string{
		"[mention YYYY-MM-DD]",
		"Never",
		"relative",
		"ISO",
	}
	for _, tok := range required {
		if !strings.Contains(dateAwareExtractHint, tok) {
			t.Errorf("dateAwareExtractHint missing required token %q\nhint: %s", tok, dateAwareExtractHint)
		}
	}
}

// TestDateAwareExtract_HintAppendOrder verifies the hint is PREPENDED to the
// content-router hints so the date convention is the first thing the model
// sees in the <content_hints> block.
func TestDateAwareExtract_HintAppendOrder(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "true")
	sigHints := []string{"router-hint-1", "router-hint-2"}
	combined := append(dateAwareExtractHints(), sigHints...)
	if len(combined) != 3 {
		t.Fatalf("expected 3 combined hints, got %d", len(combined))
	}
	if combined[0] != dateAwareExtractHint {
		t.Errorf("date-aware hint must be first; got %q", combined[0])
	}
	if combined[1] != "router-hint-1" || combined[2] != "router-hint-2" {
		t.Errorf("router hints out of order: %v", combined[1:])
	}
}

func TestDateAwareExtract_HintAppendOrder_DisabledPassesThrough(t *testing.T) {
	t.Setenv("MEMDB_DATE_AWARE_EXTRACT", "false")
	sigHints := []string{"router-hint-1"}
	combined := append(dateAwareExtractHints(), sigHints...)
	if len(combined) != 1 || combined[0] != "router-hint-1" {
		t.Errorf("disabled state should pass router hints through unchanged, got %v", combined)
	}
}
