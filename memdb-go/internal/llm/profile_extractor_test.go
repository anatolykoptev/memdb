package llm

// profile_extractor_test.go — unit tests for the M10 Stream 2 profile
// extractor: prompt construction + parser coverage. These tests do NOT
// touch the network — the parser is exercised against canned LLM payloads.

import (
	"strings"
	"testing"
	"time"
)

// --- prompt-construction tests ---

func TestProfileFactRetrievalPrompt_VerbatimMemobaseFragments(t *testing.T) {
	// Spot-check that the verbatim Memobase strings survived the port.
	// If any of these vanish, the prompt has drifted from the source and
	// downstream LOCOMO numbers will silently regress.
	wants := []string{
		"You are a professional psychologist.",
		"#### Topics Guidelines",
		"You'll be given some user-relatedtopics and subtopics", // Memobase upstream contains the typo "user-relatedtopics" (no space). We preserve it verbatim per port contract — changing it would diverge from the source prompt.
		"#### Profile",
		"- TOPIC\tSUB_TOPIC\tMEMO",
		"- basic_info\tname\tmelinda",
		"- work\ttitle\tsoftware engineer",
		"## Extraction Examples",
		"never use relative dates like \"today\" or \"yesterday\"",
		"#### Memo",
	}
	for _, w := range wants {
		if !strings.Contains(profileFactRetrievalPrompt+profilePackUser("X"), w) {
			t.Errorf("prompt missing expected fragment: %q", w)
		}
	}
}

func TestProfilePackUser_IncludesMemo(t *testing.T) {
	memo := "user: I love hiking on weekends."
	body := profilePackUser(memo)
	if !strings.Contains(body, memo) {
		t.Errorf("packUser missing memo body, got: %q", body)
	}
	if !strings.Contains(body, "#### Memo") {
		t.Errorf("packUser missing #### Memo header")
	}
}

// --- parser tests ---

func TestParseProfileResponse_TSVHappyPath(t *testing.T) {
	raw := `Some thinking about the user...
---
- basic_info	name	alice
- work	company	acme
- interest	movie	Inception, Interstellar
`
	got := parseProfileResponse(raw, "u1", "cube-u1", time.Now())
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d (%+v)", len(got), got)
	}
	for _, e := range got {
		if e.UserID != "u1" {
			t.Errorf("user_id: want u1, got %q", e.UserID)
		}
		if e.Confidence == 0 {
			t.Errorf("confidence default not applied: entry=%+v", e)
		}
	}
	if got[0].Topic != "basic_info" || got[0].SubTopic != "name" || got[0].Memo != "alice" {
		t.Errorf("first entry mismatch: %+v", got[0])
	}
}

func TestParseProfileResponse_NoDivider(t *testing.T) {
	// LLM occasionally drops the `---` line. We still parse `- TOPIC\tSUB\tMEMO`.
	raw := "- basic_info\tname\tbob\n- work\ttitle\tengineer\n"
	got := parseProfileResponse(raw, "u2", "cube-u2", time.Now())
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
}

func TestParseProfileResponse_PrePostText(t *testing.T) {
	raw := `Sure! Here is the analysis:

[POSSIBLE TOPICS THINKING about Alice's hobbies, work…]
---
- interest	movie	Inception
- work	company	acme

Hope that helps!`
	got := parseProfileResponse(raw, "u3", "cube-u3", time.Now())
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
}

func TestParseProfileResponse_EmptyOutput(t *testing.T) {
	for _, raw := range []string{
		"",
		"---\n",
		"[POSSIBLE TOPICS THINKING...]\n---\n",
		"No relevant facts.",
	} {
		got := parseProfileResponse(raw, "u4", "cube-u4", time.Now())
		if len(got) != 0 {
			t.Errorf("want 0 entries for %q, got %d", raw, len(got))
		}
	}
}

func TestParseProfileResponse_MalformedLinesSkipped(t *testing.T) {
	raw := `---
- basic_info	name	alice
this line is prose with no leading dash
- work title engineer
- interest	movie	Inception
- 	empty_topic	value
- only-two-fields	value
`
	got := parseProfileResponse(raw, "u5", "cube-u5", time.Now())
	// Only the two well-formed lines (alice, Inception) should survive.
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d (%+v)", len(got), got)
	}
}

func TestParseProfileResponse_JSONEnvelope(t *testing.T) {
	raw := `{"facts":[
		{"topic":"basic_info","sub_topic":"name","memo":"alice"},
		{"topic":"work","sub_topic":"company","memo":"acme"}
	]}`
	got := parseProfileResponse(raw, "u6", "cube-u6", time.Now())
	if len(got) != 2 {
		t.Fatalf("want 2 entries from JSON envelope, got %d", len(got))
	}
	if got[0].Topic != "basic_info" || got[0].Memo != "alice" {
		t.Errorf("first entry mismatch: %+v", got[0])
	}
}

func TestParseProfileResponse_JSONBareArray(t *testing.T) {
	raw := "```json\n[{\"topic\":\"interest\",\"sub_topic\":\"movie\",\"memo\":\"Tenet\"}]\n```"
	got := parseProfileResponse(raw, "u7", "cube-u7", time.Now())
	if len(got) != 1 {
		t.Fatalf("want 1 entry from bare JSON array, got %d", len(got))
	}
}

func TestParseProfileResponse_TopicLowerCased(t *testing.T) {
	raw := "---\n- BASIC_INFO\tName\tCarol\n"
	got := parseProfileResponse(raw, "u8", "cube-u8", time.Now())
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].Topic != "basic_info" || got[0].SubTopic != "name" {
		t.Errorf("topic/sub_topic should be lower-cased; got topic=%q sub=%q", got[0].Topic, got[0].SubTopic)
	}
	// Memo MUST NOT be case-folded.
	if got[0].Memo != "Carol" {
		t.Errorf("memo case lost; got %q", got[0].Memo)
	}
}

func TestParseProfileResponse_ValidAtStamped(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	raw := "---\n- work\tcompany\tacme\n"
	got := parseProfileResponse(raw, "u9", "cube-u9", now)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].ValidAt == nil || !got[0].ValidAt.Equal(now) {
		t.Errorf("valid_at not stamped: %+v", got[0].ValidAt)
	}
}
