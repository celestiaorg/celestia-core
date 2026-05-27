package cat

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat/chunked"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

const (
	// default duration to wait before considering a peer non-responsive
	// and searching for the tx from a new peer
	DefaultGossipDelay = 60 * time.Second

	// MempoolDataChannel channel for SeenTx and blob messages.
	MempoolDataChannel = byte(0x31)

	// MempoolWantsChannel channel for wantTx messages.
	MempoolWantsChannel = byte(0x32)

	// MempoolChunkChannel carries the chunked + erasure-coded propagation
	// path described in ADR-012: SeenLargeTx, HaveTxChunks, WantTxChunks,
	// TxChunk. Kept separate from MempoolDataChannel to avoid head-of-line
	// blocking small txs behind multi-megabyte chunk traffic.
	MempoolChunkChannel = byte(0x33)

	// defaultChunkChannelPriority sits above MempoolDataChannel (3) so chunk
	// traffic gets fair scheduling, but well below the propagation reactor
	// (140+) so block-part traffic still wins at consensus time.
	defaultChunkChannelPriority = 6

	// defaultChunkChannelSendCapacity is the per-peer send queue depth for
	// MempoolChunkChannel. Larger than MempoolDataChannel because each
	// chunked-tx admission can enqueue 2K chunks.
	defaultChunkChannelSendCapacity = 2000

	// peerHeightDiff signifies the tolerance in difference in height between the peer and the height
	// the node received the tx
	peerHeightDiff = 10

	// ReactorIncomingMessageQueueSize the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 5000

	// maxSeenTxBroadcast defines the maximum number of peers to which a SeenTx message should be broadcasted.
	maxSeenTxBroadcast = 15

	// defaultMaxPersistentStickyPeers caps how many persistent peers are guaranteed
	// to receive SeenTx broadcasts per signer (added on top of the natural sticky
	// set, never displacing it). Used when ReactorOptions.MaxPersistentStickyPeers
	// is unset.
	defaultMaxPersistentStickyPeers = 4

	// maxReceivedBufferSize limits how far ahead of the expected sequence we will
	// request/buffer transactions. Txs with sequence > expected + maxReceivedBufferSize are rejected.
	maxReceivedBufferSize = 30

	// maxRequestsPerPeer limits the number of concurrent outstanding requests to a single peer.
	// When a peer reaches this limit, requests will be sent to alternative peers instead.
	maxRequestsPerPeer = 30

	// maxSignerLength is the maximum allowed length for a signer field in SeenTx messages.
	maxSignerLength = 64
)

var (
	errSignerTooLong = errors.New("signer field exceeds maximum length")
	errTooManyTxs    = errors.New("txs message contains too many transactions")
	errEmptyTx       = errors.New("txs message contains an empty transaction")
)

// Reactor handles mempool tx broadcasting logic amongst peers. For the main
// logic behind the protocol, refer to `ReceiveEnvelope` or to the english
// spec under /.spec.md
type Reactor struct {
	p2p.BaseReactor
	opts           *ReactorOptions
	mempool        *TxPool
	ids            *mempoolIDs
	requests       *requestScheduler
	pendingSeen    *pendingSeenTracker
	receivedBuffer *receivedTxBuffer // buffer for out-of-order tx arrivals
	traceClient    trace.Tracer
	// stickySalt stores []byte rendezvous salt for sticky peer selection; nil/empty keeps default ordering.
	stickySalt atomic.Value

	// chunkedStore holds per-tx state for the chunked + erasure-coded
	// propagation path (ADR-012). Used by the SeenLargeTx/HaveTxChunks/
	// WantTxChunks/TxChunk handlers in reactor_chunked.go.
	chunkedStore *chunked.Store
}

type ReactorOptions struct {
	// ListenOnly means that the node will never broadcast any of the transactions that
	// it receives. This is useful for keeping transactions private
	ListenOnly bool

	// MaxTxSize is the maximum size of a transaction that can be received
	MaxTxSize int

	// MaxGossipDelay is the maximum allotted time that the reactor expects a transaction to
	// arrive before issuing a new request to a different peer
	MaxGossipDelay time.Duration

	// TraceClient is the trace client for collecting trace level events
	TraceClient trace.Tracer

	// StickyPeerSalt is used to derive the rendezvous hash for sticky peer selection.
	// If unset, all nodes will derive the same peer ordering.
	StickyPeerSalt []byte

	// MaxPersistentStickyPeers caps how many persistent peers are guaranteed
	// to receive SeenTx broadcasts per signer, added on top of the natural sticky
	// set without displacing it. <= 0 falls back to defaultMaxPersistentStickyPeers.
	MaxPersistentStickyPeers int

	// RPCPushMode, when true, changes how RPC-submitted transactions are
	// disseminated: instead of announcing them via the chunked path and
	// waiting for peers to pull, the reactor pushes the full Txs message to
	// every connected peer. Receiving peers admit each tx and then re-broadcast
	// via the chunked path, so peer-to-peer propagation still uses chunked.
	// Inbound message handling is unchanged. When set, the broadcast goroutine
	// runs even if ListenOnly is true.
	RPCPushMode bool
}

func (opts *ReactorOptions) VerifyAndComplete() error {
	if opts.MaxTxSize == 0 {
		opts.MaxTxSize = cfg.DefaultMempoolConfig().MaxTxBytes
	}

	if opts.MaxGossipDelay == 0 {
		opts.MaxGossipDelay = DefaultGossipDelay
	}

	if opts.MaxPersistentStickyPeers <= 0 {
		opts.MaxPersistentStickyPeers = defaultMaxPersistentStickyPeers
	}

	if opts.MaxTxSize < 0 {
		return fmt.Errorf("max tx size (%d) cannot be negative", opts.MaxTxSize)
	}

	if opts.MaxGossipDelay < 0 {
		return fmt.Errorf("max gossip delay (%d) cannot be negative", opts.MaxGossipDelay)
	}

	return nil
}

// NewReactor returns a new Reactor with the given config and mempool.
func NewReactor(mempool *TxPool, opts *ReactorOptions) (*Reactor, error) {
	err := opts.VerifyAndComplete()
	if err != nil {
		return nil, err
	}
	traceClient := opts.TraceClient
	if traceClient == nil {
		traceClient = trace.NoOpTracer()
	}
	memR := &Reactor{
		opts:           opts,
		mempool:        mempool,
		ids:            newMempoolIDs(),
		requests:       newRequestScheduler(opts.MaxGossipDelay, defaultGlobalRequestTimeout),
		pendingSeen:    newPendingSeenTracker(0),
		receivedBuffer: newReceivedTxBuffer(),
		traceClient:    traceClient,
		chunkedStore:   chunked.NewStore(),
	}
	memR.BaseReactor = *p2p.NewBaseReactor("CAT", memR,
		p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize),
		p2p.WithQueueingFunc(memR.TryQueueUnprocessedEnvelope),
		p2p.WithTraceClient(traceClient),
	)
	memR.stickySalt.Store([]byte(nil)) // establish concrete []byte type for atomic.Value
	memR.SetStickySalt(opts.StickyPeerSalt)
	// Lock-step PartsState lifecycle with mempool: when a tx leaves the mempool
	// (commit, eviction, recheck), drop its chunks from the chunked store.
	mempool.SetOnTxRemoved(func(txKey types.TxKey) {
		memR.chunkedStore.Remove(txKey)
	})
	return memR, nil
}

// SetLogger sets the Logger on the reactor and the underlying mempool.
func (memR *Reactor) SetLogger(l log.Logger) {
	memR.Logger = l
}

// SetStickySalt configures the salt used for sticky peer selection. Passing nil resets it.
func (memR *Reactor) SetStickySalt(salt []byte) {
	if salt == nil {
		memR.stickySalt.Store([]byte(nil))
		return
	}
	cp := make([]byte, len(salt))
	copy(cp, salt)
	memR.stickySalt.Store(cp)
}

func (memR *Reactor) currentStickyPeerSalt() []byte {
	val := memR.stickySalt.Load()
	if val == nil {
		return nil
	}
	salt := val.([]byte)
	if len(salt) == 0 {
		return nil
	}
	return salt
}

// OnStart implements Service.
func (memR *Reactor) OnStart() error {
	if memR.opts.RPCPushMode {
		memR.Logger.Info("Mempool RPC push mode enabled: RPC-submitted txs will be pushed as Txs messages to all peers")
	}
	if !memR.opts.ListenOnly || memR.opts.RPCPushMode {
		go func() {
			for {
				select {
				case <-memR.Quit():
					return

				// listen in for any newly verified tx via RPC, then immediately
				// broadcast it to all connected peers.
				case nextTx := <-memR.mempool.next():
					memR.broadcastNewTx(nextTx)
				}
			}
		}()
	} else {
		memR.Logger.Info("Tx broadcasting is disabled")
	}

	go memR.heightSignalLoop()

	return nil
}

// OnStop implements Service
func (memR *Reactor) OnStop() {
	// stop all the timers tracking outbound requests
	memR.requests.Close()
}

// GetChannels implements Reactor. Only MempoolChunkChannel is used —
// ADR-013 removed the legacy SeenTx/WantTx/Txs path. The three legacy channels
// are still registered so peers running older binaries during a rolling
// upgrade can still complete the p2p handshake; they will simply receive no
// traffic on those channels from this reactor.
func (memR *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	// Worst-case per-chunk wire footprint: 64 KiB data + a Merkle proof
	// (~21 levels × 32 B aunts ≈ 1 KiB). Batched TxChunks carries up to
	// DefaultChunksBatchSize chunks. Round generously.
	const chunkProofOverhead = 4 * 1024
	const perChunkBudget = chunked.ChunkSize + chunkProofOverhead
	chunkRecvCap := DefaultChunksBatchSize*perChunkBudget + 1024 // +envelope/key

	return []*p2p.ChannelDescriptor{
		{
			ID:                  mempool.MempoolChannel,
			Priority:            1,
			SendQueueCapacity:   10,
			RecvMessageCapacity: chunkRecvCap,
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolDataChannel,
			Priority:            3,
			SendQueueCapacity:   1000,
			RecvMessageCapacity: chunkRecvCap,
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolWantsChannel,
			Priority:            3,
			SendQueueCapacity:   1000,
			RecvMessageCapacity: chunkRecvCap,
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolChunkChannel,
			Priority:            defaultChunkChannelPriority,
			SendQueueCapacity:   defaultChunkChannelSendCapacity,
			RecvMessageCapacity: chunkRecvCap,
			MessageType:         &protomem.Message{},
		},
	}
}

// InitPeer implements Reactor by creating a state for the peer.
func (memR *Reactor) InitPeer(peer p2p.Peer) (p2p.Peer, error) {
	memR.ids.ReserveForPeer(peer)
	return peer, nil
}

// RemovePeer implements Reactor. For all current outbound requests to this
// peer it will find a new peer to rerequest the same transactions.
func (memR *Reactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	peerID := memR.ids.Reclaim(peer.ID())
	// clear all memory of seen txs by that peer
	memR.mempool.seenByPeersSet.RemovePeer(peerID)
	memR.pendingSeen.removePeer(peerID)

	// remove and rerequest all pending outbound requests to that peer since we know
	// we won't receive any responses from them.
	outboundRequests := memR.requests.ClearAllRequestsFrom(peerID)
	for key := range outboundRequests {
		memR.pendingSeen.markRequestFailed(key, peerID)
		memR.mempool.metrics.RequestedTxs.Add(1)
		memR.findNewPeerToRequestTx(key)
	}
}

// Receive dispatches ADR-013 chunked-mempool messages. The legacy
// SeenTx/WantTx/Txs path has been removed entirely; only the four chunked
// messages are accepted.
func (memR *Reactor) Receive(e p2p.Envelope) {
	switch msg := e.Message.(type) {
	case *protomem.SeenLargeTx:
		memR.handleSeenLargeTx(e.Src, msg)
	case *protomem.HaveTxChunks:
		memR.handleHaveTxChunks(e.Src, msg)
	case *protomem.WantTxChunks:
		memR.handleWantTxChunks(e.Src, msg)
	case *protomem.TxChunks:
		memR.handleTxChunks(e.Src, msg)
	default:
		memR.Logger.Error("unknown message type", "src", e.Src, "chId", e.ChannelID, "msg", fmt.Sprintf("%T", msg))
		memR.Switch.StopPeerForError(e.Src, fmt.Errorf("mempool cannot handle message of type: %T", msg), memR.String())
		return
	}
}

// PeerState describes the state of a peer.
type PeerState interface {
	GetHeight() int64
}

// broadcastNewTx is the entry point for gossiping a newly-admitted tx.
// Per ADR-013, the role is per-tx and per-node:
//   - RPC-admit, env RPC=1   → Push RPC (broadcastNewLargeTxPush)
//   - RPC-admit, env RPC unset → Default RPC (broadcastNewLargeTxDefault)
//
// Gossip-admitted txs reach the broadcast goroutine via markToBeBroadcast in
// reconstructAndAdmit and are re-announced via broadcastFullHaveAfterAdmit
// (called inline from the reconstruction path), not via this function.
func (memR *Reactor) broadcastNewTx(wtx *wrappedTx) {
	if memR.opts.RPCPushMode {
		memR.broadcastNewLargeTxPush(wtx)
		return
	}
	memR.broadcastNewLargeTxDefault(wtx)
}

// tryAddNewTx attempts to add a tx to the mempool and traces the result.
// Returns the response and true if processing should continue (success or already in mempool).
func (memR *Reactor) tryAddNewTx(cachedTx *types.CachedTx, key types.TxKey, txInfo mempool.TxInfo, peerID string) (*abci.ResponseCheckTx, error) {
	rsp, err := memR.mempool.TryAddNewTx(cachedTx, key, txInfo)
	if err != nil {
		signer, sequence := "", uint64(0)
		if rsp != nil {
			signer = string(rsp.Address)
			sequence = rsp.Sequence
		}
		if errors.Is(err, ErrTxInMempool) {
			schema.WriteMempoolAddResult(memR.traceClient, peerID, key[:], schema.AlreadyInMempool, err, signer, sequence)
			return nil, err
		}
		schema.WriteMempoolAddResult(memR.traceClient, peerID, key[:], schema.Rejected, err, signer, sequence)
		memR.Logger.Debug("Could not add tx", "txKey", key, "err", err)
		return nil, err
	}

	if rsp.Code != 0 {
		schema.WriteMempoolAddResult(memR.traceClient, peerID, key[:], schema.Rejected, fmt.Errorf("execution code: %d", rsp.Code), string(rsp.Address), rsp.Sequence)
	} else {
		schema.WriteMempoolAddResult(memR.traceClient, peerID, key[:], schema.Added, nil, string(rsp.Address), rsp.Sequence)
	}
	return rsp, nil
}

// heightSignalLoop drains the mempool's height-signal channel. ADR-013 has no
// sequence-aware buffering, so there is no per-signer state to refresh on
// height changes; we simply consume the signal so producers don't block.
func (memR *Reactor) heightSignalLoop() {
	heightSignal := memR.mempool.HeightSignal()
	if heightSignal == nil {
		return
	}
	for {
		select {
		case <-memR.Quit():
			return
		case _, ok := <-heightSignal:
			if !ok {
				return
			}
		}
	}
}

// findNewPeerToRequestTx is a no-op under ADR-013: there is no timer-based
// retry. K-of-2K redundancy plus broad HaveTxChunks gossip drive recovery
// from peer disconnect.
func (memR *Reactor) findNewPeerToRequestTx(_ types.TxKey) {}
