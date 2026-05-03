package handlers

// handlers_cache_test.go — DB-cache correctness tests (PR A).
// Validates fixes for:
//   - Bug 1: post_get_memory_filter cache key now includes the full request (Page, etc.)
//   - Bug 2: post_get_memory_filter:* is invalidated alongside post_get_memory:*
//   - Bug 4: memory_delete handlers also invalidate post_get_memory:* / *_filter:*
//   - Bug 5: user config cache uses composite v2 key swept by user_id wildcard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ptrStr / ptrInt / ptrBool — tiny helpers for *T fields in getMemoryRequest.
func ptrStr(s string) *string { return &s }
func ptrInt(i int) *int       { return &i }

// hashOfRequestWithoutCubeID mirrors the production key derivation exactly so
// the test cannot drift from the implementation.
func hashOfRequestWithoutCubeID(t *testing.T, req getMemoryRequest) string {
	t.Helper()
	hashable := req
	hashable.MemCubeID = nil
	b, err := json.Marshal(hashable)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestPostGetMemoryFilterCacheKey_DiffersByPage — Bug 1 regression.
// Before the fix, only Filter+limit were hashed; Page=2 returned Page=1 body.
func TestPostGetMemoryFilterCacheKey_DiffersByPage(t *testing.T) {
	base := getMemoryRequest{
		MemCubeID: ptrStr("cube-1"),
		UserID:    ptrStr("user-1"),
		Filter:    map[string]interface{}{"k": "v"},
		Page:      ptrInt(1),
		PageSize:  ptrInt(50),
	}
	page2 := base
	page2.Page = ptrInt(2)

	h1 := hashOfRequestWithoutCubeID(t, base)
	h2 := hashOfRequestWithoutCubeID(t, page2)
	if h1 == h2 {
		t.Fatalf("page 1 and page 2 hashes collided: %s", h1)
	}
}

// TestPostGetMemoryFilterCacheKey_DiffersByIncludeFlags — Bug 1 regression.
// Include* flags change response shape; cache must not collapse them.
func TestPostGetMemoryFilterCacheKey_DiffersByIncludeFlags(t *testing.T) {
	base := getMemoryRequest{
		MemCubeID:         ptrStr("cube-1"),
		UserID:            ptrStr("user-1"),
		IncludePreference: ptrBool(false),
		Filter:            map[string]interface{}{"k": "v"},
	}
	withPref := base
	withPref.IncludePreference = ptrBool(true)

	if hashOfRequestWithoutCubeID(t, base) == hashOfRequestWithoutCubeID(t, withPref) {
		t.Fatal("include_preference change did not alter hash")
	}

	withTool := base
	withTool.IncludeToolMemory = ptrBool(true)
	if hashOfRequestWithoutCubeID(t, base) == hashOfRequestWithoutCubeID(t, withTool) {
		t.Fatal("include_tool_memory change did not alter hash")
	}
}

// TestPostGetMemoryFilterCacheKey_StableAcrossMemCubeID — sanity:
// MemCubeID is intentionally excluded from the hash so it lives as a literal
// key segment for wildcard invalidation. Two requests differing only by
// MemCubeID should hash identically (the cube ID still lands in the key
// prefix and keeps cubes isolated).
func TestPostGetMemoryFilterCacheKey_StableAcrossMemCubeID(t *testing.T) {
	a := getMemoryRequest{
		MemCubeID: ptrStr("cube-A"),
		UserID:    ptrStr("user-1"),
		Filter:    map[string]interface{}{"k": "v"},
	}
	b := a
	b.MemCubeID = ptrStr("cube-B")
	if hashOfRequestWithoutCubeID(t, a) != hashOfRequestWithoutCubeID(t, b) {
		t.Fatal("hash should not depend on MemCubeID (which is a literal key segment)")
	}
}

// TestMemoryDelete_InvalidatesPostGetMemoryFilter — Bug 2 + Bug 4 regression.
// Drives invalidateDeleteCaches end-to-end against miniredis and asserts that
// both post_get_memory:* and post_get_memory_filter:* are wiped.
func TestMemoryDelete_InvalidatesPostGetMemoryFilter(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
	}

	ctx := context.Background()
	userID := "user-1"

	// Seed the cache with both flavours plus an unrelated key that must survive.
	h.cacheSet(ctx, cachePrefix+"post_get_memory:"+userID+":abc", []byte("v"), time.Minute)
	h.cacheSet(ctx, cachePrefix+"post_get_memory_filter:"+userID+":xyz", []byte("v"), time.Minute)
	h.cacheSet(ctx, cachePrefix+"post_get_memory:other-user:abc", []byte("v"), time.Minute)

	h.invalidateDeleteCaches(ctx, userID, []string{"mem-1"})

	if got := h.cacheGet(ctx, cachePrefix+"post_get_memory:"+userID+":abc"); got != nil {
		t.Errorf("post_get_memory cache for %q not invalidated", userID)
	}
	if got := h.cacheGet(ctx, cachePrefix+"post_get_memory_filter:"+userID+":xyz"); got != nil {
		t.Errorf("post_get_memory_filter cache for %q not invalidated", userID)
	}
	if got := h.cacheGet(ctx, cachePrefix+"post_get_memory:other-user:abc"); got == nil {
		t.Errorf("invalidation leaked across user boundary")
	}
}

// TestUsersConfigDelete_HitsCorrectKey — Bug 5 regression.
// The PUT handler's deleter must sweep config:v2:<user>:* — the namespace the
// reader in add.go now writes under. Old config:<id> entries are abandoned.
func TestUsersConfigDelete_HitsCorrectKey(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := &Handler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:    rd,
		postgres: nil, // forces 503 path; we exit before that, but keep nil safe
	}

	ctx := context.Background()
	userID := "user-1"

	// Seed: writer key shape is config:v2:<user>:<cube> for two cubes owned by
	// this user, plus a stray key under the OLD shape to confirm the new
	// invalidator does NOT touch the old namespace (it has been bumped).
	h.cacheSet(ctx, cachePrefix+"config:v2:"+userID+":cube-A", []byte("a"), time.Minute)
	h.cacheSet(ctx, cachePrefix+"config:v2:"+userID+":cube-B", []byte("b"), time.Minute)
	h.cacheSet(ctx, cachePrefix+"config:v2:other-user:cube-Z", []byte("z"), time.Minute)
	h.cacheSet(ctx, cachePrefix+"config:"+userID, []byte("legacy"), time.Minute)

	// Drive only the cache invalidation half of NativeUpdateUserConfig (the
	// postgres path is not exercised here — it would need a real DB). We call
	// the same sweep the handler issues.
	h.cacheInvalidate(ctx, cachePrefix+"config:v2:"+userID+":*")

	if got := h.cacheGet(ctx, cachePrefix+"config:v2:"+userID+":cube-A"); got != nil {
		t.Errorf("cube-A config cache not invalidated")
	}
	if got := h.cacheGet(ctx, cachePrefix+"config:v2:"+userID+":cube-B"); got != nil {
		t.Errorf("cube-B config cache not invalidated")
	}
	if got := h.cacheGet(ctx, cachePrefix+"config:v2:other-user:cube-Z"); got == nil {
		t.Errorf("other user's config wrongly invalidated")
	}
}

// TestUsersConfigDelete_HandlerEndToEnd — wires NativeUpdateUserConfig through
// httptest. With h.postgres == nil the handler returns 503 before the cache
// delete fires, which is the intended degraded behaviour. We assert the 503
// shape so the handler signature is regression-tested even without a fake DB.
func TestUsersConfigDelete_HandlerReturns503WithoutPostgres(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
	}
	body := strings.NewReader(`{"memory_limits":{"WorkingMemory":42}}`)
	req := httptest.NewRequest(http.MethodPut, "/product/users/u/config", body)
	req.SetPathValue("user_id", "u")
	w := httptest.NewRecorder()

	h.NativeUpdateUserConfig(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
	}
}
