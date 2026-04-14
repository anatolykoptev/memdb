// Package llm — feedback LLM types and extraction functions.
// Ports Python's mem_feedback_prompts.py prompts and feedback.py LLM calls.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// KeywordReplaceResult is the result of keyword replacement detection.
type KeywordReplaceResult struct {
	IsKeywordReplace string `json:"if_keyword_replace"` // "true" or "false"
	DocScope         string `json:"doc_scope"`
	Original         string `json:"original"`
	Target           string `json:"target"`
}

// FeedbackJudgement is a single judgement item from the feedback analysis.
type FeedbackJudgement struct {
	Validity     string   `json:"validity"`      // "true" or "false"
	UserAttitude string   `json:"user_attitude"`  // "dissatisfied", "satisfied", "irrelevant"
	CorrectedInfo string  `json:"corrected_info"`
	Key          string   `json:"key"`
	Tags         []string `json:"tags"`
}

// MemoryOperation describes an ADD/UPDATE/NONE operation on a memory.
type MemoryOperation struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Operation string `json:"operation"` // ADD, UPDATE, NONE
	OldMemory string `json:"old_memory,omitempty"`
}

// memoryOpsResponse wraps the JSON response for memory operations.
type memoryOpsResponse struct {
	Operations []MemoryOperation `json:"operations"`
}

// UpdateJudgement is the safety verification result for an UPDATE operation.
type UpdateJudgement struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	OldMemory string `json:"old_memory"`
	Judgement  string `json:"judgement"` // UPDATE_APPROVED, INVALID, NONE
}

// updateJudgementResponse wraps the JSON response for update judgements.
type updateJudgementResponse struct {
	OperationsJudgement []UpdateJudgement `json:"operations_judgement"`
}

// IsReplace returns true if the keyword replacement was detected.
func (r *KeywordReplaceResult) IsReplace() bool {
	return r != nil && r.IsKeywordReplace == "true"
}

// DetectKeywordReplace checks if feedback is a keyword replacement request.
func DetectKeywordReplace(ctx context.Context, client *Client, feedback string) (*KeywordReplaceResult, error) {
	prompt := fmt.Sprintf(keywordReplacePrompt, feedback)
	msgs := []map[string]string{
		{"role": "user", "content": prompt},
	}
	raw, err := client.Chat(ctx, msgs, 500)
	if err != nil {
		return nil, fmt.Errorf("keyword replace detect: %w", err)
	}
	raw = stripFencesInline(raw)
	var result KeywordReplaceResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("keyword replace parse: %w", err)
	}
	return &result, nil
}

// JudgeFeedback analyzes user feedback validity, attitude, and extracts corrected info.
func JudgeFeedback(ctx context.Context, client *Client, chatHistory, feedback, feedbackTime string) ([]FeedbackJudgement, error) {
	prompt := strings.NewReplacer(
		"{chat_history}", chatHistory,
		"{user_feedback}", feedback,
		"{feedback_time}", feedbackTime,
	).Replace(feedbackJudgementPrompt)

	msgs := []map[string]string{
		{"role": "user", "content": prompt},
	}
	raw, err := client.Chat(ctx, msgs, 1000)
	if err != nil {
		return nil, fmt.Errorf("feedback judgement: %w", err)
	}
	raw = stripFencesInline(raw)
	var items []FeedbackJudgement
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("feedback judgement parse: %w", err)
	}
	return items, nil
}

// DecideMemoryOperations decides ADD/UPDATE/NONE for new facts against existing memories.
func DecideMemoryOperations(ctx context.Context, client *Client, currentMemories, newFacts, chatHistory, nowTime string) ([]MemoryOperation, error) {
	prompt := strings.NewReplacer(
		"{current_memories}", currentMemories,
		"{new_facts}", newFacts,
		"{chat_history}", chatHistory,
		"{now_time}", nowTime,
	).Replace(updateFormerMemoriesPrompt)

	msgs := []map[string]string{
		{"role": "user", "content": prompt},
	}
	raw, err := client.Chat(ctx, msgs, 2000)
	if err != nil {
		return nil, fmt.Errorf("decide memory ops: %w", err)
	}
	raw = stripFencesInline(raw)
	var resp memoryOpsResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("decide memory ops parse: %w", err)
	}
	return resp.Operations, nil
}

// JudgeUpdateSafety validates UPDATE operations for entity consistency and semantic relevance.
func JudgeUpdateSafety(ctx context.Context, client *Client, rawOperations string) ([]UpdateJudgement, error) {
	prompt := strings.Replace(updateJudgementPrompt, "{raw_operations}", rawOperations, 1)
	msgs := []map[string]string{
		{"role": "user", "content": prompt},
	}
	raw, err := client.Chat(ctx, msgs, 1500)
	if err != nil {
		return nil, fmt.Errorf("update safety judgement: %w", err)
	}
	raw = stripFencesInline(raw)
	var resp updateJudgementResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("update safety judgement parse: %w", err)
	}
	return resp.OperationsJudgement, nil
}

// stripFencesInline removes markdown code fences from LLM output (inline version).
func stripFencesInline(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
