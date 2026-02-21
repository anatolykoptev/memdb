package handlers

import "testing"

// ---- streamBase tests -------------------------------------------------------

func TestStreamBase_WithLabel(t *testing.T) {
	key := "scheduler:messages:stream:v2.0:user123:cube456:mem_update"
	got := streamBase(key)
	want := "scheduler:messages:stream:v2.0:user123:cube456"
	if got != want {
		t.Errorf("streamBase(%q) = %q, want %q", key, got, want)
	}
}

func TestStreamBase_NoLabel(t *testing.T) {
	key := "scheduler:messages:stream:v2.0:user123:cube456"
	got := streamBase(key)
	// No colon after cube — base = full key
	if got == "" {
		t.Errorf("streamBase(%q) = empty, want non-empty", key)
	}
}

func TestStreamBase_UnknownPrefix(t *testing.T) {
	key := "some:other:key:with:colons"
	got := streamBase(key)
	// Should return key unchanged (no known prefix)
	if got != key {
		t.Errorf("streamBase(%q) = %q, want original key", key, got)
	}
}

func TestStreamBase_DifferentLabels(t *testing.T) {
	labels := []string{"mem_organize", "mem_read", "pref_add", "mem_feedback", "add"}
	for _, label := range labels {
		key := "scheduler:messages:stream:v2.0:u:cube:" + label
		got := streamBase(key)
		want := "scheduler:messages:stream:v2.0:u:cube"
		if got != want {
			t.Errorf("label %q: streamBase = %q, want %q", label, got, want)
		}
	}
}
