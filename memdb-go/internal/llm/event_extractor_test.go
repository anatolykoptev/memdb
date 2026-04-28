package llm

import (
	"strings"
	"testing"
	"time"
)

func TestParseEventResponse_HappyPath(t *testing.T) {
	now := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	raw := `[event_summary]
The user moved to Berlin to start a new consulting job [mention 2024/03/12].

[event_tags]
- location	Berlin
- goals	Start a new consulting job
- emotion	excited`

	out := parseEventResponse(raw, now)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	ev := out[0]
	if !strings.Contains(ev.EventText, "Berlin") {
		t.Errorf("event_text missing Berlin: %q", ev.EventText)
	}
	if strings.Contains(ev.EventText, "[mention") {
		t.Errorf("mention marker not stripped from text: %q", ev.EventText)
	}
	if ev.EventDate == nil || ev.EventDate.Format("2006-01-02") != "2024-03-12" {
		t.Errorf("event_date wrong, got %v", ev.EventDate)
	}
	wantTags := map[string]bool{
		"location:Berlin": true, "goals:Start a new consulting job": true, "emotion:excited": true,
	}
	if len(ev.Tags) != len(wantTags) {
		t.Errorf("tags count mismatch: %v", ev.Tags)
	}
	for _, tag := range ev.Tags {
		if !wantTags[tag] {
			t.Errorf("unexpected tag: %q", tag)
		}
	}
}

func TestParseEventResponse_NoMentionLeavesDateNil(t *testing.T) {
	// Un-anchored events must NOT default to `now` — that would make every
	// freshly-extracted event match every cat-4 query inside the ±7d
	// search window (false positives). Leaving EventDate nil routes the
	// row past the partial btree index (WHERE event_date IS NOT NULL).
	now := time.Date(2024, 5, 1, 12, 30, 0, 0, time.UTC)
	raw := `[event_summary]
User mentioned they enjoy hiking on weekends.

[event_tags]
- preference	hiking on weekends`

	out := parseEventResponse(raw, now)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	if out[0].EventDate != nil {
		t.Errorf("expected nil EventDate when no [mention] anchor, got %v", out[0].EventDate)
	}
}

func TestParseEventResponse_MalformedReturnsEmpty(t *testing.T) {
	now := time.Now().UTC()
	raw := "Sorry, I can't extract events from this conversation."
	if out := parseEventResponse(raw, now); len(out) != 0 {
		t.Errorf("want 0 events on malformed reply, got %d", len(out))
	}
}

func TestParseEventResponse_MissingTagsBlock(t *testing.T) {
	now := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	raw := `[event_summary]
User updated their email address [mention 2024-05-01].`

	out := parseEventResponse(raw, now)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	if len(out[0].Tags) != 0 {
		t.Errorf("expected no tags, got %v", out[0].Tags)
	}
	if out[0].EventDate == nil || out[0].EventDate.Format("2006-01-02") != "2024-05-01" {
		t.Errorf("event_date wrong, got %v", out[0].EventDate)
	}
}

func TestBuildEventSystemPrompt_ContainsTags(t *testing.T) {
	p := buildEventSystemPrompt([]string{"emotion(the user's current emotion)", "location(where the user is)"})
	if !strings.Contains(p, "emotion(the user's current emotion)") {
		t.Error("emotion tag missing from prompt")
	}
	if !strings.Contains(p, "[event_summary]") || !strings.Contains(p, "[event_tags]") {
		t.Error("section markers missing from prompt")
	}
}
