package filter

import (
	"fmt"
	"strings"
)

// BuildAGEWhereConditions renders a parsed Filter into a slice of SQL WHERE
// fragments, one per top-level clause. Callers join the slice with " AND " at
// the outer WHERE level. A nil or empty filter returns an empty slice.
//
// Semantics match polardb/filters.py:_build_filter_conditions verbatim.
// Field names must have been validated upstream (Parse enforces this).
func BuildAGEWhereConditions(f *Filter) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	switch {
	case len(f.Or) > 0:
		return buildOr(f.Or)
	case len(f.And) > 0:
		return buildAnd(f.And)
	case len(f.Flat) > 0:
		frag, err := renderBlock(f.Flat)
		if err != nil {
			return nil, err
		}
		if frag == "" {
			return nil, nil
		}
		return []string{frag}, nil
	}
	return nil, nil
}

// buildOr renders each child as its own AND block, then wraps them in one
// outer (A OR B OR ...) group — matching Python's SQL branch that returns a
// single combined fragment.
func buildOr(conds []Condition) ([]string, error) {
	// Split children at implicit boundaries. Python treats each item in
	// filter["or"] as its own dict, but Parse already flattens them into a
	// single list; re-group by field occurrence.
	groups := splitByFieldGroup(conds)
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		frag, err := renderBlock(g)
		if err != nil {
			return nil, err
		}
		if frag != "" {
			parts = append(parts, "("+frag+")")
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return []string{"(" + strings.Join(parts, " OR ") + ")"}, nil
}

// buildAnd renders each child block wrapped in parens, returning one fragment
// per block so the caller ANDs them at the outer level.
func buildAnd(conds []Condition) ([]string, error) {
	groups := splitByFieldGroup(conds)
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		frag, err := renderBlock(g)
		if err != nil {
			return nil, err
		}
		if frag != "" {
			out = append(out, "("+frag+")")
		}
	}
	return out, nil
}

// splitByFieldGroup returns each Condition as its own single-element group.
// Parse emits one Condition per field, so there is no multi-field dict to
// preserve. This keeps the Python semantics (each "or" element is an
// independent implicit-AND block) without needing to track grouping state.
func splitByFieldGroup(conds []Condition) [][]Condition {
	out := make([][]Condition, 0, len(conds))
	for _, c := range conds {
		out = append(out, []Condition{c})
	}
	return out
}

// renderBlock renders a flat implicit-AND block (a []Condition) into a single
// SQL fragment joined with " AND ".
func renderBlock(conds []Condition) (string, error) {
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		frag, err := renderCondition(c)
		if err != nil {
			return "", err
		}
		if frag != "" {
			parts = append(parts, frag)
		}
	}
	return strings.Join(parts, " AND "), nil
}

// renderCondition dispatches a single Condition to the operator-specific
// renderer. All string values are escaped via escapeSQLString before being
// inlined into the output SQL.
func renderCondition(c Condition) (string, error) {
	prop := propRef(c.Field)
	switch c.Op {
	case OpEq:
		return renderEq(c.Field, prop, c.Value)
	case OpGt, OpLt, OpGte, OpLte:
		return renderCmp(c.Field, prop, c.Op, c.Value)
	case OpContains:
		return renderContains(prop, c.Value)
	case OpIn:
		return renderIn(c.Field, prop, c.Value)
	case OpLike:
		return renderLike(prop, c.Value)
	}
	return "", fmt.Errorf("filter: unhandled operator %q", c.Op)
}
