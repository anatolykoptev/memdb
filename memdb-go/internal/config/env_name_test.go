package config

import "testing"

func TestNormalizeEnv(t *testing.T) {
	cases := map[string]string{
		"":           EnvProd,
		"prod":       EnvProd,
		"PROD":       EnvProd,
		"eval":       EnvEval,
		" Eval ":     EnvEval,
		"evaluation": EnvProd, // typo must fail closed
		"staging":    EnvProd,
	}
	for in, want := range cases {
		if got := NormalizeEnv(in); got != want {
			t.Errorf("NormalizeEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoad_EnvDefaultsToProd(t *testing.T) {
	t.Setenv("MEMDB_ENV", "")
	if got := Load().Env; got != EnvProd {
		t.Fatalf("Load().Env with MEMDB_ENV unset = %q, want %q", got, EnvProd)
	}
	t.Setenv("MEMDB_ENV", "eval")
	if got := Load().Env; got != EnvEval {
		t.Fatalf("Load().Env with MEMDB_ENV=eval = %q, want %q", got, EnvEval)
	}
}
