// Package memprops builds JSONB property maps for memory rows, with the
// observation_date invariant enforced at the type-system boundary.
//
// Why this package exists
// -----------------------
// MemDB writes Memory rows from many code paths: the synchronous fine/atomic
// add pipeline, the buffer-flush worker, a fleet of background extractors
// (episodic summary, skill, tool, profile, preference, event), and the tree
// reorganizer. Historically each path hand-rolled its own `map[string]any`,
// and those hand-rolled maps systematically forgot to write `observation_date`.
//
// `observation_date` is the in-conversation timestamp of the source data
// (latest source-message chat_time, or max of children's observation_date for
// derived parents). The LoCoMo eval harness reads it via
//
//	evaluation/locomo/query.py::_extract_ts
//
// in priority order:
//
//	observation_date → chat_time → created_at → ...
//
// When `observation_date` is absent on derived rows, retrieval falls through
// to `created_at` — which is wall-clock at ingest. That poisons temporal
// answers ("2026-05-01" instead of "2023-08-25") and inflates today-leak.
//
// This package centralises derived-memory construction so the invariant
// cannot be silently regressed: BuildDerivedMemoryProps fails loud when
// ObservationDate is empty, and the type encodes the contract.
//
// Boundary rule for callers: if you cannot supply ObservationDate at the
// call site, fix the data plumbing — do NOT silence the error by passing a
// wall-clock fallback. Wall-clock is the bug.
package memprops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// DerivedMemoryProps is the strict input contract for derived-memory
// JSONB construction. Optional fields have safe defaults; required fields
// surface as validation errors.
type DerivedMemoryProps struct {
	ID              string
	Memory          string
	MemoryType      string
	UserID          string
	UserName        string
	AgentID         string
	SessionID       string
	Now             string
	ObservationDate string
	Source          string
	HierarchyLevel  string
	ParentMemoryID  string
	Confidence      float64
	Tags            []string
}

// ErrObservationDateRequired is returned when a derived caller forgets to
// thread the in-conversation timestamp through.
var ErrObservationDateRequired = errors.New("observation_date required for derived memories (M12.1 invariant)")

// BuildDerivedMemoryProps validates and emits the JSONB property map for a
// single derived memory row.
func BuildDerivedMemoryProps(p DerivedMemoryProps) (map[string]any, error) {
	if strings.TrimSpace(p.ObservationDate) == "" {
		return nil, fmt.Errorf("%w (memory_type=%q source=%q)", ErrObservationDateRequired, p.MemoryType, p.Source)
	}
	for _, f := range []struct {
		name, val string
	}{
		{"ID", p.ID},
		{"Memory", p.Memory},
		{"MemoryType", p.MemoryType},
		{"Source", p.Source},
		{"UserName", p.UserName},
	} {
		if strings.TrimSpace(f.val) == "" {
			return nil, fmt.Errorf("BuildDerivedMemoryProps: %s required", f.name)
		}
	}

	hierarchy := p.HierarchyLevel
	if hierarchy == "" {
		hierarchy = "raw"
	}
	confidence := p.Confidence
	if confidence == 0 {
		confidence = 0.99
	}

	props := map[string]any{
		"id":                strings.TrimSpace(p.ID),
		"memory":            p.Memory,
		"memory_type":       p.MemoryType,
		"status":            "activated",
		"user_name":         p.UserName,
		"user_id":           p.UserID,
		"agent_id":          p.AgentID,
		"session_id":        p.SessionID,
		"created_at":        p.Now,
		"updated_at":        p.Now,
		"delete_time":       "",
		"delete_record_id":  "",
		"tags":              p.Tags,
		"key":               "",
		"usage":             []string{},
		"sources":           []string{},
		"background":        "",
		"confidence":        confidence,
		"type":              "fact",
		"info":              map[string]any{},
		"graph_id":          uuid.New().String(),
		"importance_score":  1.0,
		"retrieval_count":   0,
		"last_retrieved_at": "",
		"hierarchy_level":   hierarchy,
		"parent_memory_id":  nil,
		"source":            p.Source,
		"observation_date":  strings.TrimSpace(p.ObservationDate),
	}
	if p.ParentMemoryID != "" {
		props["parent_memory_id"] = p.ParentMemoryID
	}
	return props, nil
}

// LatestObservationDate scans a slice of memory-row property maps and
// returns the maximum of `observation_date` (with `chat_time` as fallback
// per row). Returns "" when no row carries a usable date.
func LatestObservationDate(rows []map[string]any) string {
	latest := ""
	for _, r := range rows {
		ts := pickPropDate(r, "observation_date", "chat_time")
		if ts == "" {
			continue
		}
		if len(ts) > 10 {
			ts = ts[:10]
		}
		if ts > latest {
			latest = ts
		}
	}
	return latest
}

func pickPropDate(props map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := props[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			continue
		}
		return s
	}
	return ""
}
