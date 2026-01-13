package headersync

import (
	"github.com/go-kit/kit/metrics"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "headersync"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by this package.
type Metrics struct {
	// Height of the last synced header.
	HeaderHeight metrics.Gauge

	// Number of headers synced.
	HeadersSynced metrics.Counter

	// Number of pending header requests.
	PendingRequests metrics.Gauge

	// Number of connected peers with headers.
	Peers metrics.Gauge

	// Header sync rate (headers per second).
	SyncRate metrics.Gauge

	// Verification failures.
	VerificationFailures metrics.Counter

	// Whether or not a node is header syncing. 1 if yes, 0 if no.
	Syncing metrics.Gauge
}
