package propagation

import (
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// Propagator provides the necessary propagation mechanism for the
// consensus reactor and state.
type Propagator interface {
	GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool)
	ProposeBlock(proposal *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData)
}
