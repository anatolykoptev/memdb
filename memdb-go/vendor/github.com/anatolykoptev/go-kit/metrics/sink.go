package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// The Label syntax is name{k=v,k=v}: parseLabeled splits pairs on ',' and
// each pair on its FIRST '='. So a value may hold anything but ',' (and a
// line break, which would split a TextSink line); a key must also be free
// of '=', '{' and '}'. Everything else — '=', braces and quotes in a value,
// e.g. a route pattern "GET /items/{id}" — is left as is: Prometheus
// escapes it on exposition and existing series keep their names.
var (
	labelValueSanitizer = strings.NewReplacer(",", "_", "\n", "_", "\r", "_")
	labelKeySanitizer   = strings.NewReplacer(",", "_", "=", "_", "{", "_", "}", "_", "\n", "_", "\r", "_")
)

// SanitizeLabelValue returns v as Label would embed it. Code that observes
// a prometheus vec directly with a value it also passes to Label must use
// it, so both series carry the same label value.
func SanitizeLabelValue(v string) string { return labelValueSanitizer.Replace(v) }

// Label builds a metric key with labels. Labels are alternating key-value pairs.
// Label("requests", "method", "GET") returns "requests{method=GET}".
// Label("rpc", "service", "auth", "method", "login") returns "rpc{service=auth,method=login}".
// Returns name unchanged if no labels or odd number of label values.
// Request-controlled values are safe to pass: a ',' or line break in a
// value, and any of , = { } or a line break in a key, becomes '_', so the
// key always parses back to exactly len(kvs)/2 labels. Keys are expected
// to be code constants.
func Label(name string, kvs ...string) string {
	if len(kvs) == 0 || len(kvs)%2 != 0 {
		return name
	}
	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteByte('{')
	for i := 0; i < len(kvs); i += 2 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(labelKeySanitizer.Replace(kvs[i]))
		sb.WriteByte('=')
		sb.WriteString(labelValueSanitizer.Replace(kvs[i+1]))
	}
	sb.WriteByte('}')
	return sb.String()
}

// Sink formats metrics for output.
type Sink interface {
	WriteMetrics(w io.Writer, counters map[string]int64, gauges map[string]float64) error
}

// TextSink outputs metrics as sorted key=value lines.
type TextSink struct{}

func (TextSink) WriteMetrics(w io.Writer, counters map[string]int64, gauges map[string]float64) error {
	type entry struct {
		name string
		text string
	}
	entries := make([]entry, 0, len(counters)+len(gauges))
	for k, v := range counters {
		entries = append(entries, entry{k, fmt.Sprintf("%s=%d", k, v)})
	}
	for k, v := range gauges {
		entries = append(entries, entry{k, fmt.Sprintf("%s=%.2f", k, v)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		fmt.Fprintln(w, e.text)
	}
	return nil
}

// JSONSink outputs metrics as a JSON object.
type JSONSink struct{}

func (JSONSink) WriteMetrics(w io.Writer, counters map[string]int64, gauges map[string]float64) error {
	data := make(map[string]any, len(counters)+len(gauges))
	for k, v := range counters {
		data[k] = v
	}
	for k, v := range gauges {
		data[k] = v
	}
	return json.NewEncoder(w).Encode(data)
}

// WriteTo writes all metrics to w using the given Sink.
func (r *Registry) WriteTo(w io.Writer, sink Sink) error {
	if r == nil {
		return nil
	}
	return sink.WriteMetrics(w, r.Snapshot(), r.GaugeSnapshot())
}
