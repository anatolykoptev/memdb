package handlers

// chat_prompt_factual_test.go — unit tests for the answer_style="factual"
// routing in buildSystemPrompt. Split from chat_prompt_test.go to keep both
// files under the 200-line target / 300-line hard cap (repo policy).

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_FactualEN(t *testing.T) {
	memories := []map[string]any{
		{"memory": "User likes Go"},
	}
	prompt := buildSystemPrompt("What language does the user like?", memories, "", "", "factual")

	if !strings.Contains(prompt, "SHORTEST factual phrase") {
		t.Error("prompt missing factual EN signature 'SHORTEST factual phrase'")
	}
	if strings.Contains(prompt, "MemDB Assistant") || strings.Contains(prompt, "Four-Step Verdict") {
		t.Error("factual EN prompt should not contain default cloud chat boilerplate")
	}
}

func TestBuildSystemPrompt_FactualZH(t *testing.T) {
	// Sanity: detectLang must classify the query as zh; otherwise the test premise
	// (factual ZH branch) is not exercised.
	if got := detectLang("你好"); got != "zh" {
		t.Fatalf("precondition: detectLang('你好') = %q, want 'zh'", got)
	}
	memories := []map[string]any{
		{"memory": "用户喜欢 Go"},
	}
	prompt := buildSystemPrompt("你好", memories, "", "", "factual")

	if !strings.Contains(prompt, "严格遵守") {
		t.Error("factual ZH prompt missing translated rules header '严格遵守'")
	}
	// Rule 6 must keep the literal English "no answer" so LoCoMo scoring matches.
	if !strings.Contains(prompt, "no answer") {
		t.Error("factual ZH prompt must keep literal 'no answer' (rule 6)")
	}
	if strings.Contains(prompt, "智能助手") {
		t.Error("factual ZH prompt should not contain default cloud chat ZH text")
	}
}

func TestBuildSystemPrompt_FactualWithCustomBase(t *testing.T) {
	// basePrompt non-empty must win over answer_style=factual (backward-compat).
	prompt := buildSystemPrompt("anything", nil, "", "Custom system prompt.", "factual")
	if prompt != "Custom system prompt." {
		t.Errorf("prompt = %q, want raw custom base — basePrompt must win over answer_style", prompt)
	}
	if strings.Contains(prompt, "SHORTEST factual phrase") {
		t.Error("custom base should suppress factual template entirely")
	}
}

func TestBuildSystemPrompt_FactualWithMemories(t *testing.T) {
	memories := []map[string]any{
		{"memory": "User started learning Go in May 2023"},
		{"memory": "User lives in Moscow"},
		{"memory": "User's dog is named Rex"},
	}
	prompt := buildSystemPrompt("Where does the user live?", memories, "", "", "factual")

	for i, want := range []string{
		"1. User started learning Go in May 2023",
		"2. User lives in Moscow",
		"3. User's dog is named Rex",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("factual prompt missing memory %d (%q)", i+1, want)
		}
	}
	if !strings.Contains(prompt, "SHORTEST factual phrase") {
		t.Error("factual prompt must still carry rule 1")
	}
}

func TestBuildSystemPrompt_ConversationalExplicit(t *testing.T) {
	// answer_style="conversational" must behave identically to "" (default branch).
	memories := []map[string]any{{"memory": "fact"}}
	defaultPrompt := buildSystemPrompt("hello", memories, "", "", "")
	explicitPrompt := buildSystemPrompt("hello", memories, "", "", "conversational")
	// Compare structural markers — the "Current Time" embedded by Sprintf
	// changes per call, so equality on the full string is fragile.
	for _, want := range []string{"MemDB", "1. fact"} {
		if !strings.Contains(defaultPrompt, want) {
			t.Fatalf("precondition: default prompt missing %q", want)
		}
		if !strings.Contains(explicitPrompt, want) {
			t.Errorf("explicit conversational prompt missing %q (must equal default branch)", want)
		}
	}
	if strings.Contains(explicitPrompt, "SHORTEST factual phrase") {
		t.Error("conversational must not pick the factual template")
	}
}
