package observability_test

// m12_smoke_test.go — end-to-end verification that the M12.5 metric series
// reach the Prometheus exporter with expected names. Registers the OTel
// meter provider, fires one of each instrument, then scrapes the
// promhttp.Handler() to confirm the series text-format wire names match
// the alert-rule expressions in alerts-memdb-go.yml.
//
// This is integration-level inside a unit test: no external server boot,
// just the in-process exporter pipeline. Catches name-collision /
// pre-registration regressions that pure unit tests miss.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
)

func TestM12MetricsExportWireNames(t *testing.T) {
	// Wire OTel SDK + Prometheus exporter the same way cmd/server/main.go does.
	exp, err := promexporter.New()
	if err != nil {
		t.Fatalf("prometheus exporter: %v", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	otel.SetMeterProvider(mp)

	// Force singleton init AFTER the meter provider is wired; otherwise
	// instruments end up on the default no-op meter and never surface.
	mx := observability.M12()
	if mx == nil {
		t.Fatalf("M12 instruments are nil")
	}
	ctx := context.Background()

	// Fire one sample per instrument so the histogram/counter has data
	// beyond pre-registration (some series are conditional on labels).
	observability.RecordChatRefusedWithEvidence(ctx, "the memories do not contain that.", 5, "1", "factual")
	observability.RecordChatPredLength(ctx, "Emma", "factual")
	observability.RecordChatTop1Cosine(ctx, 0.75)
	observability.RecordChatContextTokens(ctx, "system prompt", "cube-1", "factual")
	observability.RecordD2AddedCandidates(ctx, 12, "cube-1")
	observability.RecordD10EnhanceOutcome(ctx, "answered")
	observability.RecordStageCandidatesAdded(ctx, "linked_expand", 3)
	observability.RecordJudgeChangedTop1(ctx, "swap")
	observability.AddRowsScanned(ctx, "VectorSearch", 50)

	// Scrape the exporter.
	srv := httptest.NewServer(promhttp.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<20)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	// Read the rest if more available
	for {
		more := make([]byte, 1<<20)
		n2, _ := resp.Body.Read(more)
		if n2 == 0 {
			break
		}
		body += string(more[:n2])
	}

	wants := []string{
		"memdb_chat_refused_with_evidence_total",
		"memdb_chat_pred_length_chars",
		"memdb_chat_top1_cosine_score",
		"memdb_chat_context_tokens",
		"memdb_search_d2_added_candidates",
		"memdb_search_d10_enhance_outcome_total",
		"memdb_search_stage_candidates_added",
		"memdb_search_judge_changed_top1_total",
		"memdb_db_query_duration_ms",
		"memdb_db_pgxpool_acquire_ms",
		"memdb_db_rows_scanned_total",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("scraped /metrics missing series %q", want)
		}
	}

	// Verbose dump on -v so reviewers / the PR body can paste a snippet.
	if testing.Verbose() {
		var snippet []string
		for _, line := range strings.Split(body, "\n") {
			for _, w := range wants {
				if strings.Contains(line, w) {
					snippet = append(snippet, line)
					break
				}
			}
		}
		t.Logf("M12.5 sample metrics output:\n%s\n", strings.Join(snippet, "\n"))
	}
}
