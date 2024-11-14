package consensus

import (
	"fmt"
	"sync"

	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	cmtcons "github.com/tendermint/tendermint/proto/tendermint/consensus"
	cmttypes "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

// DataRoutine is responsible for gossiping block data. This includes proposal
// data and catchup data.
type DataRoutine struct {
	store   *store.BlockStore
	tracer  trace.Tracer
	logger  log.Logger
	pswitch *p2p.Switch

	mtx       *sync.RWMutex
	peerState map[p2p.ID]*dataPeerState

	currentProposal *types.Proposal
	currentBlock    *types.PartSet
	previousBlock   *types.PartSet // previous block is kept for catchup
}

func NewDataRoutine(logger log.Logger, tracer trace.Tracer, store *store.BlockStore, pswitch *p2p.Switch) *DataRoutine {
	return &DataRoutine{
		store:     store,
		tracer:    tracer,
		logger:    logger,
		mtx:       &sync.RWMutex{},
		peerState: make(map[p2p.ID]*dataPeerState),
	}
}

// handleProposal adds a proposal to the data routine. This should be called any
// time a proposal is recevied from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
func (d *DataRoutine) handleProposal(proposal *types.Proposal, from p2p.ID) {

	if !d.isNewProposal(proposal) {
		d.handleHaves(from, proposal.Height, proposal.Round, proposal.HaveParts)
		return
	}

	d.mtx.Lock()
	d.previousBlock = d.currentBlock
	d.currentBlock = types.NewPartSetFromHeader(proposal.BlockID.PartSetHeader)
	d.currentProposal = proposal
	d.mtx.Unlock()

	d.handleHaves(from, proposal.Height, proposal.Round, proposal.HaveParts)
	d.broadcastProposal(proposal, from)
}

func (d *DataRoutine) isNewProposal(prop *types.Proposal) bool {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	if d.currentProposal == nil {
		d.currentProposal = prop
		d.currentBlock = types.NewPartSetFromHeader(prop.BlockID.PartSetHeader)
		return true
	}
	return prop.Height > d.currentProposal.Height || (prop.Height == d.currentProposal.Height && prop.Round > d.currentProposal.Round)
}

func (d *DataRoutine) handlePartState(peer p2p.ID, ps *types.PartState) {
	if d.currentProposal == nil {
		d.logger.Error("received part state without a proposal", "peer", peer)
		return
	}

	if ps.Height != d.currentProposal.Height {
		d.logger.Debug("received part state for wrong height", "peer", peer, "height", ps.Height, "expected_height", d.currentProposal.Height)
		return
	}

}

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts.
func (d *DataRoutine) handleHaves(peer p2p.ID, height int64, round int32, haves *bits.BitArray) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()

	// if the haves are for a future block, then that means the peer didn't send
	// us the proposal for that future block, in which case we should disconnect
	// from that peer.
	if d.currentProposal == nil {
		err := fmt.Errorf("received have without a proposal: peer %v height %d round %d", peer, height, round)
		d.logger.Error(err.Error())
		d.pswitch.StopPeerForError(d.peerState[peer].peer, err)
		return
	}

	if d.currentProposal.Height < height || (d.currentProposal.Height == height && d.currentProposal.Round < round) {
		err := fmt.Errorf("received have without a proposal: peer %v height %d round %d", peer, height, round)
		d.logger.Error(err.Error())
		d.pswitch.StopPeerForError(d.peerState[peer].peer, err)
		return
	}

	// if the haves are for a block in the past, ignore them.
	if d.currentProposal.Height > height || (d.currentProposal.Height == height && d.currentProposal.Round > round) {
		d.logger.Debug("received have for old proposal", "peer", peer, "height", height, "round", round)
		return
	}

	// Update the peer's haves.
	d.peerState[peer].SetHaves(height, round, haves)

	// Check if the sender has parts that we don't have.
	hc := haves.Copy()
	hc.Sub(d.currentBlock.BitArray())
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

	if !p2p.SendEnvelopeShim(d.peerState[peer].peer, e, d.logger) {
		d.logger.Error("failed to send part state", "peer", peer, "height", height, "round", round)
		return
	}

	// send the haves that we have now reqested to the rest of our peers.
	d.broadcastHaves(height, round, hc, peer)
}

// handleWants is called when a peer sends a want message. This is used to send
// peers data that this node already has and store the wants to send them data
// in the future.
func (d *DataRoutine) handleWants(peer p2p.ID, height int64, round int32, wants *bits.BitArray) {

	ps, err := d.getPartSet(height, round)
	if err != nil {
		d.logger.Error("failed to get part set", "height", height, "round", round, "error", err)
		return
	}

	// if we have the parts, send them to the peer.
	wc := wants.Copy()
	canSend := ps.BitArray().Sub(wc)
	for _, partIndex := range canSend.GetTrueIndices() {
		part := ps.GetPart(partIndex)
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

		p2p.SendEnvelopeShim(d.peerState[peer].peer, e, d.logger)
	}

	// for parts that we don't have but they still want, store the wants.
	stillMissing := wants.Sub(canSend)
	d.peerState[peer].SetWants(height, round, stillMissing)
}

// getProposal returns the proposal for the given height and round.
func (d *DataRoutine) getPartSet(height int64, round int32) (*types.PartSet, error) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()

	if d.currentBlock == nil {
		return nil, fmt.Errorf("no current proposal for height %d and round %d", height, round)
	}

	if d.currentProposal.Height < height {
		return nil, fmt.Errorf("current block is less than the requested height %d and round %d", height, round)
	}

	if d.currentProposal.Height == height && d.currentProposal.Round == round {
		return d.currentBlock, nil
	}

	if d.previousBlock == nil {
		return nil, fmt.Errorf("no previous block for height %d and round %d", height, round)
	}

	if d.currentProposal.Height-1 == height {
		return d.previousBlock, nil
	}

	// attempt to load the previous block from the store
	partset := d.store.LoadPartSet(height)
	if partset == nil {
		return nil, fmt.Errorf("no part set for height %d", height)
	}

	return partset, nil
}

// handleBlockPart is called when a peer sends a block part message. This is used
// to store the part and clear any wants for that part.
func (d *DataRoutine) handleBlockPart(peer p2p.ID, part *BlockPartMessage) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()

	if d.currentProposal == nil {
		d.logger.Error("received block part without a proposal", "peer", peer)
		return
	}

	// todo(future): enable the ability to request and receive parts for more than one blocks.
	if d.currentProposal.Height != part.Height || d.currentProposal.Round != part.Round {
		d.logger.Error("received block part for wrong proposal", "peer", peer, "height", part.Height, "round", part.Round)
		return
	}

	if d.currentBlock == nil {
		d.logger.Error("received block part without a part set", "peer", peer)
		return
	}

	if _, err := d.currentBlock.AddPart(part.Part); err != nil {
		d.logger.Error("failed to add part to part set", "peer", peer, "height", part.Height, "round", part.Round, "part", part.Part.Index, "error", err)
		return
	}

	d.clearWants(part)
}

// broadcastProposal gossips the provided proposal to all peers. This should
// only be called upon receiving a proposal for the first time or after creating
// a proposal block.
func (d *DataRoutine) broadcastProposal(proposal *types.Proposal, from p2p.ID) {
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message:   &cmtcons.Proposal{Proposal: *proposal.ToProto()},
	}
	for _, peer := range d.peerState {
		if peer.peer.ID() == from {
			continue
		}
		// todo(evan): don't rely strictly on try, however since we're using
		// pull based gossip, this isn't as big as a deal since if someone asks
		// for data, they must already have the proposal.
		p2p.SendEnvelopeShim(peer.peer, e, d.logger)
	}
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
	for _, peer := range d.peerState {
		if peer.peer.ID() == from {
			continue
		}
		// todo(evan): don't rely strictly on try, however since we're using
		// pull based gossip, this isn't as big as a deal since if someone asks
		// for data, they must already have the proposal.
		p2p.SendEnvelopeShim(peer.peer, e, d.logger)
	}
}

// ClearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
func (d *DataRoutine) clearWants(part *BlockPartMessage) {
	var (
		ppart *cmttypes.Part
		err   error
	)
	for _, peer := range d.peerState {
		if peer.WantsPart(part.Height, part.Round, part.Part.Index) {
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
			p2p.SendEnvelopeShim(peer.peer, e, d.logger)
		}
	}
}

// AddPeer adds the peer to the data routine. This should be called when a peer
// is connected. The proposal is sent to the peer so that it can start catchup
// or request data.
func (d *DataRoutine) AddPeer(peer p2p.Peer) {
	d.peerState[peer.ID()] = newDataPeerState(peer)

	if d.currentProposal == nil {
		return
	}

	proposal := d.currentProposal
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: DataChannel,
		Message:   &cmtcons.Proposal{Proposal: *proposal.ToProto()},
	}

	p2p.TrySendEnvelopeShim(peer, e, d.logger)
}

// RemovePeer removes the peer data routine. This should be called when a peer
// is disconnected.
func (d *DataRoutine) RemovePeer(peer p2p.Peer) {
	delete(d.peerState, peer.ID())
}

// DeleteHeight removes all proposals and wants for a given height.
func (d *DataRoutine) DeleteHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	for _, peer := range d.peerState {
		peer.DeleteHeight(height)
	}
}
