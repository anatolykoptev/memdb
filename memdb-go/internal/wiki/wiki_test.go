package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSlug(t *testing.T) {
	cases := []struct {
		slug    string
		wantErr bool
	}{
		{"caroline", false},
		{"themes/family", false},
		{"a-b_c.1", false},
		{"deep/nested/path", false},
		{"", true},
		{"/leading-slash", true},
		{"..", true},
		{"a/../b", true},
		{"a/b/..", true},
		{`back\slash`, true},
		{"with\x01control", true},
	}
	for _, c := range cases {
		err := validateSlug(c.slug)
		if (err != nil) != c.wantErr {
			t.Errorf("validateSlug(%q) err=%v, wantErr=%v", c.slug, err, c.wantErr)
		}
	}
}

func TestExportToFilesystem_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("UPLOADS_ROOT", tmp)

	p := Page{
		CubeID: "cube-A",
		Slug:   "caroline",
		Title:  "Caroline",
		Body:   "# Caroline\n\nMain character.",
	}
	abs, err := ExportToFilesystem(p)
	if err != nil {
		t.Fatalf("ExportToFilesystem: %v", err)
	}
	wantPrefix := filepath.Join(tmp, "memdb-go", "wiki", "cube-A")
	if !strings.HasPrefix(abs, wantPrefix) {
		t.Errorf("path %q does not start with %q", abs, wantPrefix)
	}
	if !strings.HasSuffix(abs, "caroline.md") {
		t.Errorf("path %q does not end with caroline.md", abs)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(got), "# Caroline") {
		t.Errorf("body missing header: %q", got)
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Errorf("body should end with newline")
	}
}

func TestExportToFilesystem_NestedSlugCreatesParents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("UPLOADS_ROOT", tmp)

	p := Page{
		CubeID: "cube-B",
		Slug:   "themes/family/relationships",
		Body:   "deep content",
	}
	abs, err := ExportToFilesystem(p)
	if err != nil {
		t.Fatalf("nested slug: %v", err)
	}
	want := filepath.Join(tmp, "memdb-go", "wiki", "cube-B", "themes", "family", "relationships.md")
	if abs != want {
		t.Errorf("abs=%q want=%q", abs, want)
	}
}

func TestExportToFilesystem_RejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("UPLOADS_ROOT", tmp)

	cases := []Page{
		{CubeID: "c", Slug: "../escape"},
		{CubeID: "c", Slug: "a/../../etc/passwd"},
		{CubeID: "c", Slug: "/abs/path"},
	}
	for _, p := range cases {
		if _, err := ExportToFilesystem(p); err == nil {
			t.Errorf("ExportToFilesystem(%q) should have rejected, got nil err", p.Slug)
		}
	}
}

func TestExportToFilesystem_EmptyFieldsRejected(t *testing.T) {
	t.Setenv("UPLOADS_ROOT", t.TempDir())

	if _, err := ExportToFilesystem(Page{Slug: "x"}); err == nil {
		t.Error("empty cube_id should be rejected")
	}
	if _, err := ExportToFilesystem(Page{CubeID: "c"}); err == nil {
		t.Error("empty slug should be rejected")
	}
}

func TestAppendLog_AppendsTimestampedLine(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("UPLOADS_ROOT", tmp)

	if err := AppendLog("cube-X", "first ingest"); err != nil {
		t.Fatalf("AppendLog 1: %v", err)
	}
	if err := AppendLog("cube-X", "second ingest"); err != nil {
		t.Fatalf("AppendLog 2: %v", err)
	}
	logPath := filepath.Join(tmp, "memdb-go", "wiki", "cube-X", "log.md")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d:\n%s", len(lines), got)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("log line should start with '- ', got: %q", line)
		}
	}
	if !strings.Contains(lines[0], "first ingest") {
		t.Errorf("first line missing payload: %q", lines[0])
	}
	if !strings.Contains(lines[1], "second ingest") {
		t.Errorf("second line missing payload: %q", lines[1])
	}
}

func TestCubeRoot_CreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("UPLOADS_ROOT", tmp)

	root, err := CubeRoot("cube-Z")
	if err != nil {
		t.Fatalf("CubeRoot: %v", err)
	}
	want := filepath.Join(tmp, "memdb-go", "wiki", "cube-Z")
	if root != want {
		t.Errorf("CubeRoot=%q want=%q", root, want)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Errorf("expected %q to be a directory: err=%v", root, err)
	}
}
