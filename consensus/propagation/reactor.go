package propagation

import (
	"fmt"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p/conn"
	"reflect"

	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
)

const (
	// TODO: set a valid max msg size
	maxMsgSize = 1048576

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
}

func NewReactor(self p2p.ID, tracer trace.Tracer, store *store.BlockStore, mempool Mempool, options ...ReactorOption) *Reactor {
	if tracer == nil {
		// TODO not pass nil. instead, use a NOOP and allow the tracer to be passed as an option
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
	reactor.BaseReactor = *p2p.NewBaseReactor("BlockProp", reactor)

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
	// TODO: implement
}

func (blockProp *Reactor) GetChannels() []*conn.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			// TODO: set better values
			ID:                  WantChannel,
			Priority:            6,
			SendQueueCapacity:   100,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propproto.Message{},
		},
		{
			// TODO: set better values
			ID:                  DataChannel,
			Priority:            10,
			SendQueueCapacity:   1000,
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
		return
	}

	// TODO pass a separate logger if needed
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
			blockProp.handleCompactBlock(msg, e.Src.ID())
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

func (blockProp *Reactor) Receive(e p2p.Envelope) {
	blockProp.ReceiveEnvelope(e)
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
