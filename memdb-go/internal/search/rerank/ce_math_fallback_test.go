package rerank

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
)

// fakeRerankServerWithScores returns a server that responds with the
// given scores in input order. counter increments per request.
func fakeRerankServerWithScores(scores []float64, counter *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(counter, 1)
		results := make([]map[string]any, 0, len(scores))
		for i, s := range scores {
			results = append(results, map[string]any{
				"index":           i,
				"relevance_score": s,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[`))
		for i, r := range results {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(`{"index":`))
			_, _ = w.Write([]byte(itoa(r["index"].(int))))
			_, _ = w.Write([]byte(`,"relevance_score":`))
			_, _ = w.Write([]byte(ftoa(r["relevance_score"].(float64))))
			_, _ = w.Write([]byte(`}`))
		}
		_, _ = w.Write([]byte(`]}`))
	}))
}

func itoa(i int) string  { return string(rune('0' + i)) }
func ftoa(f float64) string {
	if f == 0 {
		return "0"
	}
	// Naive serialization sufficient for tests with small floats.
	return float64Str(f)
}

// float64Str — minimal float→string for test fixtures (no exponent).
func float64Str(f float64) string {
	// Scale to 4 decimals and stringify via rune-by-rune.
	// Avoid fmt to keep zero-import overhead.
	neg := f < 0
	if neg {
		f = -f
	}
	intPart := int(f)
	frac := int((f - float64(intPart)) * 10000)
	out := []byte{}
	if neg {
		out = append(out, '-')
	}
	if intPart == 0 {
		out = append(out, '0')
	} else {
		var digits []byte
		for intPart > 0 {
			digits = append([]byte{byte('0' + intPart%10)}, digits...)
			intPart /= 10
		}
		out = append(out, digits...)
	}
	out = append(out, '.')
	for i := 0; i < 4; i++ {
		d := frac / 1000
		out = append(out, byte('0'+d))
		frac = (frac - d*1000) * 10
	}
	return string(out)
}

func TestQualityFloor_DefaultLoweredForSigmoid(t *testing.T) {
	t.Parallel()
	if ceQualityFloorDefault != 0.01 {
		t.Errorf("ceQualityFloorDefault=%v, want 0.01 (post-sigmoid-default flip)", ceQualityFloorDefault)
	}
}

func TestQualityFloor_EnvOverride(t *testing.T) {
	t.Setenv("MEMDB_CE_QUALITY_FLOOR", "0.5")
	if got := ceQualityFloor(); got != 0.5 {
		t.Errorf("got %v, want 0.5", got)
	}
}

func TestSpreadFloor_DisabledByDefault(t *testing.T) {
	t.Parallel()
	if ceSpreadFloorDefault != 1.0 {
		t.Errorf("default %v, want 1.0 (disabled)", ceSpreadFloorDefault)
	}
}

func TestSpreadFloor_EnvParsing(t *testing.T) {
	t.Setenv("MEMDB_CE_SPREAD_FLOOR", "1.5")
	if got := ceSpreadFloor(); got != 1.5 {
		t.Errorf("got %v, want 1.5", got)
	}
	t.Setenv("MEMDB_CE_SPREAD_FLOOR", "0.5") // invalid (< 1.0)
	if got := ceSpreadFloor(); got != ceSpreadFloorDefault {
		t.Errorf("invalid value should fall back to default")
	}
}

func TestBypassCosineThreshold_DisabledByDefault(t *testing.T) {
	t.Parallel()
	if ceBypassCosineThresholdDefault != 0.0 {
		t.Errorf("default %v, want 0 (disabled)", ceBypassCosineThresholdDefault)
	}
}

func TestBypassCosineThreshold_EnvParsing(t *testing.T) {
	t.Setenv("MEMDB_CE_BYPASS_COSINE_THRESHOLD", "0.85")
	if got := ceBypassCosineThreshold(); got != 0.85 {
		t.Errorf("got %v, want 0.85", got)
	}
	t.Setenv("MEMDB_CE_BYPASS_COSINE_THRESHOLD", "1.5") // invalid (>1)
	if got := ceBypassCosineThreshold(); got != ceBypassCosineThresholdDefault {
		t.Errorf("out-of-range should fall back to default")
	}
}

func TestTopCosineScore_PicksMax(t *testing.T) {
	t.Parallel()
	q := []float32{1, 0, 0}
	docs := []rerank.Doc{
		{ID: "1", EmbedVector: []float32{0, 1, 0}},     // cos = 0
		{ID: "2", EmbedVector: []float32{1, 0, 0}},     // cos = 1
		{ID: "3", EmbedVector: []float32{0.5, 0.5, 0}}, // cos ≈ 0.707
	}
	got := topCosineScore(q, docs)
	if got != 1 {
		t.Errorf("got %v, want 1", got)
	}
}

func TestTopCosineScore_EmptyInput(t *testing.T) {
	t.Parallel()
	if topCosineScore(nil, nil) != 0 {
		t.Errorf("nil input should return 0")
	}
	if topCosineScore([]float32{1, 2, 3}, nil) != 0 {
		t.Errorf("empty docs should return 0")
	}
}

func TestPreBypass_SkipsCEWhenCosineHigh(t *testing.T) {
	t.Setenv("MEMDB_CE_BYPASS_COSINE_THRESHOLD", "0.85")
	var calls int32
	ts := fakeRerankServerWithScores([]float64{1, 2, 3}, &calls)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	q := []float32{1, 0, 0}
	ce := CrossEncoder{
		Client:   client,
		QueryVec: q,
	}
	docs := []rerank.Doc{
		{ID: "1", Text: "a", EmbedVector: []float32{1, 0, 0}}, // cos=1.0 > 0.85
		{ID: "2", Text: "b", EmbedVector: []float32{0, 1, 0}}, // cos=0
	}
	out := ce.rerankWithMathFallback(context.Background(), "q", docs)
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("expected 0 CE calls (pre-bypass), got %d", calls)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 scored, got %d", len(out))
	}
	// Cosine math should preserve high-cos doc on top.
	if out[0].ID != "1" {
		t.Errorf("expected ID=1 (cos=1) on top, got %v", out[0].ID)
	}
}

func TestPreBypass_DisabledFallsThroughToCE(t *testing.T) {
	t.Setenv("MEMDB_CE_BYPASS_COSINE_THRESHOLD", "0") // disabled
	var calls int32
	ts := fakeRerankServerWithScores([]float64{0.9, 0.5}, &calls)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	ce := CrossEncoder{
		Client:   client,
		QueryVec: []float32{1, 0, 0},
	}
	docs := []rerank.Doc{
		{ID: "1", Text: "a", EmbedVector: []float32{1, 0, 0}},
		{ID: "2", Text: "b", EmbedVector: []float32{0, 1, 0}},
	}
	_ = ce.rerankWithMathFallback(context.Background(), "q", docs)
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 CE call (bypass disabled), got %d", calls)
	}
}

func TestLowSpread_TriggersFallback(t *testing.T) {
	t.Setenv("MEMDB_CE_SPREAD_FLOOR", "1.5")
	t.Setenv("MEMDB_CE_QUALITY_FLOOR", "0.001") // pass quality
	var calls int32
	// CE returns clustered scores: top-1=0.06, top-2=0.05 → ratio=1.2 < 1.5
	ts := fakeRerankServerWithScores([]float64{0.06, 0.05}, &calls)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	var fallbackReason string
	ce := CrossEncoder{
		Client:   client,
		QueryVec: []float32{1, 0, 0},
		OnMathFallback: func(_ context.Context, reason string) {
			fallbackReason = reason
		},
	}
	docs := []rerank.Doc{
		{ID: "1", Text: "a", EmbedVector: []float32{0, 1, 0}}, // cos=0
		{ID: "2", Text: "b", EmbedVector: []float32{1, 0, 0}}, // cos=1
	}
	_ = ce.rerankWithMathFallback(context.Background(), "q", docs)
	if fallbackReason != "low_spread" {
		t.Errorf("expected fallback reason 'low_spread', got %q", fallbackReason)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("CE should still be called once (spread checked after), got %d", calls)
	}
}
