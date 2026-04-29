package handlers

import (
	"strings"
	"testing"
)

func TestDetectLang_English(t *testing.T) {
	if lang := detectLang("Hello, how are you?"); lang != "en" {
		t.Errorf("detectLang(English) = %q, want 'en'", lang)
	}
}

func TestDetectLang_Chinese(t *testing.T) {
	if lang := detectLang("你好世界"); lang != "zh" {
		t.Errorf("detectLang(Chinese) = %q, want 'zh'", lang)
	}
}

func TestDetectLang_Russian(t *testing.T) {
	if lang := detectLang("Привет мир"); lang != "ru" {
		t.Errorf("detectLang(Russian) = %q, want 'ru'", lang)
	}
}

func TestDetectLang_Mixed(t *testing.T) {
	// English with a few Russian words — majority English
	if lang := detectLang("Hello world from Moscow город"); lang != "en" {
		t.Errorf("detectLang(mixed) = %q, want 'en'", lang)
	}
}

func TestDetectLang_Empty(t *testing.T) {
	if lang := detectLang(""); lang != "en" {
		t.Errorf("detectLang('') = %q, want 'en'", lang)
	}
}

func TestDetectLang_OnlyDigits(t *testing.T) {
	if lang := detectLang("12345"); lang != "en" {
		t.Errorf("detectLang(digits) = %q, want 'en'", lang)
	}
}

func TestBuildSystemPrompt_DefaultEN(t *testing.T) {
	memories := []map[string]any{
		{"memory": "User likes Go"},
		{"memory": "User lives in Moscow"},
	}
	prompt := buildSystemPrompt("Hello", memories, "", "", "")

	if !strings.Contains(prompt, "1. User likes Go") {
		t.Error("prompt missing memory 1")
	}
	if !strings.Contains(prompt, "2. User lives in Moscow") {
		t.Error("prompt missing memory 2")
	}
	// Should use EN template (query is English)
	if !strings.Contains(prompt, "MemDB") {
		t.Error("prompt missing MemDB reference from EN template")
	}
}

func TestBuildSystemPrompt_DefaultZH(t *testing.T) {
	memories := []map[string]any{
		{"memory": "用户喜欢编程"},
	}
	prompt := buildSystemPrompt("你好", memories, "", "", "")

	// Should use ZH template (query is Chinese)
	if !strings.Contains(prompt, "智能助手") {
		t.Error("prompt missing ZH template text")
	}
}

func TestBuildSystemPrompt_CustomBase(t *testing.T) {
	prompt := buildSystemPrompt("test", nil, "", "You are a helpful assistant.", "")
	if prompt != "You are a helpful assistant." {
		t.Errorf("prompt = %q, want custom base", prompt)
	}
}

func TestBuildSystemPrompt_CustomBaseWithPlaceholder(t *testing.T) {
	memories := []map[string]any{
		{"memory": "fact one"},
	}
	prompt := buildSystemPrompt("test", memories, "", "Context: {memories}", "")
	if !strings.Contains(prompt, "1. fact one") {
		t.Errorf("prompt = %q, missing formatted memories", prompt)
	}
	if strings.Contains(prompt, "{memories}") {
		t.Error("prompt still contains {memories} placeholder")
	}
}

func TestBuildSystemPrompt_CustomBaseAppendMemories(t *testing.T) {
	memories := []map[string]any{
		{"memory": "fact one"},
	}
	prompt := buildSystemPrompt("test", memories, "", "Base system prompt.", "")
	if !strings.Contains(prompt, "## Fact Memories:") {
		t.Error("prompt missing '## Fact Memories:' header")
	}
	if !strings.Contains(prompt, "1. fact one") {
		t.Error("prompt missing memory text")
	}
}

func TestBuildSystemPrompt_WithPrefString(t *testing.T) {
	memories := []map[string]any{
		{"memory": "fact"},
	}
	prompt := buildSystemPrompt("hello", memories, "User prefers short answers", "", "")
	if !strings.Contains(prompt, "User prefers short answers") {
		t.Error("prompt missing pref string")
	}
}

func TestFormatMemories_Empty(t *testing.T) {
	result := formatMemories(nil, "")
	if result != "" {
		t.Errorf("formatMemories(nil, '') = %q, want empty", result)
	}
}

func TestFormatMemories_WithPrefOnly(t *testing.T) {
	result := formatMemories(nil, "short answers please")
	// No memories → output is just "\n\n" + prefString
	if !strings.Contains(result, "short answers please") {
		t.Errorf("result = %q, want to contain pref string", result)
	}
}

func TestChatBuildMessages_Basic(t *testing.T) {
	msgs := chatBuildMessages("system prompt", "user query", nil)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "system prompt" {
		t.Errorf("msgs[0] = %v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "user query" {
		t.Errorf("msgs[1] = %v", msgs[1])
	}
}

func TestChatBuildMessages_WithHistory(t *testing.T) {
	history := []map[string]string{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "hello"},
	}
	msgs := chatBuildMessages("sys", "new query", history)
	// system + 2 history + user query = 4
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
}

func TestChatBuildMessages_TruncatesHistory(t *testing.T) {
	// Create 30 history messages (exceeds chatMaxHistory=20)
	history := make([]map[string]string, 30)
	for i := range history {
		history[i] = map[string]string{"role": "user", "content": "msg"}
	}
	msgs := chatBuildMessages("sys", "query", history)
	// system + 20 (truncated) + user = 22
	if len(msgs) != 22 {
		t.Fatalf("expected 22 messages, got %d", len(msgs))
	}
}

func TestFilterOuterMemory_Basic(t *testing.T) {
	memories := []map[string]any{
		{"memory": "personal fact", "metadata": map[string]any{"memory_type": "PersonalMemory"}},
		{"memory": "internet fact", "metadata": map[string]any{"memory_type": "OuterMemory"}},
		{"memory": "another personal", "metadata": map[string]any{"memory_type": "PersonalMemory"}},
	}
	result := filterOuterMemory(memories)
	if len(result) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(result))
	}
	for _, m := range result {
		if memType(m) == memTypeOuter {
			t.Error("OuterMemory should be filtered out")
		}
	}
}

func TestFilterOuterMemory_Empty(t *testing.T) {
	result := filterOuterMemory(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestFilterMemoriesByThreshold_AboveThreshold(t *testing.T) {
	memories := []map[string]any{
		{"memory": "high", "metadata": map[string]any{"relativity": 0.9, "memory_type": "PersonalMemory"}},
		{"memory": "mid", "metadata": map[string]any{"relativity": 0.5, "memory_type": "PersonalMemory"}},
		{"memory": "low", "metadata": map[string]any{"relativity": 0.1, "memory_type": "PersonalMemory"}},
	}
	result := filterMemoriesByThreshold(memories, 0.3, 1)
	// high + mid are above 0.3
	if len(result) < 2 {
		t.Errorf("expected at least 2, got %d", len(result))
	}
}

func TestFilterMemoriesByThreshold_MinNum(t *testing.T) {
	memories := []map[string]any{
		{"memory": "only one above", "metadata": map[string]any{"relativity": 0.9, "memory_type": "PersonalMemory"}},
		{"memory": "below threshold", "metadata": map[string]any{"relativity": 0.1, "memory_type": "PersonalMemory"}},
		{"memory": "also below", "metadata": map[string]any{"relativity": 0.05, "memory_type": "PersonalMemory"}},
	}
	// Threshold 0.5 → only 1 above, but minNum=3 forces all 3
	result := filterMemoriesByThreshold(memories, 0.5, 3)
	if len(result) < 3 {
		t.Errorf("expected at least 3 (minNum), got %d", len(result))
	}
}

func TestFilterMemoriesByThreshold_Empty(t *testing.T) {
	result := filterMemoriesByThreshold(nil, 0.3, 3)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

// M12.1 — chatPromptNow honours MEMDB_CHAT_NOW_OVERRIDE so harnesses can
// pin the "Current Time" baseline against historic conversation dates.

func TestChatPromptNow_NoOverrideUsesWallClock(t *testing.T) {
	t.Setenv(chatNowOverrideEnvVar, "")
	got := chatPromptNow()
	// We can't pin the actual value (clock moves), but format is fixed.
	// "2006-01-02 15:04 (Monday)" → 23 chars including the day name.
	if len(got) < len("2006-01-02 15:04 (Mon)") {
		t.Fatalf("chatPromptNow() = %q, format too short for %%Y-%%m-%%d %%H:%%M (%%A)", got)
	}
	// Sanity — must contain at least one digit and a paren for the weekday.
	if !strings.ContainsAny(got, "0123456789") || !strings.Contains(got, "(") {
		t.Errorf("chatPromptNow() = %q, expected wall-clock format", got)
	}
}

func TestChatPromptNow_OverrideWins(t *testing.T) {
	t.Setenv(chatNowOverrideEnvVar, "2023-05-08")
	if got := chatPromptNow(); got != "2023-05-08" {
		t.Errorf("chatPromptNow() = %q, want %q", got, "2023-05-08")
	}
}

func TestChatPromptNow_WhitespaceIsTrimmed(t *testing.T) {
	t.Setenv(chatNowOverrideEnvVar, "  2023-05-08  ")
	if got := chatPromptNow(); got != "2023-05-08" {
		t.Errorf("chatPromptNow() = %q, want trimmed %q", got, "2023-05-08")
	}
}

func TestChatPromptNow_BlankOverrideFallsBackToWallClock(t *testing.T) {
	// An override of just whitespace must NOT inject an empty header —
	// fall back to wall-clock (legacy behaviour).
	t.Setenv(chatNowOverrideEnvVar, "   ")
	got := chatPromptNow()
	if got == "" || got == "   " {
		t.Errorf("chatPromptNow() = %q, want wall-clock fallback when override is blank", got)
	}
}

func TestBuildSystemPrompt_FactualHonoursNowOverride(t *testing.T) {
	t.Setenv(chatNowOverrideEnvVar, "2023-05-08 12:00 (Monday)")
	memories := []map[string]any{{"memory": "fact"}}
	prompt := buildSystemPrompt("test", memories, "", "", answerStyleFactual)
	if !strings.Contains(prompt, "Current Time: 2023-05-08 12:00 (Monday)") {
		t.Errorf("factual prompt did not honour MEMDB_CHAT_NOW_OVERRIDE: %q", prompt)
	}
}

// M12.1 — observation_date is emitted only when ObservationDate is set, and
// (as a top-level prop) survives the sources-strip in search/response.go so
// retrieval clients can read it from metadata.

func TestBuildNodeProps_ObservationDateEmittedWhenSet(t *testing.T) {
	props := buildNodeProps(memoryNodeProps{
		ID:              "id1",
		Memory:          "Marcus got promoted",
		MemoryType:      "LongTermMemory",
		UserName:        "cube1",
		Mode:            modeRaw,
		Now:             "2026-04-26T12:00:00",
		CreatedAt:       "2026-04-26T12:00:00",
		Info:            map[string]any{},
		ObservationDate: "2023-05-08",
		ExtractionState: extractionStateExtracted,
	})
	if got, ok := props["observation_date"].(string); !ok || got != "2023-05-08" {
		t.Errorf("observation_date = %v, want \"2023-05-08\"", props["observation_date"])
	}
}

func TestBuildNodeProps_ObservationDateOmittedWhenEmpty(t *testing.T) {
	// Back-compat: callers that don't set ObservationDate must not introduce
	// the field. Keeps payload lean and prevents downstream consumers from
	// reading "" as a sentinel.
	props := buildNodeProps(memoryNodeProps{
		ID:              "id1",
		Memory:          "fact",
		MemoryType:      "LongTermMemory",
		UserName:        "cube1",
		Mode:            modeFast,
		Now:             "2026-04-26T12:00:00",
		CreatedAt:       "2026-04-26T12:00:00",
		Info:            map[string]any{},
		ExtractionState: extractionStateExtracted,
	})
	if _, ok := props["observation_date"]; ok {
		t.Errorf("observation_date must be absent when ObservationDate is empty, got %v", props["observation_date"])
	}
}

// observationDateFromMessages picks the LATEST chat_time and tolerates
// missing/short values.

func TestObservationDateFromMessages_LatestWins(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "a", ChatTime: "2023-05-08T13:56:00"},
		{Role: "assistant", Content: "b", ChatTime: "2023-05-09T09:00:00"},
		{Role: "user", Content: "c", ChatTime: "2023-05-09T18:00:00"},
	}
	if got := observationDateFromMessages(msgs); got != "2023-05-09" {
		t.Errorf("observationDateFromMessages = %q, want 2023-05-09", got)
	}
}

func TestObservationDateFromMessages_SkipsShortValues(t *testing.T) {
	// Short strings (<10 chars) must be skipped — fall through to a usable one.
	msgs := []chatMessage{
		{Role: "user", Content: "a", ChatTime: "2023-05-08T13:56:00"},
		{Role: "user", Content: "b", ChatTime: "bad"},
	}
	if got := observationDateFromMessages(msgs); got != "2023-05-08" {
		t.Errorf("observationDateFromMessages = %q, want 2023-05-08 (skipped \"bad\")", got)
	}
}

func TestObservationDateFromMessages_AllEmptyReturnsBlank(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	if got := observationDateFromMessages(msgs); got != "" {
		t.Errorf("observationDateFromMessages = %q, want \"\"", got)
	}
}

func TestObservationDateFromMessages_NilSliceReturnsBlank(t *testing.T) {
	if got := observationDateFromMessages(nil); got != "" {
		t.Errorf("observationDateFromMessages(nil) = %q, want \"\"", got)
	}
}
