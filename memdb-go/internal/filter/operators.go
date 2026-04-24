package filter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// escapeSQLString doubles single quotes for safe inline inclusion inside a
// SQL string literal. This is the ONLY safety barrier between user input and
// the AGE heredoc — AGE rejects $1 parameter binding inside cypher().
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// escapeLikeValue escapes backslash, %, and _ so the caller can safely
// interpolate the result into LIKE '%...%'. After escaping LIKE metachars it
// then applies escapeSQLString so the value survives the surrounding literal.
func escapeLikeValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return escapeSQLString(s)
}

// propDirect renders AGE access for a top-level property key.
// Mirrors prop_direct() in polardb/filters.py.
func propDirect(key string) string {
	return fmt.Sprintf(
		`ag_catalog.agtype_access_operator(properties::text::agtype, '"%s"'::agtype)`,
		key,
	)
}

// propNested renders AGE access for a nested info.<field> property.
// Mirrors prop_nested() in polardb/filters.py.
func propNested(infoField string) string {
	return fmt.Sprintf(
		`ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties::text::agtype, '"info"'::ag_catalog.agtype, '"%s"'::ag_catalog.agtype])`,
		infoField,
	)
}

// propRef picks propDirect or propNested based on the "info." prefix.
// The field name is assumed to have already passed validateField().
func propRef(field string) string {
	if strings.HasPrefix(field, "info.") {
		return propNested(field[5:])
	}
	return propDirect(field)
}

// fmtStrVal formats an already-escaped string value as an agtype literal.
func fmtStrVal(escaped string) string {
	return fmt.Sprintf(`'"%s"'::agtype`, escaped)
}

// fmtNonStrVal JSON-marshals a non-string scalar (int/float/bool) and casts
// it to agtype for inline numeric comparisons.
// Returns an error for unmarshalable or unsafe values.
// Note: AGE 1.7 removed the agtype_in(text) overload, so we use the
// '<literal>'::agtype cast form (text → agtype implicit cast chain).
func fmtNonStrVal(value any) (string, error) {
	switch value.(type) {
	case int64, float64, bool:
		raw, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		// Marshal output for int64/float64/bool never contains quotes, so
		// escapeSQLString is belt-and-braces only.
		return fmt.Sprintf("'%s'::agtype", escapeSQLString(string(raw))), nil
	default:
		return "", fmt.Errorf("filter: unsupported scalar value type %T", value)
	}
}

// fmtArrayEqSingleStr renders `'["value"]'::agtype` for single-element array
// equality checks on array fields (tags/sources/file_ids).
func fmtArrayEqSingleStr(escaped string) string {
	return fmt.Sprintf(`'["%s"]'::agtype`, escaped)
}

// fmtArrayEqList renders an N-element JSON array literal for array-field
// equality. Each element is stringified then escapeSQLString'd.
func fmtArrayEqList(items []any) (string, error) {
	escaped := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			// Match Python: str(item) for non-strings.
			s = fmt.Sprintf("%v", item)
		}
		escaped = append(escaped, escapeSQLString(s))
	}
	raw, err := json.Marshal(escaped)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("'%s'::agtype", escapeSQLString(string(raw))), nil
}

// cmpOpMap maps DSL operators to raw SQL comparison operators.
var cmpOpMap = map[Op]string{
	OpGt:  ">",
	OpLt:  "<",
	OpGte: ">=",
	OpLte: "<=",
}
