package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeOpenAPISpec verifies that /openapi.json returns valid JSON with correct content-type.
func TestServeOpenAPISpec(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	serveOpenAPISpec(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: want 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("content-type: want application/json, got %q", ct)
	}
	// Verify it's valid JSON and has openapi field
	var spec map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := spec["openapi"]; !ok {
		t.Error("spec missing 'openapi' field")
	}
	if _, ok := spec["paths"]; !ok {
		t.Error("spec missing 'paths' field")
	}
}

// TestServeOpenAPISpec_CORS verifies CORS header is set.
func TestServeOpenAPISpec_CORS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()

	serveOpenAPISpec(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header: want *, got %q", got)
	}
}

// TestServeSwaggerUI verifies that /docs returns HTML with Swagger UI.
func TestServeSwaggerUI(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	req.Host = "localhost:8080"
	w := httptest.NewRecorder()

	serveSwaggerUI(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: want 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("content-type: want text/html, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "swagger-ui") {
		t.Error("body should contain swagger-ui")
	}
	if !strings.Contains(body, "/openapi.json") {
		t.Error("body should reference /openapi.json")
	}
	if !strings.Contains(body, "SwaggerUIBundle") {
		t.Error("body should contain SwaggerUIBundle script")
	}
}

// TestServeSwaggerUI_SpecURL verifies that the spec URL uses the request host.
func TestServeSwaggerUI_SpecURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	req.Host = "api.example.com"
	w := httptest.NewRecorder()

	serveSwaggerUI(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "api.example.com") {
		t.Error("spec URL should use request host api.example.com")
	}
}

// TestRegisterOpenAPIRoutes verifies that /openapi.json and /docs are registered.
func TestRegisterOpenAPIRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerOpenAPIRoutes(mux)

	cases := []struct {
		method string
		path   string
		wantOK bool
	}{
		{"GET", "/openapi.json", true},
		{"GET", "/docs", true},
		{"GET", "/docs/", true},
		{"POST", "/openapi.json", false}, // only GET registered
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Host = "localhost"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		got200 := w.Code == http.StatusOK
		if tc.wantOK && !got200 {
			t.Errorf("%s %s: want 200, got %d", tc.method, tc.path, w.Code)
		}
	}
}

// TestOpenAPISpec_ContainsKeyEndpoints verifies the embedded spec has our key endpoints.
func TestOpenAPISpec_ContainsKeyEndpoints(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("embedded spec is not valid JSON: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("spec.paths is not an object")
	}

	required := []string{
		"/health",
		"/product/add",
		"/product/search",
		"/product/get_all",
		"/v1/embeddings",
		"/product/users",
	}
	for _, p := range required {
		if _, exists := paths[p]; !exists {
			t.Errorf("spec missing path %q", p)
		}
	}
}
