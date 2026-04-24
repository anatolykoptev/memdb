// Package scheduler — tuning.go: M4 runtime-readable D3 hyperparameters.
//
// Tier-promotion parameters (raw→episodic, episodic→semantic) were
// hard-coded as package-level const. For tuning grid-runs we need to
// sweep them via .env without rebuilding memdb-go. This file exposes
// them as env-readable accessors, each with bounded validation and a
// silent fallback to the compile-time default on invalid input.
//
// Naming: the existing constants use "episodic*" to name the raw→episodic
// promotion (the tier being produced) and "semantic*" for episodic→semantic.
// The env vars adopt the other convention — naming the SOURCE tier:
// MEMDB_D3_*_RAW for raw→episodic, MEMDB_D3_*_EPISODIC for episodic→semantic.
// This matches the M4 spec and is less ambiguous when sweeping.
package scheduler

import (
	"log/slog"
	"os"
	"strconv"
	"sync"
)

var (
	envOverrideLogOnce sync.Once
	envOverrideMu     sync.Mutex
	envOverrides      = map[string]string{}
)

func recordOverride(name, value string) {
	envOverrideMu.Lock()
	envOverrides[name] = value
	envOverrideMu.Unlock()
}

// LogTuningOverrides writes a single slog.Info line listing every
// MEMDB_D3_* hyperparameter override picked up from the environment.
// Idempotent — subsequent calls are no-ops.
func LogTuningOverrides(logger *slog.Logger) {
	envOverrideLogOnce.Do(func() {
		if logger == nil {
			return
		}
		envOverrideMu.Lock()
		defer envOverrideMu.Unlock()
		if len(envOverrides) == 0 {
			return
		}
		attrs := make([]any, 0, 2*len(envOverrides))
		for k, v := range envOverrides {
			attrs = append(attrs, slog.String(k, v))
		}
		logger.Info("scheduler: tuning env overrides active", attrs...)
	})
}

func parseEnvFloat(name string, lo, hi, def float64) float64 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < lo || v > hi {
		return def
	}
	if v != def {
		recordOverride(name, raw)
	}
	return v
}

func parseEnvInt(name string, lo, hi, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return def
	}
	if v != def {
		recordOverride(name, raw)
	}
	return v
}

// ---- D3 — tree_manager ----------------------------------------------------

const (
	defaultEpisodicMinClusterSize  = 3   // raw→episodic cluster floor
	defaultSemanticMinClusterSize  = 2   // episodic→semantic cluster floor
	defaultEpisodicCosineThreshold = 0.7 // raw→episodic cosine gate
	defaultSemanticCosineThreshold = 0.6 // episodic→semantic cosine gate
)

// episodicMinClusterSize — raw→episodic min cluster size.
// Env: MEMDB_D3_MIN_CLUSTER_RAW in [2, 50].
func episodicMinClusterSize() int {
	return parseEnvInt("MEMDB_D3_MIN_CLUSTER_RAW", 2, 50, defaultEpisodicMinClusterSize)
}

// semanticMinClusterSize — episodic→semantic min cluster size.
// Env: MEMDB_D3_MIN_CLUSTER_EPISODIC in [2, 50].
func semanticMinClusterSize() int {
	return parseEnvInt("MEMDB_D3_MIN_CLUSTER_EPISODIC", 2, 50, defaultSemanticMinClusterSize)
}

// episodicCosineThreshold — raw→episodic cosine gate.
// Env: MEMDB_D3_COS_THRESHOLD_RAW in [0, 1].
func episodicCosineThreshold() float64 {
	return parseEnvFloat("MEMDB_D3_COS_THRESHOLD_RAW", 0, 1, defaultEpisodicCosineThreshold)
}

// semanticCosineThreshold — episodic→semantic cosine gate.
// Env: MEMDB_D3_COS_THRESHOLD_EPISODIC in [0, 1].
func semanticCosineThreshold() float64 {
	return parseEnvFloat("MEMDB_D3_COS_THRESHOLD_EPISODIC", 0, 1, defaultSemanticCosineThreshold)
}
