package propagation

import (
	"math/rand"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	"github.com/tendermint/tendermint/types"
)

func (blockProp *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	blockProp.Logger.Info("adding commitment", "height", height, "round", round, "psh", psh)
	blockProp.pmtx.Lock()

	blockProp.Logger.Info("added commitment", "height", height, "round", round)
	schema.WriteGap(blockProp.traceClient, height, round)

	if blockProp.proposals[height] == nil {
		blockProp.proposals[height] = make(map[int32]*proposalData)
	}

	combinedSet := proptypes.NewCombinedPartSetFromOriginal(types.NewPartSetFromHeader(*psh), true)

	if blockProp.proposals[height][round] != nil {
		return
	}

	cb := proptypes.CompactBlock{
		Proposal: types.Proposal{
			Height: height,
			Round:  round,
		},
	}
	blockProp.proposals[height][round] = &proposalData{
		compactBlock: &cb,
		catchup:      true,
		block:        combinedSet,
		maxRequests:  bits.NewBitArray(int(psh.Total * 2)), // this assumes that the parity parts are the same size
	}
	blockProp.pmtx.Unlock()

	select {
	case <-blockProp.ctx.Done():
		return
	case blockProp.CommitmentChan <- &cb:
	}
}

func shuffle[T any](slice []T) []T {
	n := len(slice)
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}
