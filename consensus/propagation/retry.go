package propagation

import (
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
)

// retryLastBlocks is a retry mechanism for previous heights where the reactor
// didn't receive all the data or none.
func (blockProp *Reactor) retryLastBlocks(targetHeight int64, targetRound int32) {
	// if we're missing heights, we will be requesting them from the connected peers
	for height := blockProp.currentHeight + 1; height < targetHeight; height++ {
		blockProp.Logger.Info("retrying to get block", "height", height)
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
		blockProp.Logger.Info("retrying to get round", "height", targetHeight, "round", round)
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
