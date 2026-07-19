package privval

import (
	"github.com/go-kit/kit/metrics"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "privval"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by the privval package.
type Metrics struct {
	// SigningLatencySeconds is the time to obtain a signature from the remote
	// signer.
	//metrics:Time in seconds to obtain a signature from the remote signer.
	SigningLatencySeconds metrics.Histogram `metrics_labels:"message_type" metrics_bucketsizes:".0005,.001,.002,.005,.01,.02,.05,.1,.25,.5,1"`

	// SigningLatencyMedianSeconds is the median signing latency over the last
	// N successful remote signing events.
	//metrics:Median signing latency in seconds over the recent signing window.
	SigningLatencyMedianSeconds metrics.Gauge
}
