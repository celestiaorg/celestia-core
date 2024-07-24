package mempool

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/cometbft/cometbft/libs/os"
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

	// FailedTxs defines the number of failed transactions. These were marked
	// invalid by the application in either CheckTx or RecheckTx.
	FailedTxs metrics.Counter

	// EvictedTxs defines the number of evicted transactions. These are valid
	// transactions that passed CheckTx and existed in the mempool but were later
	// evicted to make room for higher priority valid transactions
	EvictedTxs metrics.Counter

	// ExpiredTxs defines transactions that were removed from the mempool due
	// to a TTL
	ExpiredTxs metrics.Counter

	// SuccessfulTxs defines the number of transactions that successfully made
	// it into a block.
	SuccessfulTxs metrics.Counter

	// Number of times transactions are rechecked in the mempool.
	RecheckTimes metrics.Counter

	// AlreadySeenTxs defines the number of transactions that entered the
	// mempool which were already present in the mempool. This is a good
	// indicator of the degree of duplication in message gossiping.
	AlreadySeenTxs metrics.Counter

	// RequestedTxs defines the number of times that the node requested a
	// tx to a peer
	RequestedTxs metrics.Counter

	// RerequestedTxs defines the number of times that a requested tx
	// never received a response in time and a new request was made.
	RerequestedTxs metrics.Counter
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

		EvictedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "evicted_txs",
			Help:      "Number of evicted transactions.",
		}, labels).With(labelsAndValues...),

		ExpiredTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "expired_txs",
			Help:      "Number of expired transactions.",
		}, labels).With(labelsAndValues...),

		SuccessfulTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "successful_txs",
			Help:      "Number of transactions that successfully made it into a block.",
		}, labels).With(labelsAndValues...),

		RecheckTimes: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "recheck_times",
			Help:      "Number of times transactions are rechecked in the mempool.",
		}, labels).With(labelsAndValues...),

		AlreadySeenTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "already_seen_txs",
			Help:      "Number of transactions that entered the mempool but were already present in the mempool.",
		}, labels).With(labelsAndValues...),

		RequestedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "requested_txs",
			Help:      "Number of initial requests for a transaction",
		}, labels).With(labelsAndValues...),

		RerequestedTxs: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "rerequested_txs",
			Help:      "Number of times a transaction was requested again after a previous request timed out",
		}, labels).With(labelsAndValues...),
	}
}

// NopMetrics returns no-op Metrics.
func NopMetrics() *Metrics {
	return &Metrics{
		Size:           discard.NewGauge(),
		TxSizeBytes:    discard.NewHistogram(),
		FailedTxs:      discard.NewCounter(),
		EvictedTxs:     discard.NewCounter(),
		ExpiredTxs:     discard.NewCounter(),
		SuccessfulTxs:  discard.NewCounter(),
		RecheckTimes:   discard.NewCounter(),
		AlreadySeenTxs: discard.NewCounter(),
		RequestedTxs:   discard.NewCounter(),
		RerequestedTxs: discard.NewCounter(),
	}
}

type JSONMetrics struct {
	dir      string
	interval int
	sync.Mutex
	StartTime           time.Time
	EndTime             time.Time
	Blocks              uint64
	Transactions        []uint64
	TransactionsMissing []uint64
	// measured in ms
	TimeTakenFetchingTxs []uint64
}

func NewJSONMetrics(dir string) *JSONMetrics {
	return &JSONMetrics{
		dir:                  dir,
		StartTime:            time.Now().UTC(),
		Transactions:         make([]uint64, 0),
		TransactionsMissing:  make([]uint64, 0),
		TimeTakenFetchingTxs: make([]uint64, 0),
	}
}

func (m *JSONMetrics) Save() {
	m.EndTime = time.Now().UTC()
	content, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		panic(err)
	}
	path := filepath.Join(m.dir, fmt.Sprintf("metrics_%d.json", m.interval))
	os.MustWriteFile(path, content, 0644)
	m.StartTime = m.EndTime
	m.interval++
	m.reset()
}

func (m *JSONMetrics) reset() {
	m.Blocks = 0
	m.Transactions = make([]uint64, 0)
	m.TransactionsMissing = make([]uint64, 0)
	m.TimeTakenFetchingTxs = make([]uint64, 0)
}
