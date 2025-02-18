package propagation

import (
	"fmt"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	"reflect"

	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/consensus"
	types2 "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/proto/tendermint/propagation"
)

const (
	// TODO: set a valid max msg size
	maxMsgSize = 1048576

	// ReactorIncomingMessageQueueSize the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 1000
)

type Reactor struct {
	p2p.BaseReactor // BaseService + p2p.Switch

	// TODO remove nolint
	//nolint:unused
	conS *consensus.State

	// TODO: we shouldn't be propagating messages when syncing.
	// make sure that's the case and it makes sense to only pass this function here.
	waitSync func() bool

	peerstate map[p2p.ID]*PeerState

	// ProposalCache temporarily stores recently active proposals and their
	// block data for gossiping.
	*ProposalCache

	mtx         *sync.RWMutex
	traceClient trace.Tracer
}

func NewReactor(consensusState *consensus.State, waitSync func() bool, options ...ReactorOption) *Reactor {
	reactor := &Reactor{
		waitSync: waitSync,
	}
	reactor.BaseReactor = *p2p.NewBaseReactor("BlockProp", reactor, p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize))

	for _, option := range options {
		option(reactor)
	}
	return reactor
}

type ReactorOption func(*Reactor)

func ReactorWithTraceClient(traceClient trace.Tracer) ReactorOption {
	return func(reactor *Reactor) {
		reactor.traceClient = traceClient
	}
}

func (blockProp *Reactor) OnStart() error {
	// TODO: implement
	return nil
}

func (blockProp *Reactor) OnStop() {
	// TODO: implement
}

func GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			// TODO: set better values
			ID:                  consensus.PropagationChannel,
			Priority:            6,
			SendQueueCapacity:   100,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propagation.Message{},
		},
	}
}

func (blockProp *Reactor) AddPeer(peer p2p.Peer) {
	// TODO: implement
}

func (blockProp *Reactor) ReceiveEnvelop(e p2p.Envelope) {
	if !blockProp.IsRunning() {
		blockProp.Logger.Debug("Receive", "src", e.Src, "chId", e.ChannelID)
		return
	}

	m := e.Message
	if wm, ok := m.(p2p.Wrapper); ok {
		m = wm.Wrap()
	}
	msg, err := types2.MsgFromProto(m.(*propagation.Message))
	if err != nil {
		blockProp.Logger.Error("Error decoding message", "src", e.Src, "chId", e.ChannelID, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err)
		return
	}

	if err = msg.ValidateBasic(); err != nil {
		blockProp.Logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err)
		return
	}
	switch e.ChannelID {
	case consensus.PropagationChannel:
		switch msg := msg.(type) {
		case *types2.TxMetaData:
			// TODO: implement
		case *types2.CompactBlock:
			// TODO: implement
		case *types2.PartMetaData:
			// TODO: implement
		case *types2.HaveParts:
			// TODO check if we need to bypass request limits
			blockProp.handleHaves(e.Src.ID(), msg, false)
		case *types2.WantParts:
			// TODO: implement
		case *types2.RecoveryPart:
			// TODO: implement
		default:
			blockProp.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
		}
	default:
		blockProp.Logger.Error(fmt.Sprintf("Unknown chId %X", e.ChannelID))
	}
}

func (blockProp *Reactor) Receive(chID byte, peer p2p.Peer, msgBytes []byte) {
	msg := &propagation.Message{}
	err := proto.Unmarshal(msgBytes, msg)
	if err != nil {
		panic(err)
	}
	uw, err := msg.Unwrap()
	if err != nil {
		panic(err)
	}
	blockProp.ReceiveEnvelope(p2p.Envelope{
		ChannelID: chID,
		Src:       peer,
		Message:   uw,
	})
}

// getPeer returns the peer state for the given peer. If the peer does not exist,
// nil is returneblockProp.
func (blockProp *Reactor) getPeer(peer p2p.ID) *PeerState {
	blockProp.mtx.RLock()
	defer blockProp.mtx.RUnlock()
	return blockProp.peerstate[peer]
}

// getPeers returns a list of all peers that the data routine is aware of.
func (blockProp *Reactor) getPeers() []*PeerState {
	blockProp.mtx.RLock()
	defer blockProp.mtx.RUnlock()
	peers := make([]*PeerState, 0, len(blockProp.peerstate))
	for _, peer := range blockProp.peerstate {
		peers = append(peers, peer)
	}
	return peers
}

// setPeer sets the peer state for the given peer.
func (blockProp *Reactor) setPeer(peer p2p.ID, state *PeerState) {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
	blockProp.peerstate[peer] = state
}

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts.
// the peer must always send the proposal before sending parts, if they did
// not this node must disconnect from them.
// fmt.Println("unknown proposal", height, round, "from", peer)
// blockProp.pswitch.StopPeerForError(p.peer, fmt.Errorf("received part state for unknown proposal"))
func (blockProp *Reactor) handleHaves(peer p2p.ID, haves *types2.HaveParts, bypassRequestLimit bool) {
	if haves == nil {
		// fmt.Println("nil no parts to request", height, round)
		return
	}
	height := haves.Height
	round := haves.Round
	p := blockProp.getPeer(peer)
	if p == nil || p.peer == nil {
		blockProp.Logger.Error("peer not found", "peer", peer)
		return
	}
	_, parts, fullReqs, has := blockProp.GetProposal(height, round)
	if !has {
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

	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: consensus.PropagationChannel,
		Message: &propagation.HaveParts{
			Height: height,
			Round:  round,
			Parts:  hc.ToProto().Parts,
		},
	}

	if !p2p.SendEnvelopeShim(p.peer, e, blockProp.Logger) {
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
	blockProp.broadcastHaves(height, round, hc, peer)
}

// todo(evan): refactor to not iterate so often and just store which peers
func (blockProp *Reactor) countRequests(height int64, round int32, part int) []p2p.ID {
	var peers []p2p.ID
	for _, peer := range blockProp.getPeers() {
		if reqs, has := peer.GetRequests(height, round); has && reqs.GetIndex(part) {
			peers = append(peers, peer.peer.ID())
		}
	}
	return peers
}

// broadcastHaves gossips the provided have msg to all peers except to the
// original sender. This should only be called upon receiving a new have for the
// first time.
func (blockProp *Reactor) broadcastHaves(height int64, round int32, haves *types2.HaveParts, from p2p.ID) {
	e := p2p.Envelope{ //nolint: staticcheck
		ChannelID: consensus.PropagationChannel,
		Message: &propagation.HaveParts{
			Height: height,
			Round:  round,
			Parts:  haves.ToProto().Parts,
		},
	}
	for _, peer := range blockProp.getPeers() {
		if peer.peer.ID() == from {
			continue
		}

		// skip sending anything to this peer if they already have all the
		// parts.
		ph, has := peer.GetHaves(height, round)
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
		if p2p.SendEnvelopeShim(peer.peer, e, blockProp.Logger) {
			schema.WriteBlockPartState(
				blockProp.traceClient,
				height,
				round,
				haves.GetTrueIndices(),
				true,
				string(peer.peer.ID()),
				schema.Upload,
			)
		}
	}
}
