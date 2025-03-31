package propagation

import (
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/types"
)

// Propagator provides the necessary propagation mechanism for the
// consensus reactor and state.
type Propagator interface {
	GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool)
	ProposeBlock(proposal *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData)
	AddCommitment(height int64, round int32, psh *types.PartSetHeader)
	Prune(committedHeight int64)
	SetConsensusRound(height int64, round int32)
}
