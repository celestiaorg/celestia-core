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
func (blockProp *Reactor) retryWants() {
	if !blockProp.started.Load() {
		return
	}
	data := blockProp.unfinishedHeights()
	peers := blockProp.getPeers()
	for _, prop := range data {
		height, round := prop.compactBlock.Proposal.Height, prop.compactBlock.Proposal.Round

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
			if peer.consensusPeerState.GetHeight() < height-1 {
				blockProp.Logger.Debug("retryWants: skipping peer")
				continue
			}
			mc := missing.Copy()

			reqs, has := peer.GetRequests(height, round)
			if has {
				mc = mc.Sub(reqs)
			}

			if mc.IsEmpty() {
				continue
			}

			missingPartsCount := countRemainingParts(int(prop.block.Total()), len(prop.block.BitArray().GetTrueIndices()))
			if missingPartsCount == 0 {
				continue
			}
			e := p2p.Envelope{
				ChannelID: WantChannel,
				Message: &protoprop.WantParts{
					Parts:             *mc.ToProto(),
					Height:            height,
					Round:             round,
					Prove:             true,
					MissingPartsCount: missingPartsCount,
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

	combinedSet := proptypes.NewCombinedPartSetFromOriginal(types.NewPartSetFromHeader(*psh, types.BlockPartSizeBytes), true)

	if blockProp.proposals[height][round] != nil {
		existingPSH := blockProp.proposals[height][round].block.Original().Header()
		if existingPSH.Total == psh.Total && bytes.Equal(existingPSH.Hash, psh.Hash) {
			return
		}
		blockProp.Logger.Error("replacing existing proposal with new one", "height", height, "round", round, "psh", psh, "existingPSH", existingPSH)
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

	// increment the local copies of the height and round
	blockProp.height = height
	blockProp.round = 0
	blockProp.ticker.Reset(RetryTime)
	go blockProp.retryWants()
}

func shuffle[T any](slice []T) []T {
	n := len(slice)
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}

// applyCachedProposalIfAvailable checks for cached proposals at the current height/round
// and applies the first valid one. Called automatically after SetProposer or SetHeightAndRound
// to enable fast catchup when a node falls behind.
//
// This function iterates through ALL peers' cached proposals for the current height/round,
// trying each one until it finds a valid proposal. This ensures a single invalid proposal
// from one peer doesn't block valid proposals from other peers.
func (blockProp *Reactor) applyCachedProposalIfAvailable() {
	blockProp.pmtx.Lock()
	currentHeight := blockProp.height
	currentRound := blockProp.round
	blockProp.pmtx.Unlock()

	// Check if we already have a proposal for this height/round (normal case)
	_, _, has := blockProp.GetProposal(currentHeight, currentRound)
	if has {
		return // Already have proposal, no need to check cache
	}

	// Iterate through all peers looking for a valid cached proposal
	peers := blockProp.getPeers()
	for _, peer := range peers {
		if peer == nil {
			continue
		}

		cb := peer.GetUnverifiedProposal(currentHeight)
		if cb == nil {
			continue // This peer has no cached proposal for this height
		}

		// Skip proposals for different rounds - they'll be tried when we advance
		if cb.Proposal.Round != currentRound {
			continue
		}

		// Try to validate this proposal
		if err := blockProp.validateCompactBlock(cb); err != nil {
			blockProp.Logger.Debug("cached proposal failed validation",
				"height", currentHeight, "round", currentRound, "peer", peer.peer.ID(), "err", err)
			continue // Try next peer's cached proposal
		}

		// Found a valid proposal - apply it
		blockProp.Logger.Info("applying cached proposal from catchup",
			"height", currentHeight, "round", cb.Proposal.Round, "peer", peer.peer.ID())

		blockProp.handleCachedCompactBlock(cb)

		// Clean up the cache entry for this peer
		peer.DeleteUnverifiedProposal(currentHeight)
		return
	}
}

// handleCachedCompactBlock processes a verified cached compact block.
// Similar to handleCompactBlock but skips validation (already verified) and triggers immediate catchup.
func (blockProp *Reactor) handleCachedCompactBlock(cb *proptypes.CompactBlock) {
	blockProp.Logger.Info("applying cached compact block", "height", cb.Proposal.Height, "round", cb.Proposal.Round)

	// generate (and cache) the proofs from the partset hashes in the compact block
	_, err := cb.Proofs()
	if err != nil {
		blockProp.Logger.Error("cached compact block has invalid proofs", "err", err.Error())
		return
	}

	// Send proposal to consensus reactor
	select {
	case <-blockProp.ctx.Done():
		return
	case blockProp.proposalChan <- ProposalAndSrc{
		Proposal: cb.Proposal,
		From:     blockProp.self, // From self since it's from cache
	}:
	}

	// Add to proposal cache
	added := blockProp.AddProposal(cb)
	if !added {
		blockProp.Logger.Debug("cached proposal already exists", "height", cb.Proposal.Height, "round", cb.Proposal.Round)
		return
	}

	// Mark as catchup to skip parity requests in retryWants
	blockProp.pmtx.Lock()
	if prop := blockProp.proposals[cb.Proposal.Height][cb.Proposal.Round]; prop != nil {
		prop.catchup = true
	}
	blockProp.pmtx.Unlock()

	// Recover any parts from mempool
	blockProp.recoverPartsFromMempool(cb)

	// Immediately trigger part requests (like AddCommitment)
	blockProp.ticker.Reset(RetryTime)
	go blockProp.retryWants()
}
