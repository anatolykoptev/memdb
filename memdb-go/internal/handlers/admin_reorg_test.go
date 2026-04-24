package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

// mockReorg implements reorgRunner for tests.
// It records which method was called and with what arguments, and unblocks via done channel.
type mockReorg struct {
	mu        sync.Mutex
	runCalls  []string // cube IDs passed to Run
	targeted  []targetedCall
	treeCalls []string // cube IDs passed to RunTreeReorgForCube
	done      chan struct{}
}

type targetedCall struct {
	cubeID string
	ids    []string
}

func newMockReorg() *mockReorg {
	return &mockReorg{done: make(chan struct{}, 1)}
}

func (m *mockReorg) Run(_ context.Context, cubeID string) {
	m.mu.Lock()
	m.runCalls = append(m.runCalls, cubeID)
	m.mu.Unlock()
	select {
	case m.done <- struct{}{}:
	default:
	}
}

func (m *mockReorg) RunTargeted(_ context.Context, cubeID string, ids []string) {
	m.mu.Lock()
	m.targeted = append(m.targeted, targetedCall{cubeID, ids})
	m.mu.Unlock()
	select {
	case m.done <- struct{}{}:
	default:
	}
}

// RunTreeReorgForCube records the cube id for the D3 tree reorg dispatch.
// Does not signal done so the existing tests (which only wait for Run /
// RunTargeted) remain deterministic.
func (m *mockReorg) RunTreeReorgForCube(_ context.Context, cubeID string) {
	m.mu.Lock()
	m.treeCalls = append(m.treeCalls, cubeID)
	m.mu.Unlock()
}

func (m *mockReorg) wait() { <-m.done }

func newTestHandler(reorg reorgRunner) *Handler {
	return &Handler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		reorg:  reorg,
	}
}

func TestAdminReorg(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		body          any
		reorg         reorgRunner
		wantStatus    int
		wantMode      string
		wantCubeID    string
		wantRunCalled bool
		wantTargetIDs []string
	}{
		{
			name:          "full reorg",
			method:        http.MethodPost,
			body:          map[string]any{"cube_id": "memos"},
			reorg:         newMockReorg(),
			wantStatus:    http.StatusAccepted,
			wantMode:      "full",
			wantCubeID:    "memos",
			wantRunCalled: true,
		},
		{
			name:          "targeted reorg",
			method:        http.MethodPost,
			body:          map[string]any{"cube_id": "hully", "ids": []string{"a", "b"}},
			reorg:         newMockReorg(),
			wantStatus:    http.StatusAccepted,
			wantMode:      "targeted",
			wantCubeID:    "hully",
			wantTargetIDs: []string{"a", "b"},
		},
		{
			name:       "missing cube_id",
			method:     http.MethodPost,
			body:       map[string]any{},
			reorg:      newMockReorg(),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty cube_id string",
			method:     http.MethodPost,
			body:       map[string]any{"cube_id": ""},
			reorg:      newMockReorg(),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "GET returns 405",
			method:     http.MethodGet,
			body:       nil,
			reorg:      newMockReorg(),
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "nil reorganizer returns 503",
			method:     http.MethodPost,
			body:       map[string]any{"cube_id": "memos"},
			reorg:      nil,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.reorg)

			var buf bytes.Buffer
			if tt.body != nil {
				if err := json.NewEncoder(&buf).Encode(tt.body); err != nil {
					t.Fatalf("encode body: %v", err)
				}
			}

			req := httptest.NewRequest(tt.method, "/product/admin/reorg", &buf)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.AdminReorg(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tt.wantStatus, w.Body.String())
			}

			if tt.wantStatus != http.StatusAccepted {
				return
			}

			// Parse response
			var resp reorgResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Status != "accepted" {
				t.Errorf("status = %q, want accepted", resp.Status)
			}
			if resp.CubeID != tt.wantCubeID {
				t.Errorf("cube_id = %q, want %q", resp.CubeID, tt.wantCubeID)
			}
			if resp.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", resp.Mode, tt.wantMode)
			}
			if resp.TriggeredAt == "" {
				t.Error("triggered_at must not be empty")
			}

			// Wait for goroutine to complete
			mock := tt.reorg.(*mockReorg)
			mock.wait()

			mock.mu.Lock()
			defer mock.mu.Unlock()

			if tt.wantRunCalled {
				if len(mock.runCalls) == 0 || mock.runCalls[0] != tt.wantCubeID {
					t.Errorf("Run not called with cube_id %q; runCalls=%v", tt.wantCubeID, mock.runCalls)
				}
			}
			if tt.wantTargetIDs != nil {
				if len(mock.targeted) == 0 {
					t.Fatal("RunTargeted not called")
				}
				call := mock.targeted[0]
				if call.cubeID != tt.wantCubeID {
					t.Errorf("RunTargeted cube_id = %q, want %q", call.cubeID, tt.wantCubeID)
				}
				if len(call.ids) != len(tt.wantTargetIDs) {
					t.Errorf("RunTargeted ids = %v, want %v", call.ids, tt.wantTargetIDs)
				}
			}
		})
	}
}

// TestAdminReorg_TreeHierarchyGate asserts that AdminReorg dispatches
// RunTreeReorgForCube iff the hierarchy flag is enabled. Covers the gate
// added for M5 so admin-triggered reorg produces D3 edges on small cubes.
func TestAdminReorg_TreeHierarchyGate(t *testing.T) {
	cases := []struct {
		name         string
		flagEnabled  bool
		wantTreeCall bool
	}{
		{"flag off — tree reorg skipped", false, false},
		{"flag on — tree reorg dispatched", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Swap the package-level flag stub for this subtest only.
			orig := treeHierarchyEnabledFn
			treeHierarchyEnabledFn = func() bool { return tc.flagEnabled }
			defer func() { treeHierarchyEnabledFn = orig }()

			mock := newMockReorg()
			h := newTestHandler(mock)

			var buf bytes.Buffer
			if err := json.NewEncoder(&buf).Encode(map[string]any{"cube_id": "memos"}); err != nil {
				t.Fatalf("encode body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/product/admin/reorg", &buf)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.AdminReorg(w, req)
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", w.Code)
			}
			// Wait for Run to complete — RunTreeReorgForCube fires right after
			// on the same goroutine, so by the time we observe done the tree
			// call (if any) has also executed.
			mock.wait()

			mock.mu.Lock()
			defer mock.mu.Unlock()
			gotTreeCall := len(mock.treeCalls) > 0
			if gotTreeCall != tc.wantTreeCall {
				t.Fatalf("tree reorg call = %v, want %v (treeCalls=%v)", gotTreeCall, tc.wantTreeCall, mock.treeCalls)
			}
			if tc.wantTreeCall && mock.treeCalls[0] != "memos" {
				t.Errorf("tree reorg cube_id = %q, want memos", mock.treeCalls[0])
			}
		})
	}
}
