// Package main — cgroup-based GOMEMLIMIT auto-detection.
//
// Without GOMEMLIMIT the Go runtime does not know the container memory ceiling
// and will keep allocating until the OOM killer fires.  This module reads the
// cgroup memory limit at startup and calls runtime/debug.SetMemoryLimit to 80%
// of that value, unless GOMEMLIMIT is already set explicitly in the environment.
//
// When the cgroup reports "no limit" (v2 "max" sentinel or v1 unlimited
// sentinel >2^62) the runtime would be left without a ceiling.  In that case a
// fallback limit is derived from (in priority order):
//  1. MEMDB_GOMEMLIMIT_FALLBACK_MIB env var (explicit operator override, MiB)
//  2. /proc/meminfo MemTotal (host RAM detection — bare-metal / no cgroup)
//  3. defaultFallbackMib (4096 MiB — safe conservative ceiling)
//
// Setting MEMDB_GOMEMLIMIT_FALLBACK_MIB=0 explicitly disables the fallback,
// leaving GC heuristics unchanged (use only when you know what you're doing).
//
// Cgroup v2: /sys/fs/cgroup/memory.max  (value or "max")
// Cgroup v1: /sys/fs/cgroup/memory/memory.limit_in_bytes
//
// Reference: feedback_set_e_recovery_scripts.md — harden startup to prevent
// container OOM instead of graceful GC pause.
package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// cgroupV2MemFile is the cgroup v2 memory limit file inside a container.
	// Var (not const) so tests can redirect to a temp file.
	cgroupV2MemFile = "/sys/fs/cgroup/memory.max"
	// cgroupV1MemFile is the cgroup v1 memory limit file inside a container.
	// Var (not const) so tests can redirect to a temp file.
	cgroupV1MemFile = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	// procMeminfoFile is the host RAM info file used by the fallback path.
	// Var (not const) so tests can redirect to a temp file.
	procMeminfoFile = "/proc/meminfo"
)

const (
	// memLimitFraction is the fraction of the container limit to use as GOMEMLIMIT.
	// 80% leaves headroom for non-Go heap (CGO, goroutine stacks, mmap arenas).
	memLimitFraction = 0.80
	// noLimit is the cgroup v2 sentinel meaning "no limit set".
	noLimit = "max"
	// defaultFallbackMib is the conservative default GOMEMLIMIT (in MiB) when no
	// cgroup limit and no /proc/meminfo are available.  4096 MiB is safe for most
	// container runtimes and prevents unbounded allocation → OOM kill.
	defaultFallbackMib = int64(4096)
	// fallbackMibEnv is the env var name for an explicit fallback override (MiB).
	// Set to "0" to explicitly disable the fallback.
	fallbackMibEnv = "MEMDB_GOMEMLIMIT_FALLBACK_MIB"
)

// gomemlimitGauge exports the effective GOMEMLIMIT in bytes via Prometheus.
// A value of 0 means no limit was set — alert resource_exhaustion when this
// fires in production (the runtime has no memory ceiling → OOM killer risk).
var gomemlimitGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "memdb_gomemlimit_bytes",
	Help: "Effective GOMEMLIMIT in bytes (0 = unset, alert resource_exhaustion).",
})

// gomemlimitBytesVal mirrors the gauge value for in-process reads (tests,
// health checks).  Updated atomically alongside gomemlimitGauge.Set.
var gomemlimitBytesVal atomic.Int64

// applyGoMemLimit sets GOMEMLIMIT from the container cgroup limit if GOMEMLIMIT
// is not already set in the environment.  When the cgroup reports no limit
// (e.g. "max" sentinel), a fallback is derived from the host RAM or a default
// so the runtime always has a memory ceiling unless explicitly disabled.
// Logs the effective limit at INFO level.
// Safe to call multiple times; subsequent calls are no-ops when env is set.
func applyGoMemLimit(logger *slog.Logger) {
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		// Already set by the operator — honor it, just log what we see.
		// The Go runtime parses GOMEMLIMIT itself; we don't call SetMemoryLimit.
		logger.Info("GOMEMLIMIT: using operator-supplied value", slog.String("value", v))
		// We cannot know the parsed bytes here (the runtime owns parsing), so
		// leave the gauge at 0 — the env var is the source of truth.
		return
	}

	limitBytes, source, err := detectCgroupMemLimit()
	if err == nil && limitBytes > 0 {
		target := int64(math.Round(float64(limitBytes) * memLimitFraction))
		debug.SetMemoryLimit(target)
		gomemlimitGauge.Set(float64(target))
		gomemlimitBytesVal.Store(target)
		logger.Info("GOMEMLIMIT: auto-set from container limit",
			slog.String("source", source),
			slog.Int64("container_limit_bytes", limitBytes),
			slog.Int64("gomemlimit_bytes", target),
			slog.String("fraction", fmt.Sprintf("%.0f%%", memLimitFraction*100)),
			slog.String("human", humanBytes(target)),
		)
		return
	}

	// Cgroup limit absent or unlimited — fall back to host RAM / default so the
	// runtime always has a memory ceiling (prevents OOM killer).
	fbBytes, fbSource, fbErr := resolveFallbackLimit()
	if fbErr != nil {
		// Fallback explicitly disabled (MEMDB_GOMEMLIMIT_FALLBACK_MIB=0) or
		// all detection paths failed.  Leave the gauge at 0 — alert fires.
		logger.Warn("GOMEMLIMIT: no container limit and fallback disabled/unavailable",
			slog.String("cgroup_source", source),
			slog.Any("cgroup_err", err),
			slog.Any("fallback_err", fbErr),
		)
		return
	}

	debug.SetMemoryLimit(fbBytes)
	gomemlimitGauge.Set(float64(fbBytes))
	gomemlimitBytesVal.Store(fbBytes)
	logger.Info("GOMEMLIMIT: auto-set from fallback (no cgroup limit)",
		slog.String("cgroup_source", source),
		slog.Any("cgroup_err", err),
		slog.String("fallback_source", fbSource),
		slog.Int64("gomemlimit_bytes", fbBytes),
		slog.String("human", humanBytes(fbBytes)),
	)
}

// resolveFallbackLimit determines the fallback GOMEMLIMIT (in bytes) when no
// cgroup limit is available.  Priority:
//  1. MEMDB_GOMEMLIMIT_FALLBACK_MIB env (operator override, MiB; "0" = disabled)
//  2. /proc/meminfo MemTotal (host RAM)
//  3. defaultFallbackMib (4096 MiB)
//
// Returns (bytes, source, nil) on success, or (0, "", err) when explicitly
// disabled or all paths fail.
func resolveFallbackLimit() (int64, string, error) {
	// 1. Explicit operator override.
	if v := os.Getenv(fallbackMibEnv); v != "" {
		mib, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("parse %s=%q: %w", fallbackMibEnv, v, err)
		}
		if mib <= 0 {
			// Explicitly disabled — operator accepts the OOM risk.
			return 0, "", fmt.Errorf("%s=0: fallback explicitly disabled", fallbackMibEnv)
		}
		bytes := mib * 1024 * 1024
		return bytes, fallbackMibEnv, nil
	}

	// 2. Host RAM via /proc/meminfo.
	if hostBytes, err := detectHostMemLimit(); err == nil && hostBytes > 0 {
		target := int64(math.Round(float64(hostBytes) * memLimitFraction))
		return target, procMeminfoFile, nil
	}

	// 3. Conservative default.
	return defaultFallbackMib * 1024 * 1024, "default", nil
}

// detectHostMemLimit reads /proc/meminfo and returns MemTotal in bytes.
func detectHostMemLimit() (int64, error) {
	f, err := os.Open(procMeminfoFile)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "MemTotal:       16384000 kB"
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		// fields = ["MemTotal:", "16384000", "kB"]
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemTotal line: %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal %q: %w", fields[1], err)
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal not found in %s", procMeminfoFile)
}

// detectCgroupMemLimit tries cgroup v2 then v1.  Returns (bytes, source, err).
// Returns (0, source, nil) when the file exists but no limit is configured.
func detectCgroupMemLimit() (int64, string, error) {
	// Try cgroup v2 first (most modern kernels + Docker on Linux).
	if v2, err := readCgroupFile(cgroupV2MemFile); err == nil {
		bytes, parseErr := parseCgroupValue(v2)
		return bytes, cgroupV2MemFile, parseErr
	}
	// Fall back to cgroup v1.
	if v1, err := readCgroupFile(cgroupV1MemFile); err == nil {
		bytes, parseErr := parseCgroupValue(v1)
		return bytes, cgroupV1MemFile, parseErr
	}
	return 0, "none", nil
}

// readCgroupFile reads and trims a cgroup control file.
func readCgroupFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// parseCgroupValue parses a cgroup memory value string.
// Returns (0, nil) for the "max" sentinel (meaning no limit).
func parseCgroupValue(s string) (int64, error) {
	if s == noLimit || s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected cgroup value %q: %w", s, err)
	}
	// cgroup v1 uses the maximum possible integer (9223372036854771712) for "no limit".
	// Treat anything above 2^62 as "unlimited".
	const unlimitedThreshold = int64(1) << 62
	if n > unlimitedThreshold {
		return 0, nil
	}
	return n, nil
}

// humanBytes formats bytes as a human-readable string (GiB/MiB/KiB/B).
func humanBytes(b int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.2f KiB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
