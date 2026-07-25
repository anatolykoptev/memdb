// Package handlers — metric_contract_test.go: BUG B metric contract test.
//
// Verifies that the fine-fallback and add-failure metrics are exposed in the
// Prometheus text exposition format with the correct name and bounded label
// values. Uses an anchored regex parse (NOT strings.Contains) so a malformed
// metric line (wrong name, missing label, unbounded value) fails the test.
//
// TestMain sets up the OTel Prometheus exporter BEFORE any test initializes
// the addMx() singleton, so the instruments land on the real meter provider
// and are scrape-able via promhttp.Handler().
package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestMain(m *testing.M) {
	exp, err := promexporter.New()
	if err != nil {
		panic("prometheus exporter: " + err.Error())
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	otel.SetMeterProvider(mp)
	os.Exit(m.Run())
}

// scrapeMetrics fires the metrics and scrapes the Prometheus endpoint.
func scrapeMetrics(t *testing.T, fire func(ctx context.Context)) string {
	t.Helper()
	ctx := context.Background()
	fire(ctx)

	srv := httptest.NewServer(promhttp.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("scrape: read body: %v", err)
	}
	return string(body)
}

// TestMetricContract_FineFallback verifies that memdb_add_fine_fallback_total
// is exposed with a reason="llm_error" label in the Prometheus text format.
// The anchored regex ensures the metric name, label, and value are well-formed
// — a substring match would pass on a malformed line.
func TestMetricContract_FineFallback(t *testing.T) {
	body := scrapeMetrics(t, func(ctx context.Context) {
		recordFineFallback(ctx, "llm_error")
	})
	// Anchored: metric name + braces with reason label + numeric value.
	// Matches: memdb_add_fine_fallback_total{...reason="llm_error"...} 1
	re := regexp.MustCompile(`^memdb_add_fine_fallback_total\{[^}]*reason="llm_error"[^}]*\} [0-9]`)
	for _, line := range regexp.MustCompile("\n").Split(body, -1) {
		if re.MatchString(line) {
			return // pass
		}
	}
	t.Errorf("metric contract: no line matching %s in /metrics output (fine_fallback_total with reason=llm_error)", re.String())
}

// TestMetricContract_AddFailures verifies that memdb_add_failures_total is
// exposed with a reason label in the Prometheus text format.
func TestMetricContract_AddFailures(t *testing.T) {
	body := scrapeMetrics(t, func(ctx context.Context) {
		recordAddFailure(ctx, "llm_exhausted")
	})
	re := regexp.MustCompile(`^memdb_add_failures_total\{[^}]*reason="llm_exhausted"[^}]*\} [0-9]`)
	for _, line := range regexp.MustCompile("\n").Split(body, -1) {
		if re.MatchString(line) {
			return // pass
		}
	}
	t.Errorf("metric contract: no line matching %s in /metrics output (failures_total with reason=llm_exhausted)", re.String())
}

// TestMetricContract_LLMRequestStatus verifies that
// memdb_llm_structured_call_total is exposed with a status label (BUG B —
// pre-fix the atomic path's metric had only model+outcome, so 429/502 rates
// were invisible). The structured_call_total metric is the correct seam for
// the production atomic add path; memdb_llm_requests_total is the legacy
// Client.Chat() counter and carries no status label (see internal/llm/client.go).
func TestMetricContract_LLMRequestStatus(t *testing.T) {
	body := scrapeMetrics(t, func(ctx context.Context) {
		// Pre-warm the llm package's structured-call metric singleton so
		// the status= label series is visible in the Prometheus output.
		llm.PrewarmMetrics()
	})
	// The structured_call_total metric should have a status label.
	re := regexp.MustCompile(`^memdb_llm_structured_call_total\{[^}]*status="`)
	for _, line := range regexp.MustCompile("\n").Split(body, -1) {
		if re.MatchString(line) {
			return // pass
		}
	}
	t.Errorf("metric contract: no memdb_llm_structured_call_total line with status= label in /metrics output")
}
