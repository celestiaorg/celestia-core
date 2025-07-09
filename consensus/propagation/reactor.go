package propagation

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/crypto"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p/conn"

	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
)

const (
	maxMsgSize = 4194304 // 4MiB

	// DataChannel the propagation reactor channel handling the haves, the compact block,
	// and the recovery parts.
	DataChannel = byte(0x50)

	// WantChannel the propagation reactor channel handling the wants.
	WantChannel = byte(0x51)
)

var RetryTime = 6 * time.Second

type Reactor struct {
	p2p.BaseReactor // BaseService + p2p.Switch

	peerstate map[p2p.ID]*PeerState

	// ProposalCache temporarily stores recently active proposals and their
	// block data for gossiping.
	*ProposalCache
	currentProposer crypto.PubKey

	privval       types.PrivValidator
	chainID       string
	BlockMaxBytes int64

	// mempool access to read the transactions by hash from the mempool
	// and eventually remove it.
	mempool Mempool

	partChan     chan<- types.PartInfo
	proposalChan chan<- types.Proposal

	mtx         *sync.Mutex
	traceClient trace.Tracer
	self        p2p.ID
	started     atomic.Bool

	ctx    context.Context
	cancel context.CancelFunc
}

type Config struct {
	Store         *store.BlockStore
	Mempool       Mempool
	Privval       types.PrivValidator
	ChainID       string
	BlockMaxBytes int64
	PartChan      chan<- types.PartInfo
	ProposalChan  chan<- types.Proposal
}

func NewReactor(
	self p2p.ID,
	config Config,
	options ...ReactorOption,
) *Reactor {
	ctx, cancel := context.WithCancel(context.Background())
	reactor := &Reactor{
		self:          self,
		traceClient:   trace.NoOpTracer(),
		peerstate:     make(map[p2p.ID]*PeerState),
		mtx:           &sync.Mutex{},
		ProposalCache: NewProposalCache(config.Store),
		mempool:       config.Mempool,
		started:       atomic.Bool{},
		ctx:           ctx,
		cancel:        cancel,
		privval:       config.Privval,
		chainID:       config.ChainID,
		BlockMaxBytes: config.BlockMaxBytes,
		partChan:      config.PartChan,
		proposalChan:  config.ProposalChan,
	}
	reactor.BaseReactor = *p2p.NewBaseReactor("Recovery", reactor)

	for _, option := range options {
		option(reactor)
	}

	// start the catchup routine
	go func() {
		// TODO dynamically set the ticker depending on how many blocks are missing
		ticker := time.NewTicker(RetryTime)
		for {
			select {
			case <-reactor.ctx.Done():
				return
			case <-ticker.C:
				reactor.pmtx.Lock()
				currentHeight := reactor.currentHeight
				reactor.pmtx.Unlock()
				// run the catchup routine to recover any missing parts for past heights.
				reactor.retryWants(currentHeight)
			}
		}
	}()

	return reactor
}

type ReactorOption func(*Reactor)

func WithTracer(tracer trace.Tracer) func(r *Reactor) {
	return func(r *Reactor) {
		r.traceClient = tracer
	}
}

func (blockProp *Reactor) SetLogger(logger log.Logger) {
	blockProp.Logger = logger
}

func (blockProp *Reactor) OnStart() error {
	return nil
}

func (blockProp *Reactor) OnStop() {
	blockProp.cancel()
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
func (blockProp *Reactor) AddPeer(peer p2p.Peer) error {
	// Ignore the peer if it is ourselves.
	if peer.ID() == blockProp.self {
		return fmt.Errorf("ignoring self peer")
	}

	// ignore the peer if it already exists.
	if p := blockProp.getPeer(peer.ID()); p != nil {
		return fmt.Errorf("peer exists in propagation reactors, peer ID: %v", peer.ID())
	}

	peerState := newPeerState(blockProp.ctx, peer, blockProp.Logger)
	blockProp.setPeer(peer.ID(), peerState)
	go blockProp.requestFromPeer(peerState)

	cb, _, found := blockProp.GetCurrentCompactBlock()
	if !found {
		blockProp.Logger.Error("failed to get current compact block", "peer", peer.ID())
		return nil
	}

	// send the current proposal
	e := p2p.Envelope{
		ChannelID: DataChannel,
		Message:   cb.ToProto(),
	}

	if !peer.TrySend(e) {
		blockProp.Logger.Debug("failed to send proposal to peer", "peer", peer.ID())
	}
	return nil
}

func (blockProp *Reactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
	p := blockProp.peerstate[peer.ID()]
	if p != nil {
		p.cancel()
	}
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
		blockProp.Switch.StopPeerForError(e.Src, err, blockProp.String())
		return
	}

	if err = msg.ValidateBasic(); err != nil {
		blockProp.Logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err, blockProp.String())
		return
	}
	switch e.ChannelID {
	case DataChannel:
		switch msg := msg.(type) {
		case *proptypes.CompactBlock:
			blockProp.handleCompactBlock(msg, e.Src.ID(), false)
			schema.WriteProposal(blockProp.traceClient, msg.Proposal.Height, msg.Proposal.Round, string(e.Src.ID()), schema.Download)
		case *proptypes.HaveParts:
			blockProp.handleHaves(e.Src.ID(), msg)
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

// Prune removes all peer and proposal state from the block propagation reactor.
// This should be called only after a block has been committed.
func (blockProp *Reactor) Prune(committedHeight int64) {
	prunePast := committedHeight
	peers := blockProp.getPeers()
	for _, peer := range peers {
		peer.prune(prunePast)
	}
	blockProp.prune(prunePast)
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()
	blockProp.consensusHeight = committedHeight
	blockProp.ResetRequestCounts()
}

func (blockProp *Reactor) SetProposer(proposer crypto.PubKey) {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
	blockProp.currentProposer = proposer
}

func (blockProp *Reactor) SetConsensusRound(height int64, round int32) {
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()
	blockProp.consensusRound = round
	blockProp.ResetRequestCounts()
	// todo: delete the old round data as its no longer relevant don't delete
	// past round data if it has a POL
}

func (blockProp *Reactor) ResetRequestCounts() {
	peers := blockProp.getPeers()
	for _, p := range peers {
		if p == nil {
			// todo: investigate why nil peers can be present
			continue
		}
		p.SetConcurrentReqs(0)
	}
}

func (blockProp *Reactor) StartProcessing() {
	blockProp.started.Store(true)
}

func ConcurrentRequestLimit(peersCount, partsCount int) int64 {
	if peersCount == 0 || partsCount == 0 {
		return 1
	}
	faultyValCount := math.Ceil(float64(peersCount) * 0.33)
	faultyPartCount := float64(partsCount) / 2
	return int64(math.Ceil(faultyPartCount / faultyValCount))
}

// getPeer returns the peer state for the given peer. If the peer does not exist,
// nil is returned.
func (blockProp *Reactor) getPeer(peer p2p.ID) *PeerState {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
	return blockProp.peerstate[peer]
}

// getPeers returns a list of all peers that the data routine is aware of.
func (blockProp *Reactor) getPeers() []*PeerState {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
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
