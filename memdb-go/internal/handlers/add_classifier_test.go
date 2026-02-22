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
		if reason != skipTrivial {
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
	if reason != skipCodeOnly {
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
	// No messages → no user content → skip.
	skip, reason := classifySkipExtraction(nil, "")
	if !skip {
		t.Error("expected skip for nil messages (no user content)")
	}
	if reason != skipNoUser {
		t.Errorf("expected reason %q, got %q", skipNoUser, reason)
	}

	skip, reason = classifySkipExtraction([]chatMessage{}, "")
	if !skip {
		t.Error("expected skip for empty messages slice (no user content)")
	}
	if reason != skipNoUser {
		t.Errorf("expected reason %q, got %q", skipNoUser, reason)
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
	if sig.ContentType != contentOpinion {
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
	if sig.ContentType != contentTechnical {
		t.Errorf("expected content type 'technical', got %q", sig.ContentType)
	}
}

func TestClassifyContent_Factual(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "The meeting is on 2026-03-15 at 3pm in Berlin."}}
	conv := "user: [2025-01-01T00:00:00]: The meeting is on 2026-03-15 at 3pm in Berlin."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != contentFactual {
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
	if sig.ContentType != contentMultiTurn {
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
	if sig.SkipReason != skipTrivial {
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
	if sig.ContentType != contentOpinion {
		t.Errorf("expected 'opinion', got %q", sig.ContentType)
	}
}

func TestClassifyContentFromText_MultiTurn(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: What do you think?\nassistant: [2025-01-01T00:00:01]: I suggest X.\nuser: [2025-01-01T00:00:02]: Great idea."
	sig := classifyContentFromText(conv)
	if sig.ContentType != contentMultiTurn {
		t.Errorf("expected 'multi-turn', got %q", sig.ContentType)
	}
}

func TestClassifyContent_MixedContent(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "The sky is blue today."}}
	conv := "user: [2025-01-01T00:00:00]: The sky is blue today."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != contentMixed {
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
	if st != sessionDecision {
		t.Errorf("expected %q, got %q", sessionDecision, st)
	}
}

func TestDetectSessionType_Learning(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: TIL that Go channels are not thread-safe by default.\nassistant: [2025-01-01T00:00:01]: Right, turns out you need to use sync primitives."
	st := detectSessionType(conv)
	if st != sessionLearning {
		t.Errorf("expected %q, got %q", sessionLearning, st)
	}
}

func TestDetectSessionType_Debug(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: Got an error: connection refused on port 5432. Fixed by restarting postgres."
	st := detectSessionType(conv)
	if st != sessionDebug {
		t.Errorf("expected %q, got %q", sessionDebug, st)
	}
}

func TestDetectSessionType_Planning(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: The roadmap for next sprint includes auth and rate limiting. TODO: set up OAuth."
	st := detectSessionType(conv)
	if st != sessionPlanning {
		t.Errorf("expected %q, got %q", sessionPlanning, st)
	}
}

func TestDetectSessionType_General(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: The weather is nice today."
	st := detectSessionType(conv)
	if st != sessionGeneral {
		t.Errorf("expected %q, got %q", sessionGeneral, st)
	}
}

func TestSessionPromptFocus(t *testing.T) {
	if sessionPromptFocus(sessionDecision) == "" {
		t.Error("expected non-empty focus for decision")
	}
	if sessionPromptFocus(sessionGeneral) != "" {
		t.Error("expected empty focus for general")
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

// --- Bug fix regression tests ---

func TestClassifyContent_EmptyContent_Skips(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: ""}}
	conv := "user: [2025-01-01T00:00:00]: "
	sig := classifyContent(msgs, conv)
	if !sig.Skip {
		t.Error("empty user content should be skipped")
	}
}

func TestClassifyContent_WhitespaceContent_Skips(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "   \n\t  "}}
	conv := "user: [2025-01-01T00:00:00]:    \n\t  "
	sig := classifyContent(msgs, conv)
	if !sig.Skip {
		t.Error("whitespace-only user content should be skipped")
	}
}

func TestClassifyContent_SystemOnly_Skips(t *testing.T) {
	msgs := []chatMessage{{Role: "system", Content: "You are a helpful assistant."}}
	conv := "system: [2025-01-01T00:00:00]: You are a helpful assistant."
	sig := classifyContent(msgs, conv)
	if !sig.Skip {
		t.Error("system-only message should be skipped (no user content)")
	}
	if sig.SkipReason != skipNoUser {
		t.Errorf("expected reason %q, got %q", skipNoUser, sig.SkipReason)
	}
}

func TestClassifyContent_AssistantOnly_Skips(t *testing.T) {
	msgs := []chatMessage{{Role: "assistant", Content: "Here is some information for you."}}
	conv := "assistant: [2025-01-01T00:00:00]: Here is some information for you."
	sig := classifyContent(msgs, conv)
	if !sig.Skip {
		t.Error("assistant-only message should be skipped (no user content)")
	}
}

func TestClassifyContent_SystemPlusUser_NotSkipped(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "I prefer dark mode in all my editors."},
	}
	conv := "system: [2025-01-01T00:00:00]: You are a helpful assistant.\nuser: [2025-01-01T00:00:01]: I prefer dark mode in all my editors."
	sig := classifyContent(msgs, conv)
	if sig.Skip {
		t.Error("system + user message should NOT be skipped")
	}
}

func TestClassifyContentFromText_Empty_Skips(t *testing.T) {
	sig := classifyContentFromText("")
	if !sig.Skip {
		t.Error("empty text should be skipped in buffer zone classifier")
	}
}

func TestClassifyContentFromText_CodeOnly_Skips(t *testing.T) {
	conv := "```go\npackage main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n```"
	sig := classifyContentFromText(conv)
	if !sig.Skip {
		t.Error("code-only text should be skipped in buffer zone classifier")
	}
}

func TestHasUserContent(t *testing.T) {
	tests := []struct {
		name     string
		messages []chatMessage
		want     bool
	}{
		{"nil", nil, false},
		{"empty", []chatMessage{}, false},
		{"user with content", []chatMessage{{Role: "user", Content: "hello"}}, true},
		{"user empty", []chatMessage{{Role: "user", Content: ""}}, false},
		{"user whitespace", []chatMessage{{Role: "user", Content: "  \t "}}, false},
		{"system only", []chatMessage{{Role: "system", Content: "prompt"}}, false},
		{"assistant only", []chatMessage{{Role: "assistant", Content: "response"}}, false},
		{"system+user", []chatMessage{{Role: "system", Content: "prompt"}, {Role: "user", Content: "query"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasUserContent(tc.messages)
			if got != tc.want {
				t.Errorf("hasUserContent = %v, want %v", got, tc.want)
			}
		})
	}
}
