package consensus

import (
	"strings"
	"time"

	"github.com/go-kit/kit/metrics"

	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	types "github.com/cometbft/cometbft/types"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "consensus"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by this package.
type Metrics struct {
	// Height of the chain.
	Height metrics.Gauge

	// Last height signed by this validator if the node is a validator.
	ValidatorLastSignedHeight metrics.Gauge `metrics_labels:"validator_address"`

	// Number of rounds.
	Rounds metrics.Gauge

	// Histogram of round duration.
	RoundDurationSeconds metrics.Histogram `metrics_buckettype:"exprange" metrics_bucketsizes:"0.1, 100, 8"`

	// Number of validators.
	Validators metrics.Gauge
	// Total power of all validators.
	ValidatorsPower metrics.Gauge
	// Power of a validator.
	ValidatorPower metrics.Gauge `metrics_labels:"validator_address"`
	// Amount of blocks missed per validator.
	ValidatorMissedBlocks metrics.Gauge `metrics_labels:"validator_address"`
	// Number of validators who did not sign.
	MissingValidators metrics.Gauge
	// Total power of the missing validators.
	MissingValidatorsPower metrics.Gauge
	// Number of validators who tried to double sign.
	ByzantineValidators metrics.Gauge
	// Total power of the byzantine validators.
	ByzantineValidatorsPower metrics.Gauge

	// Time between this and the last block.
	BlockIntervalSeconds metrics.Histogram

	// Number of transactions.
	NumTxs metrics.Gauge
	// Size of the block.
	BlockSizeBytes metrics.Gauge
	// Size of the chain in bytes.
	ChainSizeBytes metrics.Counter
	// Total number of transactions.
	TotalTxs metrics.Gauge
	// The latest block height.
	CommittedHeight metrics.Gauge `metrics_name:"latest_block_height"`

	// Number of block parts transmitted by each peer.
	BlockParts metrics.Counter `metrics_labels:"peer_id"`

	// Number of times we received a duplicate block part
	DuplicateBlockPart metrics.Counter

	// Number of times we received a duplicate vote
	DuplicateVote metrics.Counter

	// Histogram of durations for each step in the consensus protocol.
	StepDurationSeconds metrics.Histogram `metrics_labels:"step" metrics_buckettype:"exprange" metrics_bucketsizes:"0.1, 100, 8"`
	stepStart           time.Time

	// Number of block parts received by the node, separated by whether the part
	// was relevant to the block the node is trying to gather or not.
	BlockGossipPartsReceived metrics.Counter `metrics_labels:"matches_current"`

	// QuroumPrevoteMessageDelay is the interval in seconds between the proposal
	// timestamp and the timestamp of the earliest prevote that achieved a quorum
	// during the prevote step.
	//
	// To compute it, sum the voting power over each prevote received, in increasing
	// order of timestamp. The timestamp of the first prevote to increase the sum to
	// be above 2/3 of the total voting power of the network defines the endpoint
	// the endpoint of the interval. Subtract the proposal timestamp from this endpoint
	// to obtain the quorum delay.
	//metrics:Interval in seconds between the proposal timestamp and the timestamp of the earliest prevote that achieved a quorum.
	QuorumPrevoteDelay metrics.Gauge `metrics_labels:"proposer_address"`

	// FullPrevoteDelay is the interval in seconds between the proposal
	// timestamp and the timestamp of the latest prevote in a round where 100%
	// of the voting power on the network issued prevotes.
	//metrics:Interval in seconds between the proposal timestamp and the timestamp of the latest prevote in a round where all validators voted.
	FullPrevoteDelay metrics.Gauge `metrics_labels:"proposer_address"`

	// VoteExtensionReceiveCount is the number of vote extensions received by this
	// node. The metric is annotated by the status of the vote extension from the
	// application, either 'accepted' or 'rejected'.
	VoteExtensionReceiveCount metrics.Counter `metrics_labels:"status"`

	// ProposalReceiveCount is the total number of proposals received by this node
	// since process start.
	// The metric is annotated by the status of the proposal from the application,
	// either 'accepted' or 'rejected'.
	ProposalReceiveCount metrics.Counter `metrics_labels:"status"`

	// ProposalCreationCount is the total number of proposals created by this node
	// since process start.
	// The metric is annotated by the status of the proposal from the application,
	// either 'accepted' or 'rejected'.
	ProposalCreateCount metrics.Counter

	// RoundVotingPowerPercent is the percentage of the total voting power received
	// with a round. The value begins at 0 for each round and approaches 1.0 as
	// additional voting power is observed. The metric is labeled by vote type.
	RoundVotingPowerPercent metrics.Gauge `metrics_labels:"vote_type"`

	// LateVotes stores the number of votes that were received by this node that
	// correspond to earlier heights and rounds than this node is currently
	// in.
	LateVotes metrics.Counter `metrics_labels:"vote_type"`

	// StartHeight is the height at which metrics began.
	StartHeight metrics.Gauge
	// BlockTimeSeconds is the duration between this block and the preceding one.
	BlockTimeSeconds metrics.Gauge
	// ApplicationRejectedProposals is the number of proposals rejected by the application.
	ApplicationRejectedProposals metrics.Counter
	// TimedOutProposals is the number of proposals that failed to be received in time.
	TimedOutProposals metrics.Counter
}

func (m *Metrics) MarkProposalProcessed(accepted bool) {
	status := "accepted"
	if !accepted {
		status = "rejected"
	}
	m.ProposalReceiveCount.With("status", status).Add(1)
}

func (m *Metrics) MarkVoteExtensionReceived(accepted bool) {
	status := "accepted"
	if !accepted {
		status = "rejected"
	}
	m.VoteExtensionReceiveCount.With("status", status).Add(1)
}

func (m *Metrics) MarkVoteReceived(vt cmtproto.SignedMsgType, power, totalPower int64) {
	p := float64(power) / float64(totalPower)
	n := types.SignedMsgTypeToShortString(vt)
	m.RoundVotingPowerPercent.With("vote_type", n).Add(p)
}

func (m *Metrics) MarkRound(r int32, st time.Time) {
	m.Rounds.Set(float64(r))
	roundTime := time.Since(st).Seconds()
	m.RoundDurationSeconds.Observe(roundTime)

	pvn := types.SignedMsgTypeToShortString(cmtproto.PrevoteType)
	m.RoundVotingPowerPercent.With("vote_type", pvn).Set(0)

	pcn := types.SignedMsgTypeToShortString(cmtproto.PrecommitType)
	m.RoundVotingPowerPercent.With("vote_type", pcn).Set(0)
}

func (m *Metrics) MarkLateVote(vt cmtproto.SignedMsgType) {
	n := types.SignedMsgTypeToShortString(vt)
	m.LateVotes.With("vote_type", n).Add(1)
}

func (m *Metrics) MarkStep(s cstypes.RoundStepType) {
	if !m.stepStart.IsZero() {
		stepTime := time.Since(m.stepStart).Seconds()
		stepName := strings.TrimPrefix(s.String(), "RoundStep")
		m.StepDurationSeconds.With("step", stepName).Observe(stepTime)
	}
	m.stepStart = time.Now()
}
