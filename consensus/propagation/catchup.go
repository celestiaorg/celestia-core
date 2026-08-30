package propagation

import (
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

			// a peer at or above the target height may still not retain the
			// proposal: after repeated unanswered requests, release its
			// pending requests so other peers can serve those parts, and stop
			// asking it.
			if peer.CatchupAttempts(height) >= MaxCatchupAttempts {
				peer.ClearRequests(height, round)
				blockProp.Logger.Debug("retryWants: skipping peer with unanswered catch-up requests",
					"peer", peer.peer.ID(), "height", height)
				continue
			}
			mc := missing.Copy()

			reqs, has := peer.GetRequests(height, round)
			if has {
				mc = mc.Sub(reqs)
				// parts previously requested from this peer that are still
				// missing are unanswered: count them so that a peer that does
				// not serve this height is eventually skipped.
				if unanswered := prop.block.MissingOriginal().And(reqs); unanswered != nil && !unanswered.IsEmpty() {
					peer.AddCatchupAttempt(height)
				}
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
	stored, replaced := blockProp.addCommitment(height, round, psh)
	if !stored {
		return
	}
	if replaced {
		// the replaced proposal's part state is bound to a different identity
		// and must not be mixed with the committed part-set header.
		for _, peer := range blockProp.getPeers() {
			peer.DeleteRound(height, round)
		}
	}
	// service any Wants that arrived before this commitment.
	blockProp.servePendingWants(height, round)
	blockProp.ticker.Reset(RetryTime)
	go blockProp.retryWants()
}

// addCommitment stores a commitment-backed placeholder for the committed
// part-set header. stored is false when an identical placeholder already
// exists. replaced reports that a proposal with a conflicting identity was
// quarantined and replaced.
func (blockProp *Reactor) addCommitment(height int64, round int32, psh *types.PartSetHeader) (stored, replaced bool) {
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()

	schema.WriteGap(blockProp.traceClient, height, round)

	if blockProp.proposals[height] == nil {
		blockProp.proposals[height] = make(map[int32]*proposalData)
	}

	combinedSet := proptypes.NewCombinedPartSetFromOriginal(types.NewPartSetFromHeader(*psh, types.BlockPartSizeBytes), true)

	if existing := blockProp.proposals[height][round]; existing != nil {
		existingPSH := existing.block.Original().Header()
		if existingPSH.Equals(*psh) {
			return false, false
		}
		// the commitment is backed by +2/3 precommits, so it wins over a
		// conflicting proposal: quarantine the replaced identity.
		blockProp.Logger.Error("replacing existing proposal with new one", "height", height, "round", round, "psh", psh, "existingPSH", existingPSH)
		if !existing.commitmentBacked {
			blockProp.markRejected(height, round, existing.compactBlock.Proposal.BlockID)
		}
		replaced = true
	}

	blockProp.proposals[height][round] = &proposalData{
		compactBlock: &proptypes.CompactBlock{
			Proposal: types.Proposal{
				Height: height,
				Round:  round,
			},
		},
		catchup:          true,
		commitmentBacked: true,
		block:            combinedSet,
		maxRequests:      bits.NewBitArray(int(psh.Total * 2)), // this assumes that the parity parts are the same size
	}

	// increment the local copies of the height and round
	blockProp.height = height
	blockProp.round = 0
	return true, replaced
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

		applied, conflict := blockProp.handleCachedCompactBlock(cb)
		if conflict {
			// a conflicting proposal can never be applied at this height and
			// round: drop it so it is not retried.
			peer.DeleteUnverifiedProposal(currentHeight)
			continue
		}
		if applied {
			// Clean up the cache entry for this peer only if we successfully applied it.
			peer.DeleteUnverifiedProposal(currentHeight)
			return
		}
	}
}

// handleCachedCompactBlock processes a verified cached compact block.
// Similar to handleCompactBlock but skips validation (already verified) and triggers immediate catchup.
// applied reports that the cached block was applied. conflict reports that it
// conflicts with the identity already stored for its height and round.
func (blockProp *Reactor) handleCachedCompactBlock(cb *proptypes.CompactBlock) (applied, conflict bool) {
	blockProp.Logger.Info("applying cached compact block", "height", cb.Proposal.Height, "round", cb.Proposal.Round)

	// generate (and cache) the proofs from the partset hashes in the compact block
	_, err := cb.Proofs()
	if err != nil {
		blockProp.Logger.Error("cached compact block has invalid proofs", "err", err.Error())
		return false, false
	}

	// insert and identity-check the proposal before forwarding it to
	// consensus. A cached proposal conflicting with the stored identity for
	// this height and round must never be forwarded.
	added, conflict := blockProp.AddProposal(cb)
	if conflict {
		blockProp.Logger.Info("rejecting cached compact block conflicting with existing proposal",
			"height", cb.Proposal.Height, "round", cb.Proposal.Round)
		return false, true
	}
	if !added {
		blockProp.Logger.Debug("cached proposal already exists", "height", cb.Proposal.Height, "round", cb.Proposal.Round)
	}

	// Send proposal to consensus reactor
	select {
	case <-blockProp.ctx.Done():
		return false, false
	case blockProp.proposalChan <- ProposalAndSrc{
		Proposal: cb.Proposal,
		From:     blockProp.self, // From self since it's from cache
	}:
	}

	propFound := false
	blockProp.pmtx.Lock()
	if props, ok := blockProp.proposals[cb.Proposal.Height]; ok {
		if prop := props[cb.Proposal.Round]; prop != nil {
			// Mark as catchup to skip parity requests in retryWants
			prop.catchup = true
			propFound = true
		}
	}
	blockProp.pmtx.Unlock()
	if !propFound {
		return false, false
	}

	// Recover any parts from mempool
	blockProp.recoverPartsFromMempool(cb)

	// service any Wants that arrived before this compact block.
	blockProp.servePendingWants(cb.Proposal.Height, cb.Proposal.Round)

	// Immediately trigger part requests (like AddCommitment)
	blockProp.ticker.Reset(RetryTime)
	go blockProp.retryWants()
	return true, false
}
