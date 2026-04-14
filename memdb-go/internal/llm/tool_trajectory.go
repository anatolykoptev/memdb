package llm

// tool_trajectory.go — tool call trajectory extraction (LLM-based).
// Extracts tool usage patterns, success/failure experiences from conversations.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TrajectoryItem represents one extracted tool call trajectory.
type TrajectoryItem struct {
	Correctness    string           `json:"correctness"`
	Trajectory     string           `json:"trajectory"`
	Experience     string           `json:"experience"`
	ToolUsedStatus []ToolUsedStatus `json:"tool_used_status"`
}

// ToolUsedStatus captures per-tool success rate and experience within a trajectory.
type ToolUsedStatus struct {
	UsedTool       string  `json:"used_tool"`
	SuccessRate    float64 `json:"success_rate"`
	ErrorType      string  `json:"error_type"`
	ToolExperience string  `json:"tool_experience"`
}

const trajectoryMaxTokens = 2000

const toolTrajectoryPrompt = `You are a professional tool experience extraction expert. Your task is to extract complete tool call trajectory experiences from given conversation messages.

## Analysis and Judgment Steps:

**Step 1: Assess Task Completion**
Determine correctness based on user feedback: success or failed, user feedback has higher priority than execution results, if user feedback is incorrect, then determine as failed

**Step 2: Successful Trajectory (success) - Experience Extraction**
Extract general principles or rules from success patterns, using "when...then..." structure:
- when: clearly describe the scenario characteristics that trigger this experience (task type, tool environment, parameter characteristics, etc.)
- then: summarize effective parameter patterns, calling strategies, and best practices
Note: Experience is at the trajectory-level problem-solving, not just for a single tool

**Step 3: Failed Trajectory (failed) - Error Analysis and Experience Extraction**

3.1 Tool Requirement Assessment
  - Does the task require tools? (required/direct answer/unnecessary call)

3.2 Tool Call Verification
  - Tool availability: provided in system?
  - Tool selection: correct tool chosen?
  - Parameter correctness: conform to type definitions?
  - Hallucination detection: calling non-existent tools?

3.3 Root Cause Identification
  Combine error feedback from messages with above analysis to precisely output root cause

3.4 Experience Extraction (Core)
  Extract general principles or rules from failure patterns, using "when...then..." structure:
  - when: clearly describe the scenario characteristics that trigger this experience
  - then: provide general strategies to avoid errors, correct calling approaches, or decision rules

## Output Format:
Return a JSON array in the following format:

[
  {
    "correctness": "success or failed",
    "trajectory": "Concise natural language summary: [task -> execution action -> result] (possibly multiple rounds) -> final answer",
    "experience": "Use when...then... format",
    "tool_used_status": [
      {
        "used_tool": "Tool name",
        "success_rate": 0.0-1.0,
        "error_type": "Error description or empty string",
        "tool_experience": "Preconditions and post-effects"
      }
    ]
  }
]

## Notes:
- Each trajectory must be an independent complete process
- A trajectory may involve multiple tools, each recorded independently in tool_used_status
- If no tool was called, tool_used_status is an empty array []
- Experience must be general and reusable rules, not descriptions specific to concrete cases

Please analyze the following conversation messages and extract tool call trajectories:
<messages>
%s
</messages>`

// ExtractToolTrajectory extracts tool call trajectories from a conversation using an LLM.
// Returns nil (no error) if the conversation yields no trajectories.
func ExtractToolTrajectory(ctx context.Context, client *Client, messages string) ([]TrajectoryItem, error) {
	prompt := fmt.Sprintf(toolTrajectoryPrompt, messages)
	msgs := []map[string]string{
		{"role": "system", "content": prompt},
		{"role": "user", "content": messages},
	}

	raw, err := client.Chat(ctx, msgs, trajectoryMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("extract tool trajectory: %w", err)
	}

	raw = stripTrajectoryFences(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return nil, nil //nolint:nilnil // nil = no trajectories found
	}

	var items []TrajectoryItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("extract tool trajectory parse: %w (raw: %.300s)", err, raw)
	}

	if len(items) == 0 {
		return nil, nil //nolint:nilnil // empty array = no trajectories
	}

	return items, nil
}

// stripTrajectoryFences removes optional ```json ... ``` markdown fences from LLM output.
func stripTrajectoryFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
