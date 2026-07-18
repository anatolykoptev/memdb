package main

import (
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// --- existing unit tests for helpers (unchanged) ---

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

// --- fallback / applyGoMemLimit integration tests ---
//
// These exercise the REAL applyGoMemLimit code path (not a mock) by
// redirecting the cgroup file path vars and /proc/meminfo path var to temp
// files, then verifying debug.SetMemoryLimit was called with a non-zero value.

// quietLogger returns a discard logger for tests.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// currentMemLimit reads the live GOMEMLIMIT via debug.SetMemoryLimit(-1).
func currentMemLimit() int64 {
	return debug.SetMemoryLimit(-1)
}

// saveMemLimit restores the original GOMEMLIMIT after the test.
func saveMemLimit(t *testing.T) {
	t.Helper()
	orig := currentMemLimit()
	t.Cleanup(func() { debug.SetMemoryLimit(orig) })
}

// redirectCgroupPaths points the package-level cgroup file path vars at temp
// files in dir.  Original values are restored on test cleanup.
func redirectCgroupPaths(t *testing.T, dir string) {
	t.Helper()
	origV2 := cgroupV2MemFile
	origV1 := cgroupV1MemFile
	t.Cleanup(func() {
		cgroupV2MemFile = origV2
		cgroupV1MemFile = origV1
	})
	cgroupV2MemFile = filepath.Join(dir, "memory.max")
	cgroupV1MemFile = filepath.Join(dir, "memory", "limit_in_bytes")
}

// redirectMeminfo points the package-level procMeminfoFile var at a temp file.
func redirectMeminfo(t *testing.T, path string) {
	t.Helper()
	orig := procMeminfoFile
	t.Cleanup(func() { procMeminfoFile = orig })
	procMeminfoFile = path
}

// setEnv sets an env var for the duration of the test.
func setEnv(t *testing.T, key, val string) {
	t.Helper()
	orig, ok := os.LookupEnv(key)
	t.Cleanup(func() {
		if ok {
			os.Setenv(key, orig)
		} else {
			os.Unsetenv(key)
		}
	})
	os.Setenv(key, val)
}

// unsetEnv unsets an env var for the duration of the test.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	orig, ok := os.LookupEnv(key)
	t.Cleanup(func() {
		if ok {
			os.Setenv(key, orig)
		} else {
			os.Unsetenv(key)
		}
	})
	os.Unsetenv(key)
}

// TestApplyGoMemLimit_FallbackOnMaxSentinel is the RED test from the spec:
// mock cgroup file with "max", assert debug.SetMemoryLimit is called with a
// non-zero fallback.
func TestApplyGoMemLimit_FallbackOnMaxSentinel(t *testing.T) {
	saveMemLimit(t)
	unsetEnv(t, "GOMEMLIMIT")
	unsetEnv(t, fallbackMibEnv)

	dir := t.TempDir()
	redirectCgroupPaths(t, dir)

	// Write a cgroup v2 file with the "max" sentinel (no limit).
	if err := os.WriteFile(cgroupV2MemFile, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Point meminfo at a non-existent file so the default fallback kicks in.
	redirectMeminfo(t, filepath.Join(dir, "nonexistent_meminfo"))

	// Reset the gauge to 0 so we can detect the set.
	gomemlimitGauge.Set(0)
	gomemlimitBytesVal.Store(0)

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	if got <= 0 {
		t.Fatalf("GOMEMLIMIT not set (got %d) — fallback should have fired for cgroup 'max'", got)
	}

	// Should be the default fallback (4096 MiB) since meminfo is unavailable.
	const wantDefault = defaultFallbackMib * 1024 * 1024
	if got != wantDefault {
		t.Errorf("GOMEMLIMIT = %d, want default fallback %d (%s)", got, wantDefault, humanBytes(wantDefault))
	}

	// Gauge must reflect the set value.
	if g := gomemlimitBytesVal.Load(); g != wantDefault {
		t.Errorf("gomemlimitGauge = %d, want %d", g, wantDefault)
	}
}

// TestApplyGoMemLimit_FallbackOnV1Unlimited tests the cgroup v1 unlimited
// sentinel (>2^62) triggers the fallback.
func TestApplyGoMemLimit_FallbackOnV1Unlimited(t *testing.T) {
	saveMemLimit(t)
	unsetEnv(t, "GOMEMLIMIT")
	unsetEnv(t, fallbackMibEnv)

	dir := t.TempDir()
	redirectCgroupPaths(t, dir)

	// v2 file missing → v1 file with unlimited sentinel.
	v1Dir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cgroupV1MemFile, []byte("9223372036854771712\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	redirectMeminfo(t, filepath.Join(dir, "nonexistent_meminfo"))

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	if got <= 0 {
		t.Fatalf("GOMEMLIMIT not set (got %d) — fallback should fire for v1 unlimited sentinel", got)
	}
}

// TestApplyGoMemLimit_FallbackFromEnv tests that MEMDB_GOMEMLIMIT_FALLBACK_MIB
// is honored when set.
func TestApplyGoMemLimit_FallbackFromEnv(t *testing.T) {
	saveMemLimit(t)
	unsetEnv(t, "GOMEMLIMIT")
	setEnv(t, fallbackMibEnv, "2048") // 2048 MiB

	dir := t.TempDir()
	redirectCgroupPaths(t, dir)
	if err := os.WriteFile(cgroupV2MemFile, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	redirectMeminfo(t, filepath.Join(dir, "nonexistent_meminfo"))

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	const want = int64(2048) * 1024 * 1024
	if got != want {
		t.Errorf("GOMEMLIMIT = %d, want env override %d", got, want)
	}
}

// TestApplyGoMemLimit_FallbackFromMeminfo tests that /proc/meminfo MemTotal is
// used when no env override and no cgroup limit.
func TestApplyGoMemLimit_FallbackFromMeminfo(t *testing.T) {
	saveMemLimit(t)
	unsetEnv(t, "GOMEMLIMIT")
	unsetEnv(t, fallbackMibEnv)

	dir := t.TempDir()
	redirectCgroupPaths(t, dir)
	if err := os.WriteFile(cgroupV2MemFile, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mock /proc/meminfo with MemTotal: 8388608 kB = 8 GiB.
	meminfoPath := filepath.Join(dir, "meminfo")
	const memTotalKB = int64(8388608) // 8 GiB
	if err := os.WriteFile(meminfoPath, []byte("MemTotal:       8388608 kB\nMemFree:        4000000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	redirectMeminfo(t, meminfoPath)

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	// 80% of 8 GiB = 6.4 GiB = 6881294991.36 → rounds to 6881294991
	want := int64(math.Round(float64(memTotalKB*1024) * memLimitFraction))
	if got != want {
		t.Errorf("GOMEMLIMIT = %d, want meminfo-based %d (%s)", got, want, humanBytes(want))
	}
}

// TestApplyGoMemLimit_ExplicitGOMEMLIMIT tests that an explicit GOMEMLIMIT env
// is honored and applyGoMemLimit does NOT call SetMemoryLimit (the runtime
// owns parsing).
func TestApplyGoMemLimit_ExplicitGOMEMLIMIT(t *testing.T) {
	saveMemLimit(t)
	setEnv(t, "GOMEMLIMIT", "1073741824") // 1 GiB — runtime will parse this
	unsetEnv(t, fallbackMibEnv)

	// Even if cgroup has "max", we should not touch SetMemoryLimit.
	dir := t.TempDir()
	redirectCgroupPaths(t, dir)
	if err := os.WriteFile(cgroupV2MemFile, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set a known limit so we can verify it's NOT changed by applyGoMemLimit.
	debug.SetMemoryLimit(999)
	gomemlimitGauge.Set(0)
	gomemlimitBytesVal.Store(0)

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	if got != 999 {
		t.Errorf("GOMEMLIMIT = %d, want 999 (applyGoMemLimit should not override explicit env)", got)
	}
	// Gauge stays at 0 — env var is the source of truth.
	if g := gomemlimitBytesVal.Load(); g != 0 {
		t.Errorf("gomemlimitGauge = %d, want 0 (env owns the value)", g)
	}
}

// TestApplyGoMemLimit_FallbackDisabled tests that MEMDB_GOMEMLIMIT_FALLBACK_MIB=0
// explicitly disables the fallback, leaving the runtime without a limit.
func TestApplyGoMemLimit_FallbackDisabled(t *testing.T) {
	saveMemLimit(t)
	unsetEnv(t, "GOMEMLIMIT")
	setEnv(t, fallbackMibEnv, "0") // explicitly disabled

	dir := t.TempDir()
	redirectCgroupPaths(t, dir)
	if err := os.WriteFile(cgroupV2MemFile, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	redirectMeminfo(t, filepath.Join(dir, "nonexistent_meminfo"))

	debug.SetMemoryLimit(777) // sentinel — should remain unchanged
	gomemlimitGauge.Set(0)
	gomemlimitBytesVal.Store(0)

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	if got != 777 {
		t.Errorf("GOMEMLIMIT = %d, want 777 (fallback disabled, should not change limit)", got)
	}
	// Gauge stays at 0 — no limit set by us.
	if g := gomemlimitBytesVal.Load(); g != 0 {
		t.Errorf("gomemlimitGauge = %d, want 0 (fallback disabled)", g)
	}
}

// TestApplyGoMemLimit_CgroupLimitStillWorks tests that when a real cgroup
// limit exists, the fallback is NOT used and the 80% fraction is applied.
func TestApplyGoMemLimit_CgroupLimitStillWorks(t *testing.T) {
	saveMemLimit(t)
	unsetEnv(t, "GOMEMLIMIT")
	unsetEnv(t, fallbackMibEnv)

	dir := t.TempDir()
	redirectCgroupPaths(t, dir)

	// 2 GiB cgroup limit.
	const containerBytes = int64(2147483648)
	if err := os.WriteFile(cgroupV2MemFile, []byte("2147483648\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyGoMemLimit(quietLogger())

	got := currentMemLimit()
	want := int64(math.Round(float64(containerBytes) * memLimitFraction))
	if got != want {
		t.Errorf("GOMEMLIMIT = %d, want 80%% of cgroup = %d", got, want)
	}
}

// TestDetectHostMemLimit tests the /proc/meminfo parser.
func TestDetectHostMemLimit(t *testing.T) {
	dir := t.TempDir()

	t.Run("parses MemTotal", func(t *testing.T) {
		path := filepath.Join(dir, "meminfo")
		if err := os.WriteFile(path, []byte("MemTotal:       16384000 kB\nMemFree:        8000000 kB\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		redirectMeminfo(t, path)

		got, err := detectHostMemLimit()
		if err != nil {
			t.Fatal(err)
		}
		const want = int64(16384000 * 1024) // kB → bytes
		if got != want {
			t.Errorf("detectHostMemLimit = %d, want %d", got, want)
		}
	})

	t.Run("error on missing file", func(t *testing.T) {
		redirectMeminfo(t, filepath.Join(dir, "nonexistent"))
		_, err := detectHostMemLimit()
		if err == nil {
			t.Error("expected error for missing meminfo, got nil")
		}
	})

	t.Run("error when MemTotal absent", func(t *testing.T) {
		path := filepath.Join(dir, "meminfo_no_total")
		if err := os.WriteFile(path, []byte("MemFree:        8000000 kB\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		redirectMeminfo(t, path)
		_, err := detectHostMemLimit()
		if err == nil {
			t.Error("expected error when MemTotal line absent, got nil")
		}
	})
}

// TestResolveFallbackLimit tests the fallback resolution priority.
func TestResolveFallbackLimit(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		unsetEnv(t, fallbackMibEnv)
		setEnv(t, fallbackMibEnv, "8192")

		dir := t.TempDir()
		redirectMeminfo(t, filepath.Join(dir, "nonexistent"))

		got, source, err := resolveFallbackLimit()
		if err != nil {
			t.Fatal(err)
		}
		const want = int64(8192) * 1024 * 1024
		if got != want {
			t.Errorf("resolveFallbackLimit = %d, want %d", got, want)
		}
		if source != fallbackMibEnv {
			t.Errorf("source = %q, want %q", source, fallbackMibEnv)
		}
	})

	t.Run("env disabled returns error", func(t *testing.T) {
		unsetEnv(t, fallbackMibEnv)
		setEnv(t, fallbackMibEnv, "0")

		_, _, err := resolveFallbackLimit()
		if err == nil {
			t.Error("expected error for MEMDB_GOMEMLIMIT_FALLBACK_MIB=0, got nil")
		}
	})

	t.Run("default when no env and no meminfo", func(t *testing.T) {
		unsetEnv(t, fallbackMibEnv)
		redirectMeminfo(t, filepath.Join(t.TempDir(), "nonexistent"))

		got, source, err := resolveFallbackLimit()
		if err != nil {
			t.Fatal(err)
		}
		const want = defaultFallbackMib * 1024 * 1024
		if got != want {
			t.Errorf("resolveFallbackLimit = %d, want default %d", got, want)
		}
		if source != "default" {
			t.Errorf("source = %q, want %q", source, "default")
		}
	})
}
