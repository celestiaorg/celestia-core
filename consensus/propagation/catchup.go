package propagation

import (
	"math/rand"

	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
)

// retryWants ensures that all data for all unpruned blocks is requested.
// This is the unified catchup mechanism that uses PendingBlocksManager.
func (blockProp *Reactor) retryWants() {
	if !blockProp.started.Load() {
		return
	}

	missingParts := blockProp.pendingBlocks.GetMissingParts()
	if len(missingParts) == 0 {
		return
	}

	peers := blockProp.getPeers()
	peers = shuffle(peers)

	for _, info := range missingParts {
		if len(info.MissingIndices) == 0 {
			continue
		}

		// Build a BitArray for the missing parts.
		missing := bits.NewBitArray(int(info.TotalParts * 2)) // Include space for parity.
		for _, idx := range info.MissingIndices {
			missing.SetIndex(idx, true)
		}

		schema.WriteRetries(blockProp.traceClient, info.Height, info.Round, missing.String())

		for _, peer := range peers {
			if peer.consensusPeerState.GetHeight() < info.Height {
				continue
			}

			mc := missing.Copy()

			reqs, has := peer.GetRequests(info.Height, info.Round)
			if has {
				mc = mc.Sub(reqs)
			}

			if mc.IsEmpty() {
				continue
			}

			var missingPartsCount int32
			if info.Catchup {
				// For catchup blocks, we need ALL original parts (no parity data available).
				missingPartsCount = int32(len(info.MissingIndices))
			} else {
				// For live blocks with parity, we only need enough for erasure decoding.
				missingPartsCount = countRemainingParts(int(info.TotalParts), int(info.TotalParts)-len(info.MissingIndices))
			}
			if missingPartsCount == 0 {
				continue
			}

			e := p2p.Envelope{
				ChannelID: WantChannel,
				Message: &protoprop.WantParts{
					Parts:             *mc.ToProto(),
					Height:            info.Height,
					Round:             info.Round,
					Prove:             true,
					MissingPartsCount: missingPartsCount,
				},
			}

			if !peer.peer.TrySend(e) {
				blockProp.Logger.Error("failed to send want part", "peer", peer.peer.ID(), "height", info.Height, "round", info.Round)
				continue
			}

			schema.WriteCatchupRequest(blockProp.traceClient, info.Height, info.Round, mc.String(), string(peer.peer.ID()))

			// Subtract the parts we just requested.
			for _, partIndex := range mc.GetTrueIndices() {
				reqLimit := ReqLimit(int(info.TotalParts))
				reqsCount := blockProp.countRequests(info.Height, info.Round, partIndex)
				if len(reqsCount) >= reqLimit {
					missing.SetIndex(partIndex, false)
				}
			}

			// Keep track of which requests we've made.
			peer.AddRequests(info.Height, info.Round, mc)
		}
	}
}

// AddCommitment adds a block for download based on a PartSetHeader from a commit.
// This handles the edge case where consensus learns about a committed block
// before headersync verifies the header.
//
// The actual download is triggered via the BlockAddedCallback mechanism,
// which resets the retry ticker and immediately triggers retryWants.
func (blockProp *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	blockProp.Logger.Info("adding commitment", "height", height, "round", round, "psh", psh)
	schema.WriteGap(blockProp.traceClient, height, round)

	// AddFromCommitment will trigger the onBlockAdded callback if a new block is added.
	blockProp.pendingBlocks.AddFromCommitment(height, round, psh)
}

// onBlockAdded is called by PendingBlocksManager when a new block is added.
// This triggers immediate catchup for commitment and header-sync sourced blocks.
func (blockProp *Reactor) onBlockAdded(height int64, source BlockSource) {
	// Only trigger immediate catchup for catchup-sourced blocks.
	// Compact blocks from live gossip don't need immediate catchup.
	if source == SourceCommitment || source == SourceHeaderSync {
		blockProp.Logger.Debug("block added, triggering catchup",
			"height", height, "source", source.String())
		blockProp.ticker.Reset(RetryTime)
		go blockProp.retryWants()
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
