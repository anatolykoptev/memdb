// Package filter implements a JSON-driven filter DSL for MemDB's AGE-backed
// Postgres store. It parses a small query language (used by delete_memory
// and get_memory) into a strongly typed Filter and renders it as
// SQL WHERE fragments compatible with Apache AGE's agtype access operators.
//
// The DSL is intentionally limited: AGE inside cypher() heredocs does NOT
// support $1 parameter binding, so all values are escaped inline. Field
// names are therefore validated against a strict allowlist and a regex.
package filter

import "regexp"

// Op is a comparison operator for filter conditions.
type Op string

// Supported filter operators. Matches the set accepted by the Python
// reference implementation in polardb/filters.py.
const (
	OpEq       Op = "="
	OpGt       Op = "gt"
	OpLt       Op = "lt"
	OpGte      Op = "gte"
	OpLte      Op = "lte"
	OpIn       Op = "in"
	OpContains Op = "contains"
	OpLike     Op = "like"
)

// Filter is a structured filter parsed from request JSON.
// Exactly one of And/Or/Flat is populated. Flat is an implicit AND block.
type Filter struct {
	And  []Condition
	Or   []Condition
	Flat []Condition
}

// Condition is a single field/op/value triple.
// Field is validated against fieldNameRe + allowedFields.
// Value is restricted to string | int64 | float64 | bool | []any.
type Condition struct {
	Field string
	Op    Op
	Value any
}

// fieldNameRe accepts top-level identifiers and one level of "info.<ident>"
// nesting. Anything else (dots, quotes, semicolons, null bytes) is rejected.
var fieldNameRe = regexp.MustCompile(`^(info\.)?[a-zA-Z_][a-zA-Z0-9_]*$`)

// allowedFields is the strict allowlist of top-level field names that may
// appear in a filter condition. Nested info.<field> uses fieldNameRe only.
var allowedFields = map[string]struct{}{
	"id":         {},
	"key":        {},
	"tags":       {},
	"type":       {},
	"usage":      {},
	"memory":     {},
	"status":     {},
	"sources":    {},
	"user_id":    {},
	"graph_id":   {},
	"user_name":  {},
	"background": {},
	"confidence": {},
	"created_at": {},
	"session_id": {},
	"updated_at": {},
	"file_ids":   {},
}

// arrayFields are stored as JSON arrays in agtype and use @> semantics.
var arrayFields = map[string]struct{}{
	"tags":     {},
	"sources":  {},
	"file_ids": {},
}

// datetimeFields trigger TRIM+::timestamp casting in SQL rendering.
var datetimeFields = map[string]struct{}{
	"created_at": {},
	"updated_at": {},
}

// validOps is the set of operators accepted inside a {field: {op: value}} map.
var validOps = map[Op]struct{}{
	OpEq:       {},
	OpGt:       {},
	OpLt:       {},
	OpGte:      {},
	OpLte:      {},
	OpIn:       {},
	OpContains: {},
	OpLike:     {},
}

// isArrayField reports whether the given field (or info.<field> suffix) is
// stored as an array in the underlying agtype document.
func isArrayField(field string) bool {
	name := field
	if len(field) > 5 && field[:5] == "info." {
		name = field[5:]
	}
	_, ok := arrayFields[name]
	return ok
}

// isDatetimeField reports whether the given field uses TRIM+::timestamp casts.
func isDatetimeField(field string) bool {
	name := field
	if len(field) > 5 && field[:5] == "info." {
		name = field[5:]
	}
	_, ok := datetimeFields[name]
	return ok
}

// validateField enforces both the allowlist (for top-level fields) and the
// field-name regex. info.<x> is allowed unconditionally as long as <x> is a
// safe identifier — MemOS stores arbitrary user metadata under "info".
func validateField(field string) error {
	if !fieldNameRe.MatchString(field) {
		return &FieldError{Field: field, Reason: "invalid field name"}
	}
	if len(field) > 5 && field[:5] == "info." {
		return nil
	}
	if _, ok := allowedFields[field]; !ok {
		return &FieldError{Field: field, Reason: "field not in allowlist"}
	}
	return nil
}

// FieldError is returned when a field name fails validation.
type FieldError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *FieldError) Error() string {
	return "filter: " + e.Reason + ": " + e.Field
}
