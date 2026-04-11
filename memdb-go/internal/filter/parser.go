package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Parse converts a raw JSON-decoded map (typically from an HTTP request body)
// into a validated Filter. Returns nil, nil when the input is empty.
//
// Accepts three shapes:
//   - {"or":  [ {field: value|{op: value}}, ... ] }
//   - {"and": [ {field: value|{op: value}}, ... ] }
//   - {field: value|{op: value}, field2: ...}  (implicit AND)
//
// Field names are validated against allowedFields + fieldNameRe. Any value
// type other than string / integral / float / bool / []any is rejected.
func Parse(raw map[string]any) (*Filter, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	_, hasOr := raw["or"]
	_, hasAnd := raw["and"]
	if hasOr && hasAnd {
		return nil, errors.New("filter: cannot combine top-level 'or' and 'and'")
	}
	if (hasOr || hasAnd) && len(raw) > 1 {
		return nil, errors.New("filter: top-level 'or'/'and' must be the only key")
	}

	if hasOr {
		conds, err := parseCombinatorList(raw["or"], "or")
		if err != nil {
			return nil, err
		}
		return &Filter{Or: conds}, nil
	}
	if hasAnd {
		conds, err := parseCombinatorList(raw["and"], "and")
		if err != nil {
			return nil, err
		}
		return &Filter{And: conds}, nil
	}

	flat, err := parseFlatBlock(raw)
	if err != nil {
		return nil, err
	}
	return &Filter{Flat: flat}, nil
}

// parseCombinatorList parses the []any value of a top-level "or"/"and" key.
// Each element must be a map; nested combinators are rejected (v1 limitation).
func parseCombinatorList(v any, label string) ([]Condition, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("filter: '%s' must be an array", label)
	}
	out := make([]Condition, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("filter: '%s'[%d] must be an object", label, i)
		}
		if _, bad := m["or"]; bad {
			return nil, fmt.Errorf("filter: nested 'or' is not supported")
		}
		if _, bad := m["and"]; bad {
			return nil, fmt.Errorf("filter: nested 'and' is not supported")
		}
		conds, err := parseFlatBlock(m)
		if err != nil {
			return nil, err
		}
		out = append(out, conds...)
	}
	return out, nil
}

// parseFlatBlock parses a {field: value} map into a slice of conditions.
// Each entry is either a scalar (implicit "=") or a single-key operator map.
func parseFlatBlock(m map[string]any) ([]Condition, error) {
	out := make([]Condition, 0, len(m))
	for field, raw := range m {
		if err := validateField(field); err != nil {
			return nil, err
		}
		cond, err := parseFieldValue(field, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, cond)
	}
	return out, nil
}

// parseFieldValue resolves a single field's RHS into a Condition.
// A map with exactly one recognised op key becomes that op; otherwise the
// value is treated as an implicit equality.
func parseFieldValue(field string, raw any) (Condition, error) {
	if opMap, ok := raw.(map[string]any); ok {
		if len(opMap) != 1 {
			return Condition{}, fmt.Errorf("filter: field %q operator map must have exactly one key", field)
		}
		for opKey, opVal := range opMap {
			op := Op(strings.ToLower(opKey))
			// Support both "=" spelling and "eq" alias via OpEq only.
			if _, ok := validOps[op]; !ok {
				return Condition{}, fmt.Errorf("filter: field %q: unknown operator %q", field, opKey)
			}
			val, err := coerceValue(opVal, op == OpIn)
			if err != nil {
				return Condition{}, fmt.Errorf("filter: field %q: %w", field, err)
			}
			return Condition{Field: field, Op: op, Value: val}, nil
		}
	}
	val, err := coerceValue(raw, false)
	if err != nil {
		return Condition{}, fmt.Errorf("filter: field %q: %w", field, err)
	}
	return Condition{Field: field, Op: OpEq, Value: val}, nil
}

// coerceValue normalises JSON-decoded values into the restricted set
// supported downstream: string, int64, float64, bool, or []any (only when
// allowList=true). JSON numbers arrive as float64 from encoding/json (or
// json.Number if UseNumber was enabled); we fold integral floats into int64.
func coerceValue(v any, allowList bool) (any, error) {
	switch val := v.(type) {
	case nil:
		return nil, errors.New("null values not supported")
	case string, bool, int64:
		return val, nil
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return nil, errors.New("NaN/Inf not supported")
		}
		if val == math.Trunc(val) && val >= math.MinInt64 && val <= math.MaxInt64 {
			return int64(val), nil
		}
		return val, nil
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return i, nil
		}
		if f, err := val.Float64(); err == nil {
			return f, nil
		}
		return nil, fmt.Errorf("unparseable number %q", string(val))
	case int:
		return int64(val), nil
	case []any:
		if !allowList {
			return nil, errors.New("array values only allowed with 'in' operator")
		}
		out := make([]any, 0, len(val))
		for _, item := range val {
			coerced, err := coerceValue(item, false)
			if err != nil {
				return nil, err
			}
			out = append(out, coerced)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}
