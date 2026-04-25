package handlers

// search_levels_test.go — tests for M10 Stream 4: ?level=l1|l2|l3 routing.
//
// Handler-level tests focus on:
//   - ParseLevel: valid/invalid strings
//   - validateChatRequest: invalid level → error; valid → no level error
//   - buildSearchParams: Level field defaults to LevelAll
//
// Routing correctness (which DB methods are called per level) is covered in
// the search package: internal/search/service_levels_test.go.
//
// Note: NativeSearch checks searchService availability BEFORE level parsing,
// so HTTP-level 400 tests for invalid level require a wired service — those
// are integration tests. The parse correctness is verified here via ParseLevel.

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// TestParseLevel_Valid verifies that all valid level strings parse without error.
func TestParseLevel_Valid(t *testing.T) {
	cases := map[string]search.Level{
		"":   search.LevelAll,
		"l1": search.LevelL1,
		"l2": search.LevelL2,
		"l3": search.LevelL3,
	}
	for input, want := range cases {
		got, err := search.ParseLevel(input)
		if err != nil {
			t.Errorf("ParseLevel(%q) unexpected error: %v", input, err)
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestParseLevel_Invalid verifies that unrecognised values return an error.
func TestParseLevel_Invalid(t *testing.T) {
	for _, bad := range []string{"L1", "l4", "ltm", "all", "working", "lll"} {
		_, err := search.ParseLevel(bad)
		if err == nil {
			t.Errorf("ParseLevel(%q) expected error, got nil", bad)
		}
		if err != nil && !strings.Contains(err.Error(), "invalid level") {
			t.Errorf("ParseLevel(%q) error %q should contain 'invalid level'", bad, err.Error())
		}
	}
}

// TestChatValidation_InvalidLevel verifies that invalid level in chat request yields a
// validation error containing "invalid level".
func TestChatValidation_InvalidLevel(t *testing.T) {
	lvl := "l99"
	req := &nativeChatRequest{
		UserID: strPtr("u1"),
		Query:  strPtr("hello"),
		Level:  &lvl,
	}
	errs := validateChatRequest(req)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "invalid level") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'invalid level' in errors, got: %v", errs)
	}
}

// TestChatValidation_ValidLevels verifies that l1/l2/l3 pass chat validation without
// level-related errors.
func TestChatValidation_ValidLevels(t *testing.T) {
	for _, lvl := range []string{"l1", "l2", "l3"} {
		l := lvl
		req := &nativeChatRequest{
			UserID: strPtr("u1"),
			Query:  strPtr("hello"),
			Level:  &l,
		}
		errs := validateChatRequest(req)
		for _, e := range errs {
			if strings.Contains(e, "level") {
				t.Errorf("level=%q got unexpected level-related error: %v", lvl, errs)
			}
		}
	}
}

// TestChatValidation_OmittedLevel verifies that omitting level passes validation.
func TestChatValidation_OmittedLevel(t *testing.T) {
	req := &nativeChatRequest{
		UserID: strPtr("u1"),
		Query:  strPtr("hello"),
	}
	errs := validateChatRequest(req)
	for _, e := range errs {
		if strings.Contains(e, "level") {
			t.Errorf("omitted level got unexpected error: %v", e)
		}
	}
}

// TestBuildSearchParams_LevelField verifies that buildSearchParams does not set Level
// (level is applied separately in NativeSearch after this call).
func TestBuildSearchParams_LevelField(t *testing.T) {
	req := searchRequest{
		Query:  strPtr("q"),
		UserID: strPtr("u"),
	}
	params, err := buildSearchParams(req)
	if err != nil {
		t.Fatalf("buildSearchParams: %v", err)
	}
	if params.Level != search.LevelAll {
		t.Errorf("default level should be LevelAll (empty string), got %q", params.Level)
	}
}
