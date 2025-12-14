package propagation

import (
	"bytes"
	"fmt"
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
			if peer.consensusPeerState.GetHeight() < height {
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

// fmtMissingHeights renders a compact list of heights (e.g. "[1029 1030 1031]")
// from a slice of MissingPartsInfo for logging.
func fmtMissingHeights(missing []*MissingPartsInfo) string {
	heights := make([]int64, 0, len(missing))
	for _, m := range missing {
		heights = append(heights, m.Height)
	}
	return fmt.Sprint(heights)
}

// requestMissingParts uses the PendingBlocksManager to request missing parts
// for all tracked pending blocks. This is the unified request routine that
// replaces the old retryWants for both catchup and blocksync scenarios.
//
// It iterates through pending blocks (ordered by height - lowest first) and
// requests missing parts from peers that are at or above each block's height.
func (blockProp *Reactor) requestMissingParts() {
	fmt.Println("requesting missing parts call")
	if !blockProp.started.Load() {
		return
	}

	if blockProp.pendingBlocks == nil {
		// Fall back to legacy retryWants if PendingBlocksManager not configured
		blockProp.retryWants()
		return
	}

	// Get missing parts info for up to 100 blocks, ordered by height (lowest first)
	missing := blockProp.pendingBlocks.GetMissingParts(100)
	if len(missing) == 0 {
		return
	}

	peers := blockProp.getPeers()
	if len(peers) == 0 {
		// We know we're missing parts but have nobody to request from – this is
		// exactly the situation that can stall a node after it reconnects. Keep it
		// at debug to avoid spam when the peer set is intentionally empty (tests).
		blockProp.Logger.Debug("cannot request missing parts: no connected peers", "missing_heights", fmtMissingHeights(missing))
		return
	}

	for _, info := range missing {
		fmt.Println("found missing info", info)
		// Shuffle peers to distribute load
		peers = shuffle(peers)

		pendingBlock := blockProp.pendingBlocks.GetBlock(info.Height)
		haveParts := 0
		if pendingBlock != nil {
			haveParts = len(pendingBlock.Parts.BitArray().GetTrueIndices())
		}
		blockProp.Logger.Debug("requesting missing parts", "height", info.Height, "round", info.Round,
			"have_parts", haveParts, "missing_parts", len(info.Missing.GetTrueIndices()), "needs_proofs", info.NeedsProofs,
			"peer_candidates", len(peers))

		for _, peer := range peers {
			// Skip peers that don't have this height yet
			if peer.consensusPeerState.GetHeight() < info.Height {
				blockProp.Logger.Debug("skip want: peer below height", "peer", peer.peer.ID(), "peer_height", peer.consensusPeerState.GetHeight(), "want_height", info.Height)
				continue
			}

			// Get what parts we still need to request (subtract already requested)
			mc := info.Missing.Copy()

			reqs, has := peer.GetRequests(info.Height, info.Round)
			if has {
				mc = mc.Sub(reqs)
			}

			if mc.IsEmpty() {
				blockProp.Logger.Debug("skip want: nothing left to request from peer", "peer", peer.peer.ID(), "height", info.Height, "round", info.Round)
				continue
			}

			// Calculate how many parts we still need to decode
			// (need half the total parts with erasure coding)
			pendingBlock := blockProp.pendingBlocks.GetBlock(info.Height)
			if pendingBlock == nil {
				continue
			}
			haveParts := len(pendingBlock.Parts.BitArray().GetTrueIndices())
			missingPartsCount := countRemainingParts(int(info.Total), haveParts)
			if missingPartsCount == 0 {
				blockProp.Logger.Debug("skip want: already have enough parts to decode", "height", info.Height, "round", info.Round, "have_parts", haveParts)
				continue
			}

			// Build and send WantParts message
			e := p2p.Envelope{
				ChannelID: WantChannel,
				Message: &protoprop.WantParts{
					Parts:             *mc.ToProto(),
					Height:            info.Height,
					Round:             info.Round,
					Prove:             info.NeedsProofs, // Use proof requirement from manager
					MissingPartsCount: missingPartsCount,
				},
			}

			if !peer.peer.TrySend(e) {
				blockProp.Logger.Error("failed to send want part", "peer", peer.peer.ID(),
					"height", info.Height, "round", info.Round)
				continue
			}

			fmt.Println("want parts sent", info.Height, mc)

			blockProp.Logger.Debug("sent want parts", "peer", peer.peer.ID(), "height", info.Height, "round", info.Round,
				"requested_parts", len(mc.GetTrueIndices()), "missing_parts_count", missingPartsCount)

			schema.WriteCatchupRequest(blockProp.traceClient, info.Height, info.Round,
				mc.String(), string(peer.peer.ID()))

			// Limit requests per part to avoid over-requesting
			for _, partIndex := range mc.GetTrueIndices() {
				reqLimit := ReqLimit(int(info.Total))
				reqsCount := blockProp.countRequests(info.Height, info.Round, partIndex)
				if len(reqsCount) >= reqLimit {
					info.Missing.SetIndex(partIndex, false)
				}
			}

			// Track which requests we've made
			peer.AddRequests(info.Height, info.Round, mc)
		}
	}
}
