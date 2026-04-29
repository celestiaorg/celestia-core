package mempool

import (
	"strconv"

	"github.com/go-kit/kit/metrics"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "mempool"

	// FailedTxCodeLabel is the FailedTxs metric label key used to bucket
	// failures by ABCI response code (or the sentinels below).
	FailedTxCodeLabel = "code"
	// FailedTxCodePreCheck is the FailedTxCodeLabel value used for
	// transactions rejected by the node-level pre-check filter.
	FailedTxCodePreCheck = "precheck"
	// FailedTxCodePostCheck is the FailedTxCodeLabel value used for
	// transactions rejected by the node-level post-check filter, including
	// recheck failures where the ABCI code is OK but post-check returned an
	// error.
	FailedTxCodePostCheck = "postcheck"
)

// TxCodeToStr formats an ABCI response code as a string suitable for use as a
// Prometheus label value.
func TxCodeToStr(code uint32) string {
	return TxCodeToStrOrLabel(code, false, "")
}

// TxCodeToStrOrLabel formats an ABCI response code, but substitutes label
// when code is 0 (CodeTypeOK) and hasErr is true. Used in failure branches
// where code 0 cannot represent a real failure (e.g. node-level post-check
// rejected a tx the application accepted), so the label carries the signal.
func TxCodeToStrOrLabel(code uint32, hasErr bool, label string) string {
	if code == 0 && hasErr {
		return label
	}
	return strconv.FormatUint(uint64(code), 10)
}

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
	// deemed invalid. Labeled by the ABCI response code as a string, or by
	// the sentinels precheck and postcheck for failures from the node-level
	// filters (which have no ABCI code).
	// metrics:Number of failed transactions.
	FailedTxs metrics.Counter `metrics_labels:"code"`

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

	// UserTxLatency records the latency (in seconds) from when a
	// user-submitted transaction enters the mempool to when it is
	// included in a committed block.
	UserTxLatency metrics.Histogram `metrics_bucketsizes:"0.25, 0.5, 1, 2, 4, 6, 8, 10, 11, 12, 13, 14, 15, 18, 25, 60"`
}
