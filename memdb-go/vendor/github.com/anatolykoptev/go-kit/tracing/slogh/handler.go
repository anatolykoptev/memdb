// Package slogh wraps an [slog.Handler] to inject the active OTel trace_id
// and span_id into every log record emitted with a context that carries a
// span. It also lifts each ERROR-level record onto the span as an event
// (RecordError + status), giving Jaeger UIs a chronological view of log
// breadcrumbs inside a span.
//
// Why: without correlation, a slow-handler complaint in journalctl is a
// black box — no way to jump to the corresponding trace in Jaeger and
// vice versa. With slogh, every log line carries trace_id={hex}, every
// trace span shows the log events that happened during it.
//
// Usage:
//
//	import "github.com/anatolykoptev/go-kit/tracing/slogh"
//
//	base := slog.NewJSONHandler(os.Stdout, nil)
//	logger := slog.New(slogh.NewHandler(base))
//	slog.SetDefault(logger)
//
// Then anywhere downstream:
//
//	slog.InfoContext(ctx, "processing place", "rank", 5)
//	// log line gets trace_id=4bf92f3577b34da6a3ce929d0e0e4736 + span_id
//
// Nil base → no-op handler (drops everything). Pass a real Handler.
package slogh

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	// AttrTraceID is the slog key holding the W3C trace_id (32 hex chars).
	// Matches the OTel logs semconv name so log shippers route correctly.
	AttrTraceID = "trace_id"
	// AttrSpanID is the slog key holding the active span_id (16 hex chars).
	AttrSpanID = "span_id"
)

// Handler decorates an upstream slog.Handler with span correlation.
//
// On Handle: extracts the span from r's context (set via slog.*Context
// helpers), appends trace_id + span_id attrs, and forwards to base.
// For ERROR-level records, also annotates the span with an Event whose
// attributes are the record's attrs — Jaeger renders these as in-line
// breadcrumbs on the span timeline.
type Handler struct {
	base slog.Handler
}

// NewHandler wraps base with span-aware attribute injection.
// Pass a base that's already configured with your level + format.
func NewHandler(base slog.Handler) *Handler {
	return &Handler{base: base}
}

// Enabled defers to the base handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	if h == nil || h.base == nil {
		return false
	}
	return h.base.Enabled(ctx, l)
}

// Handle injects trace correlation, optionally annotates the active span,
// and forwards the record to the base handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if h == nil || h.base == nil {
		return nil
	}

	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if sc.IsValid() {
		// Append correlation attrs to the record. Slog.Record.AddAttrs is
		// the documented API — it doesn't allocate when the record has
		// header capacity (≤5 attrs).
		r.AddAttrs(
			slog.String(AttrTraceID, sc.TraceID().String()),
			slog.String(AttrSpanID, sc.SpanID().String()),
		)

		// ERROR-level records become span events. This gives Jaeger a
		// timeline of log lines inside the span and marks the span as
		// errored so it filters into "errors only" views.
		if r.Level >= slog.LevelError && span.IsRecording() {
			recordToSpanEvent(span, r)
		}
	}

	return h.base.Handle(ctx, r)
}

// WithAttrs delegates so SetGroup/With chains work transparently.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h == nil || h.base == nil {
		return h
	}
	return &Handler{base: h.base.WithAttrs(attrs)}
}

// WithGroup delegates.
func (h *Handler) WithGroup(name string) slog.Handler {
	if h == nil || h.base == nil {
		return h
	}
	return &Handler{base: h.base.WithGroup(name)}
}

// recordToSpanEvent copies a slog.Record onto the span as an event.
// Event name = record.Message; event attrs = record's slog.Attrs converted
// to OTel attribute.KeyValue. Non-comparable types (slog.Group, slog.LogValuer)
// fall through to .String() to keep the conversion lossless and total.
func recordToSpanEvent(span trace.Span, r slog.Record) {
	attrs := make([]attribute.KeyValue, 0, r.NumAttrs()+1)
	attrs = append(attrs, attribute.String("level", r.Level.String()))
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slogAttrToOTel(a))
		return true
	})
	span.AddEvent(r.Message, trace.WithAttributes(attrs...))
	span.SetStatus(codes.Error, r.Message)
}

// slogAttrToOTel maps a slog.Attr onto an OTel KeyValue. Falls back to
// the rendered string for groups, LogValuer, and any.
func slogAttrToOTel(a slog.Attr) attribute.KeyValue {
	switch a.Value.Kind() {
	case slog.KindString:
		return attribute.String(a.Key, a.Value.String())
	case slog.KindInt64:
		return attribute.Int64(a.Key, a.Value.Int64())
	case slog.KindUint64:
		return attribute.Int64(a.Key, int64(a.Value.Uint64())) //nolint:gosec // overflow accepted for log breadcrumb
	case slog.KindFloat64:
		return attribute.Float64(a.Key, a.Value.Float64())
	case slog.KindBool:
		return attribute.Bool(a.Key, a.Value.Bool())
	case slog.KindDuration:
		return attribute.Float64(a.Key, a.Value.Duration().Seconds())
	case slog.KindTime:
		return attribute.String(a.Key, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	default:
		// slog.KindAny, slog.KindGroup, slog.KindLogValuer — render as string.
		// Loses structure but keeps the breadcrumb readable; structural
		// preservation isn't worth the recursion + reflection cost here.
		return attribute.String(a.Key, a.Value.String())
	}
}
