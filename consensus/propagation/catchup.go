package propagation

import (
	"bytes"
	"math/rand"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
)

// retryWants ensure that all data for all unpruned compact blocks is requested.
func (blockProp *Reactor) retryWants(currentHeight int64) {
	if !blockProp.started.Load() {
		return
	}
	data := blockProp.unfinishedHeights()
	peers := blockProp.getPeers()
	for _, prop := range data {
		height, round := prop.compactBlock.Proposal.Height, prop.compactBlock.Proposal.Round

		// don't re-request parts for any round on the current height
		if height == currentHeight {
			continue
		}

		if prop.block.IsComplete() {
			continue
		}

		// only re-request original parts that are missing, not parity parts.
		missing := prop.block.MissingOriginal()
		if missing.IsEmpty() {
			blockProp.Logger.Error("no missing parts yet block is incomplete", "height", height, "round", round)
			continue
		}

		schema.WriteRetries(blockProp.traceClient, height, round, missing.String())

		// make requests from different peers
		peers = shuffle(peers)

		for _, peer := range peers {
			mc := missing.Copy()

			reqs, has := peer.GetRequests(height, round)
			if has {
				mc = mc.Sub(reqs)
			}

			if mc.IsEmpty() {
				continue
			}

			e := p2p.Envelope{
				ChannelID: WantChannel,
				Message: &protoprop.WantParts{
					Parts:  *mc.ToProto(),
					Height: height,
					Round:  round,
					Prove:  true,
				},
			}

			if !peer.peer.TrySend(e) {
				blockProp.Logger.Error("failed to send want part", "peer", peer.peer.ID(), "height", height, "round", round)
				continue
			}

			schema.WriteCatchupRequest(blockProp.traceClient, height, round, mc.String(), string(peer.peer.ID()))

			// subtract the parts we just requested
			for _, partIndex := range mc.GetTrueIndices() {
				reqLimit := ReqLimit(int(prop.block.Total()))
				reqsCount := blockProp.countRequests(height, round, partIndex)
				if len(reqsCount) >= reqLimit {
					missing.SetIndex(partIndex, false)
				}
			}

			// keep track of which requests we've made this attempt.
			peer.AddRequests(height, round, mc)
		}
	}
}

func (blockProp *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	blockProp.Logger.Info("adding commitment", "height", height, "round", round, "psh", psh)
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()

	schema.WriteGap(blockProp.traceClient, height, round)

	if blockProp.proposals[height] == nil {
		blockProp.proposals[height] = make(map[int32]*proposalData)
	}

	combinedSet := proptypes.NewCombinedPartSetFromOriginal(types.NewPartSetFromHeader(*psh), true)

	if blockProp.proposals[height][round] != nil {
		existingPSH := blockProp.proposals[height][round].block.Original().Header()
		if existingPSH.Total == psh.Total && bytes.Equal(existingPSH.Hash, psh.Hash) {
			return
		}
		blockProp.Logger.Info("replacing existing proposal with new one", "height", height, "round", round, "psh", psh, "existingPSH", existingPSH)
	}

	blockProp.proposals[height][round] = &proposalData{
		compactBlock: &proptypes.CompactBlock{
			Proposal: types.Proposal{
				Height: height,
				Round:  round,
			},
		},
		catchup:     true,
		block:       combinedSet,
		maxRequests: bits.NewBitArray(int(psh.Total * 2)), // this assumes that the parity parts are the same size
	}
	blockProp.Logger.Info("added commitment", "height", height, "round", round)

	// increment the local copies of the height and round
	blockProp.currentHeight = height + 1
	blockProp.currentRound = 0
	blockProp.consensusHeight = height
	blockProp.consensusRound = 0

	go blockProp.retryWants(height + 1)
}

func shuffle[T any](slice []T) []T {
	n := len(slice)
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}
