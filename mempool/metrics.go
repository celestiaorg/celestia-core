package mempool

import (
	"github.com/go-kit/kit/metrics"
	"github.com/go-kit/kit/metrics/discard"
	"github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "mempool"
)

// Metrics contains metrics exposed by this package.
// see MetricsProvider for descriptions.
type Metrics struct {
	// Size of the mempool.
	Size metrics.Gauge

	// Histogram of transaction sizes, in bytes.
	TxSizeBytes metrics.Histogram

	// Number of failed transactions.
	FailedTxs metrics.Counter

	// RejectedTxs defines the number of rejected transactions. These are
	// transactions that passed CheckTx but failed to make it into the mempool
	// due to resource limits, e.g. mempool is full and no lower priority
	// transactions exist in the mempool.
	RejectedTxs metrics.Counter

	// EvictedTxs defines the number of evicted transactions. These are valid
	// transactions that passed CheckTx and existed in the mempool but were later
	// evicted to make room for higher priority valid transactions that passed
	// CheckTx.
	EvictedTxs metrics.Counter

	// Number of times transactions are rechecked in the mempool.
	RecheckTimes metrics.Counter

	// TxAvailability tracks upon receiving a block, how many of the transactions
	// did the mempool already have as a percentage between 0 and 1.
	TxAvailability metrics.Histogram
}

// PrometheusMetrics returns Metrics build using Prometheus client library.
// Optionally, labels can be provided along with their values ("foo",
// "fooValue").
func PrometheusMetrics(namespace string, labelsAndValues ...string) *Metrics {
	labels := []string{}
	for i := 0; i < len(labelsAndValues); i += 2 {
		labels = append(labels, labelsAndValues[i])
	}
	return &Metrics{
		Size: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "size",
			Help:      "Size of the mempool (number of uncommitted transactions).",
		}, labels).With(labelsAndValues...),

		TxSizeBytes: prometheus.NewHistogramFrom(stdprometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "tx_size_bytes",
			Help:      "Transaction sizes in bytes.",
			Buckets:   stdprometheus.ExponentialBuckets(1, 3, 17),
		}, labels).With(labelsAndValues...),

		FailedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "failed_txs",
			Help:      "Number of failed transactions.",
		}, labels).With(labelsAndValues...),

		RejectedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "rejected_txs",
			Help:      "Number of rejected transactions.",
		}, labels).With(labelsAndValues...),

		EvictedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "evicted_txs",
			Help:      "Number of evicted transactions.",
		}, labels).With(labelsAndValues...),

		RecheckTimes: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "recheck_times",
			Help:      "Number of times transactions are rechecked in the mempool.",
		}, labels).With(labelsAndValues...),

		TxAvailability: prometheus.NewHistogramFrom(stdprometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "tx_availability",
			Help:      "Transaction sizes in bytes.",
			Buckets:   AsymptoticBuckets(0, 1, 8),
		}, labels).With(labelsAndValues...),
	}
}

// AsymptoticBuckets starts at start and asyptotically approaches end by a factor of 2
// i.e. if start = 0 and end = 1 -> 0, 0.5, 0.75, 0.875 etc.
func AsymptoticBuckets(start, end float64, count int) []float64 {
	buckets := make([]float64, count)
	buckets[0] = start
	for i := 1; i < count; i++ {
		buckets[i] = (buckets[i-1] + end) / 2
	}
	return buckets
}

// NopMetrics returns no-op Metrics.
func NopMetrics() *Metrics {
	return &Metrics{
		Size:           discard.NewGauge(),
		TxSizeBytes:    discard.NewHistogram(),
		FailedTxs:      discard.NewCounter(),
		RejectedTxs:    discard.NewCounter(),
		EvictedTxs:     discard.NewCounter(),
		RecheckTimes:   discard.NewCounter(),
		TxAvailability: discard.NewHistogram(),
	}
}
