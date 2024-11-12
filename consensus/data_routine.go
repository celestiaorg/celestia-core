package consensus

import (
	"sync"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	cmtcons "github.com/tendermint/tendermint/proto/tendermint/consensus"
	"github.com/tendermint/tendermint/proto/tendermint/libs/bits"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

// DataRoutine is responsible for gossiping block data. This includes proposal
// data and catchup data.
type DataRoutine struct {
	store  *store.BlockStore
	tracer trace.Tracer
	logger log.Logger

	mtx           *sync.RWMutex
	currentHeight int64
	currentRound  int32
	proposals     map[int64]map[int32]*types.Proposal
	peerState     map[p2p.ID]*dataPeerState

	currentBlock  *types.PartSet
	previousBlock *types.PartSet // previous block is kept for catchup
}

func NewDataRoutine(logger log.Logger, tracer trace.Tracer, store *store.BlockStore) *DataRoutine {
	return &DataRoutine{
		store:     store,
		tracer:    tracer,
		logger:    logger,
		mtx:       &sync.RWMutex{},
		proposals: make(map[int64]map[int32]*types.Proposal),
		peerState: make(map[p2p.ID]*dataPeerState),
	}
}

// handleProposal adds a proposal to the data routine. This should be called any
// time a proposal is recevied from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
func (d *DataRoutine) handleProposal(proposal *types.Proposal, from p2p.ID) {
	d.mtx.Lock()
	defer d.mtx.Unlock()

	// keep track of the sender's haves, even if we've seen this proposal.
	d.peerState[from].SetHaves(proposal.Height, proposal.Round, proposal.HaveParts)

	h, has := d.proposals[proposal.Height]
	if !has {
		h = make(map[int32]*types.Proposal)
		d.proposals[proposal.Height] = h
	}

	// return if the proposal has already been seen.
	if _, has := h[proposal.Round]; !has {
		return
	}

	d.currentHeight = proposal.Height
	d.currentRound = proposal.Round

	d.previousBlock = d.currentBlock
	d.currentBlock = types.NewPartSetFromHeader(proposal.BlockID.PartSetHeader)

	d.broadcastProposal(proposal, from)
}

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have.
func (d *DataRoutine) handleHaves(peer p2p.ID, haves *bits.BitArray) {

}

// handleWants is called when a peer sends a want message. This is used to send
// peers data that this node already has and store the wants to send them data
// in the future.
func (d *DataRoutine) handleWants(peer p2p.ID, wants *bits.BitArray) {

}

// broadcastProposal gossips the provided proposal to all peers. This should
// only be called upon receiving a proposal for the first time or after creating
// a proposal block.
func (d *DataRoutine) broadcastProposal(proposal *types.Proposal, from p2p.ID) {
	// make the assumption that the rest of the consensus reactor is providing a
	// single proposal per round.
	d.proposals[proposal.Height][proposal.Round] = proposal
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
		p2p.TrySendEnvelopeShim(peer.peer, e, d.logger)
	}
}

// ClearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
func (d *DataRoutine) clearWants(height int64, round int32, part uint32) {
	for _, peer := range d.peerState {
		if peer.WantsPart(height, round, part) {
			e := p2p.Envelope{ //nolint: staticcheck
				ChannelID: DataChannel,
				Message:   &cmtcons.BlockPart{},
			}
			p2p.TrySendEnvelopeShim(peer.peer, e, d.logger)
		}
	}
}

// AddPeer adds the peer to the data routine. This should be called when a peer
// is connected. The proposal is sent to the peer so that it can start catchup
// or request data.
func (d *DataRoutine) AddPeer(peer p2p.Peer) {
	d.peerState[peer.ID()] = newDataPeerState(peer)

	proposal, has := d.GetProposal(d.currentHeight, d.currentRound)
	if !has {
		d.logger.Error("system error: failed to get proposal for latest height and round")
		return
	}

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

// GetProposal returns the proposal for the given height and round, if it exists.
func (d *DataRoutine) GetProposal(height int64, round int32) (*types.Proposal, bool) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	h, has := d.proposals[height]
	if !has {
		return nil, false
	}
	p, has := h[round]
	return p, has
}

// DeleteHeight removes all proposals and wants for a given height.
func (d *DataRoutine) DeleteHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	delete(d.proposals, height)
	for _, peer := range d.peerState {
		peer.DeleteHeight(height)
	}
}
