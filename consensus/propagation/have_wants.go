package propagation

import (
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	propproto "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts.
// The peer must always send the proposal before sending parts. If they did
// not, this node must disconnect from them.
// This method will:
// - get the provided peer from the peer state
// - get the proposal referenced in the haves message
// - set the provided haves as the peer's haves
// - if the returned parts from the proposal are complete, we return
// - otherwise, we check if the sender has parts that we don't have.
// - if they do, we check if we already requested those parts enough times (a limit will be defined)
// of we already requested the parts from them.
// - if so, we just gossip the haves to our connected peers.
// - otherwise, we send the wants for the missing parts to that peer before broadcasting the haves.
// - finally, we keep track of the want requests in the proposal state.
func (blockProp *Reactor) handleHaves(peer p2p.ID, haves *proptypes.HaveParts, bypassRequestLimit bool) {
	if haves == nil {
		// TODO handle the disconnection case
		return
	}
	height := haves.Height
	round := haves.Round
	p := blockProp.getPeer(peer)
	if p == nil || p.peer == nil {
		blockProp.Logger.Error("peer not found", "peer", peer)
		return
	}
	_, parts, fullReqs, has := blockProp.getAllState(height, round)
	if !has {
		// TODO disconnect from the peer
		blockProp.Logger.Error("received part state for unknown proposal", "peer", peer, "height", height, "round", round)
		return
	}

	blockProp.mtx.RLock()
	defer blockProp.mtx.RUnlock()

	// Update the peer's haves.
	p.SetHaves(height, round, haves)

	if parts.IsComplete() {
		return
	}

	// Check if the sender has parts that we don't have.
	hc := haves.Copy()
	hc.Sub(parts.BitArray())

	// remove any parts that we have already requested sufficient times.
	if !bypassRequestLimit {
		hc.Sub(fullReqs)
	}

	reqLimit := 1
	if bypassRequestLimit {
		// make this configurable
		reqLimit = 6
	}

	// if enough requests have been made for the parts, don't request them.
	for _, partIndex := range hc.GetTrueIndices() {
		reqs := blockProp.countRequests(height, round, partIndex)
		if len(reqs) >= reqLimit {
			// TODO unify the types for the indexes and similar
			hc.RemoveIndex(uint32(partIndex))
			// mark the part as fully requested.
			fullReqs.SetIndex(partIndex, true)
		}
		// don't request the part from this peer if we've already requested it
		// from them.
		for _, p := range reqs {
			// p == peer means we have already requested the part from this peer.
			if p == peer {
				hc.RemoveIndex(uint32(partIndex))
			}
		}
	}

	// todo(evan): check that this is legit. we can also exit early if we have
	// all of the data already
	if hc.IsEmpty() {
		return
	}

	// send a want back to the sender of the haves with the wants we
	e := p2p.Envelope{
		ChannelID: WantChannel,
		Message: &propproto.WantParts{
			Height: height,
			Round:  round,
			Parts:  *hc.ToBitArray().ToProto(),
		},
	}

	if !p2p.SendEnvelopeShim(p.peer, e, blockProp.Logger) { //nolint:staticcheck
		blockProp.Logger.Error("failed to send part state", "peer", peer, "height", height, "round", round)
		return
	}

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
	// TODO check if we need to persist the have parts or just their bitarray
	p.SetRequests(height, round, hc.ToBitArray())
	blockProp.broadcastHaves(hc, peer)
}

// todo(evan): refactor to not iterate so often and just store which peers
func (blockProp *Reactor) countRequests(height int64, round int32, part int) []p2p.ID {
	peers := make([]p2p.ID, 0)
	for _, peer := range blockProp.getPeers() {
		reqs, has := peer.GetRequests(height, round)
		if has {
			index := reqs.GetIndex(part)
			if index {
				peers = append(peers, peer.peer.ID())
			}
		}
	}
	return peers
}

// broadcastHaves gossips the provided have msg to all peers except to the
// original sender. This should only be called upon receiving a new have for the
// first time.
func (blockProp *Reactor) broadcastHaves(haves *proptypes.HaveParts, from p2p.ID) {
	e := p2p.Envelope{
		ChannelID: DataChannel,
		Message: &propproto.HaveParts{
			Height: haves.Height,
			Round:  haves.Round,
			Parts:  haves.ToProto().Parts,
		},
	}
	for _, peer := range blockProp.getPeers() {
		if peer.peer.ID() == from {
			continue
		}

		// skip sending anything to this peer if they already have all the
		// parts.
		ph, has := peer.GetHaves(haves.Height, haves.Round)
		if has {
			havesCopy := haves.Copy()
			havesCopy.Sub(ph.ToBitArray())
			if havesCopy.IsEmpty() {
				continue
			}
		}

		// todo(evan): don't rely strictly on try, however since we're using
		// pull based gossip, this isn't as big as a deal since if someone asks
		// for data, they must already have the proposal.
		// TODO: use retry and logs
		if p2p.SendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
			schema.WriteBlockPartState(
				blockProp.traceClient,
				haves.Height,
				haves.Round,
				haves.GetTrueIndices(),
				true,
				string(peer.peer.ID()),
				schema.Upload,
			)
		}
	}
}

// handleWants is called when a peer sends a want message. This is used to send
// peers data that this node already has and store the wants to send them data
// in the future.
// This method will:
// - get the provided peer from the state
// - get the proposal from the proposal cache
// - if the round provided in the wants is < 0, send the peer the partset header
// - if we have the wanted parts, send them to that peer.
// - if they want other parts that we don't have, store that in the peer state.
func (blockProp *Reactor) handleWants(peer p2p.ID, wants *proptypes.WantParts) {
	height := wants.Height
	round := wants.Round
	p := blockProp.getPeer(peer)
	if p == nil {
		blockProp.Logger.Error("peer not found", "peer", peer)
		return
	}

	_, parts, has := blockProp.GetProposal(height, round)
	// the peer must always send the proposal before sending parts, if they did
	//  not, this node must disconnect from them.
	if !has {
		blockProp.Logger.Error("received part state request for unknown proposal", "peer", peer, "height", height, "round", round)
		// d.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part state for unknown proposal"))
		return
	}

	// send the peer the partset header if they don't have the proposal.
	// TODO get rid of this catchup case
	if round < 0 {
		if !blockProp.sendPsh(peer, height, round) {
			blockProp.Logger.Error("failed to send PSH", "peer", peer, "height", height, "round", round)
			return
		}
	}

	// if we have the parts, send them to the peer.
	wc := wants.Parts.Copy()

	// send all the parts if the peer doesn't know which parts to request
	if wc.IsEmpty() {
		wc = parts.BitArray()
	}

	canSend := parts.BitArray().And(wc)
	if canSend == nil {
		blockProp.Logger.Error("nil can send?", "peer", peer, "height", height, "round", round, "wants", wants, "wc", wc)
		return
	}
	for _, partIndex := range canSend.GetTrueIndices() {
		part := parts.GetPart(partIndex)
		ppart, err := part.ToProto()
		if err != nil {
			blockProp.Logger.Error("failed to convert part to proto", "height", height, "round", round, "part", partIndex, "error", err)
			continue
		}
		e := p2p.Envelope{
			// TODO catch this message in the consensus reactor and send it to this propagation reactor
			// check the data routine for more information.
			ChannelID: DataChannel,
			// TODO this might require sending/verifying some proof.
			Message: &propproto.RecoveryPart{
				Height: height,
				Round:  round,
				Index:  ppart.Index,
				Data:   ppart.Bytes,
			},
		}

		if !p2p.SendEnvelopeShim(p.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Error("failed to send part", "peer", peer, "height", height, "round", round, "part", partIndex)
			continue
		}
		// p.SetHave(height, round, int(partIndex))
		schema.WriteBlockPartState(blockProp.traceClient, height, round, []int{partIndex}, true, string(peer), schema.AskForProposal)
	}

	// for parts that we don't have, but they still want, store the wants.
	stillMissing := wants.Parts.Sub(canSend)
	if !stillMissing.IsEmpty() {
		p.SetWants(&proptypes.WantParts{
			Parts:  stillMissing,
			Height: height,
			Round:  round,
		})
	}
}

// handleRecoveryPart is called when a peer sends a block part message. This is used
// to store the part and clear any wants for that part.
// This method will:
// - if the peer is not provided, we set it to self
// - get the peer from the peer state
// - get the proposal referenced by the recovery part height and round
// - if the parts are complete, return
// - add the received part to the parts
// - if the parts are decodable, clear all the wants of that block from the proposal state
// - otherwise, clear the want related to this part from the state
func (blockProp *Reactor) handleRecoveryPart(peer p2p.ID, part *proptypes.RecoveryPart) {
	if peer == "" {
		peer = blockProp.self
	}
	p := blockProp.getPeer(peer)
	if p == nil && peer != blockProp.self {
		blockProp.Logger.Error("peer not found", "peer", peer)
		return
	}
	// the peer must always send the proposal before sending parts, if they did
	// not this node must disconnect from them.
	_, parts, has := blockProp.GetProposal(part.Height, part.Round)
	if !has { // fmt.Println("unknown proposal")
		blockProp.Logger.Error("received part for unknown proposal", "peer", peer, "height", part.Height, "round", part.Round)
		// d.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part for unknown proposal"))
		return
	}

	if parts.IsComplete() {
		return
	}

	// TODO this is not verifying the proof. make it verify it
	added, err := parts.AddPartWithoutProof(&types.Part{Index: part.Index, Bytes: part.Data})
	if err != nil {
		blockProp.Logger.Error("failed to add part to part set", "peer", peer, "height", part.Height, "round", part.Round, "part", part.Index, "error", err)
		return
	}

	// if the part was not added and there was no error, the part has already
	// been seen, and therefore doesn't need to be cleared.
	if !added {
		return
	}

	// attempt to decode the remaining block parts. If they are decoded, then
	// this node should send all the wanted parts that nodes have requested.
	if parts.IsReadyForDecoding() {
		// TODO decode once we have parity data support

		// clear all the wants if they exist
		go func(height int64, round int32, parts *types.PartSet) {
			for i := uint32(0); i < parts.Total(); i++ {
				p := parts.GetPart(int(i))
				msg := &proptypes.RecoveryPart{
					Height: height,
					Round:  round,
					Index:  p.Index,
					Data:   p.Bytes,
				}
				blockProp.clearWants(msg)
			}
		}(part.Height, part.Round, parts)

		return
	}

	// todo(evan): temporarily disabling
	// go d.broadcastHaves(part.Height, part.Round, parts.BitArray(), peer)
	// TODO better go routines management
	go blockProp.clearWants(part)
}

// clearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
// This method will:
// - get all the peers
// - check if any of the peers need that part
// - if so, send it to them
// - if not, remove that want.
func (blockProp *Reactor) clearWants(part *proptypes.RecoveryPart) {
	for _, peer := range blockProp.getPeers() {
		if peer.WantsPart(part.Height, part.Round, part.Index) {
			e := p2p.Envelope{
				ChannelID: DataChannel,
				Message:   &propproto.RecoveryPart{Height: part.Height, Round: part.Round, Index: part.Index, Data: part.Data},
			}
			if p2p.SendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
				peer.SetHave(part.Height, part.Round, int(part.Index))
				peer.SetWant(part.Height, part.Round, int(part.Index), false)
				catchup := false
				blockProp.pmtx.RLock()
				if part.Height < blockProp.currentHeight {
					catchup = true
				}
				blockProp.pmtx.RUnlock()
				schema.WriteBlockPart(blockProp.traceClient, part.Height, part.Round, part.Index, catchup, string(peer.peer.ID()), schema.Upload)
			}
		}
	}
}
