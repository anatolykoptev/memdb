package handlers

import "testing"

func TestClassifySkipExtraction_TrivialMessages(t *testing.T) {
	trivials := []string{"ok", "thanks", "got it", "yes", "no", "sure", "done", "lgtm", "thx", "ty", "ack", "k", "yep", "nope", "cool", "nice", "great", "perfect", "right", "understood", "good", "fine", "alright"}
	for _, msg := range trivials {
		msgs := []chatMessage{{Role: "user", Content: msg}}
		conv := "user: [2025-01-01T00:00:00]: " + msg
		skip, reason := classifySkipExtraction(msgs, conv)
		if !skip {
			t.Errorf("expected skip for trivial %q, got false", msg)
		}
		if reason != "trivial" {
			t.Errorf("expected reason 'trivial' for %q, got %q", msg, reason)
		}
	}
}

func TestClassifySkipExtraction_TrivialCaseInsensitive(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "OK"}}
	conv := "user: [2025-01-01T00:00:00]: OK"
	skip, _ := classifySkipExtraction(msgs, conv)
	if !skip {
		t.Error("expected case-insensitive match for 'OK'")
	}
}

func TestClassifySkipExtraction_NotTrivial(t *testing.T) {
	cases := []struct {
		name string
		msgs []chatMessage
		conv string
	}{
		{
			name: "meaningful single message",
			msgs: []chatMessage{{Role: "user", Content: "I prefer Go for backend development"}},
			conv: "user: [2025-01-01T00:00:00]: I prefer Go for backend development",
		},
		{
			name: "multiple messages even if short",
			msgs: []chatMessage{
				{Role: "user", Content: "ok"},
				{Role: "assistant", Content: "ok"},
			},
			conv: "user: [2025-01-01T00:00:00]: ok\nassistant: [2025-01-01T00:00:00]: ok",
		},
		{
			name: "single message over char limit",
			msgs: []chatMessage{{Role: "user", Content: "this is a longer message that is worth extracting"}},
			conv: "user: [2025-01-01T00:00:00]: this is a longer message that is worth extracting",
		},
		{
			name: "short but not in casual list",
			msgs: []chatMessage{{Role: "user", Content: "buy milk"}},
			conv: "user: [2025-01-01T00:00:00]: buy milk",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := classifySkipExtraction(tc.msgs, tc.conv)
			if skip {
				t.Errorf("did not expect skip, got skip with reason %q", reason)
			}
		})
	}
}

func TestClassifySkipExtraction_CodeOnly(t *testing.T) {
	// Code block must dominate the conversation text (>90%).
	// The conversation format adds "role: [time]: " prefix (~30 chars),
	// so the code block must be long enough to exceed 90% of total.
	code := "```go\npackage main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"log\"\n)\n\nfunc main() {\n\thttp.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {\n\t\tfmt.Fprintf(w, \"Hello, World!\")\n\t})\n\tlog.Println(\"Starting server on :8080\")\n\tlog.Fatal(http.ListenAndServe(\":8080\", nil))\n}\n```"
	msgs := []chatMessage{{Role: "user", Content: code}}
	conv := "user: [2025-01-01T00:00:00]: " + code
	skip, reason := classifySkipExtraction(msgs, conv)
	if !skip {
		t.Errorf("expected skip for code-only content (ratio=%.2f)", codeBlockRatio(conv))
	}
	if reason != "code-only" {
		t.Errorf("expected reason 'code-only', got %q", reason)
	}
}

func TestClassifySkipExtraction_CodeWithProse(t *testing.T) {
	content := "Here's how to implement a server in Go. This uses net/http:\n```go\npackage main\n\nimport \"net/http\"\n\nfunc main() {\n\thttp.ListenAndServe(\":8080\", nil)\n}\n```\nThis starts a server on port 8080."
	msgs := []chatMessage{{Role: "user", Content: content}}
	conv := "user: [2025-01-01T00:00:00]: " + content
	skip, _ := classifySkipExtraction(msgs, conv)
	if skip {
		t.Error("should not skip code with meaningful prose around it")
	}
}

// Quality guard tests — ensure we NEVER skip memorizable content.

func TestClassifySkipExtraction_PunctuationNotTrivial(t *testing.T) {
	// "Thanks!" / "Ok." have punctuation — regex should NOT match.
	// This guards against over-aggressive trivial detection.
	cases := []string{"Thanks!", "Ok.", "ok...", "sure!", "Yes?", "no!"}
	for _, msg := range cases {
		msgs := []chatMessage{{Role: "user", Content: msg}}
		conv := "user: [2025-01-01T00:00:00]: " + msg
		skip, reason := classifySkipExtraction(msgs, conv)
		if skip {
			t.Errorf("should NOT skip %q (has punctuation), got skip with reason %q", msg, reason)
		}
	}
}

func TestClassifySkipExtraction_WhitespaceTrimmed(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "  ok  "}}
	conv := "user: [2025-01-01T00:00:00]:   ok  "
	skip, _ := classifySkipExtraction(msgs, conv)
	if !skip {
		t.Error("expected skip for ' ok ' after trimming")
	}
}

func TestClassifySkipExtraction_EmptyMessages(t *testing.T) {
	skip, _ := classifySkipExtraction(nil, "")
	if skip {
		t.Error("should not skip empty messages list")
	}
	skip, _ = classifySkipExtraction([]chatMessage{}, "")
	if skip {
		t.Error("should not skip empty messages slice")
	}
}

func TestClassifySkipExtraction_ShortMeaningfulContent(t *testing.T) {
	// Short but meaningful — must NOT be skipped.
	cases := []string{"use Rust", "I like Go", "age 30", "fix bug #42", "deploy v2"}
	for _, msg := range cases {
		msgs := []chatMessage{{Role: "user", Content: msg}}
		conv := "user: [2025-01-01T00:00:00]: " + msg
		skip, reason := classifySkipExtraction(msgs, conv)
		if skip {
			t.Errorf("should NOT skip meaningful short message %q, got skip with reason %q", msg, reason)
		}
	}
}

func TestClassifySkipExtraction_MultipleCodeBlocks(t *testing.T) {
	// Two code blocks with prose between — should NOT be skipped if prose is significant.
	content := "```go\nfunc A() {}\n```\nThis is an important architectural decision about separation of concerns.\n```go\nfunc B() {}\n```"
	msgs := []chatMessage{{Role: "user", Content: content}}
	conv := "user: [2025-01-01T00:00:00]: " + content
	skip, _ := classifySkipExtraction(msgs, conv)
	if skip {
		t.Error("should not skip multiple code blocks with meaningful prose between them")
	}
}

func TestClassifySkipExtraction_UnicodeContent(t *testing.T) {
	// Cyrillic "ок" — should match trivial (within 20 rune limit).
	// But it's NOT in the casual patterns list, so it should NOT be skipped.
	msgs := []chatMessage{{Role: "user", Content: "ок"}}
	conv := "user: [2025-01-01T00:00:00]: ок"
	skip, _ := classifySkipExtraction(msgs, conv)
	if skip {
		t.Error("should not skip non-English trivial pattern 'ок'")
	}
}

// --- ContentSignal tests ---

func TestClassifyContent_Opinion(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "I prefer Go over Rust for backend services"}}
	conv := "user: [2025-01-01T00:00:00]: I prefer Go over Rust for backend services"
	sig := classifyContent(msgs, conv)
	if sig.Skip {
		t.Error("opinion should not be skipped")
	}
	if sig.ContentType != "opinion" {
		t.Errorf("expected content type 'opinion', got %q", sig.ContentType)
	}
	if len(sig.Hints) == 0 {
		t.Error("expected at least one hint for opinion content")
	}
}

func TestClassifyContent_Technical(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "Use docker compose up -d to deploy the stack. The Dockerfile uses multi-stage builds with nginx."}}
	conv := "user: [2025-01-01T00:00:00]: Use docker compose up -d to deploy the stack. The Dockerfile uses multi-stage builds with nginx."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != "technical" {
		t.Errorf("expected content type 'technical', got %q", sig.ContentType)
	}
}

func TestClassifyContent_Factual(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "The meeting is on 2026-03-15 at 3pm in Berlin."}}
	conv := "user: [2025-01-01T00:00:00]: The meeting is on 2026-03-15 at 3pm in Berlin."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != "factual" {
		t.Errorf("expected content type 'factual', got %q", sig.ContentType)
	}
}

func TestClassifyContent_MultiTurn(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "What's the best database for this?"},
		{Role: "assistant", Content: "PostgreSQL would work well."},
		{Role: "user", Content: "OK, let's go with Postgres then."},
	}
	conv := "user: [2025-01-01T00:00:00]: What's the best database for this?\nassistant: [2025-01-01T00:00:01]: PostgreSQL would work well.\nuser: [2025-01-01T00:00:02]: OK, let's go with Postgres then."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != "multi-turn" {
		t.Errorf("expected content type 'multi-turn', got %q", sig.ContentType)
	}
}

func TestClassifyContent_TrivialStillSkips(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "ok"}}
	conv := "user: [2025-01-01T00:00:00]: ok"
	sig := classifyContent(msgs, conv)
	if !sig.Skip {
		t.Error("trivial message should still be skipped")
	}
	if sig.SkipReason != "trivial" {
		t.Errorf("expected skip reason 'trivial', got %q", sig.SkipReason)
	}
}

func TestClassifyContent_CodeWithProseGetsHint(t *testing.T) {
	content := "Here's the server:\n```go\npackage main\n\nfunc main() {}\n```\nThis uses the standard library approach."
	msgs := []chatMessage{{Role: "user", Content: content}}
	conv := "user: [2025-01-01T00:00:00]: " + content
	sig := classifyContent(msgs, conv)
	if sig.Skip {
		t.Error("code with prose should not be skipped")
	}
}

func TestClassifyContent_PureCodeStillSkips(t *testing.T) {
	code := "```go\npackage main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"log\"\n)\n\nfunc main() {\n\thttp.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {\n\t\tfmt.Fprintf(w, \"Hello, World!\")\n\t})\n\tlog.Println(\"Starting server on :8080\")\n\tlog.Fatal(http.ListenAndServe(\":8080\", nil))\n}\n```"
	msgs := []chatMessage{{Role: "user", Content: code}}
	conv := "user: [2025-01-01T00:00:00]: " + code
	sig := classifyContent(msgs, conv)
	if !sig.Skip {
		t.Error("pure code (>90%) should still be skipped")
	}
}

func TestClassifyContentFromText_Opinion(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: I think Python is better for data science"
	sig := classifyContentFromText(conv)
	if sig.ContentType != "opinion" {
		t.Errorf("expected 'opinion', got %q", sig.ContentType)
	}
}

func TestClassifyContentFromText_MultiTurn(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: What do you think?\nassistant: [2025-01-01T00:00:01]: I suggest X.\nuser: [2025-01-01T00:00:02]: Great idea."
	sig := classifyContentFromText(conv)
	if sig.ContentType != "multi-turn" {
		t.Errorf("expected 'multi-turn', got %q", sig.ContentType)
	}
}

func TestClassifyContent_MixedContent(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "The sky is blue today."}}
	conv := "user: [2025-01-01T00:00:00]: The sky is blue today."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != "mixed" {
		t.Errorf("expected content type 'mixed' for generic content, got %q", sig.ContentType)
	}
	if len(sig.Hints) != 0 {
		t.Errorf("expected no hints for mixed content, got %v", sig.Hints)
	}
}

// --- Session type tests ---

func TestDetectSessionType_Decision(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: We decided to go with PostgreSQL for the main database.\nassistant: [2025-01-01T00:00:01]: Going with Postgres makes sense."
	st := detectSessionType(conv)
	if st != "decision" {
		t.Errorf("expected 'decision', got %q", st)
	}
}

func TestDetectSessionType_Learning(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: TIL that Go channels are not thread-safe by default.\nassistant: [2025-01-01T00:00:01]: Right, turns out you need to use sync primitives."
	st := detectSessionType(conv)
	if st != "learning" {
		t.Errorf("expected 'learning', got %q", st)
	}
}

func TestDetectSessionType_Debug(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: Got an error: connection refused on port 5432. Fixed by restarting postgres."
	st := detectSessionType(conv)
	if st != "debug" {
		t.Errorf("expected 'debug', got %q", st)
	}
}

func TestDetectSessionType_Planning(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: The roadmap for next sprint includes auth and rate limiting. TODO: set up OAuth."
	st := detectSessionType(conv)
	if st != "planning" {
		t.Errorf("expected 'planning', got %q", st)
	}
}

func TestDetectSessionType_General(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: The weather is nice today."
	st := detectSessionType(conv)
	if st != "general" {
		t.Errorf("expected 'general', got %q", st)
	}
}

func TestSessionPromptFocus(t *testing.T) {
	if sessionPromptFocus("decision") == "" {
		t.Error("expected non-empty focus for 'decision'")
	}
	if sessionPromptFocus("general") != "" {
		t.Error("expected empty focus for 'general'")
	}
}

func TestCodeBlockRatio(t *testing.T) {
	tests := []struct {
		name string
		text string
		low  float64
		high float64
	}{
		{"empty", "", 0, 0},
		{"no code", "just plain text", 0, 0},
		{"all code", "```go\nfmt.Println()\n```", 0.9, 1.01},
		{"mixed", "intro\n```go\ncode\n```\noutro", 0.2, 0.8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ratio := codeBlockRatio(tc.text)
			if ratio < tc.low || ratio > tc.high {
				t.Errorf("codeBlockRatio = %f, want [%f, %f]", ratio, tc.low, tc.high)
			}
		})
	}
}
