package cat

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtbits "github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat/chunked"
	"github.com/cometbft/cometbft/p2p"
	cmtcrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	protobits "github.com/cometbft/cometbft/proto/tendermint/libs/bits"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// ADR-013 protocol implementation. Three roles, per-tx:
//   - Default RPC (own RPC, env RPC unset): SeenLargeTx + HaveTxChunks(partial)
//     bundled to 15 random peers; serve TxChunks on Want.
//   - Push RPC (own RPC, env RPC=1): SeenLargeTx + HaveTxChunks(full) to all
//     peers + push every chunk to 6 round-robin peers; serve TxChunks on Want.
//   - Intermediate (gossip-admitted): forward SeenLargeTx fanout-15; on chunk
//     receipt announce HaveTxChunks to all peers not known to hold; on
//     reconstruction send full-bitmap HaveTxChunks to all peers not known to
//     hold all chunks; serve TxChunks on Want.

const (
	// DefaultRPCAnnounceFanout: number of random peers a Default RPC origin
	// (and any Intermediate forwarding SeenLargeTx) targets.
	DefaultRPCAnnounceFanout = 15

	// DefaultHaveTxChunksRedundancy: each chunk is announced via HaveTxChunks
	// to this many peers from the fanout set on Default RPC origination.
	DefaultHaveTxChunksRedundancy = 2

	// DefaultPushRPCChunkRedundancy: minimum number of peers each chunk is
	// pushed to from a Push RPC origin. Actual redundancy is adaptive (see
	// adaptivePushRedundancy): for small blobs the redundancy climbs toward
	// the full peer set so every peer receives every chunk directly; for
	// large blobs it stays near this floor to keep origin upload bounded.
	DefaultPushRPCChunkRedundancy = 6

	// DefaultMaxPushBytes caps the total bytes a Push RPC origin uploads per
	// tx via proactive push: total_push_bytes ≈ redundancy × tx_size, so the
	// adaptive redundancy is floor(MaxPushBytes / tx_size), clamped above by
	// the connected-peer count and below by DefaultPushRPCChunkRedundancy.
	// 100 MiB lets a 1 MB blob be pushed to every peer in a ~100-peer mesh
	// while keeping a 32 MB blob at the 6× floor.
	DefaultMaxPushBytes = 200 << 20

	// DefaultPerPeerInflightCap: max chunks in-flight to a single peer.
	DefaultPerPeerInflightCap = 16

	// DefaultChunksBatchSize: max chunks per WantTxChunks bitmap-set and per
	// TxChunks response message.
	DefaultChunksBatchSize = 16

	// DefaultHaveCoalesceWindow is how long we delay HaveTxChunks gossip after
	// installing chunks, so multiple TxChunks batches arriving in quick
	// succession produce a single merged HaveTxChunks per peer instead of
	// one per batch. Cuts control-plane volume by ~5–10× under sustained
	// throughput at the cost of one window of discovery latency.
	DefaultHaveCoalesceWindow = 25 * time.Millisecond

	// maxChunkedLeafHashes caps the leaf_hashes carried in a SeenLargeTx.
	// 2K parts for the largest expected blob fit well under this.
	maxChunkedLeafHashes = 4096
)

var (
	errChunkedBadKey        = errors.New("chunked: malformed tx_key")
	errChunkedBadRoot       = errors.New("chunked: malformed parts_root")
	errChunkedBadLeafCount  = errors.New("chunked: leaf_hashes count != num_parts")
	errChunkedBadNumParts   = errors.New("chunked: invalid num_parts")
	errChunkedBadLastLength = errors.New("chunked: invalid last_length")
	errChunkedSignerLen     = errors.New("chunked: signer too long")
	errChunkedBatchTooLarge = errors.New("chunked: batch size exceeds cap")
)

// ---------------------------------------------------------------------------
// Inbound handlers
// ---------------------------------------------------------------------------

// handleSeenLargeTx processes an incoming SeenLargeTx announce. Validates the
// announce, inserts a new PartsState if we don't already track this tx, and
// forwards the announce to a random fanout-15 of other peers.
func (memR *Reactor) handleSeenLargeTx(src p2p.Peer, msg *protomem.SeenLargeTx) {
	if err := validateSeenLargeTx(msg); err != nil {
		memR.Logger.Error("malformed SeenLargeTx", "err", err, "src", src)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	schema.WriteMempoolChunkedMessage(memR.traceClient, string(src.ID()),
		schema.ChunkedSeenLargeTx, schema.Download, msg.TxKey,
		int(msg.NumParts), msg.Size(), "")

	if memR.mempool.Has(txKey) {
		memR.mempool.PeerHasTx(peerID, txKey)
		return
	}

	if existing := memR.chunkedStore.Get(txKey); existing != nil {
		// Already tracking. Nothing to learn from SeenLargeTx alone — wait for
		// HaveTxChunks to know what chunks the sender actually holds.
		return
	}

	params := chunked.InsertParams{
		TxKey:      txKey,
		PartsRoot:  msg.PartsRoot,
		NumParts:   msg.NumParts,
		LastLength: msg.LastLength,
		LeafHashes: msg.LeafHashes,
		OriginPeer: peerID,
		Signer:     msg.Signer,
		Sequence:   msg.Sequence,
	}
	state, err := memR.chunkedStore.Insert(params)
	if err != nil {
		if errors.Is(err, chunked.ErrLeafHashesMismatch) {
			memR.Switch.StopPeerForError(src, err, memR.String())
			return
		}
		memR.Logger.Debug("chunked Insert rejected", "err", err, "tx_key", txKey)
		return
	}
	memR.mempool.PeerHasTx(peerID, txKey)
	memR.forwardSeenLargeTx(state, peerID)
	_ = state
}

// handleHaveTxChunks updates per-peer haves and triggers a Want for any
// requestable chunks (first-announcer-only via cross-peer in-flight dedup).
func (memR *Reactor) handleHaveTxChunks(src p2p.Peer, msg *protomem.HaveTxChunks) {
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	state := memR.chunkedStore.Get(txKey)
	if state == nil {
		return
	}
	if msg.Parts.Bits != int64(state.NumParts) {
		memR.Switch.StopPeerForError(src, errChunkedBadNumParts, memR.String())
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	parts := protoBitArrayToInternal(&msg.Parts, int(state.NumParts))
	schema.WriteMempoolChunkedMessage(memR.traceClient, string(src.ID()),
		schema.ChunkedHaveTxChunks, schema.Download, msg.TxKey,
		len(parts.GetTrueIndices()), msg.Size(), "")
	state.RecordHaves(peerID, parts)
	memR.requestMissingFromPeer(state, peerID, src)
}

// handleWantTxChunks responds with TxChunks for chunks we hold matching the
// requested bitmap. Batched into messages of up to DefaultChunksBatchSize.
func (memR *Reactor) handleWantTxChunks(src p2p.Peer, msg *protomem.WantTxChunks) {
	if memR.opts.ListenOnly {
		return
	}
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	state := memR.chunkedStore.Get(txKey)
	if state == nil {
		return
	}
	if msg.Parts.Bits != int64(state.NumParts) {
		memR.Switch.StopPeerForError(src, errChunkedBadNumParts, memR.String())
		return
	}
	wanted := protoBitArrayToInternal(&msg.Parts, int(state.NumParts))
	schema.WriteMempoolChunkedMessage(memR.traceClient, string(src.ID()),
		schema.ChunkedWantTxChunks, schema.Download, msg.TxKey,
		len(wanted.GetTrueIndices()), msg.Size(), "")
	memR.serveChunks(state, src, wanted)
}

// handleTxChunks verifies and installs each chunk in the batch. Any proof
// failure → disconnect peer immediately. After all chunks in the batch are
// installed, gossip ONE HaveTxChunks per peer announcing the newly-installed
// bits (Fix A — batched gossip to avoid the per-chunk message storm). When
// K-of-2K is hit during the batch, trigger reconstruction.
func (memR *Reactor) handleTxChunks(src p2p.Peer, msg *protomem.TxChunks) {
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	if len(msg.Chunks) > DefaultChunksBatchSize {
		memR.Switch.StopPeerForError(src, errChunkedBatchTooLarge, memR.String())
		return
	}
	state := memR.chunkedStore.Get(txKey)
	if state == nil {
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	payloadBytes := 0
	for i := range msg.Chunks {
		payloadBytes += len(msg.Chunks[i].Data)
	}
	schema.WriteMempoolChunkedMessage(memR.traceClient, string(src.ID()),
		schema.ChunkedTxChunks, schema.Download, msg.TxKey,
		len(msg.Chunks), payloadBytes, "")
	installedThisBatch := cmtbits.NewBitArray(int(state.NumParts))
	triggerReconstruct := false
	for i := range msg.Chunks {
		c := &msg.Chunks[i]
		installed, justCompleted, err := memR.installChunk(state, peerID, c)
		if err != nil {
			memR.Switch.StopPeerForError(src, err, memR.String())
			return
		}
		if installed {
			installedThisBatch.SetIndex(int(c.Index), true)
		}
		if justCompleted {
			triggerReconstruct = true
		}
	}
	// Enqueue HaveTxChunks gossip for coalescing: the haveCoalescer goroutine
	// emits one HaveTxChunks per (txKey, peer) per ~25ms window covering all
	// chunks installed during that window, instead of one HaveTxChunks per
	// TxChunks receipt. Reconstruction is NOT coalesced; we want to admit and
	// announce-full ASAP after crossing K-of-2K.
	if installedThisBatch != nil && len(installedThisBatch.GetTrueIndices()) > 0 {
		memR.haveCoalesce.add(txKey, state.NumParts, installedThisBatch)
	}
	if triggerReconstruct {
		memR.reconstructAndAdmit(state, src)
	}
}

// installChunk verifies a single chunk's Merkle proof and installs it into
// the PartsState. Returns (installed, justCompleted, err): installed is true
// iff the chunk was newly stored; justCompleted is true iff this chunk
// crossed the K-of-2K reconstruction threshold; err is non-nil only on
// protocol violations (bad proof, out-of-range index).
func (memR *Reactor) installChunk(state *chunked.PartsState, peerID uint16, c *protomem.TxChunk) (bool, bool, error) {
	if c.Index >= state.NumParts {
		return false, false, chunked.ErrChunkOutOfRange
	}
	var proof *merkle.Proof
	if state.NumParts > 1 {
		var p merkle.Proof
		p.Total = c.Proof.Total
		p.Index = c.Proof.Index
		p.LeafHash = c.Proof.LeafHash
		p.Aunts = c.Proof.Aunts
		proof = &p
	}
	if err := chunked.VerifyChunk(state.PartsRoot, state.NumParts, c.Index, c.Data, proof); err != nil {
		return false, false, err
	}
	if err := memR.chunkedStore.ChargeChunk(state, len(c.Data)); err != nil {
		// Memory cap breach is our problem, not the peer's.
		memR.Logger.Info("chunked: cap breached, evicting partial",
			"err", err, "tx_key", state.TxKey)
		memR.chunkedStore.Remove(state.TxKey)
		return false, false, nil
	}
	justCompleted, err := state.Install(c.Index, c.Data)
	if err != nil {
		memR.chunkedStore.Release(state.OriginPeer(), int64(len(c.Data)))
		if !errors.Is(err, chunked.ErrChunkAlreadyPresent) {
			memR.Logger.Debug("chunked Install error", "err", err)
		}
		return false, false, nil
	}
	// Record sender as having this chunk for accurate dedup.
	bit := cmtbits.NewBitArray(int(state.NumParts))
	bit.SetIndex(int(c.Index), true)
	state.RecordHaves(peerID, bit)
	return true, justCompleted, nil
}

// ---------------------------------------------------------------------------
// HaveTxChunks coalescer
// ---------------------------------------------------------------------------

// haveCoalescer accumulates per-tx "chunks installed since last flush" bitmaps
// and emits one HaveTxChunks per peer per flush window. Without coalescing,
// every TxChunks receipt fires one HaveTxChunks per peer; under sustained
// throughput that dominates control-plane traffic. The window length is
// DefaultHaveCoalesceWindow (default 25 ms).
type haveCoalescer struct {
	mu       sync.Mutex
	pending  map[types.TxKey]*cmtbits.BitArray
	numParts map[types.TxKey]uint32
}

func newHaveCoalescer() *haveCoalescer {
	return &haveCoalescer{
		pending:  make(map[types.TxKey]*cmtbits.BitArray),
		numParts: make(map[types.TxKey]uint32),
	}
}

// add merges the given bits into the pending accumulator for txKey.
func (c *haveCoalescer) add(txKey types.TxKey, numParts uint32, installed *cmtbits.BitArray) {
	if installed == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.pending[txKey]
	if !ok || cur == nil {
		cur = cmtbits.NewBitArray(int(numParts))
		c.pending[txKey] = cur
		c.numParts[txKey] = numParts
	}
	cur.AddBitArray(installed)
}

// drain returns the accumulated state and resets the coalescer.
func (c *haveCoalescer) drain() (map[types.TxKey]*cmtbits.BitArray, map[types.TxKey]uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, n := c.pending, c.numParts
	c.pending = make(map[types.TxKey]*cmtbits.BitArray)
	c.numParts = make(map[types.TxKey]uint32)
	return p, n
}

// runHaveCoalescer ticks every DefaultHaveCoalesceWindow and emits one
// HaveTxChunks per (txKey, peer) covering all chunks installed during the
// window. Started from Reactor.OnStart.
func (memR *Reactor) runHaveCoalescer() {
	tick := time.NewTicker(DefaultHaveCoalesceWindow)
	defer tick.Stop()
	for {
		select {
		case <-memR.Quit():
			return
		case <-tick.C:
			memR.flushHaveCoalescer()
		}
	}
}

func (memR *Reactor) flushHaveCoalescer() {
	if memR.haveCoalesce == nil {
		return
	}
	pending, _ := memR.haveCoalesce.drain()
	if len(pending) == 0 {
		return
	}
	for txKey, bits := range pending {
		state := memR.chunkedStore.Get(txKey)
		if state == nil {
			continue
		}
		memR.gossipBatchOnReceipt(state, bits)
	}
}

// ---------------------------------------------------------------------------
// Gossip helpers (HaveTxChunks broadcast)
// ---------------------------------------------------------------------------

// gossipBatchOnReceipt announces every chunk in `installed` to every peer
// that does not already know we hold it. For each peer the gossip is sent
// as ONE HaveTxChunks with a bitmap of the new chunks (Fix A — batched
// gossip, one message per peer per TxChunks receipt instead of one message
// per chunk per peer). Uses blocking Send so the announce never silently
// drops to a backlogged peer (Fix C — control-plane reliability).
func (memR *Reactor) gossipBatchOnReceipt(state *chunked.PartsState, installed *cmtbits.BitArray) {
	if memR.opts.ListenOnly || installed == nil {
		return
	}
	installedIdx := installed.GetTrueIndices()
	if len(installedIdx) == 0 {
		return
	}
	for id, peer := range memR.ids.GetAll() {
		if peer == nil {
			continue
		}
		toTell := cmtbits.NewBitArray(int(state.NumParts))
		any := false
		for _, idx := range installedIdx {
			if state.PeerKnowsChunk(id, uint32(idx)) {
				continue
			}
			toTell.SetIndex(idx, true)
			any = true
		}
		if !any {
			continue
		}
		if peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message: &protomem.HaveTxChunks{
				TxKey: state.TxKey[:],
				Parts: *toTell.ToProto(),
			},
		}) {
			state.RecordNotified(id, toTell)
		}
	}
}

// broadcastFullHaveAfterAdmit announces a full-bitmap HaveTxChunks to every
// peer that we don't yet believe holds all chunks. Used after reconstruction.
// Uses blocking Send (Fix C) — this is the canonical "I have everything"
// announce; under no circumstances should it silently drop because of a
// briefly-full send queue.
func (memR *Reactor) broadcastFullHaveAfterAdmit(state *chunked.PartsState) {
	if memR.opts.ListenOnly {
		return
	}
	full := cmtbits.NewBitArray(int(state.NumParts))
	full.Fill()
	msg := &protomem.HaveTxChunks{
		TxKey: state.TxKey[:],
		Parts: *full.ToProto(),
	}
	for id, peer := range memR.ids.GetAll() {
		if peer == nil {
			continue
		}
		if state.PeerHasAll(id) {
			continue
		}
		if peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   msg,
		}) {
			state.RecordNotified(id, full)
		}
	}
}

// forwardSeenLargeTx re-broadcasts a SeenLargeTx to a random fanout of peers
// excluding the sender and any peer already recorded in seenByPeersSet.
func (memR *Reactor) forwardSeenLargeTx(state *chunked.PartsState, sender uint16) {
	if memR.opts.ListenOnly {
		return
	}
	peers := memR.shuffleCapablePeers()
	if len(peers) == 0 {
		return
	}
	msg := buildSeenLargeTxMsg(state)
	sent := 0
	for _, cp := range peers {
		if sent >= DefaultRPCAnnounceFanout {
			break
		}
		if cp.id == sender {
			continue
		}
		if memR.mempool.seenByPeersSet.Has(state.TxKey, cp.id) {
			continue
		}
		if cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   msg,
		}) {
			memR.mempool.PeerHasTx(cp.id, state.TxKey)
			sent++
		}
	}
}

// ---------------------------------------------------------------------------
// Want / serve
// ---------------------------------------------------------------------------

// requestMissingFromPeer sends a WantTxChunks for chunks the peer advertises
// that we still need AND aren't in-flight to any peer. First-announcer-only.
// Uses blocking Send (Fix C): with no Want retry, a silently-dropped Want
// would strand the chunk in our in-flight bitmap until peer disconnect.
func (memR *Reactor) requestMissingFromPeer(state *chunked.PartsState, peerID uint16, peer p2p.Peer) {
	if peer == nil {
		return
	}
	wanted := state.RequestableFromPeer(peerID, DefaultPerPeerInflightCap, DefaultChunksBatchSize)
	if wanted == nil {
		return
	}
	state.MarkInflight(peerID, wanted)
	_ = peer.Send(p2p.Envelope{
		ChannelID: MempoolChunkChannel,
		Message: &protomem.WantTxChunks{
			TxKey: state.TxKey[:],
			Parts: *wanted.ToProto(),
		},
	})
}

// serveChunks bundles up to DefaultChunksBatchSize TxChunk entries per
// TxChunks response message, replying with everything we hold from `wanted`.
func (memR *Reactor) serveChunks(state *chunked.PartsState, peer p2p.Peer, wanted *cmtbits.BitArray) {
	if wanted == nil {
		return
	}
	indices := wanted.GetTrueIndices()
	if len(indices) == 0 {
		return
	}
	batch := make([]protomem.TxChunk, 0, DefaultChunksBatchSize)
	for _, idx := range indices {
		if uint32(idx) >= state.NumParts {
			continue
		}
		data, proofProto, ok := chunkPayloadFor(state, uint32(idx))
		if !ok {
			continue
		}
		batch = append(batch, protomem.TxChunk{
			Index: uint32(idx),
			Data:  data,
			Proof: proofProto,
		})
		if len(batch) >= DefaultChunksBatchSize {
			memR.sendChunksBatch(peer, state.TxKey, batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		memR.sendChunksBatch(peer, state.TxKey, batch)
	}
}

func (memR *Reactor) sendChunksBatch(peer p2p.Peer, txKey types.TxKey, chunks []protomem.TxChunk) {
	send := make([]protomem.TxChunk, len(chunks))
	copy(send, chunks)
	peer.TrySend(p2p.Envelope{
		ChannelID: MempoolChunkChannel,
		Message: &protomem.TxChunks{
			TxKey:  txKey[:],
			Chunks: send,
		},
	})
}

// ---------------------------------------------------------------------------
// Reconstruction / admission
// ---------------------------------------------------------------------------

func (memR *Reactor) reconstructAndAdmit(state *chunked.PartsState, src p2p.Peer) {
	body, err := memR.chunkedStore.ReconstructAndVerify(state)
	if err != nil {
		memR.Logger.Error("chunked reconstruct failed",
			"err", err, "tx_key", state.TxKey)
		// Bogus body — origin attested to this announce. Don't disconnect src
		// (they just forwarded one chunk); drop the state and move on.
		memR.chunkedStore.Remove(state.TxKey)
		return
	}
	schema.WriteMempoolChunkedMessage(memR.traceClient, string(src.ID()),
		schema.ChunkedReconstructed, schema.Download, state.TxKey[:],
		int(state.NumParts), len(body), "")
	peerID := memR.ids.GetIDForPeer(src.ID())
	cachedTx := types.Tx(body).ToCachedTx()
	txInfo := mempool.TxInfo{SenderID: peerID, SenderP2PID: src.ID()}
	if _, err := memR.tryAddNewTx(cachedTx, state.TxKey, txInfo, string(src.ID())); err != nil {
		if !errors.Is(err, ErrTxInMempool) {
			memR.Logger.Debug("chunked tryAddNewTx failed",
				"err", err, "tx_key", state.TxKey)
		}
		memR.chunkedStore.Remove(state.TxKey)
		return
	}
	memR.broadcastFullHaveAfterAdmit(state)
}

// ---------------------------------------------------------------------------
// Origination (Default RPC / Push RPC)
// ---------------------------------------------------------------------------

// broadcastNewLargeTxDefault is Default RPC origination (Fix B — broadened):
// SeenLargeTx + HaveTxChunks(partial bitmap) bundled to ALL peers. Each chunk
// is announced to DefaultHaveTxChunksRedundancy distinct peers, with the
// 2× redundancy spread across the full peer set (chunk i → peers
// (i*r + 0..r-1) mod numPeers). Every peer immediately learns the tx's
// shape and where it can pull at least one chunk from origin; no peer waits
// on second-hop gossip to discover the tx. Origin's upload load stays
// bounded: each chunk is uploaded ~DefaultHaveTxChunksRedundancy times in
// the steady state. Uses blocking Send (Fix C) so the initial announce
// never drops silently when a peer's send queue is briefly full.
func (memR *Reactor) broadcastNewLargeTxDefault(wtx *wrappedTx) {
	state, enc, ok := memR.encodeAndStore(wtx)
	if !ok {
		return
	}
	peers := memR.shuffleCapablePeers()
	P := len(peers)
	if P == 0 {
		return
	}
	schema.WriteMempoolChunkedMessage(memR.traceClient, "",
		schema.ChunkedOriginate, schema.Upload, state.TxKey[:],
		int(enc.NumParts()), len(wtx.tx.Tx), "default_rpc")

	// Build per-peer HaveTxChunks bitmaps: chunk i is announced to
	// DefaultHaveTxChunksRedundancy distinct peers, spread across all peers.
	numParts := int(enc.NumParts())
	bitmaps := make([]*cmtbits.BitArray, P)
	for i := range bitmaps {
		bitmaps[i] = cmtbits.NewBitArray(numParts)
	}
	for i := 0; i < numParts; i++ {
		for r := 0; r < DefaultHaveTxChunksRedundancy; r++ {
			pIdx := (i*DefaultHaveTxChunksRedundancy + r) % P
			bitmaps[pIdx].SetIndex(i, true)
		}
	}

	seenMsg := buildSeenLargeTxMsg(state)
	for i, cp := range peers {
		if !cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   seenMsg,
		}) {
			continue
		}
		memR.mempool.PeerHasTx(cp.id, wtx.key())
		// HaveTxChunks may be empty for some peers if numParts is small and
		// the round-robin doesn't reach them — skip those.
		if len(bitmaps[i].GetTrueIndices()) == 0 {
			continue
		}
		_ = cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message: &protomem.HaveTxChunks{
				TxKey: state.TxKey[:],
				Parts: *bitmaps[i].ToProto(),
			},
		})
		state.RecordNotified(cp.id, bitmaps[i])
	}
}

// broadcastNewLargeTxPush is Push RPC origination. Revised dissemination
// order (post-diagnosis):
//
//  1. SeenLargeTx to all peers — tx exists, here is its shape.
//  2. Push chunks to a size-adaptive set of round-robin peers, sending one
//     TxChunks batch per peer per pass (round-robin batches, not all-of-A
//     then all-of-B). Every peer gets its first reconstruction-critical
//     batch by pass 1, regardless of position in the peer order.
//
// We deliberately do NOT pre-announce HaveTxChunks(full bitmap) before the
// seed: doing so caused every peer to immediately Want from origin and
// created an origin pull-storm on top of the push. With the full-Have
// removed, peers learn chunk availability via the pushed-to peers' Fix-A
// gossip (HaveTxChunks for what they actually installed), so pull pressure
// distributes across the network.
//
// Push redundancy is bounded by DefaultMaxPushBytes: small blobs saturate
// the network (every chunk delivered to every peer directly, no pull needed
// for reconstruction); large blobs use the DefaultPushRPCChunkRedundancy
// floor so origin upload stays bounded.
func (memR *Reactor) broadcastNewLargeTxPush(wtx *wrappedTx) {
	state, enc, ok := memR.encodeAndStore(wtx)
	if !ok {
		return
	}
	peers := memR.shuffleCapablePeers()
	if len(peers) == 0 {
		return
	}
	schema.WriteMempoolChunkedMessage(memR.traceClient, "",
		schema.ChunkedOriginate, schema.Upload, state.TxKey[:],
		int(enc.NumParts()), len(wtx.tx.Tx), "push_rpc")

	// Step 1: announce existence to all peers via SeenLargeTx only.
	seenMsg := buildSeenLargeTxMsg(state)
	for _, cp := range peers {
		if cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   seenMsg,
		}) {
			memR.mempool.PeerHasTx(cp.id, wtx.key())
		}
	}

	// Step 2: build per-peer chunk lists (adaptive redundancy round-robin
	// across peers and chunks), then send them in round-robin BATCHES so no
	// peer waits for the full payload of any other peer.
	P := len(peers)
	N := int(enc.NumParts())
	redundancy := adaptivePushRedundancy(len(wtx.tx.Tx), P)
	perPeer := make(map[uint16][]protomem.TxChunk, P)
	for i := 0; i < N; i++ {
		for r := 0; r < redundancy; r++ {
			pIdx := (i*redundancy + r) % P
			cp := peers[pIdx]
			var proof cmtcrypto.Proof
			if enc.NumParts() > 1 {
				proof = *enc.Proofs[i].ToProto()
			}
			perPeer[cp.id] = append(perPeer[cp.id], protomem.TxChunk{
				Index: uint32(i),
				Data:  enc.Chunk(uint32(i)),
				Proof: proof,
			})
		}
	}

	// Compute the maximum number of batches any single peer will receive.
	maxBatches := 0
	for _, cp := range peers {
		nb := (len(perPeer[cp.id]) + DefaultChunksBatchSize - 1) / DefaultChunksBatchSize
		if nb > maxBatches {
			maxBatches = nb
		}
	}

	memR.Logger.Info("chunked push: dispatching",
		"tx_key", state.TxKey,
		"tx_size", len(wtx.tx.Tx),
		"num_parts", N,
		"peers", P,
		"redundancy", redundancy,
		"total_chunks_pushed", N*redundancy,
		"max_batches_per_peer", maxBatches,
	)

	// Round-robin batch dispatch: pass 0 sends batch 0 to every peer that
	// has one; pass 1 sends batch 1; etc. Each peer's first batch lands
	// after at most P sends, not after all earlier peers' full payloads.
	for batchIdx := 0; batchIdx < maxBatches; batchIdx++ {
		off := batchIdx * DefaultChunksBatchSize
		for _, cp := range peers {
			chunks := perPeer[cp.id]
			if off >= len(chunks) {
				continue
			}
			end := off + DefaultChunksBatchSize
			if end > len(chunks) {
				end = len(chunks)
			}
			memR.sendChunksBatch(cp.peer, state.TxKey, chunks[off:end])
		}
	}
}

// adaptivePushRedundancy returns the per-chunk push count for a tx of
// txSize bytes when there are numPeers connected peers. The redundancy is
// chosen so total origin upload ≈ redundancy × txSize ≤ DefaultMaxPushBytes,
// floored at DefaultPushRPCChunkRedundancy and capped at numPeers (can't
// push a chunk to more than every peer).
//
// Examples (numPeers=89, MaxPushBytes=100 MiB):
//
//	256 KiB tx → 89× (saturate, every peer holds every chunk)
//	  1 MiB tx → 89× (saturate)
//	  4 MiB tx → 25× (each peer gets ~25/256 of chunks directly)
//	  8 MiB tx → 12× (origin uploads ~96 MiB)
//	 16 MiB tx →  6× (floor)
//	 32 MiB tx →  6× (floor)
func adaptivePushRedundancy(txSize, numPeers int) int {
	if numPeers <= 0 {
		return 0
	}
	if txSize <= 0 {
		return DefaultPushRPCChunkRedundancy
	}
	r := DefaultMaxPushBytes / txSize
	if r < DefaultPushRPCChunkRedundancy {
		r = DefaultPushRPCChunkRedundancy
	}
	if r > numPeers {
		r = numPeers
	}
	return r
}

// encodeAndStore encodes the tx and inserts an already-reconstructed PartsState
// so we can serve WantTxChunks on demand. Returns nil state when the body is
// empty or encoding fails.
func (memR *Reactor) encodeAndStore(wtx *wrappedTx) (*chunked.PartsState, *chunked.EncodedTx, bool) {
	body := wtx.tx.Tx
	if len(body) == 0 {
		return nil, nil, false
	}
	txKey := wtx.key()
	enc, err := chunked.Encode(body)
	if err != nil {
		memR.Logger.Error("chunked encode failed", "err", err, "tx_key", txKey)
		return nil, nil, false
	}
	params := chunked.InsertParams{
		TxKey:      txKey,
		PartsRoot:  enc.PartsRoot,
		NumParts:   enc.NumParts(),
		LastLength: enc.LastLength,
		LeafHashes: enc.LeafHashes,
		OriginPeer: 0,
		Signer:     wtx.sender,
		Sequence:   wtx.sequence,
	}
	state, err := memR.chunkedStore.InsertReconstructed(params, enc)
	if err != nil && !errors.Is(err, chunked.ErrAlreadyExists) {
		memR.Logger.Error("chunked InsertReconstructed failed", "err", err, "tx_key", txKey)
		return nil, nil, false
	}
	if state == nil {
		state = memR.chunkedStore.Get(txKey)
		if state == nil {
			return nil, nil, false
		}
	}
	return state, enc, true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type capablePeer struct {
	id   uint16
	peer p2p.Peer
}

func (memR *Reactor) shuffleCapablePeers() []capablePeer {
	all := memR.ids.GetAll()
	out := make([]capablePeer, 0, len(all))
	for id, peer := range all {
		if peer == nil {
			continue
		}
		out = append(out, capablePeer{id: id, peer: peer})
	}
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func buildSeenLargeTxMsg(state *chunked.PartsState) *protomem.Message {
	return &protomem.Message{
		Sum: &protomem.Message_SeenLargeTx{
			SeenLargeTx: &protomem.SeenLargeTx{
				TxKey:      state.TxKey[:],
				PartsRoot:  state.PartsRoot,
				NumParts:   state.NumParts,
				LastLength: state.LastLength,
				Signer:     state.Signer,
				Sequence:   state.Sequence,
				LeafHashes: state.LeafHashes,
			},
		},
	}
}

func validateSeenLargeTx(msg *protomem.SeenLargeTx) error {
	if len(msg.TxKey) != types.TxKeySize {
		return errChunkedBadKey
	}
	if len(msg.PartsRoot) != 32 {
		return errChunkedBadRoot
	}
	if msg.NumParts == 0 || msg.NumParts > maxChunkedLeafHashes {
		return errChunkedBadNumParts
	}
	if msg.NumParts > 1 && msg.NumParts%2 != 0 {
		return errChunkedBadNumParts
	}
	if uint32(len(msg.LeafHashes)) != msg.NumParts {
		return errChunkedBadLeafCount
	}
	for _, h := range msg.LeafHashes {
		if len(h) != 32 {
			return errChunkedBadLeafCount
		}
	}
	if msg.LastLength == 0 || msg.LastLength > uint32(chunked.ChunkSize) {
		return errChunkedBadLastLength
	}
	if len(msg.Signer) > maxSignerLength {
		return errChunkedSignerLen
	}
	if msg.NumParts == 1 {
		if !bytes.Equal(msg.PartsRoot, msg.LeafHashes[0]) {
			return fmt.Errorf("%w: single-chunk root != leaf_hashes[0]", errChunkedBadRoot)
		}
	}
	return nil
}

// protoBitArrayToInternal converts the wire BitArray to libs/bits.
func protoBitArrayToInternal(p *protobits.BitArray, numParts int) *cmtbits.BitArray {
	ba := cmtbits.NewBitArray(numParts)
	if p == nil || p.Bits == 0 {
		return ba
	}
	ba.FromProto(p)
	return ba
}

// chunkPayloadFor returns data + Merkle proof for an index we hold. ok is
// false when we don't have that chunk.
func chunkPayloadFor(state *chunked.PartsState, index uint32) (data []byte, proof cmtcrypto.Proof, ok bool) {
	data, p, hasProof, ok := state.ChunkPayload(index)
	if !ok {
		return nil, cmtcrypto.Proof{}, false
	}
	if !hasProof {
		return data, cmtcrypto.Proof{}, true
	}
	return data, *p.ToProto(), true
}
