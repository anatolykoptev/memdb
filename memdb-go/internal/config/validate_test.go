package config

import (
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// ── PF-1: auth disabled with no master key must fail in non-dev mode ──────────

// TestValidate_NoEnv_AuthFailsServer is the RED test for PF-1: a fresh deploy
// with no env vars has AuthEnabled=false and an empty MasterKeyHash. The server
// must refuse to start. This test goes RED if the auth guard in Validate() is
// removed.
func TestValidate_NoEnv_AuthFailsServer(t *testing.T) {
	// Clear all relevant env vars so Load() sees a "fresh deploy" state.
	unsetEnv(t, "AUTH_ENABLED")
	unsetEnv(t, "MASTER_KEY_HASH")
	unsetEnv(t, "MEMDB_DEV")
	unsetEnv(t, "MEMDB_POSTGRES_URL")

	cfg := Load()
	if err := cfg.Validate(ModeServer); err == nil {
		t.Fatal("expected Validate(ModeServer) to fail with no auth and no dev mode, got nil")
	}
}

// TestValidate_NoEnv_AuthFailsTool verifies the same auth guard fires in tool
// mode (no PostgresURL requirement, but auth is still required).
func TestValidate_NoEnv_AuthFailsTool(t *testing.T) {
	unsetEnv(t, "AUTH_ENABLED")
	unsetEnv(t, "MASTER_KEY_HASH")
	unsetEnv(t, "MEMDB_DEV")

	cfg := Load()
	if err := cfg.Validate(ModeTool); err == nil {
		t.Fatal("expected Validate(ModeTool) to fail with no auth and no dev mode, got nil")
	}
}

// TestValidate_AuthErrorMentionsSecurity confirms the error message is
// actionable — it must mention AUTH_ENABLED and MASTER_KEY_HASH so operators
// know which env vars to set.
func TestValidate_AuthErrorMentionsSecurity(t *testing.T) {
	unsetEnv(t, "AUTH_ENABLED")
	unsetEnv(t, "MASTER_KEY_HASH")
	unsetEnv(t, "MEMDB_DEV")
	unsetEnv(t, "MEMDB_POSTGRES_URL")

	cfg := Load()
	err := cfg.Validate(ModeServer)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "AUTH_ENABLED") {
		t.Errorf("error should mention AUTH_ENABLED, got: %s", msg)
	}
	if !strings.Contains(msg, "MASTER_KEY_HASH") {
		t.Errorf("error should mention MASTER_KEY_HASH, got: %s", msg)
	}
}

// ── PF-1: dev mode relaxes the auth check ─────────────────────────────────────

// TestValidate_DevMode_AllowsNoAuth verifies that MEMDB_DEV=1 allows startup
// without auth config — local development must still work.
func TestValidate_DevMode_AllowsNoAuth(t *testing.T) {
	unsetEnv(t, "AUTH_ENABLED")
	unsetEnv(t, "MASTER_KEY_HASH")
	setEnv(t, "MEMDB_DEV", "1")
	setEnv(t, "MEMDB_POSTGRES_URL", "postgres://localhost/memdb")

	cfg := Load()
	if !cfg.DevMode {
		t.Fatalf("expected DevMode=true, got false")
	}
	// Auth check is relaxed, but PostgresURL is set, so server mode passes.
	if err := cfg.Validate(ModeServer); err != nil {
		t.Fatalf("dev mode should allow no-auth startup, got: %v", err)
	}
}

// TestValidate_DevMode_ToolModeNoPG verifies dev mode + tool mode passes even
// without PostgresURL.
func TestValidate_DevMode_ToolModeNoPG(t *testing.T) {
	unsetEnv(t, "AUTH_ENABLED")
	unsetEnv(t, "MASTER_KEY_HASH")
	unsetEnv(t, "MEMDB_POSTGRES_URL")
	setEnv(t, "MEMDB_DEV", "1")

	cfg := Load()
	if err := cfg.Validate(ModeTool); err != nil {
		t.Fatalf("dev mode + tool mode should pass, got: %v", err)
	}
}

// ── PF-1: auth enabled with master key passes ─────────────────────────────────

// TestValidate_AuthEnabled_WithKey_Passes verifies a properly configured
// production deploy passes validation.
func TestValidate_AuthEnabled_WithKey_Passes(t *testing.T) {
	setEnv(t, "AUTH_ENABLED", "true")
	setEnv(t, "MASTER_KEY_HASH", "deadbeef")
	setEnv(t, "MEMDB_POSTGRES_URL", "postgres://localhost/memdb")
	unsetEnv(t, "MEMDB_DEV")

	cfg := Load()
	if err := cfg.Validate(ModeServer); err != nil {
		t.Fatalf("auth enabled + key set should pass, got: %v", err)
	}
}

// ── PF-5: PostgresURL required in server mode ─────────────────────────────────

// TestValidate_ServerMode_NoPostgresURL_Fails is the RED test for PF-5: even
// with auth configured, an empty PostgresURL must fail in server mode so the
// DB connection error surfaces at startup, not on the first request.
func TestValidate_ServerMode_NoPostgresURL_Fails(t *testing.T) {
	setEnv(t, "AUTH_ENABLED", "true")
	setEnv(t, "MASTER_KEY_HASH", "deadbeef")
	unsetEnv(t, "MEMDB_DEV")
	unsetEnv(t, "MEMDB_POSTGRES_URL")

	cfg := Load()
	err := cfg.Validate(ModeServer)
	if err == nil {
		t.Fatal("expected error for empty PostgresURL in server mode, got nil")
	}
	if !strings.Contains(err.Error(), "MEMDB_POSTGRES_URL") {
		t.Errorf("error should mention MEMDB_POSTGRES_URL, got: %s", err.Error())
	}
}

// TestValidate_ToolMode_NoPostgresURL_OK verifies tool mode does NOT require
// PostgresURL — cmd/mcp-server and other tools that don't open a DB connection
// must still start.
func TestValidate_ToolMode_NoPostgresURL_OK(t *testing.T) {
	setEnv(t, "AUTH_ENABLED", "true")
	setEnv(t, "MASTER_KEY_HASH", "deadbeef")
	unsetEnv(t, "MEMDB_DEV")
	unsetEnv(t, "MEMDB_POSTGRES_URL")

	cfg := Load()
	if err := cfg.Validate(ModeTool); err != nil {
		t.Fatalf("tool mode should not require PostgresURL, got: %v", err)
	}
}

// TestValidate_PostgresURLErrorMentionsDSN confirms the PF-5 error message is
// actionable.
func TestValidate_PostgresURLErrorMentionsDSN(t *testing.T) {
	setEnv(t, "AUTH_ENABLED", "true")
	setEnv(t, "MASTER_KEY_HASH", "deadbeef")
	unsetEnv(t, "MEMDB_DEV")
	unsetEnv(t, "MEMDB_POSTGRES_URL")

	cfg := Load()
	err := cfg.Validate(ModeServer)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Postgres") && !strings.Contains(err.Error(), "POSTGRES") {
		t.Errorf("error should mention Postgres, got: %s", err.Error())
	}
}

// ── PF-1: DevMode field is read from MEMDB_DEV env ────────────────────────────

// TestLoad_DevMode_DefaultFalse verifies DevMode defaults to false.
func TestLoad_DevMode_DefaultFalse(t *testing.T) {
	unsetEnv(t, "MEMDB_DEV")
	cfg := Load()
	if cfg.DevMode {
		t.Errorf("expected DevMode=false by default, got true")
	}
}

// TestLoad_DevMode_Enabled verifies MEMDB_DEV=1 sets DevMode=true.
func TestLoad_DevMode_Enabled(t *testing.T) {
	setEnv(t, "MEMDB_DEV", "1")
	cfg := Load()
	if !cfg.DevMode {
		t.Errorf("expected DevMode=true when MEMDB_DEV=1, got false")
	}
}

// ── Observability: auth-disabled gauge ────────────────────────────────────────

// TestValidate_AuthDisabledGaugeSet verifies the memdb_config_auth_disabled
// gauge is set to 1 when auth is off and 0 when auth is on. This is the
// observability signal for the config_drift alert.
func TestValidate_AuthDisabledGaugeSet(t *testing.T) {
	// Auth off → gauge should read 1.
	unsetEnv(t, "AUTH_ENABLED")
	unsetEnv(t, "MASTER_KEY_HASH")
	setEnv(t, "MEMDB_DEV", "1") // dev mode so Validate doesn't error
	unsetEnv(t, "MEMDB_POSTGRES_URL")

	cfg := Load()
	_ = cfg.Validate(ModeTool)

	if v := authDisabledGaugeVal(); v != 1 {
		t.Errorf("auth off: expected gauge=1, got %v", v)
	}

	// Auth on → gauge should read 0.
	setEnv(t, "AUTH_ENABLED", "true")
	setEnv(t, "MASTER_KEY_HASH", "deadbeef")

	cfg = Load()
	_ = cfg.Validate(ModeTool)

	if v := authDisabledGaugeVal(); v != 0 {
		t.Errorf("auth on: expected gauge=0, got %v", v)
	}
}

// authDisabledGaugeVal reads the current float64 value of the
// memdb_config_auth_disabled gauge via the prometheus.Metric.Write method.
func authDisabledGaugeVal() float64 {
	var m dto.Metric
	if err := authDisabledGauge.Write(&m); err != nil {
		panic(fmt.Sprintf("gauge write: %v", err))
	}
	if m.Gauge == nil || m.Gauge.Value == nil {
		panic("gauge value is nil")
	}
	return m.Gauge.GetValue()
}
