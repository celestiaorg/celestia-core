package propagation

import (
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/p2p"
)

// sendPsh sends the part set header to the provided peer.
// TODO check if we still need this?
func (blockProp *Reactor) sendPsh(peer p2p.ID, height int64, round int32) bool {
	return true
}

// requestMissingBlocks is called when a node is catching up and needs to
// request all previous blocks from the compact block source peer.
func (blockProp *Reactor) requestMissingBlocks(targetHeight int64, targetRound int32) {
	// if we're missing heights, we will be requesting them from the connected peers
	for height := blockProp.currentHeight + 1; height < targetHeight; height++ {
		heightProposal, partSet, found := blockProp.GetProposal(height, -1)
		if !found {
			blockProp.Logger.Error("couldn't find proposal", "height", height, "round", -1)
			continue
		}
		if partSet.BitArray() == nil {
			panic("partSet is nil when getting proposal!")
		}
		missingParts := partSet.BitArray().Not()
		wantPart := &proptypes.WantParts{
			Parts:  missingParts,
			Height: height,
			Round:  heightProposal.Round,
		}
		blockProp.broadcastWants(wantPart, blockProp.self)
	}

	// if we're missing rounds for the current height, request them from all the peers
	for round := int32(0); round < targetRound; round++ {
		_, partSet, found := blockProp.GetProposal(targetHeight, round)
		if !found {
			blockProp.Logger.Error("couldn't find proposal", "height", targetHeight, "round", round)
			continue
		}
		if partSet.BitArray() == nil {
			panic("partSet is nil when getting proposal!")
		}
		missingParts := partSet.BitArray().Not()
		wantPart := &proptypes.WantParts{
			Parts:  missingParts,
			Height: targetHeight,
			Round:  round,
		}
		blockProp.broadcastWants(wantPart, blockProp.self)
	}
}
