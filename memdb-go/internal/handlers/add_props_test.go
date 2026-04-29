package handlers

// add_props_test.go — unit tests for extraction_state property fields
// introduced by migration 0028 (uniform-pipeline Task 1).

import (
	"testing"
)

// TestBuildMemoryProperties_ExtractionStatePresent verifies that the
// extraction_state key is always written into the properties map, matching
// the state value passed by the caller.
func TestBuildMemoryProperties_ExtractionStatePresent(t *testing.T) {
	for _, state := range []string{
		extractionStatePending,
		extractionStateExtracting,
		extractionStateExtracted,
		extractionStateFailed,
	} {
		props := buildMemoryProperties(
			"id1", "test memory", "LongTermMemory",
			"cube1", "user1", "", "sess1", "2026-04-29T00:00:00",
			map[string]any{}, nil, nil, "",
			state, "", "",
		)
		got, ok := props["extraction_state"].(string)
		if !ok {
			t.Errorf("state=%q: extraction_state missing or wrong type in props", state)
			continue
		}
		if got != state {
			t.Errorf("state=%q: expected extraction_state=%q, got %q", state, state, got)
		}
	}
}

// TestBuildMemoryProperties_TimingFieldsPresentWhenNonEmpty verifies that
// extraction_attempted_at and extraction_completed_at are written into the
// properties map only when non-empty.
func TestBuildMemoryProperties_TimingFieldsPresentWhenNonEmpty(t *testing.T) {
	attemptedAt := "2026-04-29T10:00:00Z"
	completedAt := "2026-04-29T10:00:05Z"

	props := buildMemoryProperties(
		"id2", "test memory", "LongTermMemory",
		"cube1", "user1", "", "sess1", "2026-04-29T00:00:00",
		map[string]any{}, nil, nil, "",
		extractionStateExtracted, attemptedAt, completedAt,
	)

	if got, ok := props["extraction_attempted_at"].(string); !ok || got != attemptedAt {
		t.Errorf("expected extraction_attempted_at=%q, got %v", attemptedAt, props["extraction_attempted_at"])
	}
	if got, ok := props["extraction_completed_at"].(string); !ok || got != completedAt {
		t.Errorf("expected extraction_completed_at=%q, got %v", completedAt, props["extraction_completed_at"])
	}
}

// TestBuildMemoryProperties_TimingFieldsAbsentWhenEmpty verifies that
// extraction_attempted_at and extraction_completed_at are absent from the
// properties map when the caller passes empty strings — keeps the JSONB
// payload lean on initial insert.
func TestBuildMemoryProperties_TimingFieldsAbsentWhenEmpty(t *testing.T) {
	props := buildMemoryProperties(
		"id3", "test memory", "LongTermMemory",
		"cube1", "user1", "", "sess1", "2026-04-29T00:00:00",
		map[string]any{}, nil, nil, "",
		extractionStateExtracted, "", "",
	)

	if _, ok := props["extraction_attempted_at"]; ok {
		t.Error("extraction_attempted_at must be absent when attemptedAt is empty")
	}
	if _, ok := props["extraction_completed_at"]; ok {
		t.Error("extraction_completed_at must be absent when completedAt is empty")
	}
}

// TestBuildNodeProps_ExtractionStateAlwaysWritten verifies that the low-level
// buildNodeProps writer emits extraction_state for any non-empty state value,
// covering callers that build memoryNodeProps directly (fine, raw, atomic paths).
func TestBuildNodeProps_ExtractionStateAlwaysWritten(t *testing.T) {
	props := buildNodeProps(memoryNodeProps{
		ID:              "id4",
		Memory:          "a fine-mode fact",
		MemoryType:      "LongTermMemory",
		UserName:        "cube1",
		UserID:          "user1",
		Mode:            modeFine,
		Now:             "2026-04-29T00:00:00",
		CreatedAt:       "2026-04-29T00:00:00",
		Info:            map[string]any{},
		ExtractionState: extractionStateExtracted,
	})

	if got, ok := props["extraction_state"].(string); !ok || got != extractionStateExtracted {
		t.Errorf("expected extraction_state=%q, got %v", extractionStateExtracted, props["extraction_state"])
	}
	if _, ok := props["extraction_attempted_at"]; ok {
		t.Error("extraction_attempted_at must be absent when ExtractionAttemptedAt is empty")
	}
	if _, ok := props["extraction_completed_at"]; ok {
		t.Error("extraction_completed_at must be absent when ExtractionCompletedAt is empty")
	}
}
