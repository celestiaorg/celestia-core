package privval

import (
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/libs/log"
)

const (
	signingLatencyWarnWindow    = 50
	signingLatencyWarnThreshold = 10 * time.Millisecond
	signingLatencyWarnCooldown  = time.Minute

	// Low-cardinality message type labels for Prometheus.
	messageTypePrevote   = "prevote"
	messageTypePrecommit = "precommit"
	messageTypeProposal  = "proposal"
	messageTypeRawBytes  = "raw_bytes"
)

// signingLatencyTracker tracks recent successful signing latencies and emits a
// rate-limited warning when the median over the last N samples exceeds the
// configured threshold.
type signingLatencyTracker struct {
	mtx sync.Mutex

	samples  []time.Duration
	capacity int
	size     int
	head     int

	threshold time.Duration
	cooldown  time.Duration
	lastWarn  time.Time

	metrics *Metrics
	logger  log.Logger
}

func newSigningLatencyTracker(metrics *Metrics, logger log.Logger) *signingLatencyTracker {
	if metrics == nil {
		metrics = NopMetrics()
	}
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &signingLatencyTracker{
		samples:   make([]time.Duration, signingLatencyWarnWindow),
		capacity:  signingLatencyWarnWindow,
		threshold: signingLatencyWarnThreshold,
		cooldown:  signingLatencyWarnCooldown,
		metrics:   metrics,
		logger:    logger,
	}
}

// Record stores a successful signing latency sample, updates the median gauge
// once the window is full, and logs a warning when the median exceeds the
// threshold (rate-limited by cooldown).
func (t *signingLatencyTracker) Record(latency time.Duration) {
	t.mtx.Lock()
	defer t.mtx.Unlock()

	t.samples[t.head] = latency
	t.head = (t.head + 1) % t.capacity
	if t.size < t.capacity {
		t.size++
	}
	if t.size < t.capacity {
		return
	}

	median := t.medianLocked()
	t.metrics.SigningLatencyMedianSeconds.Set(median.Seconds())

	if median <= t.threshold {
		return
	}

	now := time.Now()
	if !t.lastWarn.IsZero() && now.Sub(t.lastWarn) < t.cooldown {
		return
	}
	t.lastWarn = now

	t.logger.Info(
		"warning: remote signer signing latency is high; consider updating your KMS setup",
		"median", median.String(),
		"threshold", t.threshold.String(),
		"window", t.capacity,
	)
}

func (t *signingLatencyTracker) medianLocked() time.Duration {
	sorted := make([]time.Duration, t.size)
	copy(sorted, t.samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

// setMetrics replaces the metrics sink used by the tracker.
func (t *signingLatencyTracker) setMetrics(metrics *Metrics) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	if metrics == nil {
		metrics = NopMetrics()
	}
	t.metrics = metrics
}
