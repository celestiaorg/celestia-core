package propagation

import (
	"context"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	propproto "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"time"
)

func startPeerProcessing(ctx context.Context, blockProp *Reactor, peer p2p.Peer, haveChan <-chan *proptypes.HaveParts, commitmentChan <-chan struct{}) {
	ticker := time.NewTicker(RetryTime)
	for {
		select {
		case <-ctx.Done():
			return
		case have, has := <-haveChan:
			if !blockProp.started.Load() || !has {
				continue
			}
			requestUsingHaves(blockProp, peer.ID(), have.Height, have.Round)
		case <-ticker.C:
			if !blockProp.started.Load() {
				continue
			}
			retryUnfinishedHeights(blockProp, peer.ID())
		case <-commitmentChan:
			if !blockProp.started.Load() {
				continue
			}
			retryUnfinishedHeights(blockProp, peer.ID())
		}
	}
}

func retryUnfinishedHeights(prop *Reactor, peerID p2p.ID) {
	p := prop.getPeer(peerID)
	if p == nil {
		// FIXME: shouldn't happen
		return
	}
	latestPeerHeight, latestPeerRound := p.latestHeight.Load(), p.latestRound.Load()
	unfinishedHeights := prop.unfinishedHeights()
	prop.Logger.Info("unfinished heights", "unfinishedHeights", len(unfinishedHeights))
	for _, unfinishedHeight := range unfinishedHeights {
		height, round := unfinishedHeight.compactBlock.Proposal.Height, unfinishedHeight.compactBlock.Proposal.Round
		switch {
		case latestPeerHeight > height || (latestPeerHeight == height && latestPeerRound > round):
			requestAllFromPeer(prop, peerID, height, round)
		case latestPeerHeight == height && latestPeerRound == round:
			requestUsingHaves(prop, peerID, height, round)
		}
	}
}

// requestUsingHaves sends a request for missing parts from a peer based on what the peer has advertised.
// It computes the parts to request by subtracting the known parts from the peer's advertised parts.
// The method updates the sent wants for tracking and returns the updated missing parts after the request.
// Returns the remaining missing parts.
func requestUsingHaves(blockProp *Reactor, peerID p2p.ID, height int64, round int32) *bits.BitArray {
	_, parts, fullReqs, has := blockProp.getAllState(height, round, false)
	if !has {
		blockProp.Logger.Error("received have part for unknown proposal", "peer", peerID, "height", height, "round", round)
		// blockProp.Switch.StopPeerForError(blockProp.getPeer(peer).peer, errors.New("received part for unknown proposal"))
		// maybe return error
		return nil
	}
	peer := blockProp.getPeer(peerID)
	blockProp.Logger.Info("getting received haves")
	hc, has := peer.GetHaves(height, round)
	if !has {
		// shouldn't happen, maybe log something or return an error
		return nil
	}
	// Check if the sender has parts that we don't have.
	hc.Sub(parts.BitArray()) // TODO add nil check for both

	hc.Sub(fullReqs)

	if hc.IsEmpty() {
		return nil
	}

	reqLimit := 1

	// if enough requests have been made for the parts, don't request them.
	for _, partIndex := range hc.GetTrueIndices() {
		reqs := blockProp.countRequests(height, round, partIndex)
		if len(reqs) >= reqLimit {
			hc.SetIndex(partIndex, false)
			// mark the part as fully requested.
			fullReqs.SetIndex(partIndex, true)
		}
		// don't request the part from this peer if we've already requested it
		// from them.
		for _, p := range reqs {
			// p == peer means we have already requested the part from this peer.
			if p == peerID {
				hc.SetIndex(partIndex, false)
			}
		}
	}

	if hc.IsEmpty() {
		return nil
	}

	// send a want back to the sender of the haves with the wants we
	e := p2p.Envelope{
		ChannelID: WantChannel,
		Message: &propproto.WantParts{
			Height: height,
			Round:  round,
			Parts:  *hc.ToProto(),
		},
	}

	if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
		blockProp.Logger.Error("failed to send part state", "peer", peer, "height", height, "round", round)
		return nil
	}

	schema.WriteBlockPartState(
		blockProp.traceClient,
		height,
		round,
		hc.GetTrueIndices(),
		false,
		string(peerID),
		schema.Haves,
	)

	// keep track of the parts that this node has requested.
	peer.AddRequests(height, round, hc)

	// TODO keep track of requests
	// TODO only request from peer with haves
	// TODO keep track of the number of requests
	// TODO keep track of the peer's maximum number of requests
	// TODO use the latest height during catchup
	return nil // FIXME maybe not return anything
}

func requestAllFromPeer(blockProp *Reactor, peerID p2p.ID, height int64, round int32) *bits.BitArray {
	_, parts, fullReqs, has := blockProp.getAllState(height, round, false)
	if !has {
		blockProp.Logger.Error("received have part for unknown proposal", "peer", peerID, "height", height, "round", round)
		// blockProp.Switch.StopPeerForError(blockProp.getPeer(peer).peer, errors.New("received part for unknown proposal"))
		// maybe return error
		return nil
	}
	peer := blockProp.getPeer(peerID)

	hc := parts.BitArray().Not()
	hc.Sub(fullReqs)
	if hc.IsEmpty() {
		return nil
	}

	reqLimit := 1

	// if enough requests have been made for the parts, don't request them.
	for _, partIndex := range hc.GetTrueIndices() {
		reqs := blockProp.countRequests(height, round, partIndex)
		if len(reqs) >= reqLimit {
			hc.SetIndex(partIndex, false)
			// mark the part as fully requested.
			fullReqs.SetIndex(partIndex, true)
		}
		// don't request the part from this peer if we've already requested it
		// from them.
		for _, p := range reqs {
			// p == peer means we have already requested the part from this peer.
			if p == peerID {
				hc.SetIndex(partIndex, false)
			}
		}
	}

	if hc.IsEmpty() {
		return nil
	}

	e := p2p.Envelope{
		ChannelID: WantChannel,
		Message: &propproto.WantParts{
			Height: height,
			Round:  round,
			Parts:  *hc.ToProto(),
		},
	}

	if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
		blockProp.Logger.Error("failed to send part state", "peer", peer, "height", height, "round", round)
		return nil
	}

	schema.WriteBlockPartState(
		blockProp.traceClient,
		height,
		round,
		hc.GetTrueIndices(),
		false,
		string(peerID),
		schema.Haves,
	)

	// keep track of the parts that this node has requested.
	peer.AddRequests(height, round, hc)

	// TODO keep track of requests
	// TODO only request from peer with haves
	// TODO keep track of the number of requests
	// TODO keep track of the peer's maximum number of requests
	// TODO use the latest height during catchup
	return nil // FIXME maybe not return anything
}
