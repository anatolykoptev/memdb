package config

import (
	"reflect"
	"testing"
)

// memdb-go receives the fleet's config/llm.env (LLM_MODEL, LLM_MODEL_FALLBACK).
// With no MEMDB_* model override, every role must follow it, so a fleet-wide
// model refresh reaches memdb without touching memdb's own config (#410).
func TestLoad_ModelsFollowFleetLLMEnv(t *testing.T) {
	for _, k := range []string{"MEMDB_LLM_MODEL", "MEMDB_LLM_SEARCH_MODEL", "MEMDB_LLM_EXTRACT_MODEL", "MEMDB_REORG_LLM_MODEL", "MEMDB_LLM_FALLBACK_MODELS"} {
		t.Setenv(k, "")
	}
	t.Setenv("LLM_MODEL", "fleet-primary")
	t.Setenv("LLM_MODEL_FALLBACK", "fleet-a, fleet-b")

	c := Load()
	for name, got := range map[string]string{
		"LLMDefaultModel": c.LLMDefaultModel, "LLMSearchModel": c.LLMSearchModel,
		"LLMExtractModel": c.LLMExtractModel, "LLMReorgModel": c.LLMReorgModel,
	} {
		if got != "fleet-primary" {
			t.Errorf("%s = %q, want the fleet LLM_MODEL", name, got)
		}
	}
	if want := []string{"fleet-a", "fleet-b"}; !reflect.DeepEqual(c.LLMFallbackModels, want) {
		t.Errorf("LLMFallbackModels = %v, want %v", c.LLMFallbackModels, want)
	}
}

// An explicit MEMDB_* setting still wins over the fleet file, per role.
func TestLoad_MemdbOverridesWinOverFleet(t *testing.T) {
	t.Setenv("LLM_MODEL", "fleet-primary")
	t.Setenv("LLM_MODEL_FALLBACK", "fleet-a")
	t.Setenv("MEMDB_LLM_MODEL", "")
	t.Setenv("MEMDB_REORG_LLM_MODEL", "memdb-reorg")
	t.Setenv("MEMDB_LLM_FALLBACK_MODELS", "memdb-fb")

	c := Load()
	if c.LLMReorgModel != "memdb-reorg" {
		t.Errorf("LLMReorgModel = %q, want the MEMDB_ override", c.LLMReorgModel)
	}
	if c.LLMSearchModel != "fleet-primary" {
		t.Errorf("LLMSearchModel = %q, want the fleet model when its MEMDB_ var is unset", c.LLMSearchModel)
	}
	if want := []string{"memdb-fb"}; !reflect.DeepEqual(c.LLMFallbackModels, want) {
		t.Errorf("LLMFallbackModels = %v, want %v", c.LLMFallbackModels, want)
	}
}
