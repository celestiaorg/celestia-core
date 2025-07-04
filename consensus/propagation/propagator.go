package propagation

import (
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/types"
)

// Propagator provides the necessary propagation mechanism for the
// consensus reactor and state.
type Propagator interface {
	GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool)
	ProposeBlock(proposal *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData)
	AddCommitment(height int64, round int32, psh *types.PartSetHeader)
	Prune(committedHeight int64)
	SetConsensusRound(height int64, round int32)
	StartProcessing()
	SetProposer(proposer crypto.PubKey)
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
