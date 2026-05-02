package handlers

import "testing"

// --- chatResolveDedup (Bug #3: was hardcoded "no") ---

func TestChatResolveDedup_NilMode(t *testing.T) {
	if got := chatResolveDedup(nil); got != "no" {
		t.Errorf("chatResolveDedup(nil) = %q, want 'no'", got)
	}
}

func TestChatResolveDedup_EmptyMode(t *testing.T) {
	empty := ""
	if got := chatResolveDedup(&empty); got != "no" {
		t.Errorf("chatResolveDedup('') = %q, want 'no'", got)
	}
}

func TestChatResolveDedup_DefaultProfile(t *testing.T) {
	mode := "default"
	if got := chatResolveDedup(&mode); got != "no" {
		t.Errorf("chatResolveDedup('default') = %q, want 'no'", got)
	}
}

func TestChatResolveDedup_InjectProfile(t *testing.T) {
	mode := "inject"
	if got := chatResolveDedup(&mode); got != "mmr" {
		t.Errorf("chatResolveDedup('inject') = %q, want 'mmr'", got)
	}
}

func TestChatResolveDedup_DeepProfile(t *testing.T) {
	mode := "deep"
	if got := chatResolveDedup(&mode); got != "mmr" {
		t.Errorf("chatResolveDedup('deep') = %q, want 'mmr'", got)
	}
}

func TestChatResolveDedup_UnknownProfile(t *testing.T) {
	mode := "nonexistent_profile"
	if got := chatResolveDedup(&mode); got != "no" {
		t.Errorf("chatResolveDedup('nonexistent') = %q, want 'no'", got)
	}
}

// --- resolveCubeIDs (Bug #2: multi-cube support) ---

func TestResolveCubeIDs_ReadableCubes(t *testing.T) {
	cubes := resolveCubeIDs([]string{"cube-a", "cube-b"}, strPtr("mem"), strPtr("user"))
	if len(cubes) != 2 || cubes[0] != "cube-a" || cubes[1] != "cube-b" {
		t.Errorf("resolveCubeIDs with readable = %v, want [cube-a, cube-b]", cubes)
	}
}

func TestResolveCubeIDs_MemCubeIDFallback(t *testing.T) {
	cubes := resolveCubeIDs(nil, strPtr("mem-cube"), strPtr("user"))
	if len(cubes) != 1 || cubes[0] != "mem-cube" {
		t.Errorf("resolveCubeIDs with memCubeID = %v, want [mem-cube]", cubes)
	}
}

func TestResolveCubeIDs_UserIDFallback(t *testing.T) {
	cubes := resolveCubeIDs(nil, nil, strPtr("user-123"))
	if len(cubes) != 1 || cubes[0] != "user-123" {
		t.Errorf("resolveCubeIDs fallback = %v, want [user-123]", cubes)
	}
}

func TestResolveCubeIDs_EmptyMemCubeID(t *testing.T) {
	empty := ""
	cubes := resolveCubeIDs(nil, &empty, strPtr("user-456"))
	if len(cubes) != 1 || cubes[0] != "user-456" {
		t.Errorf("resolveCubeIDs empty memCubeID = %v, want [user-456]", cubes)
	}
}

func TestResolveCubeIDs_EmptyReadableSlice(t *testing.T) {
	cubes := resolveCubeIDs([]string{}, strPtr("mem"), strPtr("user"))
	if len(cubes) != 1 || cubes[0] != "mem" {
		t.Errorf("resolveCubeIDs empty readable = %v, want [mem]", cubes)
	}
}

// --- filterOuterMemory (Bug #1: conditional filtering) ---

func TestFilterOuterMemory_KeepsPersonal(t *testing.T) {
	memories := []map[string]any{
		{"memory": "fact", "metadata": map[string]any{"memory_type": "PersonalMemory"}},
	}
	result := filterOuterMemory(memories)
	if len(result) != 1 {
		t.Errorf("expected 1 memory, got %d", len(result))
	}
}

func TestFilterOuterMemory_RemovesOuter(t *testing.T) {
	memories := []map[string]any{
		{"memory": "internet fact", "metadata": map[string]any{"memory_type": "OuterMemory"}},
	}
	result := filterOuterMemory(memories)
	if len(result) != 0 {
		t.Errorf("expected 0 memories after filter, got %d", len(result))
	}
}

func TestFilterOuterMemory_MixedTypes(t *testing.T) {
	memories := []map[string]any{
		{"memory": "a", "metadata": map[string]any{"memory_type": "PersonalMemory"}},
		{"memory": "b", "metadata": map[string]any{"memory_type": "OuterMemory"}},
		{"memory": "c", "metadata": map[string]any{"memory_type": "PersonalMemory"}},
		{"memory": "d", "metadata": map[string]any{"memory_type": "OuterMemory"}},
	}
	result := filterOuterMemory(memories)
	if len(result) != 2 {
		t.Errorf("expected 2 personal memories, got %d", len(result))
	}
}

// ---- M15 — chat magic-number env exposure ---------------------------------

func TestM15_ChatMinPersonalMem_Default(t *testing.T) {
	if got := chatMinPersonalMem(); got != defaultChatMinPersonalMem {
		t.Errorf("default: got %d want %d", got, defaultChatMinPersonalMem)
	}
}

func TestM15_ChatMinPersonalMem_Override(t *testing.T) {
	t.Setenv("MEMDB_M15_CHAT_MIN_PERSONAL_MEM", "7")
	if got := chatMinPersonalMem(); got != 7 {
		t.Errorf("override: got %d want 7", got)
	}
}

func TestM15_ChatMinPersonalMem_Invalid(t *testing.T) {
	for _, v := range []string{"-1", "51", "abc"} {
		t.Setenv("MEMDB_M15_CHAT_MIN_PERSONAL_MEM", v)
		if got := chatMinPersonalMem(); got != defaultChatMinPersonalMem {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultChatMinPersonalMem)
		}
	}
}

func TestM15_ChatDefaultThreshold_Default(t *testing.T) {
	got := chatDefaultThreshold()
	if got < defaultChatThreshold-1e-9 || got > defaultChatThreshold+1e-9 {
		t.Errorf("default: got %v want %v", got, defaultChatThreshold)
	}
}

func TestM15_ChatDefaultThreshold_Override(t *testing.T) {
	t.Setenv("MEMDB_M15_CHAT_DEFAULT_THRESHOLD", "0.5")
	got := chatDefaultThreshold()
	if got < 0.5-1e-9 || got > 0.5+1e-9 {
		t.Errorf("override: got %v want 0.5", got)
	}
}

func TestM15_ChatDefaultThreshold_Invalid(t *testing.T) {
	for _, v := range []string{"-0.1", "1.5", "abc"} {
		t.Setenv("MEMDB_M15_CHAT_DEFAULT_THRESHOLD", v)
		got := chatDefaultThreshold()
		if got < defaultChatThreshold-1e-9 || got > defaultChatThreshold+1e-9 {
			t.Errorf("invalid %q: got %v want %v", v, got, defaultChatThreshold)
		}
	}
}
