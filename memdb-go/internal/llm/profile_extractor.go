package llm

// profile_extractor.go — single-call user-profile extraction (M10 Stream 2).
//
// Pipeline: format conversation as memo → call LLM with the verbatim
// Memobase prompt (see profile_prompts.go) → parse the markdown TSV output
// (`- TOPIC<TAB>SUB_TOPIC<TAB>MEMO`) → return []db.InsertProfileParams rows.
//
// This is intentionally NOT the full Memobase 5-call merge pipeline; the
// multi-stage merge (pick_related_profiles + topic-locked re-extraction +
// LLM judge) is M11. M10 ships extraction-only as the first measurable
// quality lift.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	// profileExtractMaxTokens caps the LLM response. Memobase uses 4096; we
	// match it so long memos with 20+ profiles do not get truncated.
	profileExtractMaxTokens = 4096

	// profileMinMemoChars is the minimum conversation length before we even
	// attempt extraction. Avoids hitting the LLM for trivial 2-token replies.
	profileMinMemoChars = 32
)

// ErrEmptyConversation is returned by ExtractProfile when the memo is below
// profileMinMemoChars after trimming. Callers should treat as a no-op.
var ErrEmptyConversation = errors.New("profile extract: conversation too short")

// ProfileExtractor extracts user-profile facts via a single LLM call using
// the Memobase-verbatim prompt.
type ProfileExtractor struct {
	client *Client
}

// NewProfileExtractor constructs a ProfileExtractor wrapping the given Client.
// Reuses the same retry + model-fallback semantics as the fact extractor.
func NewProfileExtractor(c *Client) *ProfileExtractor {
	return &ProfileExtractor{client: c}
}

// Model returns the underlying primary model name.
func (e *ProfileExtractor) Model() string { return e.client.Model() }

// ExtractProfile sends the conversation memo to the LLM and returns the
// parsed []db.InsertProfileParams rows ready for postgres BulkUpsert.
//
// The userID is stamped on every returned entry. Confidence defaults to
// 0.9 (we trust the verbatim Memobase prompt + only persist what survives
// strict TSV parsing). ValidAt is set to the call time so M11 can later
// invalidate stale rows.
//
// On parse failure the method retries once with a "STRICTLY follow the
// output format" suffix and returns the merged result; if the second pass
// also yields nothing, it returns an empty slice + nil error so the
// fire-and-forget caller logs an "empty" outcome rather than an error.
func (e *ProfileExtractor) ExtractProfile(ctx context.Context, conversation, userID string) ([]db.InsertProfileParams, error) {
	memo := strings.TrimSpace(conversation)
	if len(memo) < profileMinMemoChars {
		return nil, ErrEmptyConversation
	}
	if userID == "" {
		return nil, errors.New("profile extract: user_id required")
	}

	msgs := []map[string]string{
		{"role": "system", "content": profileFactRetrievalPrompt},
		{"role": "user", "content": profilePackUser(memo)},
	}

	raw, err := e.client.Chat(ctx, msgs, profileExtractMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("profile extract chat: %w", err)
	}

	now := time.Now().UTC()
	entries := parseProfileResponse(raw, userID, now)
	if len(entries) > 0 {
		return entries, nil
	}

	// One retry with a stronger format reminder. LLMs occasionally drop the
	// `---` divider or wrap the list in prose; the suffix nudges them back.
	retryMsgs := append(msgs[:len(msgs):len(msgs)], map[string]string{
		"role":    "user",
		"content": "Please STRICTLY follow the output format: a `---` divider followed by lines starting with `- TOPIC\tSUB_TOPIC\tMEMO`. No prose around the list.",
	})
	raw, err = e.client.Chat(ctx, retryMsgs, profileExtractMaxTokens)
	if err != nil {
		// Treat retry transport error as empty (caller logs "llm_error").
		return nil, fmt.Errorf("profile extract retry: %w", err)
	}
	return parseProfileResponse(raw, userID, now), nil
}

// --- parser -----------------------------------------------------------------

// profileLineRE matches a single Memobase TSV line:
//
//	- TOPIC<TAB>SUB_TOPIC<TAB>MEMO
//
// The leading "- " is required. TOPIC / SUB_TOPIC may not contain TABs;
// MEMO is everything after the second TAB up to end-of-line.
var profileLineRE = regexp.MustCompile(`^[\-\*]\s+([^\t\n]+)\t([^\t\n]+)\t(.+)$`)

// jsonProfileEnvelope captures the alternative output shape some LLMs emit
// when they ignore the TSV instructions and reply with JSON instead. We
// accept it to keep the M10 extractor robust on weaker models.
type jsonProfileEnvelope struct {
	Facts []struct {
		Topic    string `json:"topic"`
		SubTopic string `json:"sub_topic"`
		Memo     string `json:"memo"`
	} `json:"facts"`
}

// parseProfileResponse extracts rows from either the Memobase TSV format
// or the JSON envelope. Tolerant to:
//   - pre/post prose around the list
//   - missing `---` divider
//   - ``` fences (json or unmarked)
//   - blank/short lines
func parseProfileResponse(raw, userID string, now time.Time) []db.InsertProfileParams {
	stripped := string(StripJSONFence([]byte(raw)))

	// Try JSON envelope first — cheap, fails fast on non-JSON.
	if entries, ok := tryParseProfileJSON(stripped, userID, now); ok {
		return entries
	}

	// Fall back to Memobase TSV markdown.
	return parseProfileTSV(stripped, userID, now)
}

func tryParseProfileJSON(raw, userID string, now time.Time) ([]db.InsertProfileParams, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}

	// Envelope shape: {"facts":[{"topic":..,"sub_topic":..,"memo":..}]}
	var env jsonProfileEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err == nil && len(env.Facts) > 0 {
		out := make([]db.InsertProfileParams, 0, len(env.Facts))
		for _, f := range env.Facts {
			if e, ok := buildProfileEntry(f.Topic, f.SubTopic, f.Memo, userID, now); ok {
				out = append(out, e)
			}
		}
		return out, true
	}

	// Bare-array shape: [{"topic":..,"sub_topic":..,"memo":..}]
	var arr []struct {
		Topic    string `json:"topic"`
		SubTopic string `json:"sub_topic"`
		Memo     string `json:"memo"`
	}
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
		out := make([]db.InsertProfileParams, 0, len(arr))
		for _, f := range arr {
			if e, ok := buildProfileEntry(f.Topic, f.SubTopic, f.Memo, userID, now); ok {
				out = append(out, e)
			}
		}
		return out, true
	}

	return nil, false
}

func parseProfileTSV(raw, userID string, now time.Time) []db.InsertProfileParams {
	// Drop everything before the `---` divider when present; otherwise scan
	// the whole body so prose-wrapped output still parses.
	body := raw
	if idx := strings.Index(body, "\n---"); idx >= 0 {
		body = body[idx+len("\n---"):]
	} else {
		body = strings.TrimPrefix(body, "---")
	}

	var out []db.InsertProfileParams
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := profileLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if e, ok := buildProfileEntry(m[1], m[2], m[3], userID, now); ok {
			out = append(out, e)
		}
	}
	return out
}

// buildProfileEntry normalises whitespace, lower-cases topic/sub_topic
// (Memobase canonical form), and rejects empty memos.
func buildProfileEntry(topic, subTopic, memo, userID string, now time.Time) (db.InsertProfileParams, bool) {
	topic = strings.ToLower(strings.TrimSpace(topic))
	subTopic = strings.ToLower(strings.TrimSpace(subTopic))
	memo = strings.TrimSpace(memo)
	if topic == "" || subTopic == "" || memo == "" {
		return db.InsertProfileParams{}, false
	}
	validAt := now
	return db.InsertProfileParams{
		UserID:     userID,
		Topic:      topic,
		SubTopic:   subTopic,
		Memo:       memo,
		Confidence: 0.9,
		ValidAt:    &validAt,
	}, true
}
