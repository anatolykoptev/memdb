package handlers

import (
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// TestCanHandleNativeAdd_FeedbackGate verifies Phase 4.5 wiring:
// feedback requests go native when llmChat is set, proxy when not.
func TestCanHandleNativeAdd_FeedbackGate(t *testing.T) {
	trueVal := true
	falseVal := false

	t.Run("feedback + no llmChat → proxy", func(t *testing.T) {
		h := handlerForWiringTest(t, false)
		req := &fullAddRequest{IsFeedback: &trueVal}
		if h.canHandleNativeAdd(req) {
			t.Fatal("expected proxy (llmChat nil)")
		}
	})

	t.Run("feedback + llmChat → native", func(t *testing.T) {
		h := handlerForWiringTest(t, true)
		req := &fullAddRequest{IsFeedback: &trueVal}
		if !h.canHandleNativeAdd(req) {
			t.Fatal("expected native (llmChat set)")
		}
	})

	t.Run("not feedback → existing rules apply", func(t *testing.T) {
		h := handlerForWiringTest(t, false)
		req := &fullAddRequest{IsFeedback: &falseVal}
		h.llmExtractor = nil
		fastMode := "fast"
		req.Mode = &fastMode
		if !h.canHandleNativeAdd(req) {
			t.Fatal("expected native for mode=fast (non-feedback)")
		}
	})
}

// handlerForWiringTest returns a Handler with non-nil postgres+embedder stubs.
// If withLLMChat is true, llmChat is set to a non-nil stub.
func handlerForWiringTest(t *testing.T, withLLMChat bool) *Handler {
	t.Helper()
	h := &Handler{
		logger:   silentLogger(),
		postgres: &stubPostgres,
		embedder: &stubEmbedder{},
	}
	if withLLMChat {
		h.llmChat = &llm.Client{}
	}
	return h
}
