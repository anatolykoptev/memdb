package embed

// Config holds all embedder configuration in one typed struct.
// Populated from environment variables by callers.
//
// Type selects the backend:
//
//   - "http"   — OpenAI-compatible /v1/embeddings endpoint (HTTPBaseURL).
//   - "ollama" — Ollama /api/embed (OllamaURL).
//   - "voyage" — Voyage AI hosted /v1/embeddings (VoyageAPIKey).
//   - "onnx"   — local ONNX Runtime; requires the embed/onnx subpackage
//     factory because it depends on cgo.
//
// Fields not relevant to the chosen Type are ignored.
type Config struct {
	Type         string // "http" | "ollama" | "voyage" | "onnx"
	ONNXModelDir string
	VoyageAPIKey string
	Model        string // voyage, ollama, or http model name
	OllamaURL    string
	OllamaDim    int    // 0 = auto-detect from first response
	OllamaPrefix string // client-side document prefix (e.g. "passage: ")
	OllamaQuery  string // client-side query prefix (e.g. "query: ")
	HTTPBaseURL  string // for type="http" — URL of embed-server sidecar
	HTTPDim      int    // dimension override (default 1024)
}
