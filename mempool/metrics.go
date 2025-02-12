package mempool

import (
	"github.com/go-kit/kit/metrics"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "mempool"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by this package.
// see MetricsProvider for descriptions.
type Metrics struct {
	// Number of uncommitted transactions in the mempool.
	Size metrics.Gauge

	// Total size of the mempool in bytes.
	SizeBytes metrics.Gauge

	// Histogram of transaction sizes in bytes.
	TxSizeBytes metrics.Histogram `metrics_buckettype:"exp" metrics_bucketsizes:"1,3,7"`

	// FailedTxs defines the number of failed transactions. These are
	// transactions that failed to make it into the mempool because they were
	// deemed invalid.
	// metrics:Number of failed transactions.
	FailedTxs metrics.Counter

	// RejectedTxs defines the number of rejected transactions. These are
	// transactions that failed to make it into the mempool due to resource
	// limits, e.g. mempool is full.
	// metrics:Number of rejected transactions.
	RejectedTxs metrics.Counter

	// EvictedTxs defines the number of evicted transactions. These are valid
	// transactions that passed CheckTx and make it into the mempool but later
	// became invalid.
	// metrics:Number of evicted transactions.
	EvictedTxs metrics.Counter

	// Number of times transactions are rechecked in the mempool.
	RecheckTimes metrics.Counter

	// Number of connections being actively used for gossiping transactions
	// (experimental feature).
	ActiveOutboundConnections metrics.Gauge

	// ExpiredTxs defines transactions that were removed from the mempool due
	// to a TTL
	ExpiredTxs metrics.Counter

	// SuccessfulTxs defines the number of transactions that successfully made
	// it into a block.
	SuccessfulTxs metrics.Counter

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
