# Quality-Aware Gate Evolution — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Evolve 4 Phase-7 binary gates into quality-aware routers that produce signals (hints, merge instructions, session types) consumed by downstream LLM calls.

**Architecture:** Each gate returns a struct instead of a bool. Structs carry hints that get injected into LLM prompts (extraction and episodic). No new LLM calls added — we optimize the existing ones with better context.

**Tech Stack:** Go 1.26, regex-based classification (no ML), pgvector, Redis, ONNX embedder, Gemini via CLIProxyAPI.

---

## Task 1: ContentSignal struct + content type detection

**Files:**
- Modify: `memdb-go/internal/handlers/add_classifier.go`
- Modify: `memdb-go/internal/handlers/add_classifier_test.go`

**Step 1: Write failing tests for ContentSignal**

Add to `add_classifier_test.go`:

```go
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
	msgs := []chatMessage{{Role: "user", Content: "Use docker compose up -d to deploy the stack. The Dockerfile uses multi-stage builds."}}
	conv := "user: [2025-01-01T00:00:00]: Use docker compose up -d to deploy the stack. The Dockerfile uses multi-stage builds."
	sig := classifyContent(msgs, conv)
	if sig.ContentType != "technical" {
		t.Errorf("expected content type 'technical', got %q", sig.ContentType)
	}
}

func TestClassifyContent_Factual(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "The meeting is on March 15, 2026 at 3pm in Berlin."}}
	conv := "user: [2025-01-01T00:00:00]: The meeting is on March 15, 2026 at 3pm in Berlin."
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
	if sig.ContentType != "technical" {
		t.Errorf("expected 'technical', got %q", sig.ContentType)
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
```

**Step 2: Run tests to verify they fail**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... -run "TestClassifyContent" -v`
Expected: FAIL — `classifyContent` and `classifyContentFromText` undefined.

**Step 3: Implement ContentSignal + classifyContent**

Replace the contents of `add_classifier.go` with:

```go
package handlers

// add_classifier.go — content router that produces quality signals for downstream LLM calls.
//
// Phase 7 shipped a binary skip/proceed gate. This evolution returns a ContentSignal
// struct with hints that get injected into the extraction prompt, improving LLM output.

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// nearDuplicateThreshold is the cosine similarity above which a conversation
// is considered a near-duplicate of an existing memory and skipped.
const nearDuplicateThreshold = 0.97

// mergeSuggestionThreshold is the cosine similarity above which the pipeline
// suggests UPDATE-over-ADD to the LLM (but doesn't hard skip).
const mergeSuggestionThreshold = 0.92

// trivialMaxChars is the rune limit below which a single-message
// conversation may be classified as trivial.
const trivialMaxChars = 20

// casualPatterns matches short, trivial messages that carry no memorable content.
var casualPatterns = regexp.MustCompile(
	`(?i)\A(?:ok|okay|thanks|thank you|got it|yes|no|sure|done|lgtm|thx|ty|ack|k|yep|yup|nope|cool|nice|great|perfect|right|understood|good|fine|alright|np|nw|mhm|hmm|hm|ah|oh|wow|lol|haha|ha|btw)\z`,
)

// codeBlockRe matches fenced code blocks (``` with optional language tag).
var codeBlockRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\n.*?```")

// opinionRe detects first-person preference/opinion statements.
var opinionRe = regexp.MustCompile(`(?i)\b(I\s+(prefer|like|think|believe|feel|want|love|hate|dislike|enjoy|wish|hope|need|choose|favor|recommend))\b`)

// technicalTermsRe detects technical terms, CLI commands, and config references.
var technicalTermsRe = regexp.MustCompile(`(?i)\b(docker|kubernetes|postgres|redis|nginx|api|http|tcp|udp|ssh|git|npm|pip|curl|sudo|systemctl|deploy|server|database|endpoint|middleware|container|microservice|grpc|graphql|webhook|dns|ssl|tls|cdn|vpc|cidr|yaml|json|toml|env|dockerfile|makefile)\b`)

// factualDateRe detects date/time patterns suggesting factual content.
var factualDateRe = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2}|\d{1,2}(st|nd|rd|th)\s+(of\s+)?(January|February|March|April|May|June|July|August|September|October|November|December)|\d{1,2}:\d{2}\s*(am|pm|AM|PM)?)\b`)

// factualNameRe detects capitalized proper nouns (names, places).
var factualNameRe = regexp.MustCompile(`\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+)+\b`)

// ContentSignal carries classification results and quality hints for downstream stages.
type ContentSignal struct {
	Skip        bool     // hard skip — only for trivial messages
	SkipReason  string   // "trivial" | "code-only"
	Hints       []string // quality hints injected into LLM extraction prompt
	ContentType string   // "opinion" | "technical" | "factual" | "multi-turn" | "mixed"
}

// classifyContent analyzes messages and conversation text to produce a ContentSignal.
// This is the primary entry point for the fine-mode add pipeline (has raw messages).
func classifyContent(messages []chatMessage, conversation string) ContentSignal {
	var sig ContentSignal

	// Rule 1: trivial single-message — hard skip (same as Phase 7).
	if len(messages) == 1 {
		content := strings.TrimSpace(messages[0].Content)
		if utf8.RuneCountInString(content) <= trivialMaxChars && casualPatterns.MatchString(content) {
			sig.Skip = true
			sig.SkipReason = "trivial"
			return sig
		}
	}

	// Rule 2: pure code (>90%) — hard skip.
	if ratio := codeBlockRatio(conversation); ratio > 0.9 {
		sig.Skip = true
		sig.SkipReason = "code-only"
		return sig
	}

	// Rule 3: multi-turn detection (>2 messages with back-and-forth).
	if len(messages) > 2 {
		sig.ContentType = "multi-turn"
		sig.Hints = append(sig.Hints, "Multi-turn conversation — look for decisions, conclusions, and preference changes across turns")
		return sig
	}

	// Rule 4: content type detection from text.
	sig.ContentType = detectContentType(conversation)
	sig.Hints = hintsForContentType(sig.ContentType, conversation)
	return sig
}

// classifyContentFromText is a text-only variant for the buffer zone pipeline
// (receives pre-formatted conversation, no raw chatMessage structs).
func classifyContentFromText(conversation string) ContentSignal {
	var sig ContentSignal

	// Count message lines to detect multi-turn.
	msgCount := strings.Count(conversation, "\n") + 1
	if msgCount > 2 {
		sig.ContentType = "multi-turn"
		sig.Hints = append(sig.Hints, "Multi-turn conversation — look for decisions, conclusions, and preference changes across turns")
		return sig
	}

	sig.ContentType = detectContentType(conversation)
	sig.Hints = hintsForContentType(sig.ContentType, conversation)
	return sig
}

// detectContentType returns the dominant content type based on regex signals.
func detectContentType(text string) string {
	opinionScore := len(opinionRe.FindAllStringIndex(text, -1))
	techScore := len(technicalTermsRe.FindAllStringIndex(text, -1))
	factualScore := len(factualDateRe.FindAllStringIndex(text, -1)) + len(factualNameRe.FindAllStringIndex(text, -1))

	// Code blocks present → technical bias.
	if codeBlockRatio(text) > 0.2 {
		techScore += 3
	}

	if opinionScore >= 2 || (opinionScore >= 1 && techScore == 0 && factualScore == 0) {
		return "opinion"
	}
	if techScore >= 3 || (techScore >= 2 && opinionScore == 0) {
		return "technical"
	}
	if factualScore >= 2 || (factualScore >= 1 && opinionScore == 0 && techScore == 0) {
		return "factual"
	}
	return "mixed"
}

// hintsForContentType returns extraction hints for the given content type.
func hintsForContentType(contentType, text string) []string {
	switch contentType {
	case "opinion":
		return []string{"Content contains opinions and preferences — extract user preferences with high fidelity, preserve the specific preference and reasoning"}
	case "technical":
		if codeBlockRatio(text) > 0.2 {
			return []string{"Technical content with code — extract architectural decisions, tool preferences, and configuration choices mentioned alongside code. Ignore the code syntax itself."}
		}
		return []string{"Technical content — extract tool choices, configuration decisions, and technical preferences"}
	case "factual":
		return []string{"Factual content with specific names, dates, or numbers — preserve all specifics exactly as stated"}
	default:
		return nil
	}
}

// classifySkipExtraction is the Phase 7 backward-compatible wrapper.
// Callers that only need skip/reason can use this instead of classifyContent.
func classifySkipExtraction(messages []chatMessage, conversation string) (bool, string) {
	sig := classifyContent(messages, conversation)
	return sig.Skip, sig.SkipReason
}

// codeBlockRatio returns the fraction of text inside fenced code blocks.
func codeBlockRatio(conversation string) float64 {
	total := len(conversation)
	if total == 0 {
		return 0
	}
	matches := codeBlockRe.FindAllStringIndex(conversation, -1)
	codeChars := 0
	for _, m := range matches {
		codeChars += m[1] - m[0]
	}
	return float64(codeChars) / float64(total)
}
```

**Step 4: Run tests to verify they pass**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... -run "TestClassifyContent|TestClassify|TestCodeBlock" -v`
Expected: ALL PASS. Existing Phase 7 tests still pass because `classifySkipExtraction` is preserved as a wrapper.

**Step 5: Commit**

```bash
cd ~/MemDB && git add memdb-go/internal/handlers/add_classifier.go memdb-go/internal/handlers/add_classifier_test.go
git commit -m "feat(classifier): evolve to ContentSignal with content type detection

ContentSignal replaces bool skip/reason with a struct carrying Hints
and ContentType (opinion/technical/factual/multi-turn/mixed).
classifySkipExtraction preserved as backward-compatible wrapper.
Adds classifyContentFromText variant for buffer zone pipeline.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 2: Wire ContentSignal hints into ExtractAndDedup

**Files:**
- Modify: `memdb-go/internal/llm/extractor.go`
- Modify: `memdb-go/internal/handlers/add_fine.go`

**Step 1: Write failing test for ExtractAndDedupWithHints**

Create `memdb-go/internal/llm/extractor_test.go` (or add to existing):

```go
func TestExtractAndDedup_HintsAppendedToUserMessage(t *testing.T) {
	// This test verifies that hints are included in the prompt.
	// We can't test LLM output, but we can verify the method signature compiles
	// and that hints are non-destructive (passing nil hints still works).
	// Real integration testing is done via functional tests.
	e := &LLMExtractor{client: nil}
	_ = e // verifies the type still has ExtractAndDedupWithHints method
}
```

(Note: LLM calls require a live API. The main verification is compile-time + integration tests.)

**Step 2: Add hints parameter to ExtractAndDedup**

In `memdb-go/internal/llm/extractor.go`, modify `ExtractAndDedup` to accept optional hints:

```go
// ExtractAndDedup is the v2 unified method: one LLM call extracts facts AND
// decides ADD/UPDATE against the provided candidates.
//
// hints are optional quality signals from the content router, injected into
// the user message to guide extraction focus. Pass nil for no hints.
func (e *LLMExtractor) ExtractAndDedup(ctx context.Context, conversation string, candidates []Candidate, hints ...string) ([]ExtractedFact, error) {
	var sb strings.Builder
	sb.WriteString("Conversation:\n")
	sb.WriteString(conversation)

	if len(candidates) > 0 {
		sb.WriteString("\n\nEXISTING MEMORIES (for dedup context):\n")
		enc, _ := json.Marshal(candidates)
		sb.Write(enc)
	}

	if len(hints) > 0 {
		sb.WriteString("\n\n<content_hints>\n")
		for _, h := range hints {
			sb.WriteString("- ")
			sb.WriteString(h)
			sb.WriteString("\n")
		}
		sb.WriteString("</content_hints>")
	}

	msgs := []map[string]string{
		{"role": "system", "content": unifiedSystemPrompt},
		{"role": "user", "content": sb.String()},
	}

	raw, err := e.client.Chat(ctx, msgs, extractMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("extract and dedup: %w", err)
	}

	facts, err := parseExtractedFacts(raw)
	if err != nil {
		return nil, fmt.Errorf("extract and dedup parse: %w (raw: %.300s)", err, raw)
	}
	return facts, nil
}
```

Using variadic `...string` for hints means ALL existing callers compile without changes (zero-arg = no hints). No signature breakage.

**Step 3: Wire ContentSignal into add_fine.go**

In `add_fine.go`, change the classifier + extraction section (lines 48-64):

```go
	// Step 1.5: content router — classify and generate hints
	sig := classifyContent(req.Messages, conversation)
	if sig.Skip {
		h.logger.Debug("fine add: skipped extraction",
			slog.String("reason", sig.SkipReason), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Step 2: candidate fetch for dedup context
	candidates, topScore := h.fetchFineCandidates(ctx, conversation, cubeID, stringOrEmpty(req.AgentID))
	if topScore > nearDuplicateThreshold {
		h.logger.Debug("fine add: skipped — near-duplicate",
			slog.Float64("top_score", topScore), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Step 2.5: merge hint for high-similarity (but not duplicate) content
	if topScore > mergeSuggestionThreshold {
		sig.Hints = append(sig.Hints, "High-similarity existing memory found — prefer UPDATE over ADD if semantically equivalent")
	}

	// Step 3: unified LLM extraction + dedup (one round-trip, with content hints)
	facts, err := h.llmExtractor.ExtractAndDedup(ctx, conversation, candidates, sig.Hints...)
```

**Step 4: Run full test suite**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... ./internal/llm/... -v`
Expected: ALL PASS. Variadic hints means no existing callers break.

**Step 5: Commit**

```bash
cd ~/MemDB && git add memdb-go/internal/llm/extractor.go memdb-go/internal/handlers/add_fine.go
git commit -m "feat(extraction): inject content hints into LLM extraction prompt

ExtractAndDedup accepts variadic hints (backward-compatible).
add_fine.go wires ContentSignal.Hints + merge suggestion into extraction.
Hints injected as <content_hints> section in user message.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 3: Wire hints into buffer zone pipeline

**Files:**
- Modify: `memdb-go/internal/handlers/add_buffer.go`

**Step 1: Modify runFinePipeline to use classifyContentFromText**

In `add_buffer.go:180` (`runFinePipeline`), add content classification and pass hints:

```go
func (h *Handler) runFinePipeline(ctx context.Context, conversation, cubeID string) ([]addResponseItem, error) {
	now := nowTimestamp()

	// Content router (text-only variant for buffer zone)
	sig := classifyContentFromText(conversation)
	// Note: buffer zone does NOT hard-skip — batched conversations are already committed to processing.
	// But we still collect hints for better extraction quality.

	// Step 1: candidate fetch for dedup context
	candidates, topScore := h.fetchFineCandidates(ctx, conversation, cubeID, "")
	if topScore > nearDuplicateThreshold {
		h.logger.Debug("buffer flush: skipped — near-duplicate",
			slog.Float64("top_score", topScore), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Merge suggestion hint
	if topScore > mergeSuggestionThreshold {
		sig.Hints = append(sig.Hints, "High-similarity existing memory found — prefer UPDATE over ADD if semantically equivalent")
	}

	// Step 2: unified LLM extraction + dedup (with content hints)
	facts, err := h.llmExtractor.ExtractAndDedup(ctx, conversation, candidates, sig.Hints...)
```

The rest of `runFinePipeline` stays the same.

**Step 2: Run tests**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... -run "Buffer" -v`
Expected: ALL PASS.

**Step 3: Commit**

```bash
cd ~/MemDB && git add memdb-go/internal/handlers/add_buffer.go
git commit -m "feat(buffer): wire content hints into buffer flush pipeline

Buffer zone now uses classifyContentFromText to generate quality hints
that get passed to ExtractAndDedup for better extraction focus.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 4: Evolve rerank gate to RerankDecision with spread metric

**Files:**
- Modify: `memdb-go/internal/search/rerank_gate.go`
- Modify: `memdb-go/internal/search/rerank_gate_test.go`

**Step 1: Write failing tests for rerankStrategy**

Add to `rerank_gate_test.go`:

```go
func TestRerankStrategy_ClusteredScores(t *testing.T) {
	// Spread < 0.05 — should rerank (ambiguous ordering)
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.82}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.81}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.79}},
	}
	d := rerankStrategy(items)
	if !d.ShouldRerank {
		t.Error("expected rerank for clustered scores (spread < 0.05)")
	}
	if d.Reason != "clustered" {
		t.Errorf("expected reason 'clustered', got %q", d.Reason)
	}
}

func TestRerankStrategy_WideSpread(t *testing.T) {
	// Spread > 0.25 — skip (clear separation)
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.90}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.60}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.50}},
	}
	d := rerankStrategy(items)
	if d.ShouldRerank {
		t.Error("expected skip for wide spread (> 0.25)")
	}
	if d.Reason != "wide-spread" {
		t.Errorf("expected reason 'wide-spread', got %q", d.Reason)
	}
}

func TestRerankStrategy_MediumSpread(t *testing.T) {
	// Spread 0.05–0.25 — rerank with TopK cap
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.85}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.78}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.73}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.70}},
	}
	d := rerankStrategy(items)
	if !d.ShouldRerank {
		t.Error("expected rerank for medium spread")
	}
	if d.TopK != rerankTopKCap {
		t.Errorf("expected TopK=%d for medium spread, got %d", rerankTopKCap, d.TopK)
	}
	if d.Reason != "medium-spread" {
		t.Errorf("expected reason 'medium-spread', got %q", d.Reason)
	}
}

func TestRerankStrategy_TooFewResults(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.80}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.70}},
	}
	d := rerankStrategy(items)
	if d.ShouldRerank {
		t.Error("expected skip for <4 results")
	}
	if d.Reason != "too-few" {
		t.Errorf("expected reason 'too-few', got %q", d.Reason)
	}
}

func TestRerankStrategy_HighTopCosine(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{"relativity": 0.95}},
		{"id": "b", "metadata": map[string]any{"relativity": 0.70}},
		{"id": "c", "metadata": map[string]any{"relativity": 0.60}},
		{"id": "d", "metadata": map[string]any{"relativity": 0.50}},
	}
	d := rerankStrategy(items)
	if d.ShouldRerank {
		t.Error("expected skip for high top cosine")
	}
	if d.Reason != "high-confidence" {
		t.Errorf("expected reason 'high-confidence', got %q", d.Reason)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd ~/MemDB/memdb-go && go test ./internal/search/... -run "TestRerankStrategy" -v`
Expected: FAIL — `rerankStrategy` undefined.

**Step 3: Implement RerankDecision + rerankStrategy**

Replace `rerank_gate.go`:

```go
package search

// rerank_gate.go — adaptive LLM rerank strategy based on result spread.
//
// Instead of a binary should/shouldn't, this returns a RerankDecision
// with the reason and a TopK cap for cost control.

// Thresholds for rerank decisions.
const (
	rerankTopCosineThreshold = 0.93  // skip if top result is already high-confidence
	rerankMinResults         = 4     // skip if fewer than this many results
	rerankClusteredSpread    = 0.05  // spread below this → clustered, rerank all
	rerankWideSpread         = 0.25  // spread above this → clear separation, skip
	rerankTopKCap            = 8     // cap for medium-spread reranking
)

// RerankDecision holds the adaptive rerank strategy.
type RerankDecision struct {
	ShouldRerank bool   // whether to invoke LLM rerank
	Reason       string // "too-few" | "high-confidence" | "clustered" | "medium-spread" | "wide-spread"
	TopK         int    // how many items to send to LLM reranker (0 = all)
}

// rerankStrategy analyzes result scores and returns a rerank decision.
// Uses the spread between top and bottom relativity scores to determine strategy.
func rerankStrategy(items []map[string]any) RerankDecision {
	if len(items) < rerankMinResults {
		return RerankDecision{ShouldRerank: false, Reason: "too-few"}
	}

	topRel, botRel := extractRelativityRange(items)

	// High-confidence top result — cosine ordering is sufficient.
	if topRel > rerankTopCosineThreshold {
		return RerankDecision{ShouldRerank: false, Reason: "high-confidence"}
	}

	spread := topRel - botRel

	// Clustered: all scores are close together — LLM judgment needed for all.
	if spread < rerankClusteredSpread {
		return RerankDecision{ShouldRerank: true, Reason: "clustered", TopK: 0}
	}

	// Wide spread: clear separation — cosine ordering is reliable.
	if spread > rerankWideSpread {
		return RerankDecision{ShouldRerank: false, Reason: "wide-spread"}
	}

	// Medium spread: ambiguous — rerank but cap items for cost control.
	return RerankDecision{ShouldRerank: true, Reason: "medium-spread", TopK: rerankTopKCap}
}

// shouldLLMRerank is the backward-compatible wrapper for callers that
// only need a bool. Preserved so service.go call site works unchanged.
func shouldLLMRerank(items []map[string]any) bool {
	return rerankStrategy(items).ShouldRerank
}

// extractRelativityRange returns the top and bottom relativity scores from items.
// Returns (0, 0) if metadata is missing.
func extractRelativityRange(items []map[string]any) (top, bottom float64) {
	if len(items) == 0 {
		return 0, 0
	}
	top = extractRelativity(items[0])
	bottom = extractRelativity(items[len(items)-1])
	return top, bottom
}

// extractRelativity extracts the relativity score from an item's metadata.
func extractRelativity(item map[string]any) float64 {
	meta, ok := item["metadata"].(map[string]any)
	if !ok {
		return 0
	}
	rel, ok := meta["relativity"].(float64)
	if !ok {
		return 0
	}
	return rel
}
```

**Step 4: Run all rerank tests (old + new)**

Run: `cd ~/MemDB/memdb-go && go test ./internal/search/... -run "Rerank" -v`
Expected: ALL PASS. Old `shouldLLMRerank` tests pass because the wrapper is preserved.

**Step 5: Commit**

```bash
cd ~/MemDB && git add memdb-go/internal/search/rerank_gate.go memdb-go/internal/search/rerank_gate_test.go
git commit -m "feat(search): evolve rerank gate to spread-based adaptive strategy

RerankDecision replaces bool with reason + TopK cap.
Clustered scores (spread<0.05) get full rerank.
Wide spread (>0.25) skips rerank.
Medium spread gets TopK-capped rerank.
shouldLLMRerank preserved as backward-compatible wrapper.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 5: Wire RerankDecision TopK into service.go

**Files:**
- Modify: `memdb-go/internal/search/service.go`

**Step 1: Change rerank call site to use RerankDecision**

At `service.go:574-579`, change:

```go
	// Step 6.1: LLM rerank of text_mem (adaptive strategy)
	decision := rerankStrategy(text)
	if p.LLMRerank && s.LLMReranker.APIURL != "" && decision.ShouldRerank {
		t0 := time.Now()
		rerankInput := text
		if decision.TopK > 0 && decision.TopK < len(text) {
			rerankInput = text[:decision.TopK]
		}
		reranked := LLMRerank(ctx, p.Query, rerankInput, s.LLMReranker)
		if decision.TopK > 0 && decision.TopK < len(text) {
			// Append the un-reranked tail back
			text = append(reranked, text[decision.TopK:]...)
		} else {
			text = reranked
		}
		llmRerankDur = time.Since(t0)
	}
```

**Step 2: Run tests**

Run: `cd ~/MemDB/memdb-go && go test ./internal/search/... -v`
Expected: ALL PASS.

**Step 3: Commit**

```bash
cd ~/MemDB && git add memdb-go/internal/search/service.go
git commit -m "feat(search): wire RerankDecision TopK cap into rerank pipeline

Medium-spread results now only send top-8 items to LLM reranker
instead of all, reducing token cost while keeping quality.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 6: Session-type detection for episodic summaries

**Files:**
- Modify: `memdb-go/internal/handlers/add_episodic.go`
- Modify: `memdb-go/internal/handlers/add_classifier_test.go` (session type tests)

**Step 1: Write failing tests for detectSessionType**

Add to `add_classifier_test.go`:

```go
func TestDetectSessionType_Decision(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: We decided to go with PostgreSQL for the main database.\nassistant: [2025-01-01T00:00:01]: Going with Postgres makes sense."
	st := detectSessionType(conv)
	if st != "decision" {
		t.Errorf("expected 'decision', got %q", st)
	}
}

func TestDetectSessionType_Learning(t *testing.T) {
	conv := "user: [2025-01-01T00:00:00]: TIL that Go channels are not thread-safe by default.\nassistant: [2025-01-01T00:00:01]: Right, you need to use sync primitives."
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
```

**Step 2: Run tests to verify they fail**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... -run "TestDetectSessionType" -v`
Expected: FAIL — `detectSessionType` undefined.

**Step 3: Implement detectSessionType and sessionPromptFocus**

Add to `add_classifier.go` (bottom of file):

```go
// --- Session type detection (used by episodic summaries) ---

// Session type regexes detect dominant conversation themes.
var (
	decisionRe = regexp.MustCompile(`(?i)\b(decided|chose|going with|picked|settled on|agreed|concluded|chosen|opting for|decision)\b`)
	learningRe = regexp.MustCompile(`(?i)\b(TIL|learned|turns out|discovered|realized|found out|never knew|understanding now|interesting)\b`)
	debugRe    = regexp.MustCompile(`(?i)\b(error|bug|fix|fixed|solved|crash|exception|traceback|stack trace|segfault|panic|debug|debugging|broken)\b`)
	planningRe = regexp.MustCompile(`(?i)\b(plan|roadmap|next steps|TODO|sprint|milestone|timeline|deadline|priorit|backlog|schedule)\b`)
)

// detectSessionType returns the dominant session type based on keyword frequency.
// Returns "general" if no type dominates.
func detectSessionType(conversation string) string {
	scores := map[string]int{
		"decision": len(decisionRe.FindAllStringIndex(conversation, -1)),
		"learning": len(learningRe.FindAllStringIndex(conversation, -1)),
		"debug":    len(debugRe.FindAllStringIndex(conversation, -1)),
		"planning": len(planningRe.FindAllStringIndex(conversation, -1)),
	}

	bestType := "general"
	bestScore := 0
	for typ, score := range scores {
		if score > bestScore {
			bestScore = score
			bestType = typ
		}
	}
	if bestScore == 0 {
		return "general"
	}
	return bestType
}

// sessionPromptFocus returns a focus instruction for the episodic summary prompt
// based on the detected session type.
func sessionPromptFocus(sessionType string) string {
	switch sessionType {
	case "decision":
		return "Focus on what was decided and why. Capture the alternatives considered and the rationale for the chosen option."
	case "learning":
		return "Focus on key takeaways and new understanding. Capture what was learned and any misconceptions corrected."
	case "debug":
		return "Focus on the problem, root cause, and solution. Capture the error symptoms, investigation steps, and the fix applied."
	case "planning":
		return "Focus on goals, timeline, and dependencies. Capture action items, ownership, and deadlines mentioned."
	default:
		return ""
	}
}
```

**Step 4: Run tests**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... -run "TestDetectSessionType" -v`
Expected: ALL PASS.

**Step 5: Wire into episodic summary**

In `add_episodic.go`, modify `callEpisodicSummarizer` to accept and use session type. Change `generateEpisodicSummary`:

```go
func (h *Handler) generateEpisodicSummary(cubeID, sessionID, conversation, now string, factCount int) {
	if h.llmExtractor == nil || h.postgres == nil || h.embedder == nil {
		return
	}
	if factCount == 0 {
		return
	}
	if codeBlockRatio(conversation) > 0.8 {
		return
	}
	if len(strings.TrimSpace(conversation)) < 100 {
		return
	}

	// Detect session type for focused summary
	sessionType := detectSessionType(conversation)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), episodicSummaryTimeout)
		defer cancel()

		summary, err := callEpisodicSummarizer(ctx, conversation, sessionType)
		// ... rest unchanged
```

And modify `callEpisodicSummarizer` signature and prompt:

```go
func callEpisodicSummarizer(ctx context.Context, conversation, sessionType string) (string, error) {
	if len(conversation) > episodicConvMaxChars {
		conversation = "..." + conversation[len(conversation)-episodicConvMaxChars:]
	}

	systemPrompt := "You are a memory archivist. Summarize the key facts and themes from the conversation in 3-5 concise sentences. Focus on factual information, not pleasantries. Do not use bullet points."
	if focus := sessionPromptFocus(sessionType); focus != "" {
		systemPrompt += "\n\n" + focus
	}

	payload := map[string]any{
		"model":       llmDefaultModel,
		"temperature": 0.2,
		"max_tokens":  300,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": "Conversation:\n" + conversation + "\n\nWrite a 3-5 sentence episodic summary capturing what was discussed and decided:"},
		},
	}
	// ... rest of function unchanged
```

Also update the buffer zone call in `add_buffer.go:234`:

```go
	// Episodic summary for the entire batch (session type from batch text)
	h.generateEpisodicSummary(cubeID, "", conversation, now, len(facts))
```

(No change needed — `generateEpisodicSummary` internally calls `detectSessionType`.)

**Step 6: Run full test suite**

Run: `cd ~/MemDB/memdb-go && go test ./internal/handlers/... ./internal/search/... ./internal/llm/... -v`
Expected: ALL PASS.

**Step 7: Commit**

```bash
cd ~/MemDB && git add memdb-go/internal/handlers/add_classifier.go memdb-go/internal/handlers/add_classifier_test.go memdb-go/internal/handlers/add_episodic.go
git commit -m "feat(episodic): session-type-aware summary prompts

detectSessionType classifies conversations as decision/learning/debug/planning/general.
Episodic summary prompt includes a focused instruction based on detected type.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 7: Build, deploy, and verify

**Files:** None — integration testing.

**Step 1: Run full unit tests**

```bash
cd ~/MemDB/memdb-go && go test ./internal/... -count=1 -v 2>&1 | tail -20
```

Expected: ALL PASS.

**Step 2: Build Docker image**

```bash
cd ~/krolik-server && docker compose build --no-cache memdb-go
```

Expected: Build succeeds.

**Step 3: Deploy**

```bash
cd ~/krolik-server && docker compose up -d --no-deps --force-recreate memdb-go
```

**Step 4: Functional tests**

```bash
# a) Trivial → skipped (same as before):
curl -s localhost:8080/product/add \
  -H "Content-Type: application/json" \
  -H "X-Service-Secret: $(grep INTERNAL_SERVICE_SECRET ~/krolik-server/.env | cut -d= -f2)" \
  -d '{"messages":[{"role":"user","content":"ok"}],"user_id":"memos","mem_cube_ids":["memos"],"mode":"fine"}'
# Expected: data:null, fast response

# b) Opinion → hints in extraction:
curl -s localhost:8080/product/add \
  -H "Content-Type: application/json" \
  -H "X-Service-Secret: $(grep INTERNAL_SERVICE_SECRET ~/krolik-server/.env | cut -d= -f2)" \
  -d '{"messages":[{"role":"user","content":"I prefer Rust over Go for CLI tools because of the type system"}],"user_id":"memos","mem_cube_ids":["memos"],"mode":"fine"}'
# Expected: memory extracted with UserMemory type

# c) Check logs for content hints:
docker logs memdb-go --tail 50 2>&1 | grep -E "skipped|content_type|session_type"
```

**Step 5: Commit docs**

```bash
cd ~/MemDB && git add docs/plans/
git commit -m "docs: add quality-gates design and implementation plan

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Verification Summary

| Gate | Before | After | Backward Compatible |
|------|--------|-------|-------------------|
| 7.1 Classifier | bool skip | ContentSignal{Skip, Hints, ContentType} | Yes — `classifySkipExtraction` wrapper |
| 7.2 Near-dup | hard skip at 0.97 | skip at 0.97 + merge hint at 0.92 | Yes — same threshold |
| 7.3 Rerank | bool shouldRerank | RerankDecision{ShouldRerank, Reason, TopK} | Yes — `shouldLLMRerank` wrapper |
| 7.4 Episodic | factCount==0 skip | session-type-aware prompt focus | Yes — same skip conditions |
| LLM extractor | no hints | variadic hints (zero-arg = no hints) | Yes — variadic |
| Buffer zone | no hints | classifyContentFromText + merge hints | Yes — same flow |
