package propagation

import (
	"context"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/p2p"
	"time"
)

func startPeerProcessing(ctx context.Context, blockProp *Reactor, peer p2p.Peer, haveChan <-chan *proptypes.HaveParts, commitmentChan <-chan struct{}) {
	ticker := time.NewTicker(RetryTime)
	for {
		select {
		case <-ctx.Done():
			return
		case have, has := <-haveChan:
			if !blockProp.started.Load() {
				continue
			}
			cb, _, _, has := blockProp.getAllState(have.Height, have.Round, false)
			if !has {
				continue
			}
			requestUsingHaves(blockProp, peer.ID(), have.Height, have.Round, have.BitArray(int(cb.Proposal.BlockID.PartSetHeader.Total)))
		case <-ticker.C:
			if !blockProp.started.Load() {
				continue
			}
			retryUnfinishedHeights(blockProp)
		case <-commitmentChan:
			if !blockProp.started.Load() {
				continue
			}
			retryUnfinishedHeights(blockProp)
		}
	}
}

func retryUnfinishedHeights(prop *Reactor) {
	unfinishedHeights := prop.unfinishedHeights()
	prop.Logger.Info("unfinished heights", "unfinishedHeights", len(unfinishedHeights))
	for _, unfinishedHeight := range unfinishedHeights {
		retryUnfinishedHeight(
			prop,
			unfinishedHeight.compactBlock.Proposal.Height,
			unfinishedHeight.compactBlock.Proposal.Round,
		)
	}
}

func retryUnfinishedHeight(blockProp *Reactor, height int64, round int32) {
	_, parts, _, has := blockProp.getAllState(height, round, true)
	if !has {
		// this shouldn't happen
		blockProp.Logger.Error("couldn't find state for proposal", "height", height, "round", round)
		return
	}
	if parts.IsComplete() {
		return
	}
	missing := parts.MissingOriginal()
	for _, peer := range shuffle(blockProp.getPeers()) {
		if missing.IsEmpty() {
			break
		}
		want := proptypes.WantParts{
			Parts:  missing,
			Height: height,
			Round:  round,
			Prove:  true,
		}
		e := p2p.Envelope{
			ChannelID: WantChannel,
			Message:   want.ToProto(),
		}

		if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Error("failed to send want part", "peer", peer, "height", height, "round", round)
			continue
		}
		/*
			schema.WriteBlockPartState(
					blockProp.traceClient,
					height,
					round,
					hc.GetTrueIndices(),
					false,
					string(peer),
					schema.Haves,
				)
				// keep track of the parts that this node has requested.
					p.AddRequests(height, round, hc)
		*/
	}
}

// requestUsingHaves sends a request for missing parts from a peer based on what the peer has advertised.
// It computes the parts to request by subtracting the known parts from the peer's advertised parts.
// The method updates the sent wants for tracking and returns the updated missing parts after the request.
// Returns the remaining missing parts.
func requestUsingHaves(blockProp *Reactor, peerID p2p.ID, height int64, round int32, missing *bits.BitArray) *bits.BitArray {
	if missing == nil {
		return nil
	}
	peer := blockProp.getPeer(peerID)
	blockProp.Logger.Info("getting received haves")
	peerHaves, has := peer.GetHaves(height, round)
	if !has {
		return missing
	}
	notMissed := missing.Not()
	toRequest := peerHaves
	if notMissed != nil {
		toRequest = peerHaves.Sub(notMissed)
	}
	want := proptypes.WantParts{
		Parts:  missing, // FIXME don't use straight missing!
		Height: height,
		Round:  round,
		Prove:  true,
	}
	e := p2p.Envelope{
		ChannelID: WantChannel,
		Message:   want.ToProto(),
	}

	if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
		blockProp.Logger.Error("failed to send want part", "peer", peer, "height", height, "round", round)
		return missing
	}
	/*
		schema.WriteBlockPartState(
				blockProp.traceClient,
				height,
				round,
				hc.GetTrueIndices(),
				false,
				string(peer),
				schema.Haves,
			)
			// keep track of the parts that this node has requested.
				p.AddRequests(height, round, hc)
	*/
	// TODO keep track of requests
	// TODO only request from peer with haves
	// TODO keep track of the number of requests
	// TODO keep track of the peer's maximum number of requests
	// TODO use the latest height during catchup
	return missing.Sub(toRequest)
}
