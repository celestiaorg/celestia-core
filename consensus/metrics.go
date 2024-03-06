package consensus

import (
	"strings"
	"time"

	"github.com/go-kit/kit/metrics"
	"github.com/go-kit/kit/metrics/discard"
	cstypes "github.com/tendermint/tendermint/consensus/types"

	prometheus "github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "consensus"
)

// Metrics contains metrics exposed by this package.
type Metrics struct {
	// Height of the chain.
	Height metrics.PGauge
	// The height when the metrics started from
	StartHeight metrics.PGauge

	// ValidatorLastSignedHeight of a validator.
	ValidatorLastSignedHeight metrics.PGauge

	// Number of rounds.
	Rounds metrics.PGauge

	// Number of validators.
	Validators metrics.PGauge
	// Total power of all validators.
	ValidatorsPower metrics.PGauge
	// Power of a validator.
	ValidatorPower metrics.PGauge
	// Amount of blocks missed by a validator.
	ValidatorMissedBlocks metrics.PGauge
	// Number of validators who did not sign.
	MissingValidators metrics.PGauge
	// Total power of the missing validators.
	MissingValidatorsPower metrics.PGauge
	// Number of validators who tried to double sign.
	ByzantineValidators metrics.PGauge
	// Total power of the byzantine validators.
	ByzantineValidatorsPower metrics.PGauge

	// Time between this and the last block.
	BlockIntervalSeconds metrics.PHistogram
	// Block time in seconds.
	BlockTimeSeconds metrics.PGauge

	// Number of transactions.
	NumTxs metrics.PGauge
	// Size of the block.
	BlockSizeBytes metrics.PGauge
	// Total number of transactions.
	TotalTxs metrics.PGauge
	// The latest block height.
	CommittedHeight metrics.PGauge
	// Whether a node is fast syncing. 1 if yes, 0 if no.
	FastSyncing metrics.PGauge
	// Whether a node is state syncing. 1 if yes, 0 if no.
	StateSyncing metrics.PGauge

	// Number of blockparts transmitted by peer.
	BlockParts metrics.PCounter

	// Histogram of step duration.
	StepDuration metrics.PHistogram
	stepStart    time.Time

	// Number of block parts received by the node, separated by whether the part
	// was relevant to the block the node is trying to gather or not.
	BlockGossipPartsReceived metrics.PCounter

	// QuroumPrevoteMessageDelay is the interval in seconds between the proposal
	// timestamp and the timestamp of the earliest prevote that achieved a quorum
	// during the prevote step.
	//
	// To compute it, sum the voting power over each prevote received, in increasing
	// order of timestamp. The timestamp of the first prevote to increase the sum to
	// be above 2/3 of the total voting power of the network defines the endpoint
	// the endpoint of the interval. Subtract the proposal timestamp from this endpoint
	// to obtain the quorum delay.
	QuorumPrevoteMessageDelay metrics.PGauge

	// FullPrevoteMessageDelay is the interval in seconds between the proposal
	// timestamp and the timestamp of the latest prevote in a round where 100%
	// of the voting power on the network issued prevotes.
	FullPrevoteMessageDelay metrics.Gauge

	// The amount of proposals that were rejected by the application.
	ApplicationRejectedProposals metrics.Counter

	// The amount of proposals that failed to be received in time
	TimedOutProposals metrics.Counter
}

func (m *Metrics) Push(p *push.Pusher) *push.Pusher {
	p = p.Collector(m.Height.Collector())
	p = p.Collector(m.Rounds.Collector())
	p = p.Collector(m.Validators.Collector())
	p = p.Collector(m.ValidatorLastSignedHeight.Collector())
	p = p.Collector(m.ValidatorsPower.Collector())
	p = p.Collector(m.ValidatorPower.Collector())
	p = p.Collector(m.ValidatorMissedBlocks.Collector())
	p = p.Collector(m.MissingValidators.Collector())
	p = p.Collector(m.MissingValidatorsPower.Collector())
	p = p.Collector(m.ByzantineValidators.Collector())
	p = p.Collector(m.ByzantineValidatorsPower.Collector())
	p = p.Collector(m.BlockIntervalSeconds.Collector())
	p = p.Collector(m.BlockTimeSeconds.Collector())
	p = p.Collector(m.NumTxs.Collector())
	p = p.Collector(m.BlockSizeBytes.Collector())
	p = p.Collector(m.TotalTxs.Collector())
	p = p.Collector(m.CommittedHeight.Collector())
	p = p.Collector(m.FastSyncing.Collector())
	p = p.Collector(m.StateSyncing.Collector())
	p = p.Collector(m.BlockParts.Collector())
	p = p.Collector(m.StepDuration.Collector())
	p = p.Collector(m.BlockGossipPartsReceived.Collector())
	p = p.Collector(m.QuorumPrevoteMessageDelay.Collector())
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
	return &Metrics{
		Height: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "height",
			Help:      "Height of the chain.",
		}, labels).WithP(labelsAndValues...),
		StartHeight: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "start_height",
			Help:      "Height that metrics began",
		}, labels).WithP(labelsAndValues...),
		Rounds: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "rounds",
			Help:      "Number of rounds.",
		}, labels).WithP(labelsAndValues...),
		Validators: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "validators",
			Help:      "Number of validators.",
		}, labels).WithP(labelsAndValues...),
		ValidatorLastSignedHeight: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "validator_last_signed_height",
			Help:      "Last signed height for a validator",
		}, append(labels, "validator_address")).WithP(labelsAndValues...),
		ValidatorMissedBlocks: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "validator_missed_blocks",
			Help:      "Total missed blocks for a validator",
		}, append(labels, "validator_address")).WithP(labelsAndValues...),
		ValidatorsPower: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "validators_power",
			Help:      "Total power of all validators.",
		}, labels).WithP(labelsAndValues...),
		ValidatorPower: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "validator_power",
			Help:      "Power of a validator",
		}, append(labels, "validator_address")).WithP(labelsAndValues...),
		MissingValidators: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "missing_validators",
			Help:      "Number of validators who did not sign.",
		}, labels).WithP(labelsAndValues...),
		MissingValidatorsPower: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "missing_validators_power",
			Help:      "Total power of the missing validators.",
		}, labels).WithP(labelsAndValues...),
		ByzantineValidators: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "byzantine_validators",
			Help:      "Number of validators who tried to double sign.",
		}, labels).WithP(labelsAndValues...),
		ByzantineValidatorsPower: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "byzantine_validators_power",
			Help:      "Total power of the byzantine validators.",
		}, labels).WithP(labelsAndValues...),
		BlockIntervalSeconds: prometheus.NewHistogramFrom(stdprometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "block_interval_seconds",
			Help:      "Time between this and the last block.",
		}, labels).WithP(labelsAndValues...),
		BlockTimeSeconds: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "block_time_seconds",
			Help:      "Duration between this block and the preceding one.",
		}, labels).WithP(labelsAndValues...),
		NumTxs: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "num_txs",
			Help:      "Number of transactions.",
		}, labels).WithP(labelsAndValues...),
		BlockSizeBytes: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "block_size_bytes",
			Help:      "Size of the block.",
		}, labels).WithP(labelsAndValues...),
		TotalTxs: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "total_txs",
			Help:      "Total number of transactions.",
		}, labels).WithP(labelsAndValues...),
		CommittedHeight: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "latest_block_height",
			Help:      "The latest block height.",
		}, labels).WithP(labelsAndValues...),
		FastSyncing: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "fast_syncing",
			Help:      "Whether or not a node is fast syncing. 1 if yes, 0 if no.",
		}, labels).WithP(labelsAndValues...),
		StateSyncing: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "state_syncing",
			Help:      "Whether or not a node is state syncing. 1 if yes, 0 if no.",
		}, labels).WithP(labelsAndValues...),
		BlockParts: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "block_parts",
			Help:      "Number of blockparts transmitted by peer.",
		}, append(labels, "peer_id")).WithP(labelsAndValues...),
		BlockGossipPartsReceived: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "block_gossip_parts_received",
			Help: "Number of block parts received by the node, labeled by whether the " +
				"part was relevant to the block the node was currently gathering or not.",
		}, append(labels, "matches_current")).WithP(labelsAndValues...),
		StepDuration: prometheus.NewHistogramFrom(stdprometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "step_duration_seconds",
			Help:      "Time spent per step.",
			Buckets:   stdprometheus.ExponentialBucketsRange(0.1, 100, 8),
		}, append(labels, "step")).WithP(labelsAndValues...),
		QuorumPrevoteMessageDelay: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "quorum_prevote_message_delay",
			Help: "Difference in seconds between the proposal timestamp and the timestamp " +
				"of the latest prevote that achieved a quorum in the prevote step.",
		}, labels).WithP(labelsAndValues...),
		FullPrevoteMessageDelay: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "full_prevote_message_delay",
			Help: "Difference in seconds between the proposal timestamp and the timestamp " +
				"of the latest prevote that achieved 100% of the voting power in the prevote step.",
		}, labels).WithP(labelsAndValues...),
		ApplicationRejectedProposals: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "application_rejected_proposals",
			Help:      "Number of proposals rejected by the application",
		}, labels).WithP(labelsAndValues...),
		TimedOutProposals: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "timed_out_proposals",
			Help:      "Number of proposals that failed to be received in time",
		}, labels).WithP(labelsAndValues...),
	}
}

// NopMetrics returns no-op Metrics.
func NopMetrics() *Metrics {
	return &Metrics{
		Height:      discard.NewPGauge(),
		StartHeight: discard.NewPGauge(),

		ValidatorLastSignedHeight: discard.NewPGauge(),

		Rounds:       discard.NewPGauge(),
		StepDuration: discard.NewPHistogram(),

		Validators:               discard.NewPGauge(),
		ValidatorsPower:          discard.NewPGauge(),
		ValidatorPower:           discard.NewPGauge(),
		ValidatorMissedBlocks:    discard.NewPGauge(),
		MissingValidators:        discard.NewPGauge(),
		MissingValidatorsPower:   discard.NewPGauge(),
		ByzantineValidators:      discard.NewPGauge(),
		ByzantineValidatorsPower: discard.NewPGauge(),

		BlockIntervalSeconds: discard.NewPHistogram(),
		BlockTimeSeconds:     discard.NewPGauge(),

		NumTxs:                       discard.NewPGauge(),
		BlockSizeBytes:               discard.NewPGauge(),
		TotalTxs:                     discard.NewPGauge(),
		CommittedHeight:              discard.NewPGauge(),
		FastSyncing:                  discard.NewPGauge(),
		StateSyncing:                 discard.NewPGauge(),
		BlockParts:                   discard.NewPCounter(),
		BlockGossipPartsReceived:     discard.NewPCounter(),
		QuorumPrevoteMessageDelay:    discard.NewPGauge(),
		FullPrevoteMessageDelay:      discard.NewPGauge(),
		ApplicationRejectedProposals: discard.NewPCounter(),
		TimedOutProposals:            discard.NewPCounter(),
	}
}

func (m *Metrics) MarkRound(r int32) {
	m.Rounds.Set(float64(r))
}

func (m *Metrics) MarkStep(s cstypes.RoundStepType) {
	if !m.stepStart.IsZero() {
		stepTime := time.Since(m.stepStart).Seconds()
		stepName := strings.TrimPrefix(s.String(), "RoundStep")
		m.StepDuration.WithP("step", stepName).Observe(stepTime)
	}
	m.stepStart = time.Now()
}
