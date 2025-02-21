package propagation

import (
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// Propagator provides the necessary propagation mechanism for the
// consensus reactor and state.
type Propagator interface {
	GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, *bits.BitArray, bool)
	ProposeBlock(proposal *types.Proposal, haves *bits.BitArray)
	HandleValidBlock(peer p2p.ID, height int64, round int32, psh types.PartSetHeader, exitEarly bool)
	HandleProposal(proposal *types.Proposal, from p2p.ID, haves *bits.BitArray)
}
