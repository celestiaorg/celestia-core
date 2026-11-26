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
	} else if len(cb.PartsHashes) == 0 {
		// this is a catchup block, we shouldn't receive haves for
		return
	}
	err := haves.ValidatePartHashes(cb.PartsHashes)
	if err != nil {
		blockProp.Logger.Error("received invalid have part", "height", haves.Height, "round", haves.Round, "err", err)
		//blockProp.Switch.StopPeerForError(p.peer, err, blockProp.String())
		return
	}

	p.Initialize(height, round, int(parts.Total()))

	bm, _ := p.GetHaves(height, round)

	for _, pmd := range haves.Parts {
		bm.SetIndex(int(pmd.Index), true)
		p.consensusPeerState.SetHasProposalBlockPart(height, round, int(pmd.Index))
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
			// avoid blocking if a single peer is backed up. This means that they
			// are sending us too many haves
			select {
			case <-p.ctx.Done():
				return
			case p.receivedHaves <- request{
				height: height,
				round:  round,
				index:  uint32(index),
			}:
				p.RequestsReady()
			default:
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
	defer blockProp.ProtectPanic(ps.peer)
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
			if !blockProp.safeRelevant(part.height, part.round) {
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

				if !blockProp.safeRelevant(have.height, have.round) {
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

				missingPartsCount := countRemainingParts(int(parts.Total()), len(parts.BitArray().GetTrueIndices()))
				if missingPartsCount == 0 {
					// we can ignore this have in this case
					continue
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
			missingPartsCount := countRemainingParts(int(parts.Total()), len(parts.BitArray().GetTrueIndices()))
			if missingPartsCount == 0 {
				// no need to send the want in this case
				continue
			}
			wants.MissingPartsCount = missingPartsCount

			err := blockProp.sendWantsWithinBudget(ps, wants, parts)
			if err != nil {
				blockProp.Logger.Error("error sending wants", "err", err)
			}
		}
	}
}

// silentPeersTolerated is how many peers can take our requests and never
// answer before we have to wait for the retry.
const silentPeersTolerated = 2

// requestLimits returns how many parts we can ask one peer for, and how many
// we can ask for in total. missing is the number of parts we still need,
// totalParts is the size of the block with parity.
//
// We can't tell which peers will really send us parts. So no peer gets more
// than a slice of what we need, and the total has room for
// silentPeersTolerated peers that send nothing.
//
// Example with a 62 part block where we still need 21:
//   - one peer gets at most 21 / 2 rounded up = 11 parts
//   - in total we ask for at most 21 + 2 * 11 = 43 parts
//   - if two peers take 11 each and send nothing, 21 are left for the others
func requestLimits(missing, totalParts int) (maxPerPeer, budget int) {
	// the slice we want: big enough that silentPeersTolerated honest peers
	// cover everything we need.
	wantedSlice := (missing + silentPeersTolerated - 1) / silentPeersTolerated

	// the slice that is safe. We only need half of the block, the other half
	// is spare. A part we asked a silent peer for can't be asked from anyone
	// else, so the silent peers together must not hold more than the spare
	// half. This only matters when we have almost no parts yet.
	spareParts := totalParts / 2
	safeSlice := spareParts / silentPeersTolerated

	maxPerPeer = min(wantedSlice, safeSlice)
	// a tiny block gives 0 here, but we still have to ask someone.
	if maxPerPeer < 1 {
		maxPerPeer = 1
	}

	budget = missing + silentPeersTolerated*maxPerPeer
	return maxPerPeer, budget
}

// sendWantsWithinBudget drops the wants that go over the request budget and
// sends the rest. Without the budget, we'd ask every peer for parts and get
// way more than we need.
func (blockProp *Reactor) sendWantsWithinBudget(ps *PeerState, wants *proptypes.WantParts, parts *proptypes.CombinedPartSet) error {
	haves, err := blockProp.trimAndSendWants(ps, wants, parts)
	if err != nil || haves == nil {
		return err
	}
	blockProp.broadcastHaves(haves, ps.peer.ID(), wants.Parts.Size())
	return nil
}

// trimAndSendWants trims the wants to the budget and sends what is left. It
// returns the haves to broadcast for the parts it requested, or nil if it
// requested nothing.
func (blockProp *Reactor) trimAndSendWants(ps *PeerState, wants *proptypes.WantParts, parts *proptypes.CombinedPartSet) (*proptypes.HaveParts, error) {
	// peers take turns here, so two of them can't spend the same budget.
	blockProp.requestMtx.Lock()
	defer blockProp.requestMtx.Unlock()

	// count again, parts may have arrived while we waited for our turn.
	have := parts.BitArray()
	missing := int(countRemainingParts(int(parts.Total()), len(have.GetTrueIndices())))
	if missing == 0 {
		return nil, nil
	}
	wants.MissingPartsCount = int32(missing)

	requested := blockProp.requestedParts(wants.Height, wants.Round, have.Size())
	inFlight := len(requested.Sub(have).GetTrueIndices())

	// parts we already asked for and are still waiting on use up the budget.
	maxPerPeer, budget := requestLimits(missing, int(parts.Total()))
	budget -= inFlight
	askedFromPeer := blockProp.outstandingRequests(ps, wants.Height, wants.Round, have)

	reqLimit := ReqLimit(int(parts.Total()))
	for _, index := range wants.Parts.GetTrueIndices() {
		// we may have got this part while we waited for our turn.
		if have.GetIndex(index) {
			wants.Parts.SetIndex(index, false)
			continue
		}
		if requested.GetIndex(index) {
			// another peer may have been asked for this part in the meantime.
			requesters := len(blockProp.countRequests(wants.Height, wants.Round, index))
			if requesters >= reqLimit {
				wants.Parts.SetIndex(index, false)
			}
			// asking a second peer for the same part (small blocks) is free.
			continue
		}
		if budget <= 0 || askedFromPeer >= maxPerPeer {
			wants.Parts.SetIndex(index, false)
			continue
		}
		budget--
		askedFromPeer++
	}

	if wants.Parts.IsEmpty() {
		return nil, nil
	}
	haves, err := blockProp.convertWantToHave(wants)
	if err != nil {
		return nil, err
	}
	blockProp.sendWant(ps, wants)
	return haves, nil
}

// outstandingRequests counts the parts we requested from this peer and don't
// have yet.
func (blockProp *Reactor) outstandingRequests(ps *PeerState, height int64, round int32, have *bits.BitArray) int {
	reqs, has := ps.GetRequests(height, round)
	if !has || reqs.Size() != have.Size() {
		return 0
	}
	return len(reqs.Sub(have).GetTrueIndices())
}

// requestedParts returns the parts we already requested from any peer.
func (blockProp *Reactor) requestedParts(height int64, round int32, size int) *bits.BitArray {
	requested := bits.NewBitArray(size)
	for _, peer := range blockProp.getPeers() {
		reqs, has := peer.GetRequests(height, round)
		if !has || reqs.Size() != size {
			continue
		}
		requested = requested.Or(reqs)
	}
	return requested
}

// countRemainingParts counts the remaining parts to decode a block.
func countRemainingParts(totalParts, existingParts int) int32 {
	remainingPartsToDecode := totalParts / 2
	return int32(max(0, remainingPartsToDecode-existingParts))
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

	if !ps.RequestParts(want.Height, want.Round, want.Parts, e) {
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
		schema.Upload,
		"want",
	)

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
			blockProp.Logger.Debug("failed to send haves to peer", "peer", peer.peer.ID())
			continue
		}
		hb := haves.BitArray(partSetSize)
		peer.AddHaves(haves.Height, haves.Round, hb)
		for _, h := range hb.GetTrueIndices() {
			peer.consensusPeerState.SetHasProposalBlockPart(haves.Height, haves.Round, h)
		}
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
		return
	}

	// get data, use the prove as a proxy for determining if this Want message
	// if for catchup
	_, parts, has, committed := blockProp.getCachedState(height, round, wants.Prove)
	// at a committed height, only a complete copy of the committed block beats the
	// store: a want parked on any other entry may never be answered.
	if has && wants.Prove && committed {
		meta := blockProp.store.LoadBlockMeta(height)
		has = meta == nil || (parts.IsComplete() && parts.Original().Header().Equals(meta.BlockID.PartSetHeader))
	}
	if !has && wants.Prove && committed {
		blockProp.handleStoredWants(p, wants)
		return
	}
	// the peer must always send the proposal before sending parts, if they did
	//  not, this node must disconnect from them.
	if !has {
		blockProp.Logger.Debug("received part state request for unknown proposal", "peer", peer, "height", height, "round", round)
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

	// Only keep per-peer request state for a proposal cached at exactly this
	// height and round, which pruning bounds. Wants answered from the block
	// store are served without it, as they may name any round. If the peer
	// state caps refuse the counter, serve the want without it as well.
	untrackedRemaining := int(wants.MissingPartsCount)
	tracked := blockProp.hasCachedRound(height, round) && p.SetRemainingRequests(height, round, untrackedRemaining)
	for _, partIndex := range canSend.GetTrueIndices() {
		if tracked && p.GetRemainingRequests(height, round) <= 0 || !tracked && untrackedRemaining <= 0 {
			break
		}
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
		if tracked {
			p.DecreaseRemainingRequests(height, round, 1)
		} else {
			untrackedRemaining--
		}
		// p.SetHave(height, round, int(partIndex))
		schema.WriteBlockPart(blockProp.traceClient, height, round, part.Index, wants.Prove, string(peer), schema.Upload)
	}

	// for parts that we don't have, but they still want, store the wants.
	stillMissing := wants.Parts.Sub(canSend)
	if tracked && !stillMissing.IsEmpty() {
		p.AddWants(height, round, stillMissing)
	}
}

// handleStoredWants serves a catchup want for a committed height that is no
// longer cached. It loads from the block store only the parts it sends. It
// stops at the first failed send, and it takes each part load from a budget
// that all peers share. This bounds the work that small want messages can
// cause on the reactor's single processing goroutine.
func (blockProp *Reactor) handleStoredWants(p *PeerState, wants *proptypes.WantParts) {
	height, round := wants.Height, wants.Round
	peer := p.peer.ID()

	meta := blockProp.store.LoadBlockMeta(height)
	if meta == nil {
		blockProp.Logger.Debug("received catchup want for unknown height", "peer", peer, "height", height)
		return
	}
	// The block store keeps only the original parts. A want covers the
	// original and parity parts, so its size is twice the original total.
	total := int(meta.BlockID.PartSetHeader.Total)
	if wants.Parts.Size() != total*2 {
		blockProp.Logger.Error("received want part with invalid parts size", "peer", peer, "height", height, "round", round, "expected_parts_size", total*2, "received_parts_size", wants.Parts.Size())
		return
	}

	// The peer chooses the round, so count its remaining requests locally
	// instead of in its PeerState, which pruning would not bound.
	remaining := int(wants.MissingPartsCount)
	for _, partIndex := range wants.Parts.GetTrueIndices() {
		// Indices are ascending, so the rest are parity parts.
		if partIndex >= total {
			break
		}
		if remaining <= 0 {
			break
		}
		if !blockProp.storedPartsBudget.allow() {
			blockProp.Logger.Debug("stored parts budget exhausted", "peer", peer, "height", height)
			break
		}
		part := blockProp.store.LoadBlockPart(height, partIndex)
		if part == nil {
			blockProp.Logger.Error("stored block part not found", "height", height, "part", partIndex)
			return
		}
		e := p2p.Envelope{
			ChannelID: DataChannel,
			Message: &propproto.RecoveryPart{
				Height: height,
				Round:  round,
				Index:  part.Index,
				Data:   part.Bytes,
				Proof:  *part.Proof.ToProto(),
			},
		}
		if !p.peer.TrySend(e) {
			blockProp.Logger.Debug("failed to send stored part", "peer", peer, "height", height, "part", partIndex)
			break
		}
		remaining--
		schema.WriteBlockPart(blockProp.traceClient, height, round, part.Index, true, string(peer), schema.Upload)
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
		return
	}
	// the peer must always send the proposal before sending parts, if they did
	// not this node must disconnect from them.
	// proven parts answer catch-up wants, which may be for a round we already left.
	// Only an incomplete cached entry can use a part, so never read the store here.
	cb, parts, has, _ := blockProp.getCachedState(part.Height, part.Round, part.Proof != nil)
	if !has {
		blockProp.Logger.Debug("received part for unknown proposal", "peer", peer, "height", part.Height, "round", part.Round)
		// blockProp.Switch.StopPeerForError(p.peer, errors.New("received recovery part for unknown proposal"))
		return
	}

	// checked before admitPart, so a dropped part doesn't use up the peer's push
	// allowance. A part is still taken after the originals complete: it was
	// requested, and peers may want it because the request advertised it.
	if parts.HasPart(int(part.Index)) {
		return
	}
	// catchup entries hold no parity set to add to.
	if part.Index >= parts.Original().Total() && parts.Parity().Total() == 0 {
		return
	}

	// a part we accept without having requested it was pushed to us
	wasPushed := p != nil && !p.Requested(part.Height, part.Round, part.Index)

	if p != nil && !blockProp.admitPart(p, part) {
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

	// tell the other peers we have the pushed part, so they can request it.
	// For a requested part this was already done when the want was sent.
	if wasPushed {
		haves := &proptypes.HaveParts{
			Height: part.Height,
			Round:  part.Round,
			Parts:  []proptypes.PartMetaData{{Index: part.Index, Hash: proof.LeafHash}},
		}
		blockProp.broadcastHaves(haves, peer, int(parts.Total()))
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

	go blockProp.clearWants(part, *proof)

	// consensus votes on the originals: rebuild the missing ones first, then
	// build parity in the background so peers can still recover from us.
	if parts.IsComplete() {
		go blockProp.buildParity(cb, parts)
		return
	}
	if !parts.CanDecode() || !parts.IsDecoding.CompareAndSwap(false, true) {
		return
	}
	proofs, err := cb.Proofs()
	if err != nil {
		parts.IsDecoding.Store(false)
		blockProp.Logger.Error("failed to get proofs from compact block", "peer", peer, "height", part.Height, "round", part.Round, "error", err)
		return
	}
	recovered, err := parts.RecoverOriginals(proofs)
	parts.IsDecoding.Store(false)
	if err != nil {
		blockProp.Logger.Error("failed to recover original parts", "peer", peer, "height", part.Height, "round", part.Round, "error", err)
	}
	if len(recovered) == 0 {
		return
	}
	blockProp.publishLocalParts(part.Height, part.Round, recovered)
	go blockProp.buildParity(cb, parts)
}

// buildParity encodes the parity of a complete block, serves the wants parked
// on it and advertises every part.
func (blockProp *Reactor) buildParity(cb *proptypes.CompactBlock, parts *proptypes.CombinedPartSet) {
	if !parts.IsComplete() || !parts.CanDecode() || parts.Parity().IsComplete() ||
		!parts.IsDecoding.CompareAndSwap(false, true) {
		return
	}
	defer parts.IsDecoding.Store(false)
	height, round := cb.Proposal.Height, cb.Proposal.Round
	if err := parts.BuildParity(); err != nil {
		blockProp.Logger.Error("failed to build parity", "height", height, "round", round, "error", err)
		return
	}
	for i := parts.Original().Total(); i < parts.Total(); i++ {
		if p, has := parts.GetPart(i); has {
			blockProp.clearWants(&proptypes.RecoveryPart{Height: height, Round: round, Index: i, Data: p.Bytes}, p.GetProof())
		}
	}
	blockProp.broadcastLocalHaves(cb, parts)
}

// publishLocalParts hands parts this node added itself to consensus and
// serves the wants parked on them.
func (blockProp *Reactor) publishLocalParts(height int64, round int32, parts []*types.Part) {
	go func() {
		for _, p := range parts {
			blockProp.clearWants(&proptypes.RecoveryPart{Height: height, Round: round, Index: p.Index, Data: p.Bytes}, p.GetProof())
		}
	}()
	for _, p := range parts {
		select {
		case <-blockProp.ctx.Done():
			return
		case blockProp.partChan <- types.PartInfo{Part: p, Height: height, Round: round}:
		}
	}
}

// broadcastLocalHaves advertises the parts this node holds, never parity it
// does not hold.
func (blockProp *Reactor) broadcastLocalHaves(cb *proptypes.CompactBlock, parts *proptypes.CombinedPartSet) {
	held := parts.BitArray()
	haves := &proptypes.HaveParts{Height: cb.Proposal.Height, Round: cb.Proposal.Round}
	for _, index := range held.GetTrueIndices() {
		haves.Parts = append(haves.Parts, proptypes.PartMetaData{Index: uint32(index), Hash: cb.PartsHashes[index]})
	}
	blockProp.broadcastHaves(haves, blockProp.self, held.Size())
}

// admitPart reports whether a part from p may be processed: either this node
// requested it, or p sent the compact block and this is one of the parts it
// may push. Any other part is dropped. The peer stays connected, because an
// honest proposer can end up here when its compact block arrives late.
func (blockProp *Reactor) admitPart(p *PeerState, part *proptypes.RecoveryPart) bool {
	if p.Requested(part.Height, part.Round, part.Index) {
		return true
	}

	if p.ConsumePushAllowance(part.Height, part.Round, part.Index) {
		return true
	}

	blockProp.Logger.Debug("dropping unrequested part", "peer", p.peer.ID(), "height", part.Height, "round", part.Round, "part", part.Index)
	return false
}

// clearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
func (blockProp *Reactor) clearWants(part *proptypes.RecoveryPart, proof merkle.Proof) {
	for _, peer := range blockProp.getPeers() {
		if peer.WantsPart(part.Height, part.Round, part.Index) {
			if peer.GetRemainingRequests(part.Height, part.Round) <= 0 {
				continue
			}
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
			if part.Height < blockProp.height {
				catchup = true
			}

			blockProp.pmtx.Unlock()
			peer.DecreaseRemainingRequests(part.Height, part.Round, 1)
			schema.WriteBlockPart(blockProp.traceClient, part.Height, part.Round, part.Index, catchup, string(peer.peer.ID()), schema.Upload)
		}
	}
}
