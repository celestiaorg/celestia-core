package cat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/tmhash"
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
	go memR.startChunkedSweeper()

	return nil
}

// OnStop implements Service
func (memR *Reactor) OnStop() {
	// stop all the timers tracking outbound requests
	memR.requests.Close()
}

// GetChannels implements Reactor by returning the list of channels for this
// reactor.
func (memR *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	largestTx := make([]byte, memR.opts.MaxTxSize)
	txMsg := protomem.Message{
		Sum: &protomem.Message_Txs{
			Txs: &protomem.Txs{Txs: [][]byte{largestTx}},
		},
	}

	stateMsg := protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey: make([]byte, tmhash.Size),
			},
		},
	}

	// Worst-case TxChunk size: full 64 KiB chunk plus a Merkle proof. The
	// Merkle proof for a tree of 2*MaxBlockPartsCount leaves is ~21 levels
	// at 32 B per aunt, well under 1 KiB. Round up generously.
	chunkMsg := protomem.Message{
		Sum: &protomem.Message_TxChunk{
			TxChunk: &protomem.TxChunk{
				TxKey: make([]byte, tmhash.Size),
				Data:  make([]byte, chunked.ChunkSize),
			},
		},
	}
	const chunkProofOverhead = 4 * 1024
	chunkRecvCap := chunkMsg.Size() + chunkProofOverhead

	return []*p2p.ChannelDescriptor{
		{
			ID:                  mempool.MempoolChannel,
			Priority:            1,
			SendQueueCapacity:   10,
			RecvMessageCapacity: txMsg.Size(),
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolDataChannel,
			Priority:            3,
			SendQueueCapacity:   1000,
			RecvMessageCapacity: txMsg.Size(),
			MessageType:         &protomem.Message{},
		},
		{
			ID:                  MempoolWantsChannel,
			Priority:            3,
			SendQueueCapacity:   1000,
			RecvMessageCapacity: stateMsg.Size(),
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

// Receive dispatches chunked-mempool messages (ADR-012) plus the legacy
// Txs message used by the RPC-push admission path. SeenTx/WantTx are no-ops:
// they only existed for the legacy pull protocol, which is gone.
func (memR *Reactor) Receive(e p2p.Envelope) {
	switch msg := e.Message.(type) {

	case *protomem.Txs:
		// RPC-push admission: a peer (typically an RPC-facing node running
		// with RPC=1) is pushing us full transactions. Admit each through
		// CheckTx; on success markToBeBroadcast queues the tx for chunked
		// re-broadcast so peer-to-peer fan-out runs via the chunked path.
		memR.handlePushedTxs(e.Src, msg)

	case *protomem.SeenTx, *protomem.WantTx:
		// Legacy pull protocol is removed; silently ignore. Kept as a case
		// so peers still running older binaries during a rolling upgrade
		// are not disconnected on every gossip.
		_ = msg

	case *protomem.SeenLargeTx:
		memR.handleSeenLargeTx(e.Src, msg)
	case *protomem.HaveTxChunks:
		memR.handleHaveTxChunks(e.Src, msg)
	case *protomem.WantTxChunks:
		memR.handleWantTxChunks(e.Src, msg)
	case *protomem.TxChunk:
		memR.handleTxChunk(e.Src, msg)

	default:
		memR.Logger.Error("unknown message type", "src", e.Src, "chId", e.ChannelID, "msg", fmt.Sprintf("%T", msg))
		memR.Switch.StopPeerForError(e.Src, fmt.Errorf("mempool cannot handle message of type: %T", msg), memR.String())
		return
	}
}

// handlePushedTxs admits each tx pushed by an RPC-mode peer and queues it
// for chunked re-broadcast. ListenOnly nodes still skip the rebroadcast at
// markToBeBroadcast/broadcast-goroutine level.
func (memR *Reactor) handlePushedTxs(src p2p.Peer, msg *protomem.Txs) {
	protoTxs := msg.GetTxs()
	if len(protoTxs) == 0 {
		memR.Switch.StopPeerForError(src, errEmptyTx, memR.String())
		return
	}
	if len(protoTxs) > mempool.MaxTxsPerMessage {
		memR.Switch.StopPeerForError(src, errTooManyTxs, memR.String())
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	txInfo := mempool.TxInfo{SenderID: peerID, SenderP2PID: src.ID()}
	for _, tx := range protoTxs {
		if len(tx) == 0 {
			memR.Switch.StopPeerForError(src, errEmptyTx, memR.String())
			return
		}
		ntx := types.Tx(tx)
		key := ntx.Key()
		schema.WriteMempoolTx(memR.traceClient, string(src.ID()), key[:], len(tx), schema.Download)
		// Mark the sender as having the tx so we don't re-push it back to them.
		memR.mempool.PeerHasTx(peerID, key)
		if memR.mempool.Has(key) {
			continue
		}
		cachedTx := ntx.ToCachedTx()
		if _, err := memR.tryAddNewTx(cachedTx, key, txInfo, string(src.ID())); err != nil {
			if errors.Is(err, ErrTxInMempool) {
				continue
			}
			memR.Logger.Debug("RPC push admit failed", "err", err, "tx_key", key)
			continue
		}
		// On non-RPC-push nodes: queue for chunked re-broadcast so peer-to-peer
		// fan-out runs via the chunked path. RPC push nodes skip the re-broadcast
		// — they are pure sources that only push txs from their own local RPC.
		if !memR.opts.RPCPushMode {
			memR.mempool.markToBeBroadcast(key)
		}
	}
}

// PeerState describes the state of a peer.
type PeerState interface {
	GetHeight() int64
}

// broadcastNewTx is the entry point for gossiping a newly-admitted tx.
//
// In normal mode it takes the chunked + erasure-coded path (ADR-012):
// encode the body, announce SeenLargeTx to chunked-capable peers, push K
// chunks across a handful of bootstrap peers.
//
// In RPC push mode (RPC env var set) it pushes the full Txs message to every
// connected peer directly. Receiving peers admit the tx and re-broadcast via
// the chunked path. This is for a small number of RPC-facing nodes that
// shoulder the fan-out cost so the rest of the network does multi-source
// chunked propagation among themselves.
func (memR *Reactor) broadcastNewTx(wtx *wrappedTx) {
	if memR.opts.RPCPushMode {
		memR.pushTxToAllPeers(wtx)
		return
	}
	memR.broadcastNewLargeTx(wtx)
}

// pushTxToAllPeers sends the full tx (Txs message on MempoolDataChannel) to
// every connected peer. Used in RPC push mode: instead of announcing via
// SeenLargeTx and serving chunks on demand, we proactively push the full body.
// Peers that are too far behind or that we already know have the tx are
// skipped.
func (memR *Reactor) pushTxToAllPeers(wtx *wrappedTx) {
	txKey := wtx.key()
	memR.Logger.Debug("pushing tx to all peers", "tx_key", txKey.String())

	msg := &protomem.Message{
		Sum: &protomem.Message_Txs{
			Txs: &protomem.Txs{Txs: [][]byte{wtx.tx.Tx}},
		},
	}

	peers := memR.ids.GetAll()
	if len(peers) == 0 {
		return
	}

	c := 0
	for id, peer := range peers {
		c++
		if c == 5 {
			break
		}
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			if p.GetHeight() < wtx.height-peerHeightDiff {
				memR.Logger.Trace("peer is too far behind us. Skipping RPC push of tx")
				continue
			}
		}
		if memR.mempool.seenByPeersSet.Has(txKey, id) {
			continue
		}
		if peer.Send(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message:   msg,
		}) {
			memR.mempool.PeerHasTx(id, txKey)
			schema.WriteMempoolTx(
				memR.traceClient,
				string(peer.ID()),
				txKey[:],
				len(wtx.tx.Tx),
				schema.Upload,
			)
		}
	}
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

// processReceivedBuffer drains buffered transactions for a signer in sequence
// order. The chunked admit path calls this after each in-order admission to
// pull dependent sequences out of receivedBuffer. Re-broadcast of admitted
// txs happens via markToBeBroadcast → broadcastNewLargeTx, not here.
func (memR *Reactor) processReceivedBuffer(signer []byte) {
	if len(signer) == 0 {
		return
	}

	for {
		expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
		if !haveExpected {
			break
		}

		buffered := memR.receivedBuffer.get(signer, expectedSeq)
		if buffered == nil {
			break
		}
		memR.receivedBuffer.removeLowerSeqs(signer, expectedSeq)

		rsp, err := memR.tryAddNewTx(buffered.tx, buffered.txKey, buffered.txInfo, buffered.peerID)
		if err == nil || errors.Is(err, ErrTxInMempool) {
			memR.pendingSeen.remove(buffered.txKey)
		}
		if err != nil {
			break
		}
		if rsp != nil && rsp.Code == 0 {
			memR.mempool.markToBeBroadcast(buffered.txKey)
		}
	}
}

// processPendingSeenForSigner tries to advance the pipeline of queued transactions for a signer.
// It requests consecutive sequences in parallel from different peers whenever we have seen a consecutive sequence numbers,
// buffering out-of-order arrivals for later processing. This allows fast catch-up even when tx sources are distributed.
func (memR *Reactor) processPendingSeenForSigner(signer []byte) {
	if len(signer) == 0 {
		return
	}

	entries := memR.pendingSeen.entriesForSigner(signer)
	if len(entries) == 0 {
		return
	}
	slices.SortFunc(entries, func(a, b *pendingSeenTx) int {
		return cmp.Compare(a.sequence, b.sequence)
	})
	expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
	if !haveExpected {
		memR.Logger.Error("no signer found in application")
		return
	}

	// Clean up old entries and request consecutive sequences in parallel
	nextSeq := expectedSeq
	requested := 0

	for _, entry := range entries {
		// Clean up entries that are already processed
		if entry.sequence < expectedSeq {
			memR.pendingSeen.remove(entry.txKey)
			continue
		}

		if entry.sequence != nextSeq {
			break
		}

		// Check limits
		if requested >= maxReceivedBufferSize {
			break
		}

		// Skip if already in mempool
		if memR.mempool.Has(entry.txKey) {
			memR.pendingSeen.remove(entry.txKey)
			nextSeq++
			continue
		}

		// Skip if already being requested, but count it
		if memR.requests.ForTx(entry.txKey) != 0 || entry.requested {
			nextSeq++
			requested++
			continue
		}

		// Request from first available peer
		if memR.tryRequestQueuedTx(entry) {
			requested++
		}
		nextSeq++
	}

	if requested > 0 {
		memR.Logger.Trace("parallel requests sent", "count", requested)
	}
}

// tryRequestQueuedTx triggers a chunked WantTxChunks for an entry drained
// from pendingSeen. Returns true if at least one peer was asked.
func (memR *Reactor) tryRequestQueuedTx(entry *pendingSeenTx) bool {
	state := memR.chunkedStore.Get(entry.txKey)
	if state == nil {
		// No chunked state — nothing we can fetch (legacy fetch removed).
		return false
	}
	asked := false
	for _, peerID := range entry.peerIDs() {
		peer := memR.ids.GetPeer(peerID)
		if peer == nil {
			continue
		}
		memR.requestChunksFrom(state, peerID, peer)
		asked = true
	}
	return asked
}

func (memR *Reactor) onRequestTimeout(txKey types.TxKey, peerID uint16) {
	memR.pendingSeen.markRequestFailed(txKey, peerID)
	memR.findNewPeerToRequestTx(txKey)
}

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
			memR.refreshPendingSeenQueues()
		}
	}
}

func (memR *Reactor) refreshPendingSeenQueues() {
	// Collect signers from both pending seen and buffer
	seenSigners := make(map[string][]byte)

	for _, signer := range memR.pendingSeen.signerKeys() {
		seenSigners[string(signer)] = signer
	}
	for _, signer := range memR.receivedBuffer.signerKeys() {
		seenSigners[string(signer)] = signer
	}

	if len(seenSigners) == 0 {
		return
	}

	for _, signer := range seenSigners {
		// First drain any buffered txs that can now be processed
		// (their expected sequence may have advanced due to block commit)
		memR.processReceivedBuffer(signer)
		// Then request more pending txs
		memR.processPendingSeenForSigner(signer)
	}
}

func (memR *Reactor) querySequenceFromApplication(signer []byte) (uint64, bool) {
	ctx := context.Background()
	resp, err := memR.mempool.proxyAppConn.QuerySequence(ctx, &abci.RequestQuerySequence{Signer: signer})
	if err != nil || resp == nil {
		return 0, false
	}
	// If the response is 0, treat it as "sequence tracking not available"
	// to maintain backward compatibility with apps that don't implement QuerySequence.
	// This prevents transactions from being stuck in pendingSeen when the app returns 0.
	if resp.Sequence == 0 {
		return 0, false
	}
	return resp.Sequence, true
}

// findNewPeerToRequestTx kicks a chunked re-fetch when an in-flight request
// fails or times out. With the chunked store driving propagation, fetching a
// missing tx means asking any peer that advertises chunks for it.
func (memR *Reactor) findNewPeerToRequestTx(txKey types.TxKey) {
	if memR.ids.Len() == 0 {
		return
	}
	state := memR.chunkedStore.Get(txKey)
	if state == nil {
		return
	}
	for _, peer := range memR.ids.GetAll() {
		if peer == nil {
			continue
		}
		peerID := memR.ids.GetIDForPeer(peer.ID())
		memR.requestChunksFrom(state, peerID, peer)
	}
}
