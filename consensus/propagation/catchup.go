package propagation

import (
	"math/rand"
	"time"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/p2p"
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

// retryWants ensure that all data for all unpruned compact blocks is requested.
//
// todo: add a request limit for each part to avoid downloading the block too
// many times. atm, this code will request the same part from every peer.
func (blockProp *Reactor) retryWants(currentHeight int64, currentRound int32) {
	data := blockProp.dumpAll()
	for _, prop := range data {
		height, round := prop.compactBlock.Proposal.Height, prop.compactBlock.Proposal.Round

		if height == currentHeight && round == currentRound {
			continue
		}

		if prop.block.IsComplete() {
			continue
		}

		peers := blockProp.getPeers()
		peers = Shuffle(peers)
		last := len(peers) - 1
		if 3 < last {
			last = 3
		}
		peers = peers[:last] // only request from half of the peers
		for _, peer := range peers {
			missing := prop.block.BitArray().Not()

			reqs, has := peer.GetRequests(height, round)
			if has {
				missing = missing.Sub(reqs)
			}

			if missing.IsEmpty() {
				continue
			}

			e := p2p.Envelope{
				ChannelID: WantChannel,
				Message: &protoprop.WantParts{
					Parts:  *missing.ToProto(),
					Height: height,
					Round:  round,
				},
			}

			if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
				blockProp.Logger.Error("failed to send want part", "peer", peer, "height", height, "round", round)
				continue
			}

			peer.AddRequests(height, round, missing)
		}
	}
}

func Shuffle[T any](slice []T) (result []T) {
	rand.Seed(time.Now().UnixNano()) // Seed the random number generator
	n := len(slice)
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}

func (blockProp *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()

	if blockProp.proposals[height] == nil {
		blockProp.proposals[height] = make(map[int32]*proposalData)
	}

	combinedSet := proptypes.NewCombinedPartSetFromOriginal(types.NewPartSetFromHeader(*psh), true)

	if blockProp.proposals[height][round] != nil {
		return
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
}
