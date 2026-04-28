package featureflags

import "testing"

func TestF3EventsEnabled_DefaultOn(t *testing.T) {
	t.Setenv(EnvF3Events, "")
	if !F3EventsEnabled() {
		t.Error("default (empty env) must be ON")
	}
}

func TestF3EventsEnabled_FalseDisables(t *testing.T) {
	for _, v := range []string{"false", "FALSE", "False", "0", "  false  "} {
		t.Setenv(EnvF3Events, v)
		if F3EventsEnabled() {
			t.Errorf("env %q should disable F3", v)
		}
	}
}

func TestF3EventsEnabled_OtherValuesEnable(t *testing.T) {
	for _, v := range []string{"true", "1", "yes", "on", "enabled"} {
		t.Setenv(EnvF3Events, v)
		if !F3EventsEnabled() {
			t.Errorf("env %q should enable F3", v)
		}
	}
}
