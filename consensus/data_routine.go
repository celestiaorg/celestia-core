package consensus

import (
	"fmt"
	"sync"

	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	cmtcons "github.com/tendermint/tendermint/proto/tendermint/consensus"
	cmttypes "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

const (
	cacheBlockCount = 10 // todo(evan): cached blocks is huge just to hack
	cacheRoundCount = 50 // todo(evan): cached rounds is huge just to hack around nodes accidentlly erasing POL proposal
)

// DataRoutine is responsible for gossiping block data. This includes proposal
// data and catchup data.
type DataRoutine struct {
	tracer  trace.Tracer
	logger  log.Logger
	pswitch *p2p.Switch
	self    p2p.ID

	// ProposalCache temporarily stores recently active proposals and their
	// block data for gossiping.
	*ProposalCache

	mtx       *sync.RWMutex
	peerstate map[p2p.ID]*dataPeerState
}

func NewDataRoutine(logger log.Logger, tracer trace.Tracer, store *store.BlockStore, pswitch *p2p.Switch, self p2p.ID) *DataRoutine {
	dr := &DataRoutine{
		tracer:        tracer,
		logger:        logger,
		mtx:           &sync.RWMutex{},
		peerstate:     make(map[p2p.ID]*dataPeerState),
		pswitch:       pswitch,
		ProposalCache: NewProposalCache(store),
		self:          self,
	}

	return dr
}

// getPeer returns the peer state for the given peer. If the peer does not exist,
// nil is returned.
func (d *DataRoutine) getPeer(peer p2p.ID) *dataPeerState {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	return d.peerstate[peer]
}

// getPeers returns a list of all peers that the data routine is aware of.
func (d *DataRoutine) getPeers() []*dataPeerState {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	peers := make([]*dataPeerState, 0, len(d.peerstate))
	for _, peer := range d.peerstate {
		peers = append(peers, peer)
	}
	return peers
}

// setPeer sets the peer state for the given peer.
func (d *DataRoutine) setPeer(peer p2p.ID, state *dataPeerState) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.peerstate[peer] = state
}

// Stop stops the data routine.
func (d *DataRoutine) Stop() {
}

// proposeBlock is called when the consensus routine has created a new proposal
// and it needs to be gossiped to the rest of the network.
func (d *DataRoutine) proposeBlock(proposal *types.Proposal) {
	d.handleProposal(proposal, d.self, true)
}

// handleProposal adds a proposal to the data routine. This should be called any
// time a proposal is recevied from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
func (d *DataRoutine) handleProposal(proposal *types.Proposal, from p2p.ID, pbbt bool) {
	// fmt.Println("handleProposal", proposal.Height, proposal.Round, from)
	// set the from to the node's ID if it is empty.
	if from == "" {
		from = d.self
	}

	// todo(evan): handle proposals with a POLRound > -1.
	added, _, _ := d.AddProposal(proposal)

	// don't handle haves if the proposal is from this node
	if from != d.self {
		// handle the haves before this node broadcasts the proposal because the
		// node must request data it doesn't have before it sends the proposal.
		d.handleHaves(from, proposal.Height, proposal.Round, proposal.HaveParts, false)
	}

	if added {
		go d.broadcastProposal(proposal, from, pbbt)

		// if the proposal is new and we still don't have a complete block for
		// the last block, request it from this peer.
		// cprop, cps, has := d.GetCurrentProposal()
		// if !has {
		// 	ba := bits.NewBitArray(int(1))
		// 	d.handleHaves(from, proposal.Height-1, -1, true)
		// }
	}
}

func (d *DataRoutine) handlePartState(peer p2p.ID, ps *types.PartState) {
	// fmt.Println("handlePartState", peer, ps.Height, ps.Round, ps.Have)
	switch ps.Have {
	case true:
		d.handleHaves(peer, ps.Height, ps.Round, ps.Parts, false)
	case false:
		d.handleWants(peer, ps.Height, ps.Round, ps.Parts)
	}
}

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts.
func (d *DataRoutine) handleHaves(peer p2p.ID, height int64, round int32, haves *bits.BitArray, bypassRequestLimit bool) {
	// fmt.Println("handleHaves", peer, height, round, haves)
	p := d.getPeer(peer)
	if p == nil || p.peer == nil {
		d.logger.Error("peer not found", "peer", peer)
		return
	}
	_, parts, fullReqs, has := d.GetProposal(height, round)
	// the peer must always send the proposal before sending parts, if they did
	// not this node must disconnect from them.
	if !has {
		// fmt.Println("unknown proposal", height, round, "from", peer)
		d.logger.Error("received part state for unknown proposal", "peer", peer, "height", height, "round", round)
		// d.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part state for unknown proposal"))
		return
	}

	if haves == nil {
		// fmt.Println("nil no parts to request", height, round)
		return
	}

	d.mtx.RLock()
	defer d.mtx.RUnlock()

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
		reqLimit = 6
	}

	// if enough requests have been made for the parts, don't request them.
	for _, partIndex := range hc.GetTrueIndices() {
		reqs := d.countRequests(height, round, partIndex)
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

	// todo(evan): check that this is legit. we can also exit early if we have
	// all of the data already
	if hc.IsEmpty() {
		return
	}

	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message: &cmtcons.PartState{
			PartState: &cmttypes.PartState{
				Height: height,
				Round:  round,
				Have:   false,
				Parts:  *hc.ToProto(),
			},
		},
	}

	if !p2p.SendEnvelopeShim(p.peer, e, d.logger) {
		d.logger.Error("failed to send part state", "peer", peer, "height", height, "round", round)
		return
	}

	schema.WriteBlockPartState(
		d.tracer,
		height,
		round,
		hc.GetTrueIndices(),
		false,
		string(peer),
		schema.Haves,
	)

	// keep track of the parts that this node has requested.
	p.SetRequests(height, round, hc)
	d.broadcastHaves(height, round, hc, peer)
}

// handleValidBlock is called the node finds a peer with a valid block. If this
// node doesn't have a block, it asks the sender for the portions that it
// doesn't have.
func (d *DataRoutine) handleValidBlock(peer p2p.ID, height int64, round int32, psh types.PartSetHeader, exitEarly bool) {
	p := d.getPeer(peer)
	if p == nil || p.peer == nil {
		d.logger.Error("peer not found", "peer", peer)
		return
	}

	// prepare the routine to receive the proposal
	_, ps, _, has := d.GetProposal(height, round)
	if has {
		if ps.IsComplete() {
			return
		}
		// assume that
		ba := bits.NewBitArray(int(psh.Total))
		if ba == nil {
			ba = bits.NewBitArray(1)
		}
		ba.Fill()
		schema.WriteNote(
			d.tracer,
			height,
			-1,
			"handleValidBlock",
			"found incomplete block: %v/%v",
			height, round,
		)
		d.handleHaves(peer, height, round, ba, true)
		return
	}

	d.pmtx.Lock()
	if _, ok := d.proposals[height]; !ok {
		d.proposals[height] = make(map[int32]*proposalData)
	}
	d.proposals[height][round] = &proposalData{
		block:       types.NewPartSetFromHeader(psh),
		maxRequests: bits.NewBitArray(int(psh.Total)),
	}
	d.pmtx.Unlock()

	// todo(evan): remove this hack and properly abstract logic
	if exitEarly {
		return
	}

	haves := bits.NewBitArray(int(psh.Total))
	if psh.Total < 1 {
		d.logger.Error("invalid part set header", "peer", peer, "height", height, "round", round, "total", psh.Total)
		haves = bits.NewBitArray(1)
	}

	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message: &cmtcons.PartState{
			PartState: &cmttypes.PartState{
				Height: height,
				Round:  round,
				Have:   false,
				Parts:  *haves.ToProto(),
			},
		},
	}

	if !p2p.SendEnvelopeShim(p.peer, e, d.logger) {
		d.logger.Error("failed to send part state", "peer", peer, "height", height, "round", round)
		return
	}

	p.SetRequests(height, round, haves)

	schema.WriteBlockPartState(
		d.tracer,
		height,
		round,
		haves.GetTrueIndices(),
		false,
		string(p.peer.ID()),
		schema.AskForProposal,
	)

	d.requestAllPreviousBlocks(peer, height)
}

// requestAllPreviousBlocks is called when a node is catching up and needs to
// request all previous blocks from a peer.
func (d *DataRoutine) requestAllPreviousBlocks(peer p2p.ID, height int64) {
	p := d.getPeer(peer)
	if p == nil || p.peer == nil {
		d.logger.Error("peer not found", "peer", peer)
		return
	}

	d.pmtx.RLock()
	currentHeight := d.currentHeight
	d.pmtx.RUnlock()
	for i := currentHeight; i < height; i++ {
		haves := bits.NewBitArray(1)
		_, ps, _, has := d.GetProposal(i, -1)
		if has {
			if ps.IsComplete() {
				continue
			}
			haves = ps.BitArray()
		}

		// todo(evan): maybe check if the peer has already been sent a request
		// or we have already sent enough requests

		e := p2p.Envelope{ //nolint: staticcheck
			ChannelID: DataChannel,
			Message: &cmtcons.PartState{
				PartState: &cmttypes.PartState{
					Height: i,
					Round:  -1, // -1 round means that we don't have the psh or the proposal and the peer needs to send us this first
					Have:   false,
					Parts:  *haves.ToProto(),
				},
			},
		}

		if !p2p.SendEnvelopeShim(p.peer, e, d.logger) {
			d.logger.Error("failed to send part state", "peer", peer, "height", height, "round", -1)
			return
		}

		p.SetRequests(height, -1, haves)

		schema.WriteBlockPartState(
			d.tracer,
			i,
			-1,
			haves.GetTrueIndices(),
			false,
			string(p.peer.ID()),
			schema.AskForProposal,
		)

	}
}

// shuffle randomizes a generic slice.
func shuffle[T any](s []T) []T {
	for i := range s {
		j := i + rand.Intn(len(s)-i)
		s[i], s[j] = s[j], s[i]
	}

	return s
}

// handleWants is called when a peer sends a want message. This is used to send
// peers data that this node already has and store the wants to send them data
// in the future.
func (d *DataRoutine) handleWants(peer p2p.ID, height int64, round int32, wants *bits.BitArray) {
	// fmt.Println("handleWants", peer, height, round, wants)
	p := d.getPeer(peer)
	if p == nil {
		d.logger.Error("peer not found", "peer", peer)
		return
	}

	_, parts, _, has := d.GetProposal(height, round)
	// the peer must always send the proposal before sending parts, if they did
	// not this node must disconnect from them.
	if !has {
		d.logger.Error("received part state requestfor unknown proposal", "peer", peer, "height", height, "round", round)
		// d.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part state for unknown proposal"))
		return
	}

	// send the peer the partsetheader if they don't have the propsal.
	if round < 0 {
		if !d.sendPsh(peer, height, round) {
			d.logger.Error("failed to send PSH", "peer", peer, "height", height, "round", round)
			return
		}
	}

	// if we have the parts, send them to the peer.
	wc := wants.Copy()

	// send all the parts if the peer doesn't know which parts to request
	if wc.IsEmpty() {
		wc = parts.BitArray()
	}

	canSend := parts.BitArray().And(wc)
	if canSend == nil {
		d.logger.Error("nil can send?", "peer", peer, "height", height, "round", round, "wants", wants, "wc", wc)
		return
	}
	for _, partIndex := range canSend.GetTrueIndices() {
		part := parts.GetPart(partIndex)
		ppart, err := part.ToProto()
		if err != nil {
			d.logger.Error("failed to convert part to proto", "height", height, "round", round, "part", partIndex, "error", err)
			continue
		}
		e := p2p.Envelope{ //nolint: staticcheck
			ChannelID: DataChannel,
			Message: &cmtcons.BlockPart{
				Height: height,
				Round:  round,
				Part:   *ppart,
			},
		}

		if !p2p.SendEnvelopeShim(p.peer, e, d.logger) {
			d.logger.Error("failed to send part", "peer", peer, "height", height, "round", round, "part", partIndex)
			continue
		}
		// p.SetHave(height, round, int(partIndex))
		schema.WriteBlockPartState(d.tracer, height, round, []int{int(partIndex)}, true, string(peer), schema.AskForProposal)
	}

	// for parts that we don't have but they still want, store the wants.
	stillMissing := wants.Sub(canSend)
	if !stillMissing.IsEmpty() {
		p.SetWants(height, round, stillMissing)
	}
}

// handleBlockPart is called when a peer sends a block part message. This is used
// to store the part and clear any wants for that part.
func (d *DataRoutine) handleBlockPart(peer p2p.ID, part *BlockPartMessage) {
	// fmt.Println("handleBlockPart", peer, part.Height, part.Round, part.Part.Index)
	if peer == "" {
		peer = d.self
	}
	p := d.getPeer(peer)
	if p == nil && peer != d.self {
		d.logger.Error("peer not found", "peer", peer)
		return
	}
	// the peer must always send the proposal before sending parts, if they did
	// not this node must disconnect from them.
	_, parts, _, has := d.GetProposal(part.Height, part.Round)
	if !has {
		// fmt.Println("unknown proposal")
		d.logger.Error("received part for unknown proposal", "peer", peer, "height", part.Height, "round", part.Round)
		// d.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part for unknown proposal"))
		return
	}

	if parts.IsComplete() {
		return
	}

	added, err := parts.AddPart(part.Part)
	if err != nil {
		d.logger.Error("failed to add part to part set", "peer", peer, "height", part.Height, "round", part.Round, "part", part.Part.Index, "error", err)
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
		err := parts.Decode()
		if err != nil {
			d.logger.Error("YOOO FAILED TO DECODE THE BLOCK", "height", part.Height, "round", part.Round, "error", err)
			return
		}

		d.logger.Info("decoded block", "height", part.Height, "round", part.Round)

		schema.WriteNote(
			d.tracer,
			part.Height,
			part.Round,
			"decoded block!",
			"",
		)

		// clear all the wants if they exist
		go func(height int64, round int32, parts *types.PartSet) {
			for i := uint32(0); i < parts.Total(); i++ {
				msg := &BlockPartMessage{
					Height: height,
					Round:  round,
					Part:   parts.GetPart(int(i)),
				}
				d.clearWants(msg)
			}
		}(part.Height, part.Round, parts)

		return
	}

	// todo(evan): temporarily disabling
	// go d.broadcastHaves(part.Height, part.Round, parts.BitArray(), peer)
	go d.clearWants(part)
}

// ClearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
func (d *DataRoutine) clearWants(part *BlockPartMessage) {
	var (
		ppart *cmttypes.Part
		err   error
	)
	for _, peer := range d.getPeers() {
		if peer.WantsPart(part.Height, part.Round, part.Part.Index) {
			// premature optimization to only serialize the struct once.
			if ppart == nil {
				ppart, err = part.Part.ToProto()
				if err != nil {
					d.logger.Error("failed to convert part to proto", "height", part.Height, "round", part.Round, "part", part.Part.Index, "error", err)
					return
				}
			}
			e := p2p.Envelope{ //nolint: staticcheck
				ChannelID: DataChannel,
				Message:   &cmtcons.BlockPart{Height: part.Height, Round: part.Round, Part: *ppart},
			}
			if p2p.SendEnvelopeShim(peer.peer, e, d.logger) {
				peer.SetHave(part.Height, part.Round, int(part.Part.Index))
				peer.SetWant(part.Height, part.Round, int(part.Part.Index), false)
				catchup := false
				d.pmtx.RLock()
				if part.Height < d.currentHeight {
					catchup = true
				}
				d.pmtx.RUnlock()
				schema.WriteBlockPart(d.tracer, part.Height, part.Round, part.Part.Index, catchup, string(peer.peer.ID()), schema.Upload)
			}
		}
	}
}

func (d *DataRoutine) LockForNoReason() {
	d.mtx.Lock()
	peerCount := len(d.peerstate)
	d.mtx.Unlock()
	schema.WriteNote(
		d.tracer,
		0,
		-1,
		"LockForNoReason",
		"took mtx lock: peers %v",
		peerCount,
	)
	d.pmtx.Lock()
	d.pmtx.Unlock()
	schema.WriteNote(
		d.tracer,
		0,
		-1,
		"LockForNoReason",
		"took pmtx lock",
	)
}

func (d *DataRoutine) sendPsh(peer p2p.ID, height int64, round int32) bool {
	var psh types.PartSetHeader
	_, parts, _, has := d.GetProposal(height, round)
	if !has {
		d.logger.Error("unknown proposal", "height", height, "round", round)
		return false
	}
	if has {
		psh = parts.Header()
	} else {
		meta := d.store.LoadBlockMeta(height)
		if meta == nil {
			d.logger.Error("failed to load block meta", "height", height)
			return false
		}
		psh = meta.BlockID.PartSetHeader
	}
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel, // note that we're sending over the data channel instead of state!
		Message: &cmtcons.NewValidBlock{
			Height:             height,
			Round:              round,
			BlockPartSetHeader: psh.ToProto(),
		},
	}

	return p2p.SendEnvelopeShim(d.getPeer(peer).peer, e, d.logger)
}

// broadcastProposal gossips the provided proposal to all peers. This should
// only be called upon receiving a proposal for the first time or after creating
// a proposal block. pbbt determines if the proposal should be broadcasted in portions
func (d *DataRoutine) broadcastProposal(proposal *types.Proposal, from p2p.ID, pbbt bool) {
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message:   &cmtcons.Proposal{Proposal: *proposal.ToProto()},
	}

	peers := d.getPeers()
	chunks := chunkParts(proposal.HaveParts.Copy(), len(peers), 2)

	for i, peer := range peers {
		if peer.peer.ID() == from {
			continue
		}

		// pbbt (pull based broadcast tree) indicates that the proposal should
		// only include a portion of the block and not the entiriety.
		if pbbt {
			// send each part of the proposal to three random peers
			proposal.HaveParts = chunks[i]
			e = p2p.Envelope{ //nolint: staticcheck
				ChannelID: DataChannel,
				Message:   &cmtcons.Proposal{Proposal: *proposal.ToProto()},
			}
		}

		// todo(evan): don't rely strictly on try, however since we're using
		// pull based gossip, this isn't as big as a deal since if someone asks
		// for data, they must already have the proposal.
		if !p2p.SendEnvelopeShim(peer.peer, e, d.logger) {
			d.logger.Error("failed to send proposal to peer", "peer", peer.peer.ID())
			continue
		}
		schema.WriteProposal(d.tracer, proposal.Height, proposal.Round, string(peer.peer.ID()), schema.Upload)
		// if 0 <= proposal.POLRound {
		// 	p2p.SendEnvelopeShim(peer.peer, p2p.Envelope{ //nolint: staticcheck
		// 		ChannelID: DataChannel,
		// 		Message: &cmtcons.ProposalPOL{
		// 			Height:           proposal.Height,
		// 			ProposalPolRound: proposal.POLRound,
		// 			// ProposalPol:      ,
		// 		},
		// 	},
		// 		d.logger,
		// 	)
		// }
	}
}
func chunkParts(p *bits.BitArray, peerCount, redundancy int) []*bits.BitArray {
	size := p.Size()
	if peerCount == 0 {
		peerCount = 1
	}
	chunkSize := size / peerCount
	// round up to use the ceil
	if size%peerCount != 0 || chunkSize == 0 {
		chunkSize++
	}

	// Create empty bit arrays for each peer
	parts := make([]*bits.BitArray, peerCount)
	for i := 0; i < peerCount; i++ {
		parts[i] = bits.NewBitArray(size)
	}

	chunks := chunkIndexes(size, chunkSize)
	cursor := 0
	for p := 0; p < peerCount; p++ {
		for r := 0; r < redundancy; r++ {
			start, end := chunks[cursor][0], chunks[cursor][1]
			for i := start; i < end; i++ {
				parts[p].SetIndex(i, true)
			}
			cursor++
			if cursor >= len(chunks) {
				cursor = 0
			}
		}
	}

	return parts
}

func chunkIndexes(totalSize, chunkSize int) [][2]int {
	if totalSize <= 0 || chunkSize <= 0 {
		panic(fmt.Sprintf("invalid input: totalSize=%d, chunkSize=%d \n", totalSize, chunkSize))
		// return nil // Handle invalid input gracefully
	}

	var chunks [][2]int
	for start := 0; start < totalSize; start += chunkSize {
		end := start + chunkSize
		if end > totalSize {
			end = totalSize // Ensure the last chunk doesn't exceed the total size
		}
		chunks = append(chunks, [2]int{start, end})
	}

	return chunks
}

// broadcastHaves gossips the provided have msg to all peers except to the
// original sender. This should only be called upon receiving a new have for the
// first time.
func (d *DataRoutine) broadcastHaves(height int64, round int32, haves *bits.BitArray, from p2p.ID) {
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message: &cmtcons.PartState{
			PartState: &cmttypes.PartState{
				Height: height,
				Round:  round,
				Have:   true,
				Parts:  *haves.ToProto(),
			},
		},
	}
	for _, peer := range d.getPeers() {
		if peer.peer.ID() == from {
			continue
		}

		// skip sending anything to this peer if they already have all of the
		// parts.
		ph, has := peer.GetHaves(height, round)
		if has {
			remaining := haves.Copy().Sub(ph.Copy())
			if remaining.IsEmpty() {
				continue
			}
		}

		// todo(evan): don't rely strictly on try, however since we're using
		// pull based gossip, this isn't as big as a deal since if someone asks
		// for data, they must already have the proposal.
		if p2p.SendEnvelopeShim(peer.peer, e, d.logger) {
			schema.WriteBlockPartState(d.tracer, height, round, haves.GetTrueIndices(), true, string(peer.peer.ID()), schema.Upload)
		}
	}
}

// AddPeer adds the peer to the data routine. This should be called when a peer
// is connected. The proposal is sent to the peer so that it can start catchup
// or request data.
func (d *DataRoutine) AddPeer(peer p2p.Peer) {
	// ignore the peer if it is ourselves. This makes setting up tests easier. :P
	if peer.ID() == d.self {
		return
	}

	// ignore the peer if it already exists.
	if p := d.getPeer(peer.ID()); p != nil {
		return
	}

	d.setPeer(peer.ID(), newDataPeerState(peer))
	currentProposal, _, has := d.GetCurrentProposal()
	if !has {
		return
	}
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message:   &cmtcons.Proposal{Proposal: *currentProposal.ToProto()},
	}

	if !p2p.SendEnvelopeShim(peer, e, d.logger) {
		d.logger.Error("failed to send proposal to new peer", "peer", peer.ID())
	}
}

func (d DataRoutine) prune() {
	d.ProposalCache.prune(cacheBlockCount, cacheRoundCount)
	for _, peer := range d.getPeers() {
		peer.prune(d.currentHeight, cacheBlockCount, cacheRoundCount)
	}
}

// RemovePeer removes the peer data routine. This should be called when a peer
// is disconnected.
func (d *DataRoutine) RemovePeer(peer p2p.Peer) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	delete(d.peerstate, peer.ID())
}

// todo(evan): refactor to not iterate so often and just store which peers
func (d DataRoutine) countRequests(height int64, round int32, part int) []p2p.ID {
	peers := []p2p.ID{}
	for _, peer := range d.getPeers() {
		if reqs, has := peer.GetRequests(height, round); has && reqs.GetIndex(part) {
			peers = append(peers, peer.peer.ID())
		}
	}
	return peers
}

// DeleteHeight removes all proposals and wants for a given height.
func (d *DataRoutine) DeleteHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	for _, peer := range d.getPeers() {
		peer.DeleteHeight(height)
	}
}
