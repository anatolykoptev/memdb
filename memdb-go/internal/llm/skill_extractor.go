package llm

// skill_extractor.go — task chunking + skill extraction (LLM-based).
// Go port of Python's process_skill_memory.py (drops OSS/MySQL/file ops).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TaskChunk represents a group of messages belonging to one logical task.
type TaskChunk struct {
	TaskID   int     `json:"task_id"`
	TaskName string  `json:"task_name"`
	Indices  [][]int `json:"message_indices"`
	Messages string  // filled post-parse: conversation lines for this task
}

// SkillMemory is the result of skill extraction from a task chunk.
type SkillMemory struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Procedure   string            `json:"procedure"`
	Experience  []string          `json:"experience"`
	Preference  []string          `json:"preference"`
	Examples    []string          `json:"examples"`
	Tags        []string          `json:"tags"`
	Scripts     map[string]string `json:"scripts"`
	Others      map[string]string `json:"others"`
	Update      bool              `json:"update"`
	OldMemoryID string            `json:"old_memory_id"`
}

// ExistingSkill is a compact representation of an existing skill for dedup context.
type ExistingSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Procedure   string   `json:"procedure"`
	Tags        []string `json:"tags"`
}

const taskChunkingPrompt = `You are an expert in dialogue analysis. Analyze the conversation and identify independent "tasks" the user asked the AI to perform.

Rules:
1. Tasks should be high-level (e.g., "Travel Planning", "Code Review", "Data Analysis")
2. Group jumping conversations: if user discusses topic A in messages 3-5, switches to B in 6-10, returns to A in 11-12, assign both 3-5 and 11-12 to task A
3. Filter greetings and chit-chat — only extract tasks with clear goals
4. If a subtask serves a main task, include it in the main task
5. Use generic, reusable task names

Output JSON array:
` + "```json\n" + `[
  {
    "task_id": 1,
    "task_name": "Generic task name",
    "message_indices": [[0, 5], [16, 17]]
  }
]
` + "```"

const skillExtractionPrompt = `You are an expert in skill abstraction. Extract a universal skill template from the conversation that can be applied to similar scenarios.

# Existing Skill Memories
%s

# Conversation Messages
%s

# Core Principles
1. Generalization: Extract abstract methodologies. Use "Travel Planning" not "Beijing Travel Planning"
2. Universality: All fields except "examples" must remain general
3. Similarity Check: If similar skill exists, set "update": true with "old_memory_id". Otherwise false
4. If this is an update, merge new info into existing skill — don't lose existing content

# Output Format
` + "```json\n" + `{
  "name": "Generic skill name",
  "description": "Universal description of what this skill accomplishes",
  "procedure": "Step-by-step process: 1. Step one 2. Step two...",
  "experience": ["General lesson learned", "Best practice..."],
  "preference": ["User preference pattern"],
  "examples": ["Output format example in markdown"],
  "tags": ["keyword1", "keyword2"],
  "scripts": null,
  "others": null,
  "update": false,
  "old_memory_id": ""
}
` + "```\n" + `
Return null if no extractable skill exists. Output JSON only.`

const (
	chunkMaxTokens = 1000
	skillMaxTokens = 2000
)

// ChunkTasks splits a conversation into independent task groups using an LLM.
func ChunkTasks(ctx context.Context, client *Client, conversation string) ([]TaskChunk, error) {
	lines := strings.Split(conversation, "\n")

	var numbered strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&numbered, "%d: %s\n", i, line)
	}

	msgs := []map[string]string{
		{"role": "user", "content": taskChunkingPrompt + "\n\nConversation:\n" + numbered.String()},
	}

	raw, err := client.Chat(ctx, msgs, chunkMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("chunk tasks: %w", err)
	}

	raw = stripFences(raw)

	var chunks []TaskChunk
	if err := json.Unmarshal([]byte(raw), &chunks); err != nil {
		return nil, fmt.Errorf("chunk tasks parse: %w (raw: %.300s)", err, raw)
	}

	for i := range chunks {
		chunks[i].Messages = sliceConversationLines(lines, chunks[i].Indices)
	}

	return chunks, nil
}

// ExtractSkill extracts a skill memory from task messages with dedup awareness.
// Returns nil if the LLM determines no extractable skill exists.
func ExtractSkill(ctx context.Context, client *Client, taskMessages string, existing []ExistingSkill) (*SkillMemory, error) {
	existingJSON := "[]"
	if len(existing) > 0 {
		b, _ := json.Marshal(existing)
		existingJSON = string(b)
	}

	prompt := fmt.Sprintf(skillExtractionPrompt, existingJSON, taskMessages)
	msgs := []map[string]string{
		{"role": "user", "content": prompt},
	}

	raw, err := client.Chat(ctx, msgs, skillMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("extract skill: %w", err)
	}

	raw = stripFences(raw)

	// LLM returns "null" when no skill is extractable
	if raw == "null" || raw == "" {
		return nil, nil //nolint:nilnil // nil = no skill found
	}

	var skill SkillMemory
	if err := json.Unmarshal([]byte(raw), &skill); err != nil {
		return nil, fmt.Errorf("extract skill parse: %w (raw: %.300s)", err, raw)
	}

	// Validate minimum fields
	if skill.Name == "" || skill.Description == "" {
		return nil, nil //nolint:nilnil // incomplete = skip
	}

	return &skill, nil
}

// sliceConversationLines extracts conversation lines by index ranges ([start, end] inclusive).
func sliceConversationLines(lines []string, indices [][]int) string {
	var sb strings.Builder
	for _, idx := range indices {
		start, end := parseIndexRange(idx)
		if start < 0 || start >= len(lines) {
			continue
		}
		if end >= len(lines) {
			end = len(lines) - 1
		}
		for i := start; i <= end; i++ {
			sb.WriteString(lines[i])
			sb.WriteByte('\n')
		}
	}
	return strings.TrimSpace(sb.String())
}

// parseIndexRange normalizes [start, end], [n], and [] into (start, end).
func parseIndexRange(idx []int) (int, int) {
	switch len(idx) {
	case 0:
		return -1, -1
	case 1:
		return idx[0], idx[0]
	default:
		return idx[0], idx[1]
	}
}
