package propagation

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/tendermint/tendermint/p2p/conn"

	"github.com/tendermint/tendermint/store"

	"github.com/gogo/protobuf/proto"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	propproto "github.com/tendermint/tendermint/proto/tendermint/propagation"
)

const (
	maxMsgSize = 4194304 // 4MiB

	// ReactorIncomingMessageQueueSize the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 2000

	// DataChannel the propagation reactor channel handling the haves, the compact block,
	// and the recovery parts.
	DataChannel = byte(0x50)

	// WantChannel the propagation reactor channel handling the wants.
	WantChannel = byte(0x51)
)

type Reactor struct {
	p2p.BaseReactor // BaseService + p2p.Switch

	peerstate map[p2p.ID]*PeerState

	// ProposalCache temporarily stores recently active proposals and their
	// block data for gossiping.
	*ProposalCache

	// mempool access to read the transactions by hash from the mempool
	// and eventually remove it.
	mempool Mempool

	mtx         *sync.RWMutex
	traceClient trace.Tracer
	self        p2p.ID
	started     bool
}

func NewReactor(self p2p.ID, tracer trace.Tracer, store *store.BlockStore, mempool Mempool, options ...ReactorOption) *Reactor {
	if tracer == nil {
		tracer = trace.NoOpTracer()
	}
	reactor := &Reactor{
		self:          self,
		traceClient:   tracer,
		peerstate:     make(map[p2p.ID]*PeerState),
		mtx:           &sync.RWMutex{},
		ProposalCache: NewProposalCache(store),
		mempool:       mempool,
	}
	reactor.BaseReactor = *p2p.NewBaseReactor("BlockProp", reactor, p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize))

	for _, option := range options {
		option(reactor)
	}
	return reactor
}

type ReactorOption func(*Reactor)

func (blockProp *Reactor) OnStart() error {
	// TODO: implement
	return nil
}

func (blockProp *Reactor) OnStop() {
}

func (blockProp *Reactor) GetChannels() []*conn.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:                  WantChannel,
			Priority:            45,
			SendQueueCapacity:   20000,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propproto.Message{},
		},
		{
			ID:                  DataChannel,
			Priority:            40,
			SendQueueCapacity:   20000,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propproto.Message{},
		},
	}
}

// AddPeer adds the peer to the block propagation reactor. This should be called when a peer
// is connected. The proposal is sent to the peer so that it can start catchup
// or request data.
func (blockProp *Reactor) AddPeer(peer p2p.Peer) {
	// Ignore the peer if it is ourselves.
	if peer.ID() == blockProp.self {
		return
	}

	// ignore the peer if it already exists.
	if p := blockProp.getPeer(peer.ID()); p != nil {
		blockProp.Logger.Error("Peer exists in propagation reactors", "peer", peer.ID())
		return
	}

	blockProp.setPeer(peer.ID(), newPeerState(peer, blockProp.Logger))
	cb, _, found := blockProp.GetCurrentCompactBlock()

	if !found {
		blockProp.Logger.Error("Failed to get current compact block", "peer", peer.ID())
		return
	}

	// send the current proposal
	e := p2p.Envelope{
		ChannelID: DataChannel,
		Message:   cb.ToProto(),
	}

	if !p2p.TrySendEnvelopeShim(peer, e, blockProp.Logger) { //nolint:staticcheck
		blockProp.Logger.Debug("failed to send proposal to peer", "peer", peer.ID())
	}
}

func (blockProp *Reactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
	delete(blockProp.peerstate, peer.ID())
}

func (blockProp *Reactor) ReceiveEnvelope(e p2p.Envelope) {
	if !blockProp.IsRunning() {
		blockProp.Logger.Debug("Receive", "src", e.Src, "chId", e.ChannelID)
		return
	}

	m := e.Message
	if wm, ok := m.(p2p.Wrapper); ok {
		m = wm.Wrap()
	}
	msg, err := proptypes.MsgFromProto(m.(*propproto.Message))
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
	case DataChannel:
		switch msg := msg.(type) {
		case *proptypes.CompactBlock:
			blockProp.handleCompactBlock(msg, e.Src.ID(), false)
			schema.WriteProposal(blockProp.traceClient, msg.Proposal.Height, msg.Proposal.Round, string(e.Src.ID()), schema.Download)
		case *proptypes.HaveParts:
			blockProp.handleHaves(e.Src.ID(), msg, false)
		case *proptypes.RecoveryPart:
			blockProp.handleRecoveryPart(e.Src.ID(), msg)
		default:
			blockProp.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
		}
	case WantChannel:
		switch msg := msg.(type) {
		case *proptypes.WantParts:
			blockProp.handleWants(e.Src.ID(), msg)
		}
	default:
		blockProp.Logger.Error(fmt.Sprintf("Unknown chId %X", e.ChannelID))
	}
}

func (blockProp *Reactor) Receive(chID byte, peer p2p.Peer, msgBytes []byte) {
	msg := &propproto.Message{}
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

// Prune removes all peer and proposal state from the block propagation reactor.
// This should be called only after a block has been committed.
func (blockProp *Reactor) Prune(committedHeight int64) {
	prunePast := committedHeight
	peers := blockProp.getPeers()
	for _, peer := range peers {
		peer.prune(prunePast)
	}
	blockProp.ProposalCache.prune(prunePast)
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()
	blockProp.consensusHeight = committedHeight
}

// getPeer returns the peer state for the given peer. If the peer does not exist,
// nil is returned.
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
