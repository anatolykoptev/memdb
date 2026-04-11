package filter

import (
	"encoding/json"
	"strings"
	"testing"
)

// decode is a helper that unmarshals a JSON literal into map[string]any, the
// same shape the HTTP layer feeds into Parse.
func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return m
}

func TestParse_FlatEquality(t *testing.T) {
	f, err := Parse(decode(t, `{"user_id": "alice"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Flat) != 1 || f.Flat[0].Field != "user_id" || f.Flat[0].Op != OpEq {
		t.Fatalf("unexpected flat: %+v", f.Flat)
	}
	if f.Flat[0].Value.(string) != "alice" {
		t.Fatalf("value: %#v", f.Flat[0].Value)
	}
}

func TestParse_NumericCoercion(t *testing.T) {
	f, err := Parse(decode(t, `{"confidence": 42}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Flat[0].Value; got != int64(42) {
		t.Fatalf("integer not coerced to int64: %#v", got)
	}
	f, err = Parse(decode(t, `{"confidence": 0.75}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Flat[0].Value; got != float64(0.75) {
		t.Fatalf("fractional not float64: %#v", got)
	}
}

func TestParse_OperatorMap(t *testing.T) {
	f, err := Parse(decode(t, `{"created_at": {"gt": "2026-01-01"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Flat[0].Op != OpGt {
		t.Fatalf("op: %v", f.Flat[0].Op)
	}
}

func TestParse_In(t *testing.T) {
	f, err := Parse(decode(t, `{"tags": {"in": ["python", "go"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	list := f.Flat[0].Value.([]any)
	if len(list) != 2 {
		t.Fatalf("len: %d", len(list))
	}
}

func TestParse_OrBlock(t *testing.T) {
	f, err := Parse(decode(t, `{"or": [{"type": "text"}, {"user_id": "bob"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Or) != 2 {
		t.Fatalf("or len: %d", len(f.Or))
	}
}

func TestParse_AndBlock(t *testing.T) {
	f, err := Parse(decode(t, `{"and": [{"type": "text"}, {"status": "active"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.And) != 2 {
		t.Fatalf("and len: %d", len(f.And))
	}
}

func TestParse_NestedInfoField(t *testing.T) {
	f, err := Parse(decode(t, `{"info.category": "science"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Flat[0].Field != "info.category" {
		t.Fatalf("field: %s", f.Flat[0].Field)
	}
}

func TestParse_RejectsUnknownField(t *testing.T) {
	cases := []string{
		`{"__proto__": "x"}`,
		`{"properties": "x"}`,
		`{"drop_table": "x"}`,
	}
	for _, c := range cases {
		if _, err := Parse(decode(t, c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestParse_RejectsInvalidFieldName(t *testing.T) {
	// Not valid JSON keys? They are — `;` is fine in a JSON key.
	cases := []string{
		`{";": "x"}`,
		`{"info.bad-name": "x"}`,
		`{"info.": "x"}`,
		`{"1foo": "x"}`,
	}
	for _, c := range cases {
		if _, err := Parse(decode(t, c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestParse_RejectsMultipleCombinators(t *testing.T) {
	if _, err := Parse(decode(t, `{"or": [], "and": []}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsCombinatorWithOtherKeys(t *testing.T) {
	if _, err := Parse(decode(t, `{"or": [], "user_id": "x"}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsUnknownOperator(t *testing.T) {
	if _, err := Parse(decode(t, `{"user_id": {"xyz": 1}}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsMultiKeyOperatorMap(t *testing.T) {
	if _, err := Parse(decode(t, `{"user_id": {"gt": 1, "lt": 10}}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsNestedCombinator(t *testing.T) {
	if _, err := Parse(decode(t, `{"or": [{"and": [{"user_id": "x"}]}]}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_EmptyFilter(t *testing.T) {
	f, err := Parse(map[string]any{})
	if err != nil || f != nil {
		t.Fatalf("empty filter should return nil,nil (got %v, %v)", f, err)
	}
}

func TestParse_NullValue(t *testing.T) {
	if _, err := Parse(decode(t, `{"user_id": null}`)); err == nil {
		t.Fatal("expected null rejection")
	}
}

func TestParse_ListOnlyWithIn(t *testing.T) {
	// plain equality with a list on a non-array field is ambiguous — rejected.
	if _, err := Parse(decode(t, `{"user_id": ["a", "b"]}`)); err == nil {
		t.Fatal("expected array-value rejection on flat eq")
	}
}

func TestParse_SQLInjectionIsEscapedAtRender(t *testing.T) {
	// Parser lets the string through; the SQL builder is responsible for
	// escaping. We only confirm Parse doesn't crash on malicious input.
	f, err := Parse(decode(t, `{"tags": "'; DROP TABLE memories; --"}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	conds, err := BuildAGEWhereConditions(f)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(conds, " AND ")
	if strings.Contains(joined, "DROP TABLE") && !strings.Contains(joined, "''; DROP") {
		t.Fatalf("injection not escaped: %s", joined)
	}
}
