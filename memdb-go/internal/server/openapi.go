package server

// openapi.go — serves OpenAPI 3.1 spec and Swagger UI.
//
// Endpoints registered in server.go:
//   GET /openapi.json  — raw OpenAPI 3.1 spec (api/openapi_go.json embedded at build time)
//   GET /docs          — Swagger UI (CDN, no extra dependencies)
//   GET /docs/         — redirect to /docs
//
// The spec is embedded via go:embed so the binary is self-contained.
// Swagger UI is served via unpkg CDN — no npm, no build step.

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed static/openapi.json
var openapiSpec []byte

// registerOpenAPIRoutes adds /openapi.json and /docs to mux.
func registerOpenAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.json", serveOpenAPISpec)
	mux.HandleFunc("GET /docs", serveSwaggerUI)
	mux.HandleFunc("GET /docs/", serveSwaggerUI)
}

// serveOpenAPISpec serves the embedded openapi_go.json.
func serveOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiSpec)
}

// serveSwaggerUI serves a self-contained Swagger UI page backed by unpkg CDN.
// No npm, no build step — just a single HTML response.
func serveSwaggerUI(w http.ResponseWriter, r *http.Request) {
	// Redirect /docs/ → /docs
	if strings.HasSuffix(r.URL.Path, "/") && r.URL.Path != "/docs/" {
		http.Redirect(w, r, "/docs", http.StatusMovedPermanently)
		return
	}

	// Derive the spec URL relative to the request host so it works behind any proxy.
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	specURL := scheme + "://" + host + "/openapi.json"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)

	page := swaggerUIPage(specURL)
	_, _ = w.Write([]byte(page))
}

// swaggerUIPage returns a minimal Swagger UI HTML page using unpkg CDN.
// Swagger UI 5.x — latest stable as of Feb 2026.
func swaggerUIPage(specURL string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>MemDB API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; }
    #swagger-ui .topbar { background-color: #1a1a2e; }
    #swagger-ui .topbar .download-url-wrapper { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
  <script>
    window.onload = function() {
      SwaggerUIBundle({
        url: "` + specURL + `",
        dom_id: '#swagger-ui',
        presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
        layout: "StandaloneLayout",
        deepLinking: true,
        displayRequestDuration: true,
        filter: true,
        tryItOutEnabled: true,
        persistAuthorization: true,
        defaultModelsExpandDepth: 1,
        defaultModelExpandDepth: 2,
      });
    };
  </script>
</body>
</html>`
}
