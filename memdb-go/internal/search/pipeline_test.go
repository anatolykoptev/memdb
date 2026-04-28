// Package search — pipeline_test.go: unit tests for the stage-based
// orchestrator (M11 R2 followup).
package search

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestRunPipeline_HappyPath walks a 3-stage chain that mutates state
// and confirms timings + ordering land as expected. Exercises the
// success outcome path of runPipeline.
func TestRunPipeline_HappyPath(t *testing.T) {
	var order []string
	stages := []stage{
		funcStage{"stage_a", func(ctx context.Context, s *pipelineState) error {
			order = append(order, "a")
			s.EmbedQuery = "a-set"
			return nil
		}},
		funcStage{"stage_b", func(ctx context.Context, s *pipelineState) error {
			order = append(order, "b")
			if s.EmbedQuery != "a-set" {
				t.Errorf("stage_b: expected EmbedQuery from stage_a, got %q", s.EmbedQuery)
			}
			s.Subqueries = []string{s.EmbedQuery, "extra"}
			return nil
		}},
		funcStage{"stage_c", func(ctx context.Context, s *pipelineState) error {
			order = append(order, "c")
			if len(s.Subqueries) != 2 {
				t.Errorf("stage_c: expected 2 subqueries, got %d", len(s.Subqueries))
			}
			return nil
		}},
	}

	st := &pipelineState{Params: SearchParams{Query: "test"}}
	logger := slog.Default()
	runPipeline(context.Background(), logger, stages, st)

	if got := strings.Join(order, ","); got != "a,b,c" {
		t.Errorf("stage execution order: want a,b,c got %s", got)
	}
	if len(st.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(st.Errors), st.Errors)
	}
	for _, name := range []string{"stage_a", "stage_b", "stage_c"} {
		if _, ok := st.Timings[name]; !ok {
			t.Errorf("expected timing for %s, missing", name)
		}
	}
}

// TestRunPipeline_StageErrorContinues verifies an erroring stage records
// the error tagged with the stage name and the pipeline continues to
// the next stage. The soft-fail contract is core to the orchestrator.
func TestRunPipeline_StageErrorContinues(t *testing.T) {
	var ranAfterError bool
	stages := []stage{
		funcStage{"stage_err", func(ctx context.Context, s *pipelineState) error {
			return errors.New("boom")
		}},
		funcStage{"stage_after", func(ctx context.Context, s *pipelineState) error {
			ranAfterError = true
			return nil
		}},
	}

	st := &pipelineState{Params: SearchParams{Query: "test"}}
	runPipeline(context.Background(), slog.Default(), stages, st)

	if !ranAfterError {
		t.Error("stage_after should have run despite stage_err returning error")
	}
	if len(st.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(st.Errors))
	}
	if !strings.Contains(st.Errors[0].Error(), "stage_err") {
		t.Errorf("error should be tagged with stage name, got %q", st.Errors[0].Error())
	}
}

// TestRunPipeline_SkippedOutcome verifies that calling state.skip(name)
// flips the outcome label without raising an error.
func TestRunPipeline_SkippedOutcome(t *testing.T) {
	stages := []stage{
		funcStage{"stage_skip", func(ctx context.Context, s *pipelineState) error {
			s.skip("stage_skip")
			return nil
		}},
	}
	st := &pipelineState{Params: SearchParams{Query: "test"}}
	runPipeline(context.Background(), slog.Default(), stages, st)

	if _, ok := st.skipped["stage_skip"]; !ok {
		t.Error("stage_skip should be marked skipped")
	}
	if len(st.Errors) != 0 {
		t.Errorf("skipped stage must not record an error, got %v", st.Errors)
	}
}
