package propagation

import (
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// Propagator provides the necessary propagation mechanism for the
// consensus reactor and state.
type Propagator interface {
	GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool)
	ProposeBlock(proposal *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData)
	AddCommitment(height int64, round int32, psh *types.PartSetHeader)
	Prune(committedHeight int64)
	SetHeightAndRound(height int64, round int32)
	StartProcessing()
	SetProposer(proposer crypto.PubKey)
	GetPartChan() <-chan types.PartInfo
	GetProposalChan() <-chan ProposalAndSrc
}

type ProposalAndSrc struct {
	Proposal types.Proposal
	From     p2p.ID
}

// PeerStateEditor defines methods for editing peer state from the propagation layer.
// This interface allows the propagation reactor to update peer state without causing
// import cycles with the consensus reactor.
type PeerStateEditor interface {
	// SetHasProposal sets the given proposal as known for the peer.
	SetHasProposal(proposal *types.Proposal)

	// SetHasProposalBlockPart sets the given block part index as known for the peer.
	SetHasProposalBlockPart(height int64, round int32, index int)
}

type noOpPSE struct{}

func (noOpPSE) SetHasProposal(_ *types.Proposal)                {}
func (noOpPSE) SetHasProposalBlockPart(_ int64, _ int32, _ int) {}

// NoOpPropagator is a no-operation implementation of the Propagator interface.
// It provides empty implementations of all methods, effectively disabling
// the propagation functionality when used in place of the actual propagation reactor.
type NoOpPropagator struct{}

// NewNoOpPropagator creates a new no-operation Propagator.
func NewNoOpPropagator() *NoOpPropagator {
	return &NoOpPropagator{}
}

var _ Propagator = &NoOpPropagator{}

func (nop *NoOpPropagator) GetProposal(_ int64, _ int32) (*types.Proposal, *types.PartSet, bool) {
	return nil, nil, false
}

func (nop *NoOpPropagator) ProposeBlock(_ *types.Proposal, _ *types.PartSet, _ []proptypes.TxMetaData) {
}

func (nop *NoOpPropagator) AddCommitment(_ int64, _ int32, _ *types.PartSetHeader) {
}

func (nop *NoOpPropagator) Prune(_ int64) {
}

func (nop *NoOpPropagator) SetHeightAndRound(_ int64, _ int32) {
}

func (nop *NoOpPropagator) StartProcessing() {
}

func (nop *NoOpPropagator) SetProposer(_ crypto.PubKey) {
}

func (nop *NoOpPropagator) GetPartChan() <-chan types.PartInfo {
	return nil
}

func (nop *NoOpPropagator) GetProposalChan() <-chan ProposalAndSrc {
	return nil
}
