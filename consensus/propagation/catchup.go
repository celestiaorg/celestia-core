package propagation

import (
	"math/rand"
	"time"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

// retryWants ensure that all data for all unpruned compact blocks is requested.
//
// todo: add a request limit for each part to avoid downloading the block too
// many times. atm, this code will request the same part from every peer.
func (blockProp *Reactor) retryWants(height int64, round int32) {
	blockProp.Logger.Info("retry wants", "height", height, "round", round)
	if !blockProp.started.Load() {
		return
	}
	peers := blockProp.getPeers()

	for {
		blockProp.Logger.Info("catching up", "height", height, "round", round)
		_, combinedPartSet, _, has := blockProp.getAllState(height, round, false)
		if !has {
			blockProp.Logger.Error("height not found in state", "height", height, "round", round)
			return
		}
		if combinedPartSet.IsComplete() {
			return
		}
		blockProp.Logger.Info("not complete nigga", "height", height, "round", round)
		// only re-request original parts that are missing, not parity parts.
		missing := combinedPartSet.MissingOriginal()
		if missing.IsEmpty() {
			blockProp.Logger.Error("no missing parts yet block is incomplete", "height", height, "round", round)
			return
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

			if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
				blockProp.Logger.Error("failed to send want part", "peer", peer, "height", height, "round", round)
				continue
			}

			schema.WriteCatchupRequest(blockProp.traceClient, height, round, mc.String(), string(peer.peer.ID()))

			// keep track of which requests we've made this attempt.
			missing = missing.Sub(mc)
			peer.AddRequests(height, round, missing)
		}
		// sleep for sometime to get time for the network messages to arrive
		time.Sleep(6 * time.Second)
	}
}

func (blockProp *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	blockProp.Logger.Info("adding commitment", "height", height, "round", round, "psh", psh)
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()

	blockProp.Logger.Info("added commitment", "height", height, "round", round)
	schema.WriteGap(blockProp.traceClient, height, round)

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

	if height > blockProp.consensusHeight || round > blockProp.consensusRound {
		go blockProp.retryWants(height, round)
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
