// Package handlers — add-pipeline domain metrics.
package handlers

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	addMetricsOnce sync.Once
	addInstruments *addMetricsStruct
)

type addMetricsStruct struct {
	Requests metric.Int64Counter     // labels: mode, outcome
	Duration metric.Float64Histogram // labels: mode
	Memories metric.Int64Counter     // labels: mode (records len(items))
}

func addMx() *addMetricsStruct {
	addMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/add")
		reqs, _ := meter.Int64Counter("memdb.add.requests_total",
			metric.WithDescription("Total add-pipeline requests by mode and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.add.duration_ms",
			metric.WithDescription("Add-pipeline duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		mems, _ := meter.Int64Counter("memdb.add.memories_total",
			metric.WithDescription("Total memories written by add pipeline"),
		)
		addInstruments = &addMetricsStruct{Requests: reqs, Duration: dur, Memories: mems}
	})
	return addInstruments
}
