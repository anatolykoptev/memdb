package handlers

// chat_default_style_test.go — unit tests for resolveAnswerStyle.
// Covers the MEMDB_DEFAULT_ANSWER_STYLE fallback path wired in G2.

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
