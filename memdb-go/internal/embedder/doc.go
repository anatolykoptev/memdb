// Package embedder is a backward-compatibility shim for the M12.11 extraction
// of the embedder code into github.com/anatolykoptev/go-kit/embed (HTTP /
// Ollama / Voyage backends + Registry + retry/metrics) and
// github.com/anatolykoptev/go-kit/embed/onnx (cgo-only ONNX backend).
//
// All public types and functions in this package are re-exports of the
// go-kit/embed equivalents. New code in memdb-go should import the go-kit
// packages directly:
//
//	"github.com/anatolykoptev/go-kit/embed"
//	"github.com/anatolykoptev/go-kit/embed/onnx"
//
// This shim exists so that any straggler imports of
// "github.com/anatolykoptev/memdb/memdb-go/internal/embedder" continue to
// compile through the M12 → M13 deprecation cycle. It will be deleted in
// M13 once the call-site migration is verified across all internal packages
// and any tests that referenced internal-only test helpers.
//
// Metrics namespace was renamed in the move:
//
//   - old (OpenTelemetry): memdb.embedder.requests_total, memdb.embedder.duration_ms,
//     memdb.embedder.batch_size, memdb.embedder.retry_total
//   - new (Prometheus):    embed_requests_total, embed_duration_seconds,
//     embed_batch_size, embed_retry_total
//
// Dashboards and alerting rules that referenced the old series names need
// to be updated; the M12.11 PR body documents the rename.
package embedder
