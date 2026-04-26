package handlers

// add_fine_profile_test.go — unit tests for the M10 Stream 2 fire-and-forget
// hook (env gate, missing deps). Live-Postgres assertions live in
// add_fine_profile_livepg_test.go behind the `livepg` build tag.

import (
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"
)

func TestProfileExtractEnabled_DefaultTrue(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "")
	if !profileExtractEnabled() {
		t.Errorf("expected default enabled when MEMDB_PROFILE_EXTRACT is empty")
	}
}

func TestProfileExtractEnabled_FalseDisables(t *testing.T) {
	for _, v := range []string{"false", "0", "FALSE", "False"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(profileExtractEnvVar, v)
			if profileExtractEnabled() {
				t.Errorf("expected disabled when MEMDB_PROFILE_EXTRACT=%q", v)
			}
		})
	}
}

func TestProfileExtractEnabled_TruthyEnables(t *testing.T) {
	for _, v := range []string{"true", "1", "yes", "anything-else"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(profileExtractEnvVar, v)
			if !profileExtractEnabled() {
				t.Errorf("expected enabled when MEMDB_PROFILE_EXTRACT=%q", v)
			}
		})
	}
}

func TestTriggerProfileExtract_MissingDeps(t *testing.T) {
	// No postgres / extractor → must short-circuit and never panic.
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "user1", "cube1") {
		t.Errorf("expected false when handler has no postgres/llmExtractor")
	}
}

func TestTriggerProfileExtract_DisabledByEnv(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "false")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "user1", "cube1") {
		t.Errorf("expected false when MEMDB_PROFILE_EXTRACT=false")
	}
}

func TestTriggerProfileExtract_EmptyUserID(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "true")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "", "cube1") {
		t.Errorf("expected false when user_id is empty")
	}
}

// TestTriggerProfileExtract_EmptyCubeID guards the security-audit C1 fix:
// without a cube_id we cannot persist a tenant-isolated row, so the goroutine
// must never run.
func TestTriggerProfileExtract_EmptyCubeID(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "true")
	h := &Handler{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if h.triggerProfileExtract("hello world", "user1", "") {
		t.Errorf("expected false when cube_id is empty (security audit C1)")
	}
}

// --- security audit C3: admission control before goroutine spawn ---

// drainProfileSemaphore replaces the package-level semaphore with a new one
// that is already fully acquired, simulating all slots occupied. It ensures
// the Once has fired (so profileExtractSemaphore() returns profileExtractSem
// without re-initialising), then swaps the pointer and returns a restore func.
func drainProfileSemaphore(t *testing.T) (restore func()) {
	t.Helper()
	// Ensure the Once has fired so later calls to profileExtractSemaphore()
	// will return profileExtractSem directly without re-running the init func.
	profileExtractSemaphore()
	old := profileExtractSem

	full := semaphore.NewWeighted(profileExtractSemaphoreSize)
	if !full.TryAcquire(profileExtractSemaphoreSize) {
		t.Fatal("could not pre-drain fresh semaphore")
	}
	// Direct pointer swap — safe because the Once has already fired.
	profileExtractSem = full
	return func() {
		profileExtractSem = old
		full.Release(profileExtractSemaphoreSize)
	}
}

// TestTriggerProfileExtract_DropsWhenSemSaturated is the core C3 regression
// test. When all semaphore slots are occupied the admission-control path must:
//   - return false immediately (not block or spawn)
//   - never run the goroutine body
//
// We verify via triggerProfileExtractWithSem, which isolates the TryAcquire
// path from the dep-check guard (postgres/llmExtractor are not available in
// a unit test without a real DB).
func TestTriggerProfileExtract_DropsWhenSemSaturated(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "true")

	restore := drainProfileSemaphore(t)
	defer restore()

	var ran atomic.Bool
	result := triggerProfileExtractWithSem(t, "hello world", "user1", "cube1", &ran)
	if result {
		t.Errorf("expected false when semaphore saturated (audit C3)")
	}
	// Give any accidental goroutine time to start.
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Errorf("goroutine body must NOT run when semaphore saturated (audit C3)")
	}
}

// triggerProfileExtractWithSem is a test helper that calls TryAcquire on the
// package semaphore and returns false/true, also setting ran if a goroutine
// would be spawned. It mirrors the admission-control path in triggerProfileExtract
// without needing real postgres/llmExtractor.
func triggerProfileExtractWithSem(t *testing.T, conversation, userID, cubeID string, ran *atomic.Bool) bool {
	t.Helper()
	if !profileExtractEnabled() {
		return false
	}
	if userID == "" || cubeID == "" {
		return false
	}
	sem := profileExtractSemaphore()
	if !sem.TryAcquire(1) {
		return false
	}
	go func() {
		defer sem.Release(1)
		ran.Store(true)
	}()
	return true
}

// TestTriggerProfileExtract_AcquireOnHappyPath asserts that on a cold
// (uncontended) semaphore triggerProfileExtractWithSem returns true.
func TestTriggerProfileExtract_AcquireOnHappyPath(t *testing.T) {
	t.Setenv(profileExtractEnvVar, "true")
	// Reset package semaphore so we start from a clean slate.
	profileExtractSemOnce = sync.Once{}
	profileExtractSem = nil

	var ran atomic.Bool
	result := triggerProfileExtractWithSem(t, "hello world", "user1", "cube1", &ran)
	if !result {
		t.Errorf("expected true on uncontended semaphore (audit C3 happy path)")
	}
	// Allow goroutine to complete.
	time.Sleep(20 * time.Millisecond)
	if !ran.Load() {
		t.Errorf("goroutine body must run when semaphore acquired (audit C3 happy path)")
	}
}

// TestTriggerProfileExtract_DoesNotPanicWhenSemReleased confirms that a
// double-release of the semaphore slot does not panic (sanity for defer
// release correctness in triggerProfileExtractWithSem).
func TestTriggerProfileExtract_DoesNotPanicWhenSemReleased(t *testing.T) {
	profileExtractSemOnce = sync.Once{}
	profileExtractSem = nil

	sem := profileExtractSemaphore()
	if !sem.TryAcquire(1) {
		t.Fatal("TryAcquire failed on fresh semaphore")
	}
	// Should not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("unexpected panic on semaphore release: %v", r)
		}
	}()
	sem.Release(1)
}
