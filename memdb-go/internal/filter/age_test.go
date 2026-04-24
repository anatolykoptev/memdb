package filter

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildFromJSON parses a JSON filter and returns the rendered fragments.
func buildFromJSON(t *testing.T, raw string) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	f, err := Parse(m)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	conds, err := BuildAGEWhereConditions(f)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return conds
}

const userIDProp = `ag_catalog.agtype_access_operator(properties::text::agtype, '"user_id"'::agtype)`
const typeProp = `ag_catalog.agtype_access_operator(properties::text::agtype, '"type"'::agtype)`
const tagsProp = `ag_catalog.agtype_access_operator(properties::text::agtype, '"tags"'::agtype)`
const fileIDsProp = `ag_catalog.agtype_access_operator(properties::text::agtype, '"file_ids"'::agtype)`
const confProp = `ag_catalog.agtype_access_operator(properties::text::agtype, '"confidence"'::agtype)`
const createdProp = `ag_catalog.agtype_access_operator(properties::text::agtype, '"created_at"'::agtype)`

func TestAGE_ScalarStringEq(t *testing.T) {
	got := buildFromJSON(t, `{"user_id": "alice"}`)
	want := []string{userIDProp + ` = '"alice"'::agtype`}
	eq(t, got, want)
}

func TestAGE_ScalarNumericEq(t *testing.T) {
	got := buildFromJSON(t, `{"confidence": 42}`)
	want := []string{confProp + ` = '42'::agtype`}
	eq(t, got, want)
}

func TestAGE_ScalarNumericGt(t *testing.T) {
	got := buildFromJSON(t, `{"confidence": {"gt": 5}}`)
	want := []string{confProp + ` > '5'::agtype`}
	eq(t, got, want)
}

func TestAGE_DatetimeGt(t *testing.T) {
	got := buildFromJSON(t, `{"created_at": {"gt": "2026-01-01"}}`)
	want := []string{
		`TRIM(BOTH '"' FROM ` + createdProp + `::text)::timestamp > '2026-01-01'::timestamp`,
	}
	eq(t, got, want)
}

func TestAGE_ArrayFieldEqSingleString(t *testing.T) {
	// tags is an array field — equality uses '["x"]'::agtype form.
	got := buildFromJSON(t, `{"tags": "python"}`)
	want := []string{tagsProp + ` = '["python"]'::agtype`}
	eq(t, got, want)
}

func TestAGE_ArrayFieldContains(t *testing.T) {
	got := buildFromJSON(t, `{"tags": {"contains": "python"}}`)
	want := []string{tagsProp + ` @> '["python"]'::agtype`}
	eq(t, got, want)
}

func TestAGE_ArrayFieldInSingle(t *testing.T) {
	got := buildFromJSON(t, `{"file_ids": {"in": ["f1"]}}`)
	want := []string{fileIDsProp + ` @> '["f1"]'::agtype`}
	eq(t, got, want)
}

func TestAGE_ArrayFieldInMany(t *testing.T) {
	got := buildFromJSON(t, `{"file_ids": {"in": ["f1", "f2"]}}`)
	want := []string{
		`(` + fileIDsProp + ` @> '["f1"]'::agtype OR ` + fileIDsProp + ` @> '["f2"]'::agtype)`,
	}
	eq(t, got, want)
}

func TestAGE_ScalarInSingle(t *testing.T) {
	got := buildFromJSON(t, `{"user_id": {"in": ["alice"]}}`)
	want := []string{userIDProp + ` = '"alice"'::agtype`}
	eq(t, got, want)
}

func TestAGE_ScalarInMany(t *testing.T) {
	got := buildFromJSON(t, `{"user_id": {"in": ["alice", "bob"]}}`)
	want := []string{
		`(` + userIDProp + ` = '"alice"'::agtype OR ` + userIDProp + ` = '"bob"'::agtype)`,
	}
	eq(t, got, want)
}

func TestAGE_InEmptyList(t *testing.T) {
	got := buildFromJSON(t, `{"user_id": {"in": []}}`)
	want := []string{"false"}
	eq(t, got, want)
}

func TestAGE_Like(t *testing.T) {
	got := buildFromJSON(t, `{"memory": {"like": "foo_bar%"}}`)
	memProp := `ag_catalog.agtype_access_operator(properties::text::agtype, '"memory"'::agtype)`
	want := []string{memProp + `::text LIKE '%foo\_bar\%%'`}
	eq(t, got, want)
}

func TestAGE_LikeEscapesSQLQuotes(t *testing.T) {
	got := buildFromJSON(t, `{"memory": {"like": "it's"}}`)
	memProp := `ag_catalog.agtype_access_operator(properties::text::agtype, '"memory"'::agtype)`
	want := []string{memProp + `::text LIKE '%it''s%'`}
	eq(t, got, want)
}

func TestAGE_OrCombinator(t *testing.T) {
	got := buildFromJSON(t, `{"or": [{"type": "text"}, {"user_id": "bob"}]}`)
	want := []string{
		`((` + typeProp + ` = '"text"'::agtype) OR (` + userIDProp + ` = '"bob"'::agtype))`,
	}
	eq(t, got, want)
}

func TestAGE_AndCombinator(t *testing.T) {
	got := buildFromJSON(t, `{"and": [{"type": "text"}, {"user_id": "bob"}]}`)
	want := []string{
		`(` + typeProp + ` = '"text"'::agtype)`,
		`(` + userIDProp + ` = '"bob"'::agtype)`,
	}
	eq(t, got, want)
}

func TestAGE_NestedInfoField(t *testing.T) {
	got := buildFromJSON(t, `{"info.category": "science"}`)
	want := []string{
		`ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties::text::agtype, '"info"'::ag_catalog.agtype, '"category"'::ag_catalog.agtype]) = '"science"'::agtype`,
	}
	eq(t, got, want)
}

func TestAGE_SQLInjectionEscaped(t *testing.T) {
	// Payload contains a single quote that must be doubled on render.
	got := buildFromJSON(t, `{"tags": "'; DROP TABLE memories; --"}`)
	if len(got) != 1 {
		t.Fatalf("len: %d", len(got))
	}
	// tags is an array field → wrapped in '["..."]'::agtype.
	want := tagsProp + ` = '["''; DROP TABLE memories; --"]'::agtype`
	if got[0] != want {
		t.Fatalf("\nwant %q\ngot  %q", want, got[0])
	}
	// Sanity: no unescaped single quote inside the literal payload region.
	if strings.Contains(got[0], `['`) && !strings.Contains(got[0], `["''`) {
		t.Fatalf("apostrophe not doubled: %q", got[0])
	}
}

func TestAGE_EmptyFilter(t *testing.T) {
	conds, err := BuildAGEWhereConditions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if conds != nil && len(conds) != 0 {
		t.Fatalf("expected empty, got %v", conds)
	}
}

// eq compares two []string slices and fails the test with a readable diff.
func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slice len: got %d %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("[%d]\nwant %q\ngot  %q", i, want[i], got[i])
		}
	}
}
