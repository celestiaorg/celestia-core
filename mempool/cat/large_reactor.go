package cat

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

func (memR *Reactor) useLargeTxFastPath(tx types.Tx) bool {
	return memR.opts.LargeTxThreshold > 0 &&
		memR.opts.LargeTxChunkSize > 0 &&
		len(tx) >= memR.opts.LargeTxThreshold
}

func (memR *Reactor) broadcastAcceptedTx(tx *types.CachedTx, key types.TxKey, height int64, signer []byte, sequence uint64, priority int64) {
	if !memR.useLargeTxFastPath(tx.Tx) {
		memR.broadcastSeenTxWithHeight(key, height, signer, sequence)
		return
	}
	local, err := memR.ensureLocalLargeTx(tx.Tx, signer, sequence, priority)
	if err != nil {
		memR.Logger.Debug("failed to build large tx manifest", "txKey", key, "err", err)
		memR.broadcastSeenTxWithHeight(key, height, signer, sequence)
		return
	}
	memR.broadcastTxManifestWithHeight(local, height)
}

func (memR *Reactor) ensureLocalLargeTx(tx types.Tx, signer []byte, sequence uint64, priority int64) (*localLargeTx, error) {
	key := tx.Key()

	memR.largeMu.Lock()
	if local := memR.largeTxs[key]; local != nil {
		updateManifestMetadata(local.manifest, signer, sequence, priority)
		memR.largeMu.Unlock()
		return local, nil
	}
	memR.largeMu.Unlock()

	local, err := buildLocalLargeTx(tx, memR.opts.LargeTxChunkSize, signer, sequence, priority)
	if err != nil {
		return nil, err
	}

	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()
	if existing := memR.largeTxs[key]; existing != nil {
		updateManifestMetadata(existing.manifest, signer, sequence, priority)
		return existing, nil
	}
	memR.largeTxs[key] = local
	return local, nil
}

func updateManifestMetadata(manifest *protomem.TxManifest, signer []byte, sequence uint64, priority int64) {
	if manifest == nil {
		return
	}
	manifest.Signer = append(manifest.Signer[:0], signer...)
	manifest.Sequence = sequence
	if priority > 0 {
		manifest.Priority = uint64(priority)
	} else {
		manifest.Priority = 0
	}
}

func (memR *Reactor) getOrBuildLocalLargeTx(txKey types.TxKey) (*localLargeTx, bool) {
	memR.largeMu.Lock()
	local := memR.largeTxs[txKey]
	if local != nil && !memR.mempool.Has(txKey) {
		delete(memR.largeTxs, txKey)
		local = nil
	}
	memR.largeMu.Unlock()
	if local != nil {
		return local, true
	}

	wtx := memR.mempool.store.get(txKey)
	if wtx == nil || !memR.useLargeTxFastPath(wtx.tx.Tx) {
		return nil, false
	}
	local, err := memR.ensureLocalLargeTx(wtx.tx.Tx, wtx.sender, wtx.sequence, wtx.priority)
	if err != nil {
		memR.Logger.Debug("failed to build large tx manifest for serving", "txKey", txKey, "err", err)
		return nil, false
	}
	return local, true
}

func (memR *Reactor) pruneLocalLargeTxs() {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	for txKey := range memR.largeTxs {
		if !memR.mempool.Has(txKey) {
			delete(memR.largeTxs, txKey)
		}
	}
}

func (memR *Reactor) discardLocalLargeTx(txKey types.TxKey) {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	if !memR.mempool.Has(txKey) {
		delete(memR.largeTxs, txKey)
	}
}

func (memR *Reactor) broadcastTxManifestWithHeight(local *localLargeTx, height int64) {
	if local == nil || local.manifest == nil {
		return
	}
	txKey, err := types.TxKeyFromBytes(local.manifest.TxKey)
	if err != nil {
		memR.Logger.Debug("not broadcasting malformed local manifest", "err", err)
		return
	}

	peers := memR.ids.GetAll()
	if len(peers) == 0 {
		return
	}

	orderedPeers := selectStickyPeers(local.manifest.Signer, peers, len(peers), memR.currentStickyPeerSalt())
	sort.SliceStable(orderedPeers, func(i, j int) bool {
		left := memR.peerScores.Score(orderedPeers[i].id)
		right := memR.peerScores.Score(orderedPeers[j].id)
		if left == right {
			return false
		}
		return left > right
	})

	sent := 0
	sentPersistent := 0
	maxAdvertisePeers := memR.opts.LargeTxMaxAdvertisePeers
	maxPersistent := memR.opts.MaxPersistentStickyPeers
	for _, peerInfo := range orderedPeers {
		isPersistent := peerInfo.peer.IsPersistent()
		canSend := sent < maxAdvertisePeers || (isPersistent && sentPersistent < maxPersistent)
		if !canSend {
			continue
		}
		if !memR.peerAtHeight(peerInfo.peer, height) {
			continue
		}
		if memR.mempool.seenByPeersSet.Has(txKey, peerInfo.id) {
			continue
		}

		if peerSupportsChannel(peerInfo.peer, MempoolChunkChannel) {
			if memR.sendTxManifest(peerInfo.peer, peerInfo.id, local.manifest) {
				sent++
				if isPersistent {
					sentPersistent++
				}
				memR.pushOptimisticChunks(peerInfo.peer, peerInfo.id, local)
			}
			continue
		}

		if memR.sendSeenTx(peerInfo.peer, peerInfo.id, txKey, local.manifest.Signer, local.manifest.Sequence) {
			sent++
			if isPersistent {
				sentPersistent++
			}
		}
	}
}

func (memR *Reactor) peerAtHeight(peer p2p.Peer, height int64) bool {
	if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
		return p.GetHeight() >= height-peerHeightDiff
	}
	return true
}

func (memR *Reactor) sendTxManifest(peer p2p.Peer, peerID uint16, manifest *protomem.TxManifest) bool {
	msg := &protomem.Message{
		Sum: &protomem.Message_TxManifest{
			TxManifest: cloneTxManifest(manifest),
		},
	}
	txKey, err := types.TxKeyFromBytes(manifest.TxKey)
	if err != nil {
		return false
	}
	if peer.Send(p2p.Envelope{ChannelID: MempoolDataChannel, Message: msg}) {
		memR.mempool.PeerHasTx(peerID, txKey)
		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.TxManifest, txKey[:], schema.Upload)
		return true
	}
	memR.peerScores.RecordSendFailure(peerID)
	return false
}

func (memR *Reactor) sendSeenTx(peer p2p.Peer, peerID uint16, txKey types.TxKey, signer []byte, sequence uint64) bool {
	msg := &protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey:    txKey[:],
				Signer:   signer,
				Sequence: sequence,
			},
		},
	}
	if peer.Send(p2p.Envelope{ChannelID: MempoolDataChannel, Message: msg}) {
		memR.mempool.PeerHasTx(peerID, txKey)
		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.SeenTx, txKey[:], schema.Upload)
		return true
	}
	memR.peerScores.RecordSendFailure(peerID)
	return false
}

func (memR *Reactor) pushOptimisticChunks(peer p2p.Peer, peerID uint16, local *localLargeTx) {
	if memR.opts.LargeTxOptimisticPushChunks <= 0 || local == nil {
		return
	}
	limit := memR.opts.LargeTxOptimisticPushChunks
	if limit > memR.opts.LargeTxMaxInflightChunksPerPeer {
		limit = memR.opts.LargeTxMaxInflightChunksPerPeer
	}
	if limit > len(local.chunks) {
		limit = len(local.chunks)
	}
	for i := 0; i < limit; i++ {
		memR.sendTxChunk(peer, peerID, local, uint32(i))
	}
}

func (memR *Reactor) receiveTxManifest(msg *protomem.TxManifest, peer p2p.Peer) {
	txKey, err := memR.validateTxManifest(msg)
	if err != nil {
		memR.Logger.Error("peer sent invalid TxManifest", "err", err)
		memR.peerScores.RecordInvalidChunk(memR.ids.GetIDForPeer(peer.ID()))
		memR.Switch.StopPeerForError(peer, err, memR.String())
		return
	}

	peerID := memR.ids.GetIDForPeer(peer.ID())
	memR.mempool.PeerHasTx(peerID, txKey)
	schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.TxManifest, txKey[:], schema.Download)

	if memR.mempool.Has(txKey) {
		memR.Logger.Trace("received a tx manifest for a tx we already have", "txKey", txKey)
		return
	}
	if rejected, _, _ := memR.mempool.WasRecentlyRejected(txKey); rejected {
		return
	}
	if memR.mempool.WasRecentlyEvicted(txKey) {
		return
	}

	waitForSequence, drop := memR.largeManifestSequenceState(msg, txKey, peer)
	if drop {
		return
	}

	created, err := memR.upsertReconstructionSession(txKey, msg, peerID, waitForSequence)
	if err != nil {
		memR.Logger.Error("could not track TxManifest", "txKey", txKey, "err", err)
		memR.Switch.StopPeerForError(peer, err, memR.String())
		return
	}
	if created {
		memR.Logger.Trace("created large tx reconstruction session", "txKey", txKey, "chunks", msg.ChunkCount)
	}
	if !waitForSequence {
		if created {
			memR.markOptimisticChunksInflight(txKey, peerID)
		} else {
			memR.hedgeChunkRequests(txKey, peerID)
		}
		memR.scheduleChunkRequests(txKey)
	}
}

func (memR *Reactor) validateTxManifest(msg *protomem.TxManifest) (types.TxKey, error) {
	if msg == nil {
		return types.TxKey{}, fmt.Errorf("%w: nil manifest", errInvalidTxManifest)
	}
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		return types.TxKey{}, err
	}
	if len(msg.Signer) > maxSignerLength {
		return types.TxKey{}, errSignerTooLong
	}
	if msg.TxSize == 0 {
		return types.TxKey{}, fmt.Errorf("%w: empty tx", errInvalidTxManifest)
	}
	if int(msg.TxSize) > memR.opts.MaxTxSize {
		return types.TxKey{}, fmt.Errorf("%w: tx size %d exceeds max %d", errInvalidTxManifest, msg.TxSize, memR.opts.MaxTxSize)
	}
	if memR.opts.LargeTxThreshold > 0 && int(msg.TxSize) < memR.opts.LargeTxThreshold {
		return types.TxKey{}, fmt.Errorf("%w: tx size %d below local large tx threshold %d", errInvalidTxManifest, msg.TxSize, memR.opts.LargeTxThreshold)
	}
	if msg.ChunkSize == 0 {
		return types.TxKey{}, fmt.Errorf("%w: chunk size must be positive", errInvalidTxManifest)
	}
	if int(msg.ChunkSize) > memR.opts.LargeTxChunkSize {
		return types.TxKey{}, fmt.Errorf("%w: chunk size %d exceeds local max %d", errInvalidTxManifest, msg.ChunkSize, memR.opts.LargeTxChunkSize)
	}
	expectedChunks := (uint64(msg.TxSize) + uint64(msg.ChunkSize) - 1) / uint64(msg.ChunkSize)
	if expectedChunks == 0 || expectedChunks > uint64(memR.opts.MaxTxSize) {
		return types.TxKey{}, fmt.Errorf("%w: bad chunk count", errInvalidTxManifest)
	}
	maxLocalChunks := (uint64(msg.TxSize) + uint64(memR.opts.LargeTxChunkSize) - 1) / uint64(memR.opts.LargeTxChunkSize)
	if expectedChunks > maxLocalChunks {
		return types.TxKey{}, fmt.Errorf("%w: chunk count %d exceeds local bound %d", errInvalidTxManifest, expectedChunks, maxLocalChunks)
	}
	if uint64(msg.ChunkCount) != expectedChunks {
		return types.TxKey{}, fmt.Errorf("%w: expected %d chunks, got %d", errInvalidTxManifest, expectedChunks, msg.ChunkCount)
	}
	if len(msg.ChunkHashes) != int(msg.ChunkCount) {
		return types.TxKey{}, fmt.Errorf("%w: expected %d chunk hashes, got %d", errInvalidTxManifest, msg.ChunkCount, len(msg.ChunkHashes))
	}
	for i, hash := range msg.ChunkHashes {
		if len(hash) != tmhash.Size {
			return types.TxKey{}, fmt.Errorf("%w: chunk hash %d has size %d", errInvalidTxManifest, i, len(hash))
		}
	}
	return txKey, nil
}

func (memR *Reactor) largeManifestSequenceState(msg *protomem.TxManifest, txKey types.TxKey, peer p2p.Peer) (waitForSequence bool, drop bool) {
	expectedSeq, haveExpected := memR.querySequenceFromApplication(msg.Signer)
	switch {
	case len(msg.Signer) == 0 || msg.Sequence == 0:
		schema.WriteMempoolPeerStateWithSeq(memR.traceClient, string(peer.ID()), schema.MissingSequence, txKey[:], schema.Download, msg.Signer, msg.Sequence)
		return false, false
	case !haveExpected:
		schema.WriteMempoolPeerStateWithSeq(memR.traceClient, string(peer.ID()), schema.MissingSequence, txKey[:], schema.Download, msg.Signer, msg.Sequence)
		return false, false
	case msg.Sequence == expectedSeq:
		return false, false
	case msg.Sequence > expectedSeq:
		if msg.Sequence > expectedSeq+maxReceivedBufferSize {
			return false, true
		}
		return true, false
	default:
		memR.Logger.Debug(
			"dropping TxManifest due to lower than expected sequence",
			"txKey", txKey,
			"sequence", msg.Sequence,
			"expectedSequence", expectedSeq,
		)
		return false, true
	}
}

func (memR *Reactor) upsertReconstructionSession(txKey types.TxKey, manifest *protomem.TxManifest, peerID uint16, waitForSequence bool) (bool, error) {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	if session := memR.reconstructions[txKey]; session != nil {
		if !manifestsEqual(session.manifest, manifest) {
			return false, fmt.Errorf("%w: conflicting manifest for %X", errInvalidTxManifest, txKey)
		}
		session.addSource(peerID)
		if !waitForSequence {
			session.waitingForSequence = false
		}
		return false, nil
	}

	if memR.reconstructionBytesLocked()+int64(manifest.TxSize) > memR.mempool.config.MaxTxsBytes {
		return false, fmt.Errorf("%w: reconstruction memory limit reached", errInvalidTxManifest)
	}
	deadline := time.AfterFunc(memR.opts.LargeTxReconstructionTimeout, func() {
		memR.onReconstructionTimeout(txKey)
	})
	session := newReconstructionSession(manifest, peerID, deadline)
	session.waitingForSequence = waitForSequence
	memR.reconstructions[txKey] = session
	return true, nil
}

func (memR *Reactor) reconstructionBytesLocked() int64 {
	var total int64
	for _, session := range memR.reconstructions {
		total += int64(session.manifest.TxSize)
	}
	return total
}

type indexedLargeTxChunk struct {
	index uint32
	data  []byte
}

func (memR *Reactor) reconstructionChunksForWant(txKey types.TxKey, indexes []uint32) []indexedLargeTxChunk {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	session := memR.reconstructions[txKey]
	if session == nil || session.waitingForSequence {
		return nil
	}

	chunks := make([]indexedLargeTxChunk, 0, len(indexes))
	for _, index := range indexes {
		if int(index) >= len(session.chunks) || !session.received[index] {
			continue
		}
		chunks = append(chunks, indexedLargeTxChunk{index: index, data: session.chunks[index]})
	}
	return chunks
}

func (memR *Reactor) receiveWantChunk(msg *protomem.WantChunk, peer p2p.Peer) {
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Logger.Error("peer sent WantChunk with incorrect tx key", "err", err)
		memR.Switch.StopPeerForError(peer, err, memR.String())
		return
	}
	if len(msg.Indexes) == 0 {
		return
	}
	if memR.opts.ListenOnly {
		return
	}

	peerID := memR.ids.GetIDForPeer(peer.ID())
	schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.WantChunk, txKey[:], schema.Download)
	indexes := msg.Indexes
	if len(indexes) > memR.opts.LargeTxMaxInflightChunksPerPeer {
		indexes = indexes[:memR.opts.LargeTxMaxInflightChunksPerPeer]
	}

	local, ok := memR.getOrBuildLocalLargeTx(txKey)
	if ok {
		for _, index := range indexes {
			if int(index) >= len(local.chunks) {
				err := fmt.Errorf("%w: requested chunk %d out of range", errInvalidTxChunk, index)
				memR.Switch.StopPeerForError(peer, err, memR.String())
				return
			}
			memR.sendTxChunk(peer, peerID, local, index)
		}
		return
	}

	for _, chunk := range memR.reconstructionChunksForWant(txKey, indexes) {
		memR.sendTxChunkData(peer, peerID, txKey[:], chunk.index, chunk.data)
	}
}

func (memR *Reactor) sendTxChunk(peer p2p.Peer, peerID uint16, local *localLargeTx, index uint32) bool {
	if int(index) >= len(local.chunks) {
		return false
	}
	return memR.sendTxChunkData(peer, peerID, local.manifest.TxKey, index, local.chunks[index])
}

func (memR *Reactor) sendTxChunkData(peer p2p.Peer, peerID uint16, txKeyBytes []byte, index uint32, data []byte) bool {
	txKey, err := types.TxKeyFromBytes(txKeyBytes)
	if err != nil {
		return false
	}
	msg := &protomem.Message{
		Sum: &protomem.Message_TxChunk{
			TxChunk: &protomem.TxChunk{
				TxKey: txKeyBytes,
				Index: index,
				Data:  data,
			},
		},
	}
	if peer.TrySend(p2p.Envelope{ChannelID: MempoolChunkChannel, Message: msg}) {
		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.TxChunk, txKey[:], schema.Upload)
		schema.WriteMempoolTx(memR.traceClient, string(peer.ID()), txKey[:], len(data), schema.Upload)
		return true
	}
	memR.peerScores.RecordSendFailure(peerID)
	return false
}

func (memR *Reactor) receiveTxChunk(msg *protomem.TxChunk, peer p2p.Peer) {
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Logger.Error("peer sent TxChunk with incorrect tx key", "err", err)
		memR.Switch.StopPeerForError(peer, err, memR.String())
		return
	}
	peerID := memR.ids.GetIDForPeer(peer.ID())
	tx, err := memR.acceptTxChunk(txKey, msg, peerID)
	if err != nil {
		memR.Logger.Error("peer sent invalid TxChunk", "txKey", txKey, "index", msg.Index, "err", err)
		memR.peerScores.RecordInvalidChunk(peerID)
		memR.Switch.StopPeerForError(peer, err, memR.String())
		return
	}
	schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.TxChunk, txKey[:], schema.Download)
	if tx == nil {
		memR.hedgeChunkRequestsFromIdleSources(txKey)
		memR.scheduleChunkRequests(txKey)
		return
	}

	txInfo := mempool.TxInfo{SenderID: peerID, SenderP2PID: peer.ID()}
	memR.peerScores.RecordReconstruction(peerID)
	memR.processReconstructedLargeTx(tx, txKey, txInfo, string(peer.ID()))
}

func (memR *Reactor) acceptTxChunk(txKey types.TxKey, msg *protomem.TxChunk, peerID uint16) (types.Tx, error) {
	var (
		recordPeer    uint16
		recordBytes   int
		recordLatency time.Duration
		completed     types.Tx
	)

	memR.largeMu.Lock()
	session := memR.reconstructions[txKey]
	if session == nil {
		memR.largeMu.Unlock()
		return nil, nil
	}
	session.addSource(peerID)
	if msg.Index >= session.manifest.ChunkCount {
		memR.largeMu.Unlock()
		return nil, fmt.Errorf("%w: chunk index %d out of range", errInvalidTxChunk, msg.Index)
	}
	expectedLen := expectedChunkLength(session.manifest, msg.Index)
	if expectedLen <= 0 || len(msg.Data) != expectedLen {
		memR.largeMu.Unlock()
		return nil, fmt.Errorf("%w: chunk %d length %d expected %d", errInvalidTxChunk, msg.Index, len(msg.Data), expectedLen)
	}
	if !bytes.Equal(tmhash.Sum(msg.Data), session.manifest.ChunkHashes[msg.Index]) {
		memR.largeMu.Unlock()
		return nil, fmt.Errorf("%w: chunk %d hash mismatch", errInvalidTxChunk, msg.Index)
	}

	requestedPeer, sentAt, wasRequested := session.clearInflight(msg.Index)
	if session.markReceived(msg.Index, msg.Data, peerID) {
		recordPeer = peerID
		if wasRequested && requestedPeer == peerID {
			recordLatency = time.Since(sentAt)
		}
		recordBytes = len(msg.Data)
	}

	if session.complete() {
		if session.waitingForSequence {
			memR.largeMu.Unlock()
			if recordPeer != 0 {
				memR.peerScores.RecordChunk(recordPeer, recordBytes, recordLatency)
			}
			return nil, nil
		}
		tx := session.reconstruct()
		if len(tx) != int(session.manifest.TxSize) {
			memR.largeMu.Unlock()
			return nil, fmt.Errorf("%w: reconstructed tx size mismatch", errInvalidTxChunk)
		}
		if reconstructedKey := tx.Key(); reconstructedKey != txKey {
			memR.largeMu.Unlock()
			return nil, fmt.Errorf("%w: reconstructed tx key mismatch", errInvalidTxChunk)
		}
		memR.largeTxs[txKey] = session.toLocalLargeTx()
		session.stop()
		delete(memR.reconstructions, txKey)
		completed = tx
	}
	memR.largeMu.Unlock()

	if recordPeer != 0 {
		memR.peerScores.RecordChunk(recordPeer, recordBytes, recordLatency)
	}
	return completed, nil
}

func (memR *Reactor) scheduleChunkRequests(txKey types.TxKey) {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()
	memR.scheduleChunkRequestsLocked(txKey)
}

func (memR *Reactor) markOptimisticChunksInflight(txKey types.TxKey, peerID uint16) {
	if memR.opts.LargeTxOptimisticPushChunks <= 0 || peerID == 0 {
		return
	}

	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	session := memR.reconstructions[txKey]
	if session == nil || session.waitingForSequence {
		return
	}
	limit := memR.opts.LargeTxOptimisticPushChunks
	if limit > memR.opts.LargeTxMaxInflightChunksPerPeer {
		limit = memR.opts.LargeTxMaxInflightChunksPerPeer
	}
	if limit > int(session.manifest.ChunkCount) {
		limit = int(session.manifest.ChunkCount)
	}

	now := time.Now().UTC()
	for i := 0; i < limit; i++ {
		index := uint32(i)
		if session.received[index] {
			continue
		}
		if _, ok := session.inflight[index]; ok {
			continue
		}
		chunkIndex := index
		timer := time.AfterFunc(memR.opts.LargeTxChunkTimeout, func() {
			memR.onChunkRequestTimeout(txKey, chunkIndex, peerID)
		})
		session.markInflight(chunkIndex, peerID, now, timer)
	}
}

func (memR *Reactor) hedgeChunkRequests(txKey types.TxKey, peerID uint16) {
	if peerID == 0 || memR.opts.LargeTxRequestParallelism <= 1 {
		return
	}

	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	session := memR.reconstructions[txKey]
	if session == nil || session.waitingForSequence || session.complete() {
		return
	}
	if session.inflightForPeer(peerID) > 0 {
		return
	}
	if session.activeInflightPeerCount() >= memR.opts.LargeTxRequestParallelism {
		return
	}

	peer := memR.ids.GetPeer(peerID)
	if peer == nil {
		session.removeSource(peerID)
		return
	}

	limit := memR.opts.LargeTxMaxInflightChunksPerPeer / 2
	if limit <= 0 {
		limit = 1
	}
	now := time.Now().UTC()
	minAge := memR.opts.LargeTxChunkTimeout / 2
	indexes := session.hedgeableInflightIndexes(peerID, limit, minAge, now)
	if len(indexes) == 0 {
		return
	}

	msg := &protomem.Message{
		Sum: &protomem.Message_WantChunk{
			WantChunk: &protomem.WantChunk{
				TxKey:   session.manifest.TxKey,
				Indexes: indexes,
			},
		},
	}
	if !peer.TrySend(p2p.Envelope{ChannelID: MempoolWantsChannel, Message: msg}) {
		memR.peerScores.RecordSendFailure(peerID)
		return
	}

	for _, index := range indexes {
		index := index
		timer := time.AfterFunc(memR.opts.LargeTxChunkTimeout, func() {
			memR.onChunkRequestTimeout(txKey, index, peerID)
		})
		session.reassignInflight(index, peerID, now, timer)
	}
	memR.mempool.metrics.RerequestedTxs.Add(float64(len(indexes)))
	schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.WantChunk, txKey[:], schema.Upload)
}

func (memR *Reactor) hedgeChunkRequestsFromIdleSources(txKey types.TxKey) {
	if memR.opts.LargeTxRequestParallelism <= 1 {
		return
	}

	memR.largeMu.Lock()
	session := memR.reconstructions[txKey]
	if session == nil || session.waitingForSequence || session.complete() ||
		session.activeInflightPeerCount() >= memR.opts.LargeTxRequestParallelism {
		memR.largeMu.Unlock()
		return
	}
	peerIDs := session.sourceIDs()
	memR.largeMu.Unlock()

	sort.SliceStable(peerIDs, func(i, j int) bool {
		left := memR.peerScores.Score(peerIDs[i])
		right := memR.peerScores.Score(peerIDs[j])
		if left == right {
			return peerIDs[i] < peerIDs[j]
		}
		return left > right
	})
	for _, peerID := range peerIDs {
		memR.hedgeChunkRequests(txKey, peerID)
	}
}

func (memR *Reactor) scheduleChunkRequestsLocked(txKey types.TxKey) {
	session := memR.reconstructions[txKey]
	if session == nil || session.waitingForSequence || session.complete() {
		return
	}
	peerIDs := session.sourceIDs()
	sort.SliceStable(peerIDs, func(i, j int) bool {
		left := memR.peerScores.Score(peerIDs[i])
		right := memR.peerScores.Score(peerIDs[j])
		if left == right {
			return peerIDs[i] < peerIDs[j]
		}
		return left > right
	})

	activePeers := session.activeInflightPeerCount()
	for _, peerID := range peerIDs {
		peer := memR.ids.GetPeer(peerID)
		if peer == nil {
			session.removeSource(peerID)
			continue
		}
		currentInflight := session.inflightForPeer(peerID)
		if currentInflight == 0 && activePeers >= memR.opts.LargeTxRequestParallelism {
			continue
		}
		quota := memR.opts.LargeTxMaxInflightChunksPerPeer - currentInflight
		if quota <= 0 {
			continue
		}
		indexes := session.missingIndexes(quota)
		if len(indexes) == 0 {
			return
		}

		msg := &protomem.Message{
			Sum: &protomem.Message_WantChunk{
				WantChunk: &protomem.WantChunk{
					TxKey:   session.manifest.TxKey,
					Indexes: indexes,
				},
			},
		}
		if !peer.TrySend(p2p.Envelope{ChannelID: MempoolWantsChannel, Message: msg}) {
			memR.peerScores.RecordSendFailure(peerID)
			continue
		}

		now := time.Now().UTC()
		for _, index := range indexes {
			index := index
			timer := time.AfterFunc(memR.opts.LargeTxChunkTimeout, func() {
				memR.onChunkRequestTimeout(txKey, index, peerID)
			})
			session.markInflight(index, peerID, now, timer)
		}
		memR.mempool.metrics.RequestedTxs.Add(float64(len(indexes)))
		schema.WriteMempoolPeerState(memR.traceClient, string(peer.ID()), schema.WantChunk, txKey[:], schema.Upload)
		if currentInflight == 0 {
			activePeers++
		}
	}
}

func (memR *Reactor) onChunkRequestTimeout(txKey types.TxKey, index uint32, peerID uint16) {
	shouldSchedule := false
	memR.largeMu.Lock()
	if session := memR.reconstructions[txKey]; session != nil {
		shouldSchedule = session.timeoutInflight(index, peerID)
	}
	memR.largeMu.Unlock()

	if shouldSchedule {
		memR.peerScores.RecordTimeout(peerID)
		memR.mempool.metrics.RerequestedTxs.Add(1)
		memR.scheduleChunkRequests(txKey)
	}
}

func (memR *Reactor) onReconstructionTimeout(txKey types.TxKey) {
	memR.largeMu.Lock()
	session := memR.reconstructions[txKey]
	if session != nil {
		session.stop()
		delete(memR.reconstructions, txKey)
	}
	memR.largeMu.Unlock()
	if session != nil {
		memR.Logger.Debug("large tx reconstruction timed out", "txKey", txKey, "chunks", session.manifest.ChunkCount)
	}
}

func (memR *Reactor) removeLargeTxPeer(peerID uint16) {
	var reschedule []types.TxKey
	memR.largeMu.Lock()
	for txKey, session := range memR.reconstructions {
		if _, ok := session.sources[peerID]; !ok {
			continue
		}
		session.removeSource(peerID)
		reschedule = append(reschedule, txKey)
	}
	memR.largeMu.Unlock()

	for _, txKey := range reschedule {
		memR.scheduleChunkRequests(txKey)
	}
}

func (memR *Reactor) stopLargeTxReconstructionSessions() {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()
	for txKey, session := range memR.reconstructions {
		session.stop()
		delete(memR.reconstructions, txKey)
	}
}

func (memR *Reactor) pendingLargeManifestSigners() [][]byte {
	memR.largeMu.Lock()
	defer memR.largeMu.Unlock()

	seen := make(map[string][]byte)
	for _, session := range memR.reconstructions {
		if !session.waitingForSequence || len(session.manifest.Signer) == 0 {
			continue
		}
		seen[string(session.manifest.Signer)] = append([]byte(nil), session.manifest.Signer...)
	}

	out := make([][]byte, 0, len(seen))
	for _, signer := range seen {
		out = append(out, signer)
	}
	return out
}

func (memR *Reactor) processPendingLargeManifestsForSigner(signer []byte) {
	if len(signer) == 0 {
		return
	}
	expectedSeq, haveExpected := memR.querySequenceFromApplication(signer)
	if !haveExpected {
		return
	}

	type completedLargeTx struct {
		tx     types.Tx
		txKey  types.TxKey
		peerID uint16
	}

	var (
		ready     []types.TxKey
		completed []completedLargeTx
	)
	memR.largeMu.Lock()
	for txKey, session := range memR.reconstructions {
		if !session.waitingForSequence || !bytes.Equal(session.manifest.Signer, signer) {
			continue
		}
		switch {
		case session.manifest.Sequence < expectedSeq:
			session.stop()
			delete(memR.reconstructions, txKey)
		case session.manifest.Sequence == expectedSeq:
			session.waitingForSequence = false
			if session.complete() {
				tx := session.reconstruct()
				if len(tx) != int(session.manifest.TxSize) || tx.Key() != txKey {
					session.stop()
					delete(memR.reconstructions, txKey)
					continue
				}
				peerID := session.firstSourcePeer()
				memR.largeTxs[txKey] = session.toLocalLargeTx()
				session.stop()
				delete(memR.reconstructions, txKey)
				completed = append(completed, completedLargeTx{tx: tx, txKey: txKey, peerID: peerID})
			} else {
				ready = append(ready, txKey)
			}
		}
	}
	memR.largeMu.Unlock()

	for _, item := range completed {
		peer := memR.ids.GetPeer(item.peerID)
		txInfo := mempool.TxInfo{SenderID: item.peerID}
		peerIDStr := ""
		if peer != nil {
			txInfo.SenderP2PID = peer.ID()
			peerIDStr = string(peer.ID())
		}
		memR.processReconstructedLargeTx(item.tx, item.txKey, txInfo, peerIDStr)
	}
	for _, txKey := range ready {
		memR.scheduleChunkRequests(txKey)
	}
}

func (memR *Reactor) processReconstructedLargeTx(tx types.Tx, txKey types.TxKey, txInfo mempool.TxInfo, peerID string) {
	cachedTx := tx.ToCachedTx()
	rsp, err := memR.tryAddNewTx(cachedTx, txKey, txInfo, peerID)
	if err == nil || errors.Is(err, ErrTxInMempool) {
		memR.pendingSeen.remove(txKey)
	}
	if err != nil {
		memR.discardLocalLargeTx(txKey)
		return
	}
	if rsp.Code != 0 {
		memR.discardLocalLargeTx(txKey)
	}

	if len(rsp.Address) > 0 {
		memR.processReceivedBuffer(rsp.Address)
		memR.processPendingSeenForSigner(rsp.Address)
		memR.processPendingLargeManifestsForSigner(rsp.Address)
	}

	if !memR.opts.ListenOnly && rsp.Code == 0 {
		memR.broadcastAcceptedTx(cachedTx, txKey, memR.mempool.Height(), rsp.Address, rsp.Sequence, rsp.Priority)
	}
}

func peerSupportsChannel(peer p2p.Peer, channelID byte) bool {
	if peer == nil {
		return false
	}
	nodeInfo, ok := peer.NodeInfo().(p2p.DefaultNodeInfo)
	if !ok {
		return false
	}
	for _, channel := range nodeInfo.Channels {
		if channel == channelID {
			return true
		}
	}
	return false
}
