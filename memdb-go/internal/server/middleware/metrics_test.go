package middleware

import "testing"

// TestRouteLabel verifies short label extraction from route keys.
func TestRouteLabel(t *testing.T) {
	cases := []struct {
		routeKey string
		want     string
	}{
		{"POST /product/search", "search"},
		{"GET /product/scheduler/allstatus", "allstatus"},
		{"GET /product/scheduler/task_queue_status", "task_queue_status"},
		{"GET /get_memory", "get_memory"},
		{"POST /add_fine", "add_fine"},
		{"no-slash", "no-slash"},
	}
	for _, tc := range cases {
		got := routeLabel(tc.routeKey)
		if got != tc.want {
			t.Errorf("routeLabel(%q) = %q; want %q", tc.routeKey, got, tc.want)
		}
	}
}
