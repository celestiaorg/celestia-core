package cat

import (
	"errors"
	"time"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

const (
	// MempoolChunkChannel carries bulk TxChunk payloads, isolated from the
	// metadata channels so chunk backlogs cannot starve SeenTx/manifest/want
	// traffic.
	MempoolChunkChannel = byte(0x33)

	// maxWantChunkIndexes bounds the number of chunk indexes a single WantChunk
	// may request, protecting both the wire size of the message and the work a
	// server does per request.
	maxWantChunkIndexes = 256
)

var (
	errEmptyWantChunk      = errors.New("want chunk message requests no indexes")
	errTooManyChunkIndexes = errors.New("want chunk message requests too many indexes")
)

// advertiseLargeTx takes the chunked fast path for a locally-accepted large tx:
// it builds and stores a manifest, advertises it to peers, and optimistically
// pushes the leading chunks to the top-scored advertised peer.
func (memR *Reactor) advertiseLargeTx(wtx *wrappedTx) {
	txKey := wtx.key()
	tx := wtx.tx.Tx
	priority := uint64(0)
	if wtx.priority > 0 {
		priority = uint64(wtx.priority)
	}
	manifest := buildManifest(txKey, tx, memR.opts.LargeTxChunkSize, wtx.sender, wtx.sequence, priority)
	memR.manifests.set(txKey, manifest)
	memR.Logger.Debug("advertising large tx via manifest", "txKey", txKey, "size", len(tx), "chunks", manifest.ChunkCount)

	advertised := memR.advertiseManifest(txKey, manifest, wtx.height, wtx.sender)
	memR.optimisticPush(txKey, manifest, tx, advertised)
}

// advertiseManifest sends a TxManifest to sticky peers (ranked by rendezvous
// hashing, capped at LargeTxMaxAdvertisePeers) and returns the peers it reached
// so the caller can target them for optimistic pushes.
func (memR *Reactor) advertiseManifest(txKey types.TxKey, manifest *protomem.TxManifest, height int64, signer []byte) []uint16 {
	peers := memR.ids.GetAll()
	if len(peers) == 0 {
		return nil
	}
	ordered := selectStickyPeers(signer, peers, len(peers), memR.currentStickyPeerSalt())
	msg := &protomem.Message{
		Sum: &protomem.Message_TxManifest{TxManifest: manifest},
	}

	advertised := make([]uint16, 0, memR.opts.LargeTxMaxAdvertisePeers)
	for _, peerInfo := range ordered {
		if len(advertised) >= memR.opts.LargeTxMaxAdvertisePeers {
			break
		}
		id := peerInfo.id
		peer := peerInfo.peer
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			if p.GetHeight() < height-peerHeightDiff {
				continue
			}
		}
		if memR.mempool.seenByPeersSet.Has(txKey, id) {
			continue
		}
		if peer.Send(p2p.Envelope{ChannelID: MempoolDataChannel, Message: msg}) {
			memR.mempool.PeerHasTx(id, txKey)
			advertised = append(advertised, id)
		}
	}
	return advertised
}

// optimisticPush sends the first LargeTxOptimisticPushChunks chunks to the
// top-scored advertised peer, removing one request round trip for early
// reconstruction. It is best-effort: dropped pushes are simply re-requested.
func (memR *Reactor) optimisticPush(txKey types.TxKey, manifest *protomem.TxManifest, tx []byte, advertised []uint16) {
	n := memR.opts.LargeTxOptimisticPushChunks
	if n <= 0 || len(advertised) == 0 {
		return
	}
	ranked := memR.peerScores.Rank(advertised)
	peer := memR.ids.GetPeer(ranked[0])
	if peer == nil {
		return
	}
	count := n
	if count > int(manifest.ChunkCount) {
		count = int(manifest.ChunkCount)
	}
	for i := 0; i < count; i++ {
		data, err := chunkData(manifest, tx, i)
		if err != nil {
			return
		}
		memR.sendChunk(peer, txKey, uint32(i), data)
	}
}

// receiveTxManifest starts (or joins) a reconstruction session for an advertised
// large tx and begins fetching its chunks.
func (memR *Reactor) receiveTxManifest(msg *protomem.TxManifest, src p2p.Peer) {
	if !memR.opts.largeTxFastPathEnabled() {
		return
	}
	if err := validateTxManifest(msg, memR.opts.MaxTxSize); err != nil {
		memR.Logger.Error("peer sent invalid tx manifest", "err", err, "src", src.ID())
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	txKey, _ := types.TxKeyFromBytes(msg.TxKey)
	peerID := memR.ids.GetIDForPeer(src.ID())
	memR.mempool.PeerHasTx(peerID, txKey)

	// Drop if we already have it, recently rejected it, or are already pulling
	// the whole tx via the legacy path.
	if memR.mempool.Has(txKey) {
		return
	}
	if rejected, _, _ := memR.mempool.WasRecentlyRejected(txKey); rejected {
		return
	}
	if memR.requests.ForTx(txKey) != 0 {
		return
	}

	session, _ := memR.reconstructions.create(msg, mempool.TxInfo{SenderID: peerID, SenderP2PID: src.ID()}, time.Now())
	session.addPeer(peerID)
	memR.scheduleChunkRequests(txKey, session)
}

// receiveWantChunk serves requested chunks of a large tx we hold by slicing them
// from the full tx and sending them on the chunk channel.
func (memR *Reactor) receiveWantChunk(msg *protomem.WantChunk, src p2p.Peer) {
	if memR.opts.ListenOnly {
		return
	}
	if len(msg.Indexes) == 0 {
		memR.Switch.StopPeerForError(src, errEmptyWantChunk, memR.String())
		return
	}
	if len(msg.Indexes) > maxWantChunkIndexes {
		memR.Switch.StopPeerForError(src, errTooManyChunkIndexes, memR.String())
		return
	}
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	manifest, ok := memR.manifests.get(txKey)
	if !ok {
		return
	}
	cached, ok := memR.mempool.GetTxByKey(txKey)
	if !ok {
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	for _, idx := range msg.Indexes {
		data, err := chunkData(manifest, cached.Tx, int(idx))
		if err != nil {
			continue
		}
		if memR.sendChunk(src, txKey, idx, data) {
			memR.mempool.PeerHasTx(peerID, txKey)
		} else {
			// Send queue full: stop serving this peer for now (backpressure).
			memR.peerScores.RecordQueueFull(peerID)
			break
		}
	}
}

// receiveTxChunk verifies a received chunk against the manifest and, once all
// chunks have arrived, reconstructs and adds the tx.
func (memR *Reactor) receiveTxChunk(msg *protomem.TxChunk, src p2p.Peer) {
	if !memR.opts.largeTxFastPathEnabled() {
		return
	}
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	session, ok := memR.reconstructions.get(txKey)
	if !ok {
		// No active session: the chunk is unsolicited (e.g. an optimistic push
		// that lost the race with its manifest, or a late duplicate). Drop it.
		return
	}

	added, err := session.addChunk(int(msg.Index), msg.Data, peerID)
	if err != nil {
		// A chunk that fails hash verification indicates a faulty or malicious
		// peer. Penalize heavily and disconnect.
		memR.peerScores.RecordInvalidChunk(peerID)
		memR.Logger.Error("peer sent invalid chunk", "txKey", txKey, "index", msg.Index, "err", err, "src", src.ID())
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}

	if reqPeer, sentAt, wasRequested := memR.chunkRequests.MarkReceived(txKey, msg.Index); wasRequested {
		memR.peerScores.RecordChunkSuccess(reqPeer, time.Since(sentAt), len(msg.Data))
	} else if added {
		// Unsolicited but useful (optimistic push): credit the sender.
		memR.peerScores.RecordChunkSuccess(peerID, 0, len(msg.Data))
	}
	session.addPeer(peerID)

	if session.isComplete() {
		memR.finishReconstruction(txKey, session)
		return
	}
	memR.scheduleChunkRequests(txKey, session)
}

// scheduleChunkRequests fills up to LargeTxRequestParallelism peers, each up to
// LargeTxMaxInflightChunksPerPeer outstanding chunks, with disjoint missing
// chunk indexes for the session, preferring higher-scored peers.
func (memR *Reactor) scheduleChunkRequests(txKey types.TxKey, session *reconstructionSession) {
	if session.isComplete() {
		return
	}
	candidates := session.peers()
	if len(candidates) == 0 {
		// Fall back to any peer that has advertised this tx (e.g. via SeenTx).
		for peerID := range memR.mempool.seenByPeersSet.Get(txKey) {
			candidates = append(candidates, peerID)
		}
	}
	ranked := memR.peerScores.Rank(candidates)

	parallelism := memR.opts.LargeTxRequestParallelism
	inflightLimit := memR.opts.LargeTxMaxInflightChunksPerPeer
	used := 0
	for _, peerID := range ranked {
		if used >= parallelism {
			break
		}
		peer := memR.ids.GetPeer(peerID)
		if peer == nil {
			continue
		}
		capacity := inflightLimit - session.inflightCountForPeer(peerID)
		if capacity <= 0 {
			continue
		}
		indexes := session.reserveChunks(peerID, capacity)
		if len(indexes) == 0 {
			continue
		}
		idxU := make([]uint32, len(indexes))
		for i, idx := range indexes {
			idxU[i] = uint32(idx)
		}
		if memR.sendWantChunk(peer, txKey, idxU) {
			for _, idx := range indexes {
				memR.chunkRequests.Add(txKey, uint32(idx), peerID, memR.onChunkTimeout)
			}
			used++
		} else {
			// Send failed: release the reservation so another peer can take it.
			for _, idx := range indexes {
				session.clearInflight(peerID, idx)
			}
			memR.peerScores.RecordQueueFull(peerID)
		}
	}
}

// onChunkTimeout fires when a requested chunk does not arrive in time. It lightly
// penalizes the peer and re-requests the chunk from an alternate peer.
func (memR *Reactor) onChunkTimeout(txKey types.TxKey, index uint32, peer uint16) {
	memR.peerScores.RecordChunkTimeout(peer)
	session, ok := memR.reconstructions.get(txKey)
	if !ok {
		return
	}
	session.clearInflight(peer, int(index))
	memR.scheduleChunkRequests(txKey, session)
}

// finishReconstruction assembles the full tx, runs CheckTx, and on success
// stores the manifest and re-advertises it onward.
func (memR *Reactor) finishReconstruction(txKey types.TxKey, session *reconstructionSession) {
	if !session.tryFinish() {
		return
	}
	memR.chunkRequests.ClearTx(txKey)
	memR.reconstructions.remove(txKey)

	tx, err := session.reconstruct()
	if err != nil {
		memR.Logger.Error("failed to reconstruct large tx", "txKey", txKey, "err", err)
		return
	}

	cached := types.Tx(tx).ToCachedTx()
	rsp, err := memR.tryAddNewTx(cached, txKey, session.txInfo, string(session.txInfo.SenderP2PID))
	if err != nil {
		// ErrTxInMempool means another path added it; anything else is a genuine
		// rejection. Either way there is nothing more to do here.
		return
	}
	if rsp.Code != 0 {
		return
	}

	// Credit the peers that sourced chunks for this successful reconstruction.
	for _, peerID := range session.sourcePeers() {
		memR.peerScores.RecordReconstruction(peerID)
	}

	// Keep the manifest so we can serve chunks and re-advertise to others.
	memR.manifests.set(txKey, session.manifest)
	if !memR.opts.ListenOnly {
		memR.advertiseManifest(txKey, session.manifest, memR.mempool.Height(), rsp.Address)
	}
}

// reconstructionSweepLoop periodically abandons reconstruction sessions that
// have passed their deadline, freeing memory and outstanding request state.
func (memR *Reactor) reconstructionSweepLoop() {
	interval := memR.opts.LargeTxReconstructionTimeout / 2
	if interval < 25*time.Millisecond {
		interval = 25 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-memR.Quit():
			return
		case <-ticker.C:
			now := time.Now()
			for _, session := range memR.reconstructions.all() {
				if session.expired(now) && session.tryFinish() {
					memR.chunkRequests.ClearTx(session.txKey)
					memR.reconstructions.remove(session.txKey)
					memR.Logger.Debug("abandoned expired large-tx reconstruction", "txKey", session.txKey)
				}
			}
		}
	}
}

func (memR *Reactor) sendWantChunk(peer p2p.Peer, txKey types.TxKey, indexes []uint32) bool {
	return peer.TrySend(p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message:   &protomem.WantChunk{TxKey: txKey[:], Indexes: indexes},
	})
}

func (memR *Reactor) sendChunk(peer p2p.Peer, txKey types.TxKey, index uint32, data []byte) bool {
	return peer.TrySend(p2p.Envelope{
		ChannelID: MempoolChunkChannel,
		Message:   &protomem.TxChunk{TxKey: txKey[:], Index: index, Data: data},
	})
}
