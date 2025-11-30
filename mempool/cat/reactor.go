package cat

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/mempool"
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

	// peerHeightDiff signifies the tolerance in difference in height between the peer and the height
	// the node received the tx
	peerHeightDiff = 10

	// ReactorIncomingMessageQueueSize the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 5000

	// maxSeenTxBroadcast defines the maximum number of peers to which a SeenTx message should be broadcasted.
	maxSeenTxBroadcast = 15

	// maxLookaheadSize limits how far ahead of the expected sequence we will
	// request/buffer transactions. Txs with sequence > expected + maxLookaheadSize are rejected.
	maxLookaheadSize = 30
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
}

func (opts *ReactorOptions) VerifyAndComplete() error {
	if opts.MaxTxSize == 0 {
		opts.MaxTxSize = cfg.DefaultMempoolConfig().MaxTxBytes
	}

	if opts.MaxGossipDelay == 0 {
		opts.MaxGossipDelay = DefaultGossipDelay
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
	if !memR.opts.ListenOnly {
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

	// removeLowerSeqs and rerequest all pending outbound requests to that peer since we know
	// we won't receive any responses from them.
	outboundRequests := memR.requests.ClearAllRequestsFrom(peerID)
	for key := range outboundRequests {
		memR.pendingSeen.markRequestFailed(key, peerID)
		memR.mempool.metrics.RequestedTxs.Add(1)
		memR.findNewPeerToRequestTx(key)
	}
}

// ReceiveEnvelope implements Reactor.
// It processes one of three messages: Txs, SeenTx, WantTx.
func (memR *Reactor) Receive(e p2p.Envelope) {
	switch msg := e.Message.(type) {

	// A peer has sent us one or more transactions. This could be either because we requested them
	// or because the peer received a new transaction and is broadcasting it to us.
	// NOTE: This setup also means that we can support older mempool implementations that simply
	// flooded the network with transactions.
	case *protomem.Txs:
		protoTxs := msg.GetTxs()
		if len(protoTxs) == 0 {
			memR.Logger.Error("received empty txs from peer", "src", e.Src)
			return
		}
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		txInfo := mempool.TxInfo{SenderID: peerID}
		txInfo.SenderP2PID = e.Src.ID()

		for _, tx := range protoTxs {
			ntx := types.Tx(tx)
			key := ntx.Key()
			cachedTx := ntx.ToCachedTx()
			schema.WriteMempoolTx(memR.traceClient, string(e.Src.ID()), key[:], len(tx), schema.Download)

			// If we requested the transaction we mark it as received.
			if memR.requests.Has(peerID, key) {
				memR.requests.MarkReceived(peerID, key)
				memR.Logger.Info("received a response for a requested transaction", "peerID", peerID, "txKey", key)
			} else {
				// If we didn't request the transaction we simply mark the peer as having the
				// tx (we'd have already done it if we were requesting the tx).
				memR.mempool.PeerHasTx(peerID, key)
				memR.Logger.Info("received new transaction", "peerID", peerID, "txKey", key)
			}

			// Look up signer/sequence from pending tracker
			pendingEntry := memR.pendingSeen.get(key)
			if pendingEntry != nil && len(pendingEntry.signer) > 0 && pendingEntry.sequence > 0 {
				// We have sequence info - check if we should buffer or process
				expectedSeq, haveExpected := memR.querySequenceFromApplication(pendingEntry.signer)

				memR.Logger.Info("received tx with sequence info",
					"txKey", key,
					"txSequence", pendingEntry.sequence,
					"expectedSeq", expectedSeq,
					"haveExpected", haveExpected)

				if haveExpected && pendingEntry.sequence > expectedSeq {
					// Reject txs too far ahead
					if pendingEntry.sequence > expectedSeq+maxLookaheadSize {
						memR.Logger.Debug("rejecting tx too far ahead",
							"txKey", key,
							"sequence", pendingEntry.sequence,
							"expectedSeq", expectedSeq,
							"maxLookahead", maxLookaheadSize)
						continue
					}

					// Future sequence within lookahead - buffer it for later
					if memR.receivedBuffer.add(pendingEntry.signer, pendingEntry.sequence, cachedTx, key, txInfo, string(e.Src.ID())) {
						memR.pendingSeen.remove(key)
						memR.Logger.Info("buffered out-of-order tx",
							"txKey", key,
							"sequence", pendingEntry.sequence,
							"expectedSeq", expectedSeq)
					}
					continue
				}
			} else {
				memR.Logger.Info("received tx without pending sequence info",
					"txKey", key,
					"hasPendingEntry", pendingEntry != nil)
			}

			// Process this tx through CheckTx without putting into buffer
			memR.processReceivedTx(cachedTx, key, txInfo, e.Src)
		}

	// A peer has indicated to us that it has a transaction. We first verify the txkey and
	// mark that peer as having the transaction. Then we proceed with the following logic:
	//
	// 1. If we have the transaction, we do nothing.
	// 2. If we don't yet have the tx but have an outgoing request for it, we do nothing.
	// 3. If we recently evicted the tx and still don't have space for it, we do nothing.
	// 4. Else, we request the transaction from that peer.
	case *protomem.SeenTx:
		txKey, err := types.TxKeyFromBytes(msg.TxKey)
		if err != nil {
			memR.Logger.Error("peer sent SeenTx with incorrect tx key", "err", err)
			memR.Switch.StopPeerForError(e.Src, err, memR.String())
			return
		}
		schema.WriteMempoolPeerState(
			memR.traceClient,
			string(e.Src.ID()),
			schema.SeenTx,
			txKey[:],
			schema.Download,
		)
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		memR.mempool.PeerHasTx(peerID, txKey)

		// Check if we don't already have the transaction
		if memR.mempool.Has(txKey) {
			memR.Logger.Trace("received a seen tx for a tx we already have", "txKey", txKey)
			return
		}

		// If we are already requesting that tx, then we don't need to go any further.
		if memR.requests.ForTx(txKey) != 0 {
			memR.Logger.Trace("received a SeenTx message for a transaction we are already requesting", "txKey", txKey)
			return
		}

		expectedSeq, haveExpected := memR.querySequenceFromApplication(msg.Signer)

		switch {
		case len(msg.Signer) == 0 || msg.Sequence == 0:
			// fall through and request immediately when sequence info is missing
			schema.WriteMempoolPeerStateWithSeq(
				memR.traceClient,
				string(e.Src.ID()),
				schema.MissingSequence,
				txKey[:],
				schema.Download,
				msg.Signer,
				msg.Sequence,
			)
		case !haveExpected:
			// fall through and request immediately if we cannot query the application
			schema.WriteMempoolPeerStateWithSeq(
				memR.traceClient,
				string(e.Src.ID()),
				schema.MissingSequence,
				txKey[:],
				schema.Download,
				msg.Signer,
				msg.Sequence,
			)
		case msg.Sequence == expectedSeq:
			// fall through and request immediately for the expected sequence
		case msg.Sequence > expectedSeq:
			memR.pendingSeen.add(msg.Signer, txKey, msg.Sequence, peerID)
			return
		default:
			memR.Logger.Debug(
				"dropping SeenTx due to lower than expected sequence",
				"txKey", txKey,
				"sequence", msg.Sequence,
				"expectedSequence", expectedSeq,
			)

			return
		}

		// We don't have the transaction, nor are we requesting it so we send the node
		// a want msg
		memR.requestTx(txKey, e.Src)

	// A peer is requesting a transaction that we have claimed to have. Find the specified
	// transaction and broadcast it to the peer. We may no longer have the transaction
	case *protomem.WantTx:
		txKey, err := types.TxKeyFromBytes(msg.TxKey)
		if err != nil {
			memR.Logger.Error("peer sent WantTx with incorrect tx key", "err", err)
			memR.Switch.StopPeerForError(e.Src, err, memR.String())
			return
		}
		schema.WriteMempoolPeerState(
			memR.traceClient,
			string(e.Src.ID()),
			schema.WantTx,
			txKey[:],
			schema.Download,
		)
		tx, has := memR.mempool.GetTxByKey(txKey)
		if has && !memR.opts.ListenOnly {
			peerID := memR.ids.GetIDForPeer(e.Src.ID())
			memR.Logger.Trace("sending a tx in response to a want msg", "peer", peerID)
			if e.Src.Send(p2p.Envelope{
				ChannelID: MempoolDataChannel,
				Message:   &protomem.Txs{Txs: [][]byte{tx.Tx}},
			}) {
				memR.mempool.PeerHasTx(peerID, txKey)
				schema.WriteMempoolTx(
					memR.traceClient,
					string(e.Src.ID()),
					txKey[:],
					len(tx.Tx),
					schema.Upload,
				)
			}
		}

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

// broadcastSeenTx broadcasts a SeenTx message to limited peers unless we
// know they have already seen the transaction
func (memR *Reactor) broadcastSeenTx(txKey types.TxKey, signer []byte, sequence uint64) {
	memR.broadcastSeenTxWithHeight(txKey, memR.mempool.Height(), signer, sequence)
}

// broadcastNewTx broadcast new transaction to limited peers unless we are already sure they have seen the tx.
func (memR *Reactor) broadcastNewTx(wtx *wrappedTx) {
	memR.broadcastSeenTxWithHeight(wtx.key(), wtx.height, wtx.sender, wtx.sequence)
}

// broadcastSeenTxWithHeight is a helper that broadcasts a SeenTx message with height checking.
func (memR *Reactor) broadcastSeenTxWithHeight(txKey types.TxKey, height int64, signer []byte, sequence uint64) {
	memR.Logger.Debug("broadcasting seen tx to limited peers", "tx_key", txKey.String())
	msg := &protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey:    txKey[:],
				Signer:   signer,
				Sequence: sequence,
			},
		},
	}

	peers := memR.ids.GetAll()
	if len(peers) == 0 {
		return
	}

	orderedPeers := selectStickyPeers(signer, peers, len(peers), memR.currentStickyPeerSalt())
	sent := 0
	for _, peerInfo := range orderedPeers {
		if sent >= maxSeenTxBroadcast {
			break
		}

		id := peerInfo.id
		peer := peerInfo.peer
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			// make sure peer isn't too far behind. This can happen
			// if the peer is blocksyncing still and catching up
			// in which case we just skip sending the transaction
			if p.GetHeight() < height-peerHeightDiff {
				memR.Logger.Trace("peer is too far behind us. Skipping broadcast of seen tx")
				continue
			}
		}

		if memR.mempool.seenByPeersSet.Has(txKey, id) {
			continue
		}

		if peer.Send(
			p2p.Envelope{
				ChannelID: MempoolDataChannel,
				Message:   msg,
			},
		) {
			memR.mempool.PeerHasTx(id, txKey)
			schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.SeenTx, txKey[:], schema.Upload)
			sent++
		}
	}
}

// requesting it from another peer if the first peer does not respond.
func (memR *Reactor) requestTx(txKey types.TxKey, peer p2p.Peer) bool {
	if peer == nil {
		// we have disconnected from the peer
		return false
	}

	peerID := memR.ids.GetIDForPeer(peer.ID())
	memR.Logger.Trace("requesting tx", "txKey", txKey, "peerID", peer.ID())
	msg := &protomem.Message{
		Sum: &protomem.Message_WantTx{
			WantTx: &protomem.WantTx{TxKey: txKey[:]},
		},
	}

	success := peer.TrySend(
		p2p.Envelope{
			ChannelID: MempoolWantsChannel,
			Message:   msg,
		},
	)
	if success {
		memR.mempool.metrics.RequestedTxs.Add(1)
		requested := memR.requests.Add(txKey, peerID, memR.onRequestTimeout)
		if !requested {
			memR.Logger.Error("have already marked a tx as requested", "txKey", txKey, "peerID", peer.ID())
		} else {
			memR.pendingSeen.markRequested(txKey, peerID, time.Now().UTC())
		}

		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.WantTx, txKey[:], schema.Upload)
	}
	return success
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

// processReceivedTx handles a received transaction by running CheckTx and then
// draining any buffered transactions for the same signer.
func (memR *Reactor) processReceivedTx(cachedTx *types.CachedTx, key types.TxKey, txInfo mempool.TxInfo, src p2p.Peer) {
	rsp, err := memR.tryAddNewTx(cachedTx, key, txInfo, string(src.ID()))
	if err == nil || errors.Is(err, ErrTxInMempool) {
		memR.pendingSeen.remove(key)
	}
	if err != nil {
		return
	}

	if len(rsp.Address) > 0 {
		memR.processReceivedBuffer(rsp.Address)
		memR.processPendingSeenForSigner(rsp.Address)
	}

	if !memR.opts.ListenOnly && rsp.Code == 0 {
		memR.broadcastSeenTx(key, rsp.Address, rsp.Sequence)
	}
}

// processReceivedBuffer drains buffered transactions for a signer in sequence order.
// It processes buffered txs as long as they match the next expected sequence.
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
		// Remove from buffer before processing (we don't want to retry bad txs)
		memR.receivedBuffer.removeLowerSeqs(signer, expectedSeq)
		memR.Logger.Info("processing buffered tx",
			"sequence", expectedSeq,
			"txKey", buffered.txKey)

		rsp, err := memR.tryAddNewTx(buffered.tx, buffered.txKey, buffered.txInfo, buffered.peerID)
		if err == nil || errors.Is(err, ErrTxInMempool) {
			memR.pendingSeen.remove(buffered.txKey)
		}
		if err != nil {
			return
		}

		if !memR.opts.ListenOnly && rsp.Code == 0 {
			memR.broadcastSeenTx(buffered.txKey, signer, expectedSeq)
		}
	}

	// Clean up any expired entries, debugging
	//memR.receivedBuffer.cleanup()
}

// processPendingSeenForSigner tries to advance the pipeline of queued transactions for a signer.
// It requests consecutive sequences in parallel from different peers, buffering out-of-order
// arrivals for later processing. This allows fast catch-up even when tx sources are distributed.
func (memR *Reactor) processPendingSeenForSigner(signer []byte) {
	if len(signer) == 0 {
		return
	}

	entries := memR.pendingSeen.entriesForSigner(signer)
	if len(entries) == 0 {
		return
	}

	expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
	if !haveExpected {
		memR.Logger.Error("no signer found in application")
		return
	}

	// Log the state for debugging
	if len(entries) > 0 {
		memR.Logger.Info("processPendingSeenForSigner",
			"expectedSeq", expectedSeq,
			"numEntries", len(entries),
			"firstEntrySeq", entries[0].sequence)
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

		// Gap detected - stop requesting beyond the gap
		if entry.sequence != nextSeq {
			memR.Logger.Info("gap detected in pending entries",
				"expectedSeq", expectedSeq,
				"nextSeq", nextSeq,
				"entrySeq", entry.sequence)
			break
		}

		// Check limits
		if requested >= maxLookaheadSize {
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
			memR.Logger.Info("requested tx in parallel",
				"sequence", entry.sequence,
				"txKey", entry.txKey)
			requested++
		}
		nextSeq++
	}

	if requested > 0 {
		memR.Logger.Info("parallel requests sent", "count", requested)
	}
}

func (memR *Reactor) tryRequestQueuedTx(entry *pendingSeenTx) bool {
	peerIDs := entry.peerIDs()
	if len(peerIDs) > 0 {
		peer := memR.ids.GetPeer(peerIDs[0])
		if peer != nil && memR.requestTx(entry.txKey, peer) {
			return true
		}
	}

	memR.findNewPeerToRequestTx(entry.txKey)
	return memR.requests.ForTx(entry.txKey) != 0
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
	start := time.Now()
	ctx := context.Background()
	resp, err := memR.mempool.proxyAppConn.QuerySequence(ctx, &abci.RequestQuerySequence{Signer: signer})
	elapsed := time.Since(start)
	if elapsed > 10*time.Millisecond {
		memR.Logger.Info("SLOW querySequenceFromApplication", "elapsed", elapsed, "signer", fmt.Sprintf("%X", signer))
	}
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

// findNewPeerToSendTx finds a new peer that has already seen the transaction to
// request a transaction from.
func (memR *Reactor) findNewPeerToRequestTx(txKey types.TxKey) {
	// ensure that we are connected to peers
	if memR.ids.Len() == 0 {
		return
	}

	seenMap := memR.mempool.seenByPeersSet.Get(txKey)
	for peerID := range seenMap {
		if memR.requests.Has(peerID, txKey) {
			continue
		}
		peer := memR.ids.GetPeer(peerID)
		if peer != nil {
			memR.mempool.metrics.RerequestedTxs.Add(1)
			memR.requestTx(txKey, peer)
			return
		}
	}

	// No other free peer has the transaction we are looking for.
	// We give up 🤷‍♂️ and hope either a peer responds late or the tx
	// is gossiped again
	memR.Logger.Trace("no other peer has the tx we are looking for", "txKey", txKey)
}
