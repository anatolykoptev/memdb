package filter

import (
	"fmt"
)

// renderEq handles the "=" operator for strings, lists, and non-string
// scalars. For array fields (tags/sources/file_ids) the comparison uses
// array-literal agtype syntax; for scalars it uses a quoted string literal.
func renderEq(field, prop string, value any) (string, error) {
	isArr := isArrayField(field)
	switch v := value.(type) {
	case string:
		esc := escapeSQLString(v)
		if isArr {
			return fmt.Sprintf("%s = %s", prop, fmtArrayEqSingleStr(esc)), nil
		}
		return fmt.Sprintf("%s = %s", prop, fmtStrVal(esc)), nil
	case []any:
		if isArr {
			arr, err := fmtArrayEqList(v)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s = %s", prop, arr), nil
		}
		return "", fmt.Errorf("filter: field %q: list equality only supported on array fields", field)
	default:
		nonStr, err := fmtNonStrVal(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s = %s", prop, nonStr), nil
	}
}

// renderCmp handles gt/lt/gte/lte. Datetime fields use the TRIM+::timestamp
// cast path; numeric fields go through agtype_in for proper AGE typing.
func renderCmp(field, prop string, op Op, value any) (string, error) {
	cmp, ok := cmpOpMap[op]
	if !ok {
		return "", fmt.Errorf("filter: unknown comparison op %q", op)
	}
	isDT := isDatetimeField(field)
	switch v := value.(type) {
	case string:
		esc := escapeSQLString(v)
		if isDT {
			return fmt.Sprintf(
				"TRIM(BOTH '\"' FROM %s::text)::timestamp %s '%s'::timestamp",
				prop, cmp, esc,
			), nil
		}
		return fmt.Sprintf("%s %s %s", prop, cmp, fmtStrVal(esc)), nil
	default:
		nonStr, err := fmtNonStrVal(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s %s", prop, cmp, nonStr), nil
	}
}

// renderContains handles "contains" on array fields using the @> operator.
// For non-string items the value is stringified and escaped (mirrors Python).
func renderContains(prop string, value any) (string, error) {
	switch v := value.(type) {
	case string:
		esc := escapeSQLString(v)
		return fmt.Sprintf(`%s @> '["%s"]'::agtype`, prop, esc), nil
	default:
		// Python: str(value) then escape.
		s := fmt.Sprintf("%v", v)
		esc := escapeSQLString(s)
		return fmt.Sprintf(`%s @> '["%s"]'::agtype`, prop, esc), nil
	}
}

// renderIn handles the "in" operator. Empty list → literal "false" (matches
// Python). Single item → fast-path equality/contains. Multi-item → OR group.
func renderIn(field, prop string, value any) (string, error) {
	list, ok := value.([]any)
	if !ok {
		return "", fmt.Errorf("filter: field %q: 'in' requires an array", field)
	}
	if len(list) == 0 {
		return "false", nil
	}
	isArr := isArrayField(field)
	if len(list) == 1 {
		return renderInItem(field, prop, list[0], isArr)
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		frag, err := renderInItem(field, prop, item, isArr)
		if err != nil {
			return "", err
		}
		parts = append(parts, frag)
	}
	return "(" + joinOr(parts) + ")", nil
}

// renderInItem renders a single value inside an "in" list, picking either
// array-contains (for tags/sources/file_ids) or scalar equality.
func renderInItem(field, prop string, item any, isArr bool) (string, error) {
	if isArr {
		switch v := item.(type) {
		case string:
			esc := escapeSQLString(v)
			return fmt.Sprintf(`%s @> '["%s"]'::agtype`, prop, esc), nil
		default:
			s := fmt.Sprintf("%v", v)
			esc := escapeSQLString(s)
			return fmt.Sprintf(`%s @> '["%s"]'::agtype`, prop, esc), nil
		}
	}
	switch v := item.(type) {
	case string:
		esc := escapeSQLString(v)
		return fmt.Sprintf("%s = %s", prop, fmtStrVal(esc)), nil
	default:
		nonStr, err := fmtNonStrVal(v)
		if err != nil {
			// Fallback: stringify and compare as agtype-text. Python's SQL path
			// uses ``{item}::agtype`` which is the same shape.
			return "", fmt.Errorf("filter: field %q: unsupported 'in' item: %w", field, err)
		}
		return fmt.Sprintf("%s = %s", prop, nonStr), nil
	}
}

// renderLike handles the "like" operator. Values are double-escaped (LIKE
// metachars then SQL quotes) and wrapped as '%...%'.
func renderLike(prop string, value any) (string, error) {
	var s string
	switch v := value.(type) {
	case string:
		s = escapeLikeValue(v)
	default:
		// Python coerces to str and inlines directly.
		s = escapeLikeValue(fmt.Sprintf("%v", v))
	}
	return fmt.Sprintf("%s::text LIKE '%%%s%%'", prop, s), nil
}

// joinOr joins an OR-list without pulling in strings.Join for one-letter
// clarity (also avoids an extra import in this file).
func joinOr(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " OR "
		}
		out += p
	}
	return out
}
