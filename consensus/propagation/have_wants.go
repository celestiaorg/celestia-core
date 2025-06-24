package propagation

import (
	"fmt"
	"math"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/proto/tendermint/crypto"

	"github.com/cometbft/cometbft/types"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
)

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts. The peer must always send the proposal before
// sending parts. If they did not, this node must disconnect from them.
func (blockProp *Reactor) handleHaves(peer p2p.ID, haves *proptypes.HaveParts) {
	if haves == nil {
		// TODO handle the disconnection case
		return
	}

	if !blockProp.started.Load() {
		return
	}

	height := haves.Height
	round := haves.Round
	p := blockProp.getPeer(peer)
	if p == nil || p.peer == nil {
		blockProp.Logger.Error("peer not found", "peer", peer)
		return
	}

	cb, parts, fullReqs, has := blockProp.getAllState(height, round, false)
	if !has {
		blockProp.Logger.Debug("received have part for unknown proposal", "peer", peer, "height", height, "round", round)
		// blockProp.Switch.StopPeerForError(blockProp.getPeer(peer).peer, errors.New("received part for unknown proposal"))
		return
	}

	if cb == nil {
		// we can't process haves for a compact block we don't have
		return
	}
	err := haves.ValidatePartHashes(cb.PartsHashes)
	if err != nil {
		blockProp.Logger.Error("received invalid have part", "height", haves.Height, "round", haves.Round, "parts", haves.Parts, "err", err)
		blockProp.Switch.StopPeerForError(p.peer, err, "propagation")
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

	if p := blockProp.getPeer(peer); p != nil {
		for _, index := range hc.GetTrueIndices() {
			select {
			case <-p.ctx.Done():
				return
			case p.receivedHaves <- request{
				height: height,
				round:  round,
				index:  uint32(index),
			}:
				p.RequestsReady()
			}
		}
	}
}

// ReqLimit limits the number of requests per part.
// It allows requesting the small blocks multiple times to avoid relying only on a few/single
// peer to upload the whole block.
// The provided partsCount is the number of parts in the block and parity data.
func ReqLimit(partsCount int) int {
	if partsCount == 0 {
		return 1
	}
	return int(math.Ceil(math.Max(1, 34/float64(partsCount))))
}

func (blockProp *Reactor) requestFromPeer(ps *PeerState) {
	for {
		availableReqs := ConcurrentRequestLimit(len(blockProp.getPeers()), int(blockProp.getCurrentProposalPartsCount())) - ps.concurrentReqs.Load()

		if availableReqs > 0 && len(ps.receivedHaves) > 0 {
			ps.RequestsReady()
		}

		select {
		case <-ps.ctx.Done():
			return

		case part, ok := <-ps.receivedParts:
			if !ok {
				return
			}
			if !blockProp.relevantHave(part.height, part.round) {
				continue
			}
			ps.DecreaseConcurrentReqs(1)

		case <-ps.CanRequest():
			canSend := ConcurrentRequestLimit(len(blockProp.getPeers()), int(blockProp.getCurrentProposalPartsCount())) - ps.concurrentReqs.Load()
			if canSend <= 0 {
				// should never be below zero
				continue
			}

			var (
				wants    *proptypes.WantParts
				parts    *proptypes.CombinedPartSet
				fullReqs *bits.BitArray
			)
			for i := min(canSend, int64(len(ps.receivedHaves))); i > 0; {
				if len(ps.receivedHaves) == 0 {
					break
				}

				have, ok := <-ps.receivedHaves
				if !ok {
					return
				}

				if wants != nil && (have.height != wants.Height || have.round != wants.Round) {
					// haves for a new height, resetting
					wants = nil
					parts = nil
				}

				if !blockProp.relevantHave(have.height, have.round) {
					continue
				}

				if parts == nil {
					var has bool
					_, parts, fullReqs, has = blockProp.getAllState(have.height, have.round, false)
					if !has {
						blockProp.Logger.Error("couldn't find proposal when filtering requests", "height", have.height, "round", have.round)
						break
					}
				}

				// don't request a part that is already downloaded
				if parts.BitArray().GetIndex(int(have.index)) {
					continue
				}

				// don't request a part that has already hit the request limit
				if fullReqs.GetIndex(int(have.index)) {
					continue
				}

				reqLimit := ReqLimit(int(parts.Total()))

				reqs := blockProp.countRequests(have.height, have.round, int(have.index))
				if len(reqs) >= reqLimit {
					fullReqs.SetIndex(int(have.index), true)
					continue
				}

				// don't request the part from this peer if we've already requested it
				// from them.
				for _, p := range reqs {
					// p == peer means we have already requested the part from this peer.
					if p == ps.peer.ID() {
						continue
					}
				}

				if wants == nil {
					wants = &proptypes.WantParts{
						Height: have.height,
						Round:  have.round,
						Parts:  bits.NewBitArray(int(parts.Total())),
					}
				}

				wants.Parts.SetIndex(int(have.index), true)
				i--
			}

			// if none of the requests were relevant, then wants will still be
			// nil
			if wants == nil {
				continue
			}

			err := blockProp.sendWantsThenBroadcastHaves(ps, wants)
			if err != nil {
				blockProp.Logger.Error("error sending wants", "err", err)
			}
		}
	}
}

func (blockProp *Reactor) sendWantsThenBroadcastHaves(ps *PeerState, wants *proptypes.WantParts) error {
	have, err := blockProp.convertWantToHave(wants)
	if err != nil {
		return err
	}
	blockProp.sendWant(ps, wants)
	blockProp.broadcastHaves(have, ps.peer.ID(), wants.Parts.Size())
	return nil
}

func (blockProp *Reactor) convertWantToHave(want *proptypes.WantParts) (*proptypes.HaveParts, error) {
	have := &proptypes.HaveParts{
		Height: want.Height,
		Round:  want.Round,
		Parts:  make([]proptypes.PartMetaData, 0),
	}
	cb, _, _, has := blockProp.getAllState(want.Height, want.Round, false)
	if !has {
		blockProp.Logger.Error("couldn't find proposal", "height", have.Height, "round", have.Round)
		return nil, fmt.Errorf("couldn't find proposal height %d round %d", have.Height, have.Round)
	}
	for _, index := range want.Parts.GetTrueIndices() {
		if len(cb.PartsHashes) <= index {
			blockProp.Logger.Error("couldn't find part hash", "index", index, "height", want.Height, "round", want.Round, "len", len(cb.PartsHashes))
			continue
		}
		have.Parts = append(have.Parts, proptypes.PartMetaData{
			Index: uint32(index),
			Hash:  cb.PartsHashes[index],
		})
	}
	return have, nil
}

func (blockProp *Reactor) sendWant(ps *PeerState, want *proptypes.WantParts) {
	e := p2p.Envelope{
		ChannelID: WantChannel,
		Message:   want.ToProto(),
	}

	if !ps.peer.TrySend(e) {
		blockProp.Logger.Error("failed to send part state", "peer", ps.peer.ID(), "height", want.Height, "round", want.Round)
		return
	}

	schema.WriteBlockPartState(
		blockProp.traceClient,
		want.Height,
		want.Round,
		want.Parts.GetTrueIndices(),
		false,
		string(ps.peer.ID()),
		schema.Haves,
	)

	// keep track of the parts that this node has requested.
	ps.AddRequests(want.Height, want.Round, want.Parts)
	ps.IncreaseConcurrentReqs(int64(len(want.Parts.GetTrueIndices())))
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
		if !peer.peer.TrySend(e) {
			blockProp.Logger.Error("failed to send haves to peer", "peer", peer.peer.ID())
			continue
		}
		peer.AddHaves(haves.Height, haves.Round, haves.BitArray(partSetSize))
	}
}

// handleWants is called when a peer sends a want message. This is used to send
// peers data that this node already has and store the wants to send them data
// in the future.
func (blockProp *Reactor) handleWants(peer p2p.ID, wants *proptypes.WantParts) {
	if !blockProp.started.Load() {
		return
	}

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
		blockProp.Logger.Error("received part state request for unknown proposal", "peer", peer, "height", height, "round", round)
		// blockProp.Switch.StopPeerForError(p.peer, errors.New("received want part for unknown proposal"))
		return
	}

	if wants.Parts.Size() != int(parts.Total()) {
		blockProp.Logger.Error("received want part with invalid parts size", "peer", peer, "height", height, "round", round, "expected_parts_size", parts.Total(), "received_parts_size", wants.Parts.Size())
		return
	}

	// if we have the parts, send them to the peer.
	wc := wants.Parts.Copy()
	canSend := parts.BitArray().And(wc)
	if canSend == nil {
		blockProp.Logger.Error("nil can send", "peer", peer, "height", height, "round", round, "wants", wants, "wc", wc)
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

		if !p.peer.TrySend(e) {
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
func (blockProp *Reactor) handleRecoveryPart(peer p2p.ID, part *proptypes.RecoveryPart) {
	if peer == "" {
		peer = blockProp.self
	}

	if !blockProp.started.Load() {
		return
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
		blockProp.Logger.Error("received part for unknown proposal", "peer", peer, "height", part.Height, "round", part.Round)
		// blockProp.Switch.StopPeerForError(p.peer, errors.New("received recovery part for unknown proposal"))
		return
	}

	if parts.HasPart(int(part.Index)) || parts.IsComplete() {
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

	added, err := parts.AddPart(part, *proof)
	if err != nil {
		blockProp.Logger.Error("failed to add part to part set", "peer", peer, "height", part.Height, "round", part.Round, "part", part.Index, "error", err)
		return
	}

	if p := blockProp.getPeer(peer); p != nil {
		// avoid blocking if a single peer is backed up. This means that they
		// are sending us too many parts
		select {
		case <-p.ctx.Done():
			return
		case p.receivedParts <- partData{height: part.Height, round: part.Round}:
		default:
		}
	}

	// if the part was not added and there was no error, the part has already
	// been seen, and therefore doesn't need to be cleared.
	if !added {
		return
	}

	// only send original parts to the consensus reactor
	if part.Index < parts.Original().Total() {
		select {
		case <-blockProp.ctx.Done():
			return
		case blockProp.partChan <- types.PartInfo{
			Part: &types.Part{
				Index: part.Index,
				Bytes: part.Data,
				Proof: *proof,
			},
			Height: part.Height,
			Round:  part.Round,
		}:
		}
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
			// only send original parts to the consensus reactor
			if i < parts.Original().Total() {
				select {
				case <-blockProp.ctx.Done():
					return
				case blockProp.partChan <- types.PartInfo{
					Part: &types.Part{
						Index: p.Index,
						Bytes: p.Bytes,
						Proof: p.GetProof(),
					},
					Height: part.Height,
					Round:  part.Round,
				}:
				}
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
					Index:  i,
					Data:   pbz,
				}
				blockProp.clearWants(msg, p.GetProof())
			}
		}(part.Height, part.Round, parts)

		return
	}

	go blockProp.clearWants(part, *proof)
}

// clearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
func (blockProp *Reactor) clearWants(part *proptypes.RecoveryPart, proof merkle.Proof) {
	for _, peer := range blockProp.getPeers() {
		if peer.WantsPart(part.Height, part.Round, part.Index) {
			e := p2p.Envelope{
				ChannelID: DataChannel,
				Message: &propproto.RecoveryPart{
					Height: part.Height,
					Round:  part.Round,
					Index:  part.Index,
					Data:   part.Data,
					Proof: crypto.Proof{
						Total:    proof.Total,
						Index:    proof.Index,
						LeafHash: proof.LeafHash,
						Aunts:    proof.Aunts,
					},
				},
			}

			if !peer.peer.TrySend(e) {
				blockProp.Logger.Error("failed to send part", "peer", peer.peer.ID(), "height", part.Height, "round", part.Round, "part", part.Index)
				continue
			}
			err := peer.SetHave(part.Height, part.Round, int(part.Index))
			if err != nil {
				continue
			}
			err = peer.SetWant(part.Height, part.Round, int(part.Index), false)
			if err != nil {
				continue
			}
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
