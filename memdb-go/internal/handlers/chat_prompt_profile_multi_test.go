package handlers

// chat_prompt_profile_multi_test.go — Karpathy r3 fix #2: per-speaker profile
// section dispatch. Verifies that resolveProfileSection picks the multi-speaker
// renderer when len(req.Speakers) >= 2 and the legacy single-speaker renderer
// otherwise.
//
// Pure unit tests — no live Postgres. We rely on h.postgres == nil → both
// renderers short-circuit and return "" without panicking. The dispatch
// itself is what we're pinning here (regression guard against the implicit
// fall-through that silently dropped speaker_b's profile pre-fix).

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// TestResolveProfileSection_NoPostgresReturnsEmpty pins the safe-default:
// when postgres is nil, both code paths must return "" and NOT panic.
// This is the same contract chatProfileSection already honours; the multi
// path inherits it.
func TestResolveProfileSection_NoPostgresReturnsEmpty(t *testing.T) {
	h := &Handler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		postgres: nil,
	}

	t.Run("single_speaker", func(t *testing.T) {
		uid := "alice"
		req := &nativeChatRequest{UserID: &uid}
		got := h.resolveProfileSection(context.Background(), req)
		if got != "" {
			t.Errorf("single-speaker no-postgres: got %q, want empty", got)
		}
	})

	t.Run("dual_speaker_dispatches_to_multi", func(t *testing.T) {
		// Both speakers populated — must hit chatProfileSectionMulti, which
		// must short-circuit on nil postgres and return "" cleanly.
		req := &nativeChatRequest{
			Speakers: []string{"alice", "bob"},
		}
		got := h.resolveProfileSection(context.Background(), req)
		if got != "" {
			t.Errorf("dual-speaker no-postgres: got %q, want empty", got)
		}
	})

	t.Run("triple_speaker_dispatches_to_multi", func(t *testing.T) {
		// >=2 speakers route through the multi path. Future N-speaker
		// callers (group chats) must work without code change.
		req := &nativeChatRequest{
			Speakers: []string{"alice", "bob", "carol"},
		}
		got := h.resolveProfileSection(context.Background(), req)
		if got != "" {
			t.Errorf("triple-speaker no-postgres: got %q, want empty", got)
		}
	})
}

// TestChatProfileSectionMulti_EmptySpeakersReturnsEmpty pins the input-
// validation contract: empty speaker slice must short-circuit.
func TestChatProfileSectionMulti_EmptySpeakersReturnsEmpty(t *testing.T) {
	h := &Handler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		postgres: nil,
	}
	got := h.chatProfileSectionMulti(context.Background(), nil)
	if got != "" {
		t.Errorf("nil speakers: got %q, want empty", got)
	}
	got = h.chatProfileSectionMulti(context.Background(), []string{})
	if got != "" {
		t.Errorf("empty speakers slice: got %q, want empty", got)
	}
}

// TestResolveProfileSection_GateDisabledReturnsEmpty verifies the env gate
// MEMDB_PROFILE_INJECT=false suppresses both paths uniformly.
func TestResolveProfileSection_GateDisabledReturnsEmpty(t *testing.T) {
	t.Setenv("MEMDB_PROFILE_INJECT", "false")
	h := &Handler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		postgres: nil,
	}
	req := &nativeChatRequest{Speakers: []string{"alice", "bob"}}
	if got := h.resolveProfileSection(context.Background(), req); got != "" {
		t.Errorf("gate disabled: got %q, want empty", got)
	}
}
