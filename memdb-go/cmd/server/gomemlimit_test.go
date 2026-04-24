package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCgroupValue(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"unlimited sentinel", "max", 0, false},
		{"empty string", "", 0, false},
		{"normal 512MiB", "536870912", 536870912, false},
		{"normal 6GiB", "6442450944", 6442450944, false},
		{"cgroup v1 unlimited", "9223372036854771712", 0, false},
		{"cgroup v1 high-but-real 8GiB", "8589934592", 8589934592, false},
		{"invalid", "notanumber", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCgroupValue(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseCgroupValue(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("parseCgroupValue(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadCgroupFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("reads and trims whitespace", func(t *testing.T) {
		path := filepath.Join(dir, "memory.max")
		if err := os.WriteFile(path, []byte("536870912\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readCgroupFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != "536870912" {
			t.Errorf("readCgroupFile = %q, want %q", got, "536870912")
		}
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		_, err := readCgroupFile(filepath.Join(dir, "nonexistent"))
		if err == nil {
			t.Error("expected error for missing file, got nil")
		}
	})
}

func TestDetectCgroupMemLimitMockFS(t *testing.T) {
	dir := t.TempDir()

	// Write a mock cgroup v2 file.
	v2Path := filepath.Join(dir, "memory.max")
	const containerBytes = int64(1073741824) // 1 GiB
	if err := os.WriteFile(v2Path, []byte("1073741824\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Directly test the reading+parsing combination.
	raw, err := readCgroupFile(v2Path)
	if err != nil {
		t.Fatalf("readCgroupFile: %v", err)
	}
	bytes, err := parseCgroupValue(raw)
	if err != nil {
		t.Fatalf("parseCgroupValue: %v", err)
	}
	if bytes != containerBytes {
		t.Errorf("bytes = %d, want %d", bytes, containerBytes)
	}

	// Verify 80% fraction math.
	target := int64(math.Round(float64(bytes) * memLimitFraction))
	// 1073741824 * 0.80 = 858993459.2 → rounds to 858993459
	const wantTarget = int64(858993459)
	if target < wantTarget-1 || target > wantTarget+1 {
		t.Errorf("80%% of 1GiB = %d, want ~%d", target, wantTarget)
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{512, "512 B"},
		{1536, "1.50 KiB"},
		{1048576, "1.00 MiB"},
		{536870912, "512.00 MiB"},
		{6442450944, "6.00 GiB"},
	}
	for _, tc := range tests {
		got := humanBytes(tc.input)
		if got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
