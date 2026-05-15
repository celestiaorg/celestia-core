package store

import (
	"github.com/go-kit/kit/metrics"
	"github.com/go-kit/kit/metrics/discard"
	kitprom "github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
)

// MetricsSubsystem is the prometheus subsystem for all metrics emitted by this package.
const MetricsSubsystem = "blockstore"

// Metrics holds counters and histograms emitted by the blockstore compactor.
type Metrics struct {
	// Successful Compact calls, labelled by prefix (H, C, SC, P).
	CompactTotal metrics.Counter
	// Failed Compact calls, labelled by prefix.
	CompactErrors metrics.Counter
	// Duration of each Compact call in seconds, labelled by prefix.
	CompactDurationSeconds metrics.Histogram
	// Wake-up signals dropped because another was already pending (single-flight collapsing).
	CompactDroppedSignals metrics.Counter
}

// PrometheusMetrics builds Prometheus-backed metrics with the given namespace
// and additional fixed labels (paired as label-name, label-value, ...).
func PrometheusMetrics(namespace string, labelsAndValues ...string) *Metrics {
	labels := []string{}
	for i := 0; i < len(labelsAndValues); i += 2 {
		labels = append(labels, labelsAndValues[i])
	}
	prefixLabels := append(append([]string{}, labels...), "prefix")
	return &Metrics{
		CompactTotal: kitprom.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "compact_total",
			Help:      "Number of successful range-compactions of the blockstore, by prefix.",
		}, prefixLabels).With(labelsAndValues...),
		CompactErrors: kitprom.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "compact_errors_total",
			Help:      "Number of failed range-compactions of the blockstore, by prefix.",
		}, prefixLabels).With(labelsAndValues...),
		CompactDurationSeconds: kitprom.NewHistogramFrom(stdprometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "compact_duration_seconds",
			Help:      "Wall-clock duration of each Compact call against the blockstore, by prefix.",
			Buckets:   stdprometheus.ExponentialBucketsRange(0.01, 60, 10),
		}, prefixLabels).With(labelsAndValues...),
		CompactDroppedSignals: kitprom.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "compact_dropped_signals_total",
			Help:      "Number of compaction wake-up signals dropped because one was already pending.",
		}, labels).With(labelsAndValues...),
	}
}

// NopMetrics returns metrics that discard all observations. Safe to use when
// Prometheus is disabled or in tests that don't care about metrics output.
func NopMetrics() *Metrics {
	return &Metrics{
		CompactTotal:           discard.NewCounter(),
		CompactErrors:          discard.NewCounter(),
		CompactDurationSeconds: discard.NewHistogram(),
		CompactDroppedSignals:  discard.NewCounter(),
	}
}
