// Package main — cgroup-based GOMEMLIMIT auto-detection.
//
// Without GOMEMLIMIT the Go runtime does not know the container memory ceiling
// and will keep allocating until the OOM killer fires.  This module reads the
// cgroup memory limit at startup and calls runtime/debug.SetMemoryLimit to 80%
// of that value, unless GOMEMLIMIT is already set explicitly in the environment.
//
// Cgroup v2: /sys/fs/cgroup/memory.max  (value or "max")
// Cgroup v1: /sys/fs/cgroup/memory/memory.limit_in_bytes
//
// Reference: feedback_set_e_recovery_scripts.md — harden startup to prevent
// container OOM instead of graceful GC pause.
package main

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	// cgroupV2MemFile is the cgroup v2 memory limit file inside a container.
	cgroupV2MemFile = "/sys/fs/cgroup/memory.max"
	// cgroupV1MemFile is the cgroup v1 memory limit file inside a container.
	cgroupV1MemFile = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	// memLimitFraction is the fraction of the container limit to use as GOMEMLIMIT.
	// 80% leaves headroom for non-Go heap (CGO, goroutine stacks, mmap arenas).
	memLimitFraction = 0.80
	// noLimit is the cgroup v2 sentinel meaning "no limit set".
	noLimit = "max"
)

// applyGoMemLimit sets GOMEMLIMIT from the container cgroup limit if GOMEMLIMIT
// is not already set in the environment.  Logs the effective limit at INFO level.
// Safe to call multiple times; subsequent calls are no-ops when env is set.
func applyGoMemLimit(logger *slog.Logger) {
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		// Already set by the operator — honor it, just log what we see.
		logger.Info("GOMEMLIMIT: using operator-supplied value", slog.String("value", v))
		return
	}

	limitBytes, source, err := detectCgroupMemLimit()
	if err != nil || limitBytes <= 0 {
		logger.Info("GOMEMLIMIT: no container memory limit detected, runtime GC heuristics unchanged",
			slog.String("source", source),
			slog.Any("err", err),
		)
		return
	}

	target := int64(math.Round(float64(limitBytes) * memLimitFraction))
	debug.SetMemoryLimit(target)
	logger.Info("GOMEMLIMIT: auto-set from container limit",
		slog.String("source", source),
		slog.Int64("container_limit_bytes", limitBytes),
		slog.Int64("gomemlimit_bytes", target),
		slog.String("fraction", fmt.Sprintf("%.0f%%", memLimitFraction*100)),
		slog.String("human", humanBytes(target)),
	)
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
