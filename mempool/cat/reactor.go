package cat

import (
	"context"
	"fmt"
	"math/rand"
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
)

// Reactor handles mempool tx broadcasting logic amongst peers. For the main
// logic behind the protocol, refer to `ReceiveEnvelope` or to the english
// spec under /.spec.md
type Reactor struct {
	p2p.BaseReactor
	opts        *ReactorOptions
	mempool     *TxPool
	ids         *mempoolIDs
	requests    *requestScheduler
	traceClient trace.Tracer
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
		opts:        opts,
		mempool:     mempool,
		ids:         newMempoolIDs(),
		requests:    newRequestScheduler(opts.MaxGossipDelay, defaultGlobalRequestTimeout),
		traceClient: traceClient,
	}
	memR.BaseReactor = *p2p.NewBaseReactor("CAT", memR,
		p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize),
		p2p.WithQueueingFunc(memR.TryQueueUnprocessedEnvelope),
	)
	return memR, nil
}

// SetLogger sets the Logger on the reactor and the underlying mempool.
func (memR *Reactor) SetLogger(l log.Logger) {
	memR.Logger = l
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

	// remove and rerequest all pending outbound requests to that peer since we know
	// we won't receive any responses from them.
	outboundRequests := memR.requests.ClearAllRequestsFrom(peerID)
	for key := range outboundRequests {
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
			schema.WriteMempoolTx(memR.traceClient, string(e.Src.ID()), key[:], len(tx), schema.Download)
			// If we requested the transaction we mark it as received.
			if memR.requests.Has(peerID, key) {
				memR.requests.MarkReceived(peerID, key)
				memR.Logger.Debug("received a response for a requested transaction", "peerID", peerID, "txKey", key)
			} else {
				// If we didn't request the transaction we simply mark the peer as having the
				// tx (we'd have already done it if we were requesting the tx).
				memR.mempool.PeerHasTx(peerID, key)
				memR.Logger.Debug("received new trasaction", "peerID", peerID, "txKey", key)
			}
			rsp, err := memR.mempool.TryAddNewTx(ntx.ToCachedTx(), key, txInfo)

			// Extract signer/sequence from CheckTx response if available
			signer, signerBytes, sequence, execCode := "", []byte(nil), uint64(0), uint32(0)
			if rsp != nil {
				signerBytes = rsp.Address
				signer = string(rsp.Address)
				sequence = rsp.Sequence
				execCode = rsp.Code
			}

			// Trace the result of adding the transaction to the mempool
			if err != nil {
				if err == ErrTxInMempool {
					schema.WriteMempoolAddResult(memR.traceClient, string(e.Src.ID()), key[:], schema.AlreadyInMempool, err, signer, sequence)
				} else {
					schema.WriteMempoolAddResult(memR.traceClient, string(e.Src.ID()), key[:], schema.Rejected, err, signer, sequence)
					memR.Logger.Debug("Could not add tx", "txKey", key, "err", err)
					return
				}
			} else {
				if execCode != 0 {
					schema.WriteMempoolAddResult(memR.traceClient, string(e.Src.ID()), key[:], schema.Rejected, fmt.Errorf("execution code: %d", execCode), signer, sequence)
				} else {
					schema.WriteMempoolAddResult(memR.traceClient, string(e.Src.ID()), key[:], schema.Added, nil, signer, sequence)
				}

			}

			if !memR.opts.ListenOnly && execCode == 0 {
				// We broadcast only transactions that we deem valid and actually have in our mempool.
				memR.broadcastSeenTx(key, signerBytes, sequence)
			}
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
			memR.Logger.Debug("received a seen tx for a tx we already have", "txKey", txKey)
			return
		}

		// If we are already requesting that tx, then we don't need to go any further.
		if memR.requests.ForTx(txKey) != 0 {
			memR.Logger.Debug("received a SeenTx message for a transaction we are already requesting", "txKey", txKey)
			return
		}

		// Use local sequence information when available, otherwise query the application.
		if len(msg.Signer) > 0 && msg.Sequence > 0 {
			expectedSeq, haveExpected := memR.mempool.lowestSequenceForSigner(msg.Signer)
			sequenceSource := "local-mempool"

			if !haveExpected {
				ctx := context.Background()
				resp, err := memR.mempool.proxyAppConn.QuerySequence(ctx, &abci.RequestQuerySequence{
					Signer: msg.Signer,
				})
				if err == nil && resp != nil && resp.Sequence > 0 {
					expectedSeq = resp.Sequence
					haveExpected = true
					sequenceSource = "application"
				}
			}

			if haveExpected && expectedSeq != msg.Sequence {
				memR.Logger.Debug("sequence mismatch for transaction, skipping request",
					"txKey", txKey,
					"expected", expectedSeq,
					"received", msg.Sequence,
					"signer", string(msg.Signer),
					"source", sequenceSource,
				)
				// Skip requesting the transaction if the sequence doesn't match
				return
			}
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
			memR.Logger.Debug("sending a tx in response to a want msg", "peer", peerID)
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

// ShufflePeers shuffles the peers map from GetAll() and returns a new shuffled map.
// Uses Fisher-Yates shuffle algorithm for maximum speed.
func ShufflePeers(peers map[uint16]p2p.Peer) map[uint16]p2p.Peer {
	if len(peers) <= 1 {
		return peers
	}

	// Extract keys into a slice for shuffling
	keys := make([]uint16, 0, len(peers))
	for id := range peers {
		keys = append(keys, id)
	}

	// Fast Fisher-Yates shuffle on keys
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := len(keys) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		keys[i], keys[j] = keys[j], keys[i]
	}

	// Build a new map with shuffled order
	result := make(map[uint16]p2p.Peer, len(peers))
	for _, id := range keys {
		result[id] = peers[id]
	}

	return result
}

// broadcastNewTx broadcast new transaction to limited peers unless we are already sure they have seen the tx.
func (memR *Reactor) broadcastNewTx(wtx *wrappedTx) {
	memR.broadcastSeenTxWithHeight(wtx.key(), wtx.height, wtx.sender, wtx.sequence)
}

// broadcastSeenTxWithHeight is a helper that broadcasts a SeenTx message with height checking.
func (memR *Reactor) broadcastSeenTxWithHeight(txKey types.TxKey, height int64, signer []byte, sequence uint64) {
	memR.Logger.Debug("broadcasting seen tx to limited peers", "tx_key", string(txKey[:]))
	msg := &protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey:    txKey[:],
				Signer:   signer,
				Sequence: sequence,
			},
		},
	}

	count := 0
	for id, peer := range ShufflePeers(memR.ids.GetAll()) {
		if count >= maxSeenTxBroadcast {
			break
		}
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			// make sure peer isn't too far behind. This can happen
			// if the peer is blocksyncing still and catching up
			// in which case we just skip sending the transaction
			if p.GetHeight() < height-peerHeightDiff {
				memR.Logger.Debug("peer is too far behind us. Skipping broadcast of seen tx")
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
		}
		count++
	}
}

// requestTx requests a transaction from a peer and tracks it,
// requesting it from another peer if the first peer does not respond.
func (memR *Reactor) requestTx(txKey types.TxKey, peer p2p.Peer) {
	if peer == nil {
		// we have disconnected from the peer
		return
	}
	memR.Logger.Debug("requesting tx", "txKey", txKey, "peerID", peer.ID())
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
		requested := memR.requests.Add(txKey, memR.ids.GetIDForPeer(peer.ID()), memR.findNewPeerToRequestTx)
		if !requested {
			memR.Logger.Error("have already marked a tx as requested", "txKey", txKey, "peerID", peer.ID())
		}

		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.WantTx, txKey[:], schema.Upload)
	}
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
	memR.Logger.Debug("no other peer has the tx we are looking for", "txKey", txKey)
}
