package cat

import (
	"cmp"
	"context"
	"encoding/hex"
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
)

// Reactor handles mempool tx broadcasting logic amongst peers. For the main
// logic behind the protocol, refer to `ReceiveEnvelope` or to the english
// spec under /.spec.md
type Reactor struct {
	p2p.BaseReactor
	opts               *ReactorOptions
	mempool            *TxPool
	ids                *mempoolIDs
	requests           *requestScheduler
	sequenceTracker    *sequenceTracker
	legacyTxKeyTracker *legacyTxKeyTracker
	upgradedPeers      *upgradedPeers
	receivedBuffer     *receivedTxBuffer
	traceClient        trace.Tracer
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
		opts:               opts,
		mempool:            mempool,
		ids:                newMempoolIDs(),
		requests:           newRequestScheduler(opts.MaxGossipDelay, defaultGlobalRequestTimeout),
		sequenceTracker:    newSequenceTracker(),
		legacyTxKeyTracker: newLegacyTxKeyTracker(),
		upgradedPeers:      newUpgradedPeers(),
		receivedBuffer:     newReceivedTxBuffer(),
		traceClient:        traceClient,
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
	memR.sequenceTracker.removePeer(peerID)
	memR.legacyTxKeyTracker.removePeer(peerID)
	memR.upgradedPeers.remove(peerID)

	// remove and rerequest all pending outbound requests to that peer since we know
	// we won't receive any responses from them.
	outboundRequests := memR.requests.ClearAllRequestsFrom(peerID)
	// Trace requests cleared (peer disconnected)
	if len(outboundRequests) > 0 {
		stats := memR.requests.Stats(peerID, nil)
		schema.WriteMempoolRequest(
			memR.traceClient,
			nil,
			nil,
			0,
			peerID,
			schema.RequestEventCleared,
			stats.TotalByTx,
			stats.TotalBySequence,
			stats.ForPeer,
			stats.ForSigner,
		)
	}
	for key, info := range outboundRequests {
		memR.legacyTxKeyTracker.markRequestFailed(key, peerID)
		memR.mempool.metrics.RequestedTxs.Add(1)
		memR.findNewPeerToRequestTx(key, info.signer, info.sequence)
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

		// validate the provided tx metadata if peer is upgraded
		isUpgraded, peerSigners, peerSequences, ok := memR.prepareTxsMetadata(msg, protoTxs, e.Src)
		if !ok {
			memR.Logger.Error("received txs from upgraded peer with incorrect metadata", "src", e.Src, "msg", msg)
			return
		}

		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		txInfo := mempool.TxInfo{SenderID: peerID}
		txInfo.SenderP2PID = e.Src.ID()

		for idx, tx := range protoTxs {
			ntx := types.Tx(tx)
			key := ntx.Key()
			cachedTx := ntx.ToCachedTx()
			schema.WriteMempoolTx(memR.traceClient, string(e.Src.ID()), key[:], len(tx), schema.Download)
			// resolve the signer and sequence for the tx based on if the peer is upgraded or not
			signer, sequence, haveSignerSequence := memR.resolveTxSignerSequence(isUpgraded, idx, key, peerSigners, peerSequences, e.Src)
			rt := txRoute{peerID: peerID, txKey: key, signer: signer, sequence: sequence}
			if memR.requests.HasRoute(memR.peerIsUpgraded(peerID), peerID, key, signer, sequence) {
				memR.requests.MarkReceivedRoute(memR.peerIsUpgraded(peerID), peerID, key, signer, sequence)
				memR.Logger.Trace("received a response for a requested transaction", "peerID", peerID, "txKey", key)
			} else {
				// If we didn't request the transaction we simply mark the peer as having the
				// tx (we'd have already done it if we were requesting the tx).
				memR.mempool.PeerHasTxRoute(memR.peerIsUpgraded(peerID), peerID, key, signer, sequence)
				memR.Logger.Trace("received new transaction", "peerID", peerID, "txKey", key)
			}

			// Look up signer/sequence from pending tracker
			if haveSignerSequence {
				// We have sequence info - check if we should buffer or process
				expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
				if haveExpected && sequence > expectedSeq {
					// If sequence is too far ahead, move on
					if sequence > expectedSeq+maxReceivedBufferSize {
						continue
					}
					// Future sequence within lookahead - buffer it for later
					if memR.receivedBuffer.add(signer, sequence, cachedTx, key, txInfo, string(e.Src.ID())) {
						// todo: decide if we should be doing this here at all.
						memR.legacyTxKeyTracker.removeBySignerSequence(signer, sequence)
					}
					continue
				}
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
		if len(msg.Signer) > maxSignerLength {
			memR.Logger.Error("peer sent SeenTx with signer too long", "len", len(msg.Signer))
			memR.Switch.StopPeerForError(e.Src, errSignerTooLong, memR.String())
			return
		}
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		isUpgraded := memR.upgradedPeers.peerUpgradeStatus(peerID, msg)

		// This will return an empty tx key if the peer is upgraded
		txKey := memR.resolveTxKey(isUpgraded, msg.TxKey, e.Src)

		// Internally we check if tx key is empty and route for legacy or upgraded peers that way
		rt := txRoute{peerID: peerID, txKey: txKey, signer: msg.Signer, sequence: msg.Sequence}
		memR.mempool.PeerHasTxRoute(memR.peerIsUpgraded(peerID), rt.peerID, rt.txKey, rt.signer, rt.sequence)
		schema.WriteMempoolPeerStateWithSeq(memR.traceClient, string(e.Src.ID()), schema.SeenTx, txKey[:], schema.Download, msg.Signer, msg.Sequence, msg.MinSequence, msg.MaxSequence)

		// Check if we don't already have the transaction
		// peer version routing is handled internally
		if memR.mempool.HasTx(memR.peerIsUpgraded(peerID), txKey, msg.Signer, msg.Sequence) {
			memR.Logger.Trace("received a seen tx for a tx we already have", "txKey", txKey)
			return
		}

		// If we are already requesting that tx, then we don't need to go any further.
		// peer version routing is handled internally
		if memR.requests.ForTxRoute(memR.peerIsUpgraded(peerID), txKey, msg.Signer, msg.Sequence) != 0 {
			memR.Logger.Trace("received a SeenTx message for a transaction we are already requesting", "txKey", txKey)
			return
		}

		expectedSeq, haveExpected := memR.querySequenceFromApplication(msg.Signer)

		switch { //NOTE: we added sequence info in last release, this will probably be released  in v8 therefore all nodes should have sequence info
		case !haveExpected:
			// fall through and request immediately if we cannot query the application
			schema.WriteMempoolPeerStateWithSeq(memR.traceClient, string(e.Src.ID()), schema.MissingSequence, txKey[:], schema.Download, msg.Signer, msg.Sequence, msg.MinSequence, msg.MaxSequence)
		case msg.Sequence == expectedSeq:
			// fall through and request immediately for the expected sequence
		case msg.Sequence > expectedSeq:
			memR.recordSeenTx(msg, txKey, peerID)
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
		memR.requestByRoute(rt)

	// A peer is requesting a transaction that we have claimed to have. Find the specified
	// transaction and broadcast it to the peer. We may no longer have the transaction
	case *protomem.WantTx:
		peerID := memR.ids.GetIDForPeer(e.Src.ID())
		isUpgraded := memR.upgradedPeers.peerUpgradeStatus(peerID, msg)
		txKey := memR.resolveTxKey(isUpgraded, msg.TxKey, e.Src)

		tx, has := memR.resolveWantTx(isUpgraded, msg, txKey)
		schema.WriteMempoolPeerState(memR.traceClient, string(e.Src.ID()), schema.WantTx, msg.TxKey, schema.Download)

		if has && !memR.opts.ListenOnly {
			memR.Logger.Trace("sending a tx in response to a want msg", "peer", peerID)
			txsMsg := &protomem.Txs{Txs: [][]byte{tx.Tx}}
			if memR.peerIsUpgraded(peerID) {
				txsMsg.Signers = [][]byte{msg.Signer}
				txsMsg.Sequences = []uint64{msg.Sequence}
			}
			if e.Src.Send(p2p.Envelope{
				ChannelID: MempoolDataChannel,
				Message:   txsMsg,
			}) {
				memR.mempool.PeerHasTxRoute(memR.peerIsUpgraded(peerID), peerID, txKey, msg.Signer, msg.Sequence)
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
	// get the lowest and highest sequence for this signer from the mempool
	minSequence, maxSequence := memR.mempool.store.getMinMaxSequenceForSigner(signer)

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

		seen := &protomem.SeenTx{
			Signer:      signer,
			Sequence:    sequence,
			MinSequence: minSequence,
			MaxSequence: maxSequence,
		}
		if !memR.upgradedPeers.has(id) {
			seen.TxKey = txKey[:]
		}
		msg := &protomem.Message{
			Sum: &protomem.Message_SeenTx{SeenTx: seen},
		}

		if peer.Send(
			p2p.Envelope{
				ChannelID: MempoolDataChannel,
				Message:   msg,
			},
		) {
			memR.mempool.PeerHasTxRoute(false, id, txKey, signer, sequence)
			schema.WriteMempoolPeerStateWithSeq(memR.traceClient, string(peer.ID()), schema.SeenTx, txKey[:], schema.Upload, signer, sequence, minSequence, maxSequence)
			sent++
		}
	}
}

// routeRequestTx routes a request to a peer based on peer version
func (memR *Reactor) routeRequestTx(txKey types.TxKey, signer []byte, sequence uint64, peer p2p.Peer) {
	peerID := memR.ids.GetIDForPeer(peer.ID())
	if memR.upgradedPeers.has(peerID) {
		memR.requestTxBySequence(signer, sequence, peer)
	} else {
		memR.requestTxWithTxKey(txKey, peer)
	}
}

// requesting it from another peer if the first peer does not respond.
func (memR *Reactor) requestTxWithTxKey(txKey types.TxKey, peer p2p.Peer) bool {
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
		requested := memR.requests.Add(txKey, []byte{}, 0, peerID, memR.onRequestTimeout)
		if !requested {
			memR.Logger.Error("have already marked a tx as requested", "txKey", txKey, "peerID", peer.ID())
		} else {
			// Trace request added (by txKey)
			stats := memR.requests.Stats(peerID, nil)
			schema.WriteMempoolRequest(
				memR.traceClient,
				txKey[:],
				nil,
				0,
				peerID,
				schema.RequestEventAdded,
				stats.TotalByTx,
				stats.TotalBySequence,
				stats.ForPeer,
				stats.ForSigner,
			)
		}

		// TODO: figure out what to do with signer and sequence here
		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.WantTx, txKey[:], schema.Upload)
	}
	return success
}

func (memR *Reactor) requestTxBySequence(signer []byte, sequence uint64, peer p2p.Peer) bool {
	if peer == nil {
		// we have disconnected from the peer
		return false
	}

	peerID := memR.ids.GetIDForPeer(peer.ID())
	if !memR.upgradedPeers.has(peerID) {
		return false
	}
	memR.Logger.Trace("requesting tx by sequence", "signer", string(signer), "sequence", sequence, "peerID", peer.ID())
	msg := &protomem.Message{
		Sum: &protomem.Message_WantTx{
			WantTx: &protomem.WantTx{Signer: signer, Sequence: sequence},
		},
	}
	success := peer.Send(p2p.Envelope{ChannelID: MempoolWantsChannel, Message: msg})
	if success {
		requested := memR.requests.Add(types.TxKey{}, signer, sequence, peerID, memR.onRequestTimeout)
		if !requested {
			memR.Logger.Error("have already marked a tx as requested", "signer", string(signer), "sequence", sequence, "peerID", peer.ID())
		} else {
			// Trace request added (by sequence)
			stats := memR.requests.Stats(peerID, signer)
			schema.WriteMempoolRequest(
				memR.traceClient,
				nil,
				signer,
				sequence,
				peerID,
				schema.RequestEventAdded,
				stats.TotalByTx,
				stats.TotalBySequence,
				stats.ForPeer,
				stats.ForSigner,
			)
		}
		// Note: We don't call markRequested here because there's no sequenceTracker entry
		// for optimistic sequence based requests. The request is tracked in the
		// requests scheduler via requestsBySequence instead.
		schema.WriteMempoolPeerStateWithSeq(memR.traceClient, string(peer.ID()), schema.WantTx, []byte{}, schema.Upload, signer, sequence, 0, 0)
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
		memR.legacyTxKeyTracker.removeByTxKey(key)
	}
	if err != nil {
		return
	}

	if len(rsp.Address) > 0 {
		memR.processReceivedBuffer(rsp.Address)
		memR.processSequenceGapsForSigner(rsp.Address)
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
		memR.receivedBuffer.removeLowerSeqs(signer, expectedSeq)

		rsp, err := memR.tryAddNewTx(buffered.tx, buffered.txKey, buffered.txInfo, buffered.peerID)
		if err == nil || errors.Is(err, ErrTxInMempool) {
			memR.legacyTxKeyTracker.removeBySignerSequence(signer, expectedSeq)
		}
		if err != nil {
			break
		}

		// todo: should we be marking tx request received here?

		if !memR.opts.ListenOnly && rsp.Code == 0 {
			memR.broadcastSeenTx(buffered.txKey, signer, expectedSeq)
		}
	}
}

// processSequenceGapsForSigner tries to advance the pipeline of transactions for a signer by requesting the ranges that peers definitely have.
// It requests consecutive sequences in parallel from different peers whenever we have seen a consecutive sequence numbers,
// buffering out-of-order arrivals for later processing. This allows fast catch-up even when tx sources are distributed.
func (memR *Reactor) processSequenceGapsForSigner(signer []byte) {
	if len(signer) == 0 {
		return
	}

	expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
	if !haveExpected {
		memR.Logger.Error("no signer found in application")
		return
	}

	// Clean up stale entries from sequence tracker below expected sequence
	memR.sequenceTracker.removeBelowSequence(signer, expectedSeq)

	// Get the maximum sequence peers claim to have and which peers can serve requests
	maxSeq := memR.sequenceTracker.getMaxSeenSeqForSigner(signer)
	if maxSeq < expectedSeq {
		return
	}

	// Request sequences from expectedSeq up to maxSeq, but cap the number of requests
	// to maxReceivedBufferSize to avoid unbounded buffering.
	requested := 0
	for curSeq := expectedSeq; curSeq <= maxSeq; curSeq++ {
		if requested >= maxReceivedBufferSize {
			break
		}
		// Skip if already buffered
		if memR.receivedBuffer.get(signer, curSeq) != nil {
			continue
		}
		// Skip if already in mempool
		_, exists := memR.mempool.store.getTxBySignerSequence(signer, curSeq)
		if exists {
			continue
		}
		// Skip if already requested
		if memR.requests.ForSignerSequence(signer, curSeq) != 0 {
			continue
		}
		// Request this sequence from any peer that claims to have it
		if memR.tryRequestTxBySequence(signer, curSeq) {
			requested++
		}
	}

	if requested > 0 {
		memR.Logger.Info("parallel requests sent", "count", requested)
	}

}

// processLegacyTxKeysForSigner tries to advance the pipeline of queued transactions for a signer.
// It requests consecutive sequences in parallel from different peers whenever we have seen a consecutive sequence numbers,
// buffering out-of-order arrivals for later processing. This allows fast catch-up even when tx sources are distributed.
func (memR *Reactor) processLegacyTxKeysForSigner(signer []byte) {
	if len(signer) == 0 {
		return
	}

	entries := memR.legacyTxKeyTracker.entriesForSigner(signer)
	if len(entries) == 0 {
		return
	}
	slices.SortFunc(entries, func(a, b *txKeyEntry) int {
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
			memR.legacyTxKeyTracker.removeByTxKey(entry.txKey)
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
			memR.legacyTxKeyTracker.removeByTxKey(entry.txKey)
			nextSeq++
			continue
		}

		// Skip if already being requested, but count it
		if memR.requests.ForTx(entry.txKey) != 0 || memR.requests.Has(entry.peer, entry.txKey) {
			nextSeq++
			requested++
			continue
		}

		// Request from first available peer
		// TODO: potentially need to make this logic the way it previously was if we still want to support this logic
		if memR.requests.CountForPeer(entry.peer) >= maxRequestsPerPeer {
			continue
		}
		peer := memR.ids.GetPeer(entry.peer)
		if peer != nil && memR.requestTxWithTxKey(entry.txKey, peer) {
			requested++
			continue
		}
		nextSeq++
	}

	if requested > 0 {
		memR.Logger.Trace("parallel requests sent", "count", requested)
	}
}

func (memR *Reactor) onRequestTimeout(txKey types.TxKey, signer []byte, sequence uint64, peerID uint16) {
	if len(signer) > 0 && sequence > 0 {
		memR.tryRequestTxBySequence(signer, sequence)
		return
	}
	memR.legacyTxKeyTracker.markRequestFailed(txKey, peerID)
	memR.findNewPeerToRequestTx(txKey, signer, sequence)
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
			memR.refreshSequenceQueues()
			memR.refreshLegacyTxKeys()
		}
	}
}

// refreshSequenceQueues refreshes the sequence tracker and processes the received buffer and sequence gaps for each signer
func (memR *Reactor) refreshSequenceQueues() {
	// Update metrics gauges
	memR.updateMetricsGauges()

	memR.logMempoolSignerState()
	// Collect signers from both pending seen and buffer
	seenSigners := make(map[string][]byte)
	for _, signer := range memR.sequenceTracker.signerKeysFromPeerMax() {
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
		memR.processSequenceGapsForSigner(signer)
	}
}

func (memR *Reactor) refreshLegacyTxKeys() {
	legacySigners := make(map[string][]byte)
	for _, entry := range memR.legacyTxKeyTracker.entries() {
		legacySigners[string(entry.signer)] = entry.signer
	}
	for _, signer := range legacySigners {
		memR.processLegacyTxKeysForSigner(signer)
	}
}

// updateMetricsGauges updates all gauge metrics with current state (per signer)
func (memR *Reactor) updateMetricsGauges() {
	// Collect all signers from pending seen, max seen seq, and buffer
	// Use hex-encoded keys for Prometheus label compatibility
	allSigners := make(map[string][]byte)
	for _, signer := range memR.sequenceTracker.signerKeysFromPeerMax() {
		allSigners[hex.EncodeToString(signer)] = signer
	}
	for _, signer := range memR.receivedBuffer.signerKeys() {
		allSigners[hex.EncodeToString(signer)] = signer
	}

	totalPendingSeen := 0
	// TODO: needs to be heavily reworked
	// Update per-signer metrics
	// for signerHex, signer := range allSigners {

		// // Buffer metrics per signer
		// bufferStats := memR.receivedBuffer.statsForSigner(signer)
		// memR.mempool.metrics.ReceivedBufferSize.With("signer", signerHex).Set(float64(bufferStats.Size))
		// memR.mempool.metrics.BufferSizePerSigner.With("signer", signerHex).Set(float64(bufferStats.Size))

		// // Expected sequence from app
		// expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
		// if haveExpected {
		// 	memR.mempool.metrics.ExpectedSeqPerSigner.With("signer", signerHex).Set(float64(expectedSeq))
		// }

		// // Max seen sequence from SeenTx messages
		// maxSeenSeq := memR.sequenceTracker.getMaxSeenSeqForSigner(signer)
		// memR.mempool.metrics.MaxSeenTxSeqPerSigner.With("signer", signerHex).Set(float64(maxSeenSeq))

		// // Max requested sequence for this signer
		// maxRequestedSeq := memR.requests.MaxSequenceForSigner(signer)
		// memR.mempool.metrics.MaxRequestedSeqPerSigner.With("signer", signerHex).Set(float64(maxRequestedSeq))

	// }

	memR.mempool.metrics.PendingSeenTotal.Set(float64(totalPendingSeen))
}

// logMempoolSignerState logs the state of all signers in the mempool
func (memR *Reactor) logMempoolSignerState() {
	memR.mempool.store.processOrderedTxSets(func(txSets []*txSet) {
		for _, set := range txSets {
			if len(set.signer) == 0 {
				continue
			}
			// Get sequence range from the set
			var minSeq, maxSeq uint64
			if len(set.txs) > 0 {
				minSeq = set.txs[0].sequence
				maxSeq = set.txs[len(set.txs)-1].sequence
			}
			// Get expected sequence from application
			expectedSeq, _ := memR.querySequenceFromApplication(set.signer)

			memR.Logger.Info("mempool signer state",
				"signer", string(set.signer),
				"txCount", len(set.txs),
				"seqRange", fmt.Sprintf("[%d-%d]", minSeq, maxSeq),
				"expectedSeq", expectedSeq,
			)
		}
	})
}

func (memR *Reactor) querySequenceFromApplication(signer []byte) (uint64, bool) {
	ctx := context.Background()
	resp, err := memR.mempool.proxyAppConn.QuerySequence(ctx, &abci.RequestQuerySequence{Signer: signer})
	if err != nil || resp == nil {
		return 0, false
	}
	// If the response is 0, treat it as "sequence tracking not available"
	// to maintain backward compatibility with apps that don't implement QuerySequence.
	// This prevents transactions from being stuck in the sequence tracker when the app returns 0.
	if resp.Sequence == 0 {
		return 0, false
	}
	return resp.Sequence, true
}

// findNewPeerToSendTx finds a new peer that has already seen the transaction to
// request a transaction from.
func (memR *Reactor) findNewPeerToRequestTx(txKey types.TxKey, signer []byte, sequence uint64) {
	// ensure that we are connected to peers
	if memR.ids.Len() == 0 {
		return
	}

	if len(signer) > 0 && sequence > 0 {
		if memR.tryRequestTxBySequence(signer, sequence) {
			memR.mempool.metrics.RerequestedTxs.Add(1)
			return
		}
	}

	if txKey == (types.TxKey{}) {
		memR.Logger.Trace("no txKey available to reroute request", "signer", string(signer), "sequence", sequence)
		return
	}

	if memR.tryRequestTxByKey(txKey) {
		memR.mempool.metrics.RerequestedTxs.Add(1)
		return
	}

	// No other free peer has the transaction we are looking for.
	// We give up 🤷‍♂️ and hope either a peer responds late or the tx
	// is gossiped again
	memR.Logger.Trace("no other peer has the tx we are looking for", "txKey", txKey)
}

// tryRequestTxBySequence tries to request a transaction by (signer, sequence)
// we just extended wanttx to be able to look up by (signer, sequence)
func (memR *Reactor) tryRequestTxBySequence(signer []byte, sequence uint64) bool {
	peersWithSignerRanges := memR.sequenceTracker.getPeersForSignerSequence(signer, sequence)
	for _, peerID := range peersWithSignerRanges {
		rt := txRoute{peerID: peerID, signer: signer, sequence: sequence}
		if !memR.peerEligibleForRoute(rt) {
			continue
		}
		if memR.requestByRoute(rt) {
			return true
		}
	}

	// All peers at capacity or unavailable for this sequence
	memR.Logger.Debug(
		"no available peer for sequence request",
		"signer", string(signer),
		"sequence", sequence,
		"peersChecked", len(peersWithSignerRanges),
	)
	return false
}

func (memR *Reactor) tryRequestTxByKey(txKey types.TxKey) bool {
	seenMap := memR.mempool.seenByPeersSet.Get(txKey)
	for peerID := range seenMap {
		rt := txRoute{peerID: peerID, txKey: txKey}
		if !memR.peerEligibleForRoute(rt) {
			continue
		}
		if memR.requestByRoute(rt) {
			return true
		}
	}
	return false
}
