package handlers

// chat_default_style_test.go — unit tests for resolveAnswerStyle.
// Covers the MEMDB_DEFAULT_ANSWER_STYLE fallback path wired in G2 and the
// canary precedence rules added in Stream 8 PRODUCT (G2).

import (
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
)

func TestResolveAnswerStyle_ExplicitRequest(t *testing.T) {
	h := &Handler{cfg: &config.Config{DefaultAnswerStyle: "factual"}}
	req := &nativeChatRequest{AnswerStyle: strPtr("conversational")}
	// Per-request value always wins.
	if got := h.resolveAnswerStyle(req); got != "conversational" {
		t.Errorf("expected conversational (request wins), got %q", got)
	}
}

func TestResolveAnswerStyle_RequestNil_UsesDefault(t *testing.T) {
	h := &Handler{cfg: &config.Config{DefaultAnswerStyle: "factual"}}
	req := &nativeChatRequest{AnswerStyle: nil}
	if got := h.resolveAnswerStyle(req); got != "factual" {
		t.Errorf("expected factual (server default), got %q", got)
	}
}

func TestResolveAnswerStyle_RequestEmpty_UsesDefault(t *testing.T) {
	h := &Handler{cfg: &config.Config{DefaultAnswerStyle: "factual"}}
	req := &nativeChatRequest{AnswerStyle: strPtr("")}
	if got := h.resolveAnswerStyle(req); got != "factual" {
		t.Errorf("expected factual (server default), got %q", got)
	}
}

func TestResolveAnswerStyle_NoConfig_EmptyResult(t *testing.T) {
	h := &Handler{cfg: nil}
	req := &nativeChatRequest{AnswerStyle: nil}
	if got := h.resolveAnswerStyle(req); got != "" {
		t.Errorf("expected empty (no config, no request), got %q", got)
	}
}

func TestResolveAnswerStyle_ConfigDefaultEmpty_EmptyResult(t *testing.T) {
	h := &Handler{cfg: &config.Config{DefaultAnswerStyle: ""}}
	req := &nativeChatRequest{AnswerStyle: nil}
	if got := h.resolveAnswerStyle(req); got != "" {
		t.Errorf("expected empty (config default is empty), got %q", got)
	}
}

func TestResolveAnswerStyle_RequestFactual_OverridesDefault(t *testing.T) {
	h := &Handler{cfg: &config.Config{DefaultAnswerStyle: "conversational"}}
	req := &nativeChatRequest{AnswerStyle: strPtr("factual")}
	if got := h.resolveAnswerStyle(req); got != "factual" {
		t.Errorf("expected factual (request override), got %q", got)
	}
}

// ── Canary precedence tests (Stream 8 PRODUCT) ───────────────────────────────

// userIDInBucket10 is a user_id whose sha256 hash bucket (hash[:4] big-endian uint32 % 100)
// is 7, which is within [0, 10).  Value: "user-2-2514175219165311687" → bucket=7.
const userIDInBucket10 = "user-2-2514175219165311687"

// userIDOutsideBucket10 is a user_id whose sha256 hash bucket is 73, outside [0, 10).
// Value: "user-0-15818923871262189983" → bucket=73.
const userIDOutsideBucket10 = "user-0-15818923871262189983"

func TestResolveAnswerStyle_Canary_WinsOverDefault_WhenInBucket(t *testing.T) {
	// Canary=10%, user in bucket, config default="conversational" → canary wins with "factual".
	h := &Handler{cfg: &config.Config{
		FactualCanaryPct:   10,
		DefaultAnswerStyle: "conversational",
	}}
	req := &nativeChatRequest{
		AnswerStyle: nil,
		UserID:      strPtr(userIDInBucket10),
	}
	if got := h.resolveAnswerStyle(req); got != "factual" {
		t.Errorf("expected factual (canary wins over default), got %q", got)
	}
}

func TestResolveAnswerStyle_Canary_FallsThrough_WhenOutsideBucket(t *testing.T) {
	// Canary=10%, user NOT in bucket, config default="conversational" → default wins.
	h := &Handler{cfg: &config.Config{
		FactualCanaryPct:   10,
		DefaultAnswerStyle: "conversational",
	}}
	req := &nativeChatRequest{
		AnswerStyle: nil,
		UserID:      strPtr(userIDOutsideBucket10),
	}
	if got := h.resolveAnswerStyle(req); got != "conversational" {
		t.Errorf("expected conversational (outside canary bucket, default wins), got %q", got)
	}
}

func TestResolveAnswerStyle_RequestOverride_WinsOverCanary(t *testing.T) {
	// Even if user is in the canary bucket, an explicit request override wins.
	h := &Handler{cfg: &config.Config{
		FactualCanaryPct:   100, // everybody in bucket
		DefaultAnswerStyle: "conversational",
	}}
	req := &nativeChatRequest{
		AnswerStyle: strPtr("conversational"),
		UserID:      strPtr("any-user"),
	}
	if got := h.resolveAnswerStyle(req); got != "conversational" {
		t.Errorf("expected conversational (explicit request wins over canary), got %q", got)
	}
}

func TestResolveAnswerStyle_CanaryOff_FallsToDefault(t *testing.T) {
	// FactualCanaryPct=0 → canary disabled → default applies.
	h := &Handler{cfg: &config.Config{
		FactualCanaryPct:   0,
		DefaultAnswerStyle: "factual",
	}}
	req := &nativeChatRequest{
		AnswerStyle: nil,
		UserID:      strPtr("any-user"),
	}
	if got := h.resolveAnswerStyle(req); got != "factual" {
		t.Errorf("expected factual (canary off, default wins), got %q", got)
	}
}
