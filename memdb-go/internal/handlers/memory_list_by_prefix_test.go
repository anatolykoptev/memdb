package handlers

// memory_list_by_prefix_test.go — validation-layer tests for
// NativeListMemoriesByPrefix. Live-Postgres coverage lives in
// memory_list_by_prefix_livepg_test.go (build tag livepg).

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
)

func TestNativeListMemoriesByPrefix_PostgresUnavailable(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := postJSON(t, h, h.NativeListMemoriesByPrefix, map[string]any{
		"cube_id": "c", "user_id": "u", "prefix": "/memories/",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidateListByPrefix(t *testing.T) {
	mk := func(cube, user, prefix string, limit, offset *int) *listMemoriesByPrefixRequest {
		var c, u, p *string
		if cube != "" {
			c = strPtr(cube)
		}
		if user != "" {
			u = strPtr(user)
		}
		if prefix != "" {
			p = strPtr(prefix)
		}
		return &listMemoriesByPrefixRequest{CubeID: c, UserID: u, Prefix: p, Limit: limit, Offset: offset}
	}
	intPtr := func(i int) *int { return &i }

	cases := []struct {
		name          string
		req           *listMemoriesByPrefixRequest
		wantErr       bool
		wantLimit     int
		wantOffsetMin int
	}{
		{"happy_defaults", mk("c", "u", "/m/", nil, nil), false, 100, 0},
		{"missing_cube", mk("", "u", "/m/", nil, nil), true, 100, 0},
		{"missing_user", mk("c", "", "/m/", nil, nil), true, 100, 0},
		{"missing_prefix", mk("c", "u", "", nil, nil), true, 100, 0},
		{"limit_zero", mk("c", "u", "/m/", intPtr(0), nil), true, 100, 0},
		{"limit_too_big", mk("c", "u", "/m/", intPtr(1001), nil), true, 100, 0},
		{"limit_max_ok", mk("c", "u", "/m/", intPtr(1000), nil), false, 1000, 0},
		{"negative_offset", mk("c", "u", "/m/", nil, intPtr(-1)), true, 100, 0},
		{"limit_offset_ok", mk("c", "u", "/m/", intPtr(50), intPtr(20)), false, 50, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, offset, errs := validateListByPrefix(tc.req)
			gotErr := len(errs) > 0
			if gotErr != tc.wantErr {
				t.Fatalf("want err=%v, got %v (errs=%v)", tc.wantErr, gotErr, errs)
			}
			if !tc.wantErr {
				if limit != tc.wantLimit {
					t.Errorf("limit: want %d got %d", tc.wantLimit, limit)
				}
				if offset != tc.wantOffsetMin {
					t.Errorf("offset: want %d got %d", tc.wantOffsetMin, offset)
				}
			}
		})
	}
}
