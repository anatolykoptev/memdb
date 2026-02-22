package handlers

import (
	"encoding/json"
	"testing"
)

func TestNormalizeSearch_MemCubeID(t *testing.T) {
	body := []byte(`{"query":"test","user_id":"memos","mem_cube_id":"cube1"}`)
	result := normalizeSearch(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	if _, ok := m["mem_cube_id"]; ok {
		t.Error("mem_cube_id should be removed")
	}
	ids, ok := m["readable_cube_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "cube1" {
		t.Errorf("expected readable_cube_ids=[cube1], got %v", m["readable_cube_ids"])
	}
}

func TestNormalizeSearch_NoChange(t *testing.T) {
	body := []byte(`{"query":"test","user_id":"memos"}`)
	result := normalizeSearch(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	if _, ok := m["readable_cube_ids"]; ok {
		t.Error("readable_cube_ids should not be added when mem_cube_id is absent")
	}
}

func TestNormalizeSearch_ExistingReadableCubeIDs(t *testing.T) {
	body := []byte(`{"query":"test","mem_cube_id":"old","readable_cube_ids":["new"]}`)
	result := normalizeSearch(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	ids := m["readable_cube_ids"].([]any)
	if len(ids) != 1 || ids[0] != "new" {
		t.Errorf("existing readable_cube_ids should not be overwritten, got %v", ids)
	}
}

func TestNormalizeAdd_MemCubeID(t *testing.T) {
	body := []byte(`{"user_id":"memos","mem_cube_id":"cube1"}`)
	result := normalizeAdd(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	ids, ok := m["writable_cube_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "cube1" {
		t.Errorf("expected writable_cube_ids=[cube1], got %v", m["writable_cube_ids"])
	}
}

func TestNormalizeAdd_MemoryContent(t *testing.T) {
	body := []byte(`{"user_id":"memos","memory_content":"hello world"}`)
	result := normalizeAdd(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	if _, ok := m["memory_content"]; ok {
		t.Error("memory_content should be removed")
	}
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", m["messages"])
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "hello world" {
		t.Errorf("expected {role:user, content:hello world}, got %v", msg)
	}
}

func TestNormalizeAdd_MemoryContentAppend(t *testing.T) {
	body := []byte(`{"user_id":"memos","messages":[{"role":"system","content":"sys"}],"memory_content":"extra"}`)
	result := normalizeAdd(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func TestNormalizeAdd_Source(t *testing.T) {
	body := []byte(`{"user_id":"memos","source":"claude-code"}`)
	result := normalizeAdd(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	if _, ok := m["source"]; ok {
		t.Error("source should be removed")
	}
	info := m["info"].(map[string]any)
	if info["source"] != "claude-code" {
		t.Errorf("expected info.source=claude-code, got %v", info["source"])
	}
}

func TestNormalizeAdd_SourceExistingInfo(t *testing.T) {
	body := []byte(`{"user_id":"memos","source":"new","info":{"existing":"val"}}`)
	result := normalizeAdd(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	info := m["info"].(map[string]any)
	if info["source"] != "new" {
		t.Errorf("expected info.source=new, got %v", info["source"])
	}
	if info["existing"] != "val" {
		t.Error("existing info fields should be preserved")
	}
}

func TestNormalizeChatComplete_MemCubeID(t *testing.T) {
	body := []byte(`{"user_id":"memos","query":"test","mem_cube_id":"cube1"}`)
	result := normalizeChatComplete(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	if _, ok := m["mem_cube_id"]; ok {
		t.Error("mem_cube_id should be removed")
	}
	rids := m["readable_cube_ids"].([]any)
	wids := m["writable_cube_ids"].([]any)
	if len(rids) != 1 || rids[0] != "cube1" {
		t.Errorf("expected readable_cube_ids=[cube1], got %v", rids)
	}
	if len(wids) != 1 || wids[0] != "cube1" {
		t.Errorf("expected writable_cube_ids=[cube1], got %v", wids)
	}
}

func TestNormalizeFeedback_MemCubeID(t *testing.T) {
	body := []byte(`{"user_id":"memos","mem_cube_id":"cube1"}`)
	result := normalizeFeedback(body)

	var m map[string]any
	_ = json.Unmarshal(result, &m)

	ids := m["writable_cube_ids"].([]any)
	if len(ids) != 1 || ids[0] != "cube1" {
		t.Errorf("expected writable_cube_ids=[cube1], got %v", ids)
	}
}

func TestNormalize_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	// All normalize functions should return original body on invalid JSON
	if string(normalizeSearch(body)) != string(body) {
		t.Error("normalizeSearch should return original body on invalid JSON")
	}
	if string(normalizeAdd(body)) != string(body) {
		t.Error("normalizeAdd should return original body on invalid JSON")
	}
	if string(normalizeChatComplete(body)) != string(body) {
		t.Error("normalizeChatComplete should return original body on invalid JSON")
	}
	if string(normalizeFeedback(body)) != string(body) {
		t.Error("normalizeFeedback should return original body on invalid JSON")
	}
}
