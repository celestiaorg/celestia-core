package propagation

import (
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	propproto "github.com/tendermint/tendermint/proto/tendermint/propagation"
)

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts. The peer must always send the proposal before
// sending parts. If they did not, this node must disconnect from them.
func (blockProp *Reactor) handleHaves(peer p2p.ID, haves *proptypes.HaveParts, _ bool) {
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

	_, parts, fullReqs, has := blockProp.getAllState(height, round, false)
	if !has {
		// TODO disconnect from the peer
		// blockProp.Logger.Debug("received part state for unknown proposal", "peer", peer, "height", height, "round", round)
		return
	}

	p.Initialize(height, round, int(parts.Total()))

	bm, _ := p.GetHaves(height, round)

	for _, pmd := range haves.Parts {
		bm.SetIndex(int(pmd.Index), true)
	}

	if parts.Original().IsComplete() {
		return
	}

	// Check if the sender has parts that we don't have.
	hc := haves.BitArray(int(parts.Total()))
	hc.Sub(parts.BitArray())

	if hc.IsEmpty() {
		return
	}

	hc.Sub(fullReqs)

	if hc.IsEmpty() {
		return
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
			if p == peer {
				hc.SetIndex(partIndex, false)
			}
		}
	}

	if hc.IsEmpty() {
		return
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

	if !p2p.TrySendEnvelopeShim(p.peer, e, blockProp.Logger) { //nolint:staticcheck
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
	p.AddRequests(height, round, hc)
	blockProp.broadcastHaves(haves, peer, int(parts.Total()))
}

// countRequests returns the number of requests for a given part.
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
func (blockProp *Reactor) broadcastHaves(haves *proptypes.HaveParts, from p2p.ID, partSetSize int) {
	for _, peer := range blockProp.getPeers() {
		if peer.peer.ID() == from {
			continue
		}

		e := p2p.Envelope{
			ChannelID: DataChannel,
			Message:   haves.ToProto(),
		}

		// todo(evan): don't rely strictly on try, however since we're using
		// pull based gossip, this isn't as big as a deal since if someone asks
		// for data, they must already have the proposal.
		// TODO: use retry and logs
		if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Error("failed to send haves to peer", "peer", peer.peer.ID())
			continue
		}
		peer.AddHaves(haves.Height, haves.Round, haves.BitArray(partSetSize))
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

	// get data, use the prove as a proxy for determining if this Want message
	// if for catchup
	_, parts, _, has := blockProp.getAllState(height, round, wants.Prove)
	// the peer must always send the proposal before sending parts, if they did
	//  not, this node must disconnect from them.
	if !has {
		return
	}

	// if we have the parts, send them to the peer.
	wc := wants.Parts.Copy()
	canSend := parts.BitArray().And(wc)
	if canSend == nil {
		return
	}

	for _, partIndex := range canSend.GetTrueIndices() {
		part, _ := parts.GetPart(uint32(partIndex))
		partBz := make([]byte, len(part.Bytes))
		copy(partBz, part.Bytes)
		rpart := &propproto.RecoveryPart{
			Height: height,
			Round:  round,
			Index:  uint32(partIndex),
			Data:   partBz,
		}
		if wants.Prove {
			rpart.Proof = *part.Proof.ToProto()
		}
		e := p2p.Envelope{
			ChannelID: DataChannel,
			Message:   rpart,
		}

		if !p2p.TrySendEnvelopeShim(p.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Error("failed to send part", "peer", peer, "height", height, "round", round, "part", partIndex)
			continue
		}
		// p.SetHave(height, round, int(partIndex))
		schema.WriteBlockPart(blockProp.traceClient, height, round, part.Index, wants.Prove, string(peer), schema.Upload)
	}

	// for parts that we don't have, but they still want, store the wants.
	stillMissing := wants.Parts.Sub(canSend)
	if !stillMissing.IsEmpty() {
		p.AddWants(height, round, stillMissing)
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
	cb, parts, _, has := blockProp.getAllState(part.Height, part.Round, false)
	if !has {
		blockProp.Logger.Debug("received part for unknown proposal", "peer", peer, "height", part.Height, "round", part.Round)
		// d.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part for unknown proposal"))
		return
	}

	if parts.IsComplete() {
		return
	}

	// todo: add these defensive checks in a better way
	if cb == nil {
		return
	}
	if parts == nil {
		return
	}

	// todo: we need to figure out a way to get the proof for a part that was
	// sent during catchup.
	proof := cb.GetProof(part.Index)
	if proof == nil {
		if part.Proof == nil {
			blockProp.Logger.Error("proof not found", "peer", peer, "height", part.Height, "round", part.Round, "part", part.Index)
			return
		}
		if len(part.Proof.LeafHash) != tmhash.Size {
			return
		}
		proof = part.Proof
	}

	// TODO: to verify, compare the hash with that of the have that was sent for
	// this part and verified.
	added, err := parts.AddPart(part, *proof)
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
	// this node should send all the wanted parts that nodes have requested. cp
	// == nil means that there was no compact block available and this was
	// during catchup. todo: use the bool found in the state instead of checking
	// for nil.
	if parts.CanDecode() {
		if parts.IsDecoding.Load() {
			return
		}
		parts.IsDecoding.Store(true)
		defer parts.IsDecoding.Store(false)

		err := parts.Decode()
		if err != nil {
			blockProp.Logger.Error("failed to decode parts", "peer", peer, "height", part.Height, "round", part.Round, "error", err)
			return
		}

		// broadcast haves for all parts since we've decoded the entire block.
		// rely on the broadcast method to ensure that parts are only sent once.
		haves := &proptypes.HaveParts{
			Height: part.Height,
			Round:  part.Round,
		}

		for i := uint32(0); i < parts.Total(); i++ {
			p, has := parts.GetPart(i)
			if !has {
				blockProp.Logger.Error("failed to get decoded part", "peer", peer, "height", part.Height, "round", part.Round, "part", i)
				continue
			}
			haves.Parts = append(haves.Parts, proptypes.PartMetaData{Index: i, Hash: p.Proof.LeafHash})
		}

		blockProp.broadcastHaves(haves, peer, int(parts.Total()))

		// clear all the wants if they exist
		go func(height int64, round int32, parts *proptypes.CombinedPartSet) {
			for i := uint32(0); i < parts.Total(); i++ {
				p, _ := parts.GetPart(i)
				pbz := make([]byte, len(p.Bytes))
				copy(pbz, p.Bytes)
				msg := &proptypes.RecoveryPart{
					Height: height,
					Round:  round,
					Index:  p.Index,
					Data:   pbz,
				}
				blockProp.clearWants(msg)
			}
		}(part.Height, part.Round, parts)

		return
	}

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

			if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
				blockProp.Logger.Error("failed to send part", "peer", peer.peer.ID(), "height", part.Height, "round", part.Round, "part", part.Index)
				continue
			}
			peer.SetHave(part.Height, part.Round, int(part.Index))
			peer.SetWant(part.Height, part.Round, int(part.Index), false)
			catchup := false
			blockProp.pmtx.Lock()
			if part.Height < blockProp.currentHeight {
				catchup = true
			}
			blockProp.pmtx.Unlock()
			schema.WriteBlockPart(blockProp.traceClient, part.Height, part.Round, part.Index, catchup, string(peer.peer.ID()), schema.Upload)
		}
	}
}
