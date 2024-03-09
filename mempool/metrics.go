package mempool

import (
	"github.com/go-kit/kit/metrics"
	"github.com/go-kit/kit/metrics/discard"
	"github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "mempool"

	TypeLabel = "type"

	FailedPrecheck = "precheck"
	FailedAdding   = "adding"
	FailedRecheck  = "recheck"

	EvictedNewTxFullMempool      = "full-removed-incoming"
	EvictedExistingTxFullMempool = "full-removed-existing"
	EvictedTxExpiredBlocks       = "expired-ttl-blocks"
	EvictedTxExpiredTime         = "expired-ttl-time"
)

// Metrics contains metrics exposed by this package.
// see MetricsProvider for descriptions.
type Metrics struct {
	// Size of the mempool.
	Size metrics.PGauge

	// Total size of the mempool in bytes.
	SizeBytes metrics.PGauge

	// Histogram of transaction sizes, in bytes.
	TxSizeBytes metrics.PHistogram

	// FailedTxs defines the number of failed transactions. These were marked
	// invalid by the application in either CheckTx or RecheckTx.
	FailedTxs metrics.PCounter

	// EvictedTxs defines the number of evicted transactions. These are valid
	// transactions that passed CheckTx and existed in the mempool but were later
	// evicted to make room for higher priority valid transactions that passed
	// CheckTx.
	EvictedTxs metrics.PCounter

	// SuccessfulTxs defines the number of transactions that successfully made
	// it into a block.
	SuccessfulTxs metrics.PCounter

	// Number of times transactions are rechecked in the mempool.
	RecheckTimes metrics.PCounter

	// AlreadySeenTxs defines the number of transactions that entered the
	// mempool which were already present in the mempool. This is a good
	// indicator of the degree of duplication in message gossiping.
	AlreadySeenTxs metrics.PCounter

	// RequestedTxs defines the number of times that the node requested a
	// tx to a peer
	RequestedTxs metrics.PCounter

	// RerequestedTxs defines the number of times that a requested tx
	// never received a response in time and a new request was made.
	RerequestedTxs metrics.PCounter
}

func (m *Metrics) Push(p *push.Pusher) *push.Pusher {
	p.Collector(m.Size.Collector())
	p.Collector(m.SizeBytes.Collector())
	p.Collector(m.TxSizeBytes.Collector())
	p.Collector(m.FailedTxs.Collector())
	p.Collector(m.EvictedTxs.Collector())
	p.Collector(m.SuccessfulTxs.Collector())
	p.Collector(m.RecheckTimes.Collector())
	p.Collector(m.AlreadySeenTxs.Collector())
	p.Collector(m.RequestedTxs.Collector())
	p.Collector(m.RerequestedTxs.Collector())
	return p
}

// PrometheusMetrics returns Metrics build using Prometheus client library.
// Optionally, labels can be provided along with their values ("foo",
// "fooValue").
func PrometheusMetrics(namespace string, labelsAndValues ...string) *Metrics {
	labels := []string{}
	for i := 0; i < len(labelsAndValues); i += 2 {
		labels = append(labels, labelsAndValues[i])
	}
	typedCounterLabels := append(append(make([]string, 0, len(labels)+1), labels...), TypeLabel)
	return &Metrics{
		Size: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "size",
			Help:      "Size of the mempool (number of uncommitted transactions).",
		}, labels).WithP(labelsAndValues...),

		SizeBytes: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "size_bytes",
			Help:      "Total size of the mempool in bytes.",
		}, labels).WithP(labelsAndValues...),

		TxSizeBytes: prometheus.NewHistogramFrom(stdprometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "tx_size_bytes",
			Help:      "Transaction sizes in bytes.",
			Buckets:   stdprometheus.ExponentialBuckets(1, 3, 17),
		}, labels).WithP(labelsAndValues...),

		FailedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "failed_txs",
			Help:      "Number of failed transactions.",
		}, typedCounterLabels).WithP(labelsAndValues...),

		EvictedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "evicted_txs",
			Help:      "Number of evicted transactions.",
		}, typedCounterLabels).WithP(labelsAndValues...),

		SuccessfulTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "successful_txs",
			Help:      "Number of transactions that successfully made it into a block.",
		}, labels).WithP(labelsAndValues...),

		RecheckTimes: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "recheck_times",
			Help:      "Number of times transactions are rechecked in the mempool.",
		}, labels).WithP(labelsAndValues...),

		AlreadySeenTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "already_seen_txs",
			Help:      "Number of transactions that entered the mempool but were already present in the mempool.",
		}, labels).WithP(labelsAndValues...),

		RequestedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "requested_txs",
			Help:      "Number of initial requests for a transaction",
		}, labels).WithP(labelsAndValues...),

		RerequestedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "rerequested_txs",
			Help:      "Number of times a transaction was requested again after a previous request timed out",
		}, labels).WithP(labelsAndValues...),
	}
}

// NopMetrics returns no-op Metrics.
func NopMetrics() *Metrics {
	return &Metrics{
		Size:           discard.NewPGauge(),
		SizeBytes:      discard.NewPGauge(),
		TxSizeBytes:    discard.NewPHistogram(),
		FailedTxs:      discard.NewPCounter(),
		EvictedTxs:     discard.NewPCounter(),
		SuccessfulTxs:  discard.NewPCounter(),
		RecheckTimes:   discard.NewPCounter(),
		AlreadySeenTxs: discard.NewPCounter(),
		RequestedTxs:   discard.NewPCounter(),
		RerequestedTxs: discard.NewPCounter(),
	}
}
