//go:build livepg

package scheduler

// tree_relation_phase_livepg_helpers_test.go — fixtures for the live-Postgres
// D3 relation-phase integration test. Isolated from the _test.go main file to
// keep each file ≤200 lines per repo policy.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// livePGDim matches the schema vector column (vector(1024)) — every embedding
// we write MUST be exactly this dimension or the ::vector(1024) cast blows up.
const livePGDim = 1024

// livepgClusterDim is the count of 1.0 entries per cluster prefix in the raw
// synthetic embeddings. 341+341+341 ≈ 1023, leaving dim 1023 under cluster C
// (dims 683..1023 inclusive = 341). Orthogonal across clusters so cosine = 0.
const livepgClusterDim = 341

// livepgEmbedder is a 1024-dim deterministic embedder used exclusively by the
// live-PG test. Returns a hash-seeded near-unit vector so each tier parent
// summary gets its own direction — that makes the relation phase's nearest
// neighbour selection non-degenerate even with a mock LLM. Dimension MUST
// equal livePGDim so the pgvector cast succeeds on Postgres.
type livepgEmbedder struct{}

func (e *livepgEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = deterministicUnitVec(t, livePGDim)
	}
	return out, nil
}
func (e *livepgEmbedder) EmbedQuery(_ context.Context, t string) ([]float32, error) {
	return deterministicUnitVec(t, livePGDim), nil
}
func (e *livepgEmbedder) Dimension() int { return livePGDim }
func (e *livepgEmbedder) Close() error   { return nil }

// deterministicUnitVec returns a unit-norm vector whose direction is a stable
// function of the input string. Uses djb2-like hashing to seed a trivial PRNG,
// avoiding any external dependency.
func deterministicUnitVec(seed string, dim int) []float32 {
	v := make([]float32, dim)
	h := uint64(5381)
	for i := 0; i < len(seed); i++ {
		h = h*33 ^ uint64(seed[i])
	}
	if h == 0 {
		h = 1
	}
	for i := 0; i < dim; i++ {
		h = h*1103515245 + 12345
		v[i] = float32(int64(h>>16)%2001-1000) / 1000.0
	}
	normaliseUnit(v)
	return v
}

// livepgMockServer is a dispatcher that discriminates between tier-summary and
// relation-detector requests by scanning the outgoing system prompt for a
// stable fragment. Merges the two mock servers (mockLLMServer + relationMockServer)
// the existing _test.go files use separately. Returns:
//   - {"summary":"theme-..."} for tier calls — each cluster gets a unique
//     summary to ensure tier parent embeddings differ.
//   - {"relation":"CAUSES","confidence":0.9,"rationale":"synthetic"} for
//     relation calls — deterministic so assertions can compare exact values.
func livepgMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		dec := json.NewDecoder(r.Body)
		_ = dec.Decode(&req)
		var systemContent, userContent string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				systemContent = m.Content
			case "user":
				userContent = m.Content
			}
		}
		var content string
		switch {
		case strings.Contains(systemContent, "memory relationship classifier"):
			content = `{"relation":"CAUSES","confidence":0.9,"rationale":"synthetic"}`
		default:
			// Tier summariser. Derive a cluster-stable theme from the first
			// 40 chars of the user payload so different clusters get different
			// summaries and therefore different embeddings.
			seed := userContent
			if len(seed) > 40 {
				seed = seed[:40]
			}
			content = fmt.Sprintf(`{"summary":"theme-%x"}`, []byte(seed))
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// livepgRawMemory is a tuple of the synthetic raw memories we need to insert.
type livepgRawMemory struct {
	ID        string
	Text      string
	Embedding []float32
}

// makeLivepgRawMemories builds 3 clusters × 2 memories each. Cluster vectors
// are orthogonal by construction (disjoint index ranges are 1.0), so cosine
// across clusters is 0 and within-cluster is ~1 even after the tiny off-axis
// perturbation on the second member.
func makeLivepgRawMemories() []livepgRawMemory {
	out := make([]livepgRawMemory, 0, 6)
	for _, cl := range []struct {
		label string
		lo    int
		hi    int
	}{
		{"A", 0, livepgClusterDim},
		{"B", livepgClusterDim, 2 * livepgClusterDim},
		{"C", 2 * livepgClusterDim, livePGDim}, // 682..1023 = 342 dims — still orthogonal.
	} {
		for i := 1; i <= 2; i++ {
			vec := make([]float32, livePGDim)
			for d := cl.lo; d < cl.hi; d++ {
				vec[d] = 1.0
			}
			if i == 2 {
				// Perturb an out-of-cluster dim (stays orthogonal between clusters).
				jitterIdx := (cl.lo + livePGDim/2) % livePGDim
				vec[jitterIdx] += 0.01
			}
			normaliseUnit(vec)
			out = append(out, livepgRawMemory{
				ID:        uuid.New().String(),
				Text:      fmt.Sprintf("cluster-%s-memory-%d", cl.label, i),
				Embedding: vec,
			})
		}
	}
	return out
}

// normaliseUnit rescales v to unit L2 norm in place.
func normaliseUnit(v []float32) {
	var sumSq float64
	for _, f := range v {
		sumSq += float64(f) * float64(f)
	}
	n := float32(math.Sqrt(sumSq))
	if n == 0 {
		return
	}
	for i := range v {
		v[i] /= n
	}
}
