package statesync

import (
	"github.com/go-kit/kit/metrics"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "statesync"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by this package.
type Metrics struct {
	// Whether or not a node is state syncing. 1 if yes, 0 if no.
	Syncing metrics.Gauge

	// RepliesNotSent is the number of replies to snapshot and chunk requests
	// that were not sent to the peer. The reason is send_queue_full when the
	// request was skipped because the peer's send queue was full, and
	// try_send_failed when the reply could not be queued.
	//metrics:Number of replies to state sync requests that were not sent, by message type and reason.
	RepliesNotSent metrics.Counter `metrics_labels:"message_type,reason"`
}
