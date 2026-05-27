package cat

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtbits "github.com/cometbft/cometbft/libs/bits"
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

	// DefaultPushRPCChunkRedundancy: number of peers each chunk is pushed to
	// (round-robin) from a Push RPC origin.
	DefaultPushRPCChunkRedundancy = 6

	// DefaultPerPeerInflightCap: max chunks in-flight to a single peer.
	DefaultPerPeerInflightCap = 16

	// DefaultChunksBatchSize: max chunks per WantTxChunks bitmap-set and per
	// TxChunks response message.
	DefaultChunksBatchSize = 16

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
	memR.serveChunks(state, src, wanted)
}

// handleTxChunks verifies and installs each chunk in the batch. Any proof
// failure → disconnect peer immediately. After installation triggers
// HaveTxChunks gossip per-chunk and reconstruction once K-of-2K is hit.
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
	triggerReconstruct := false
	for i := range msg.Chunks {
		justCompleted, err := memR.installAndGossipChunk(state, peerID, &msg.Chunks[i])
		if err != nil {
			memR.Switch.StopPeerForError(src, err, memR.String())
			return
		}
		if justCompleted {
			triggerReconstruct = true
		}
	}
	if triggerReconstruct {
		memR.reconstructAndAdmit(state, src)
	}
}

// installAndGossipChunk verifies a single chunk's Merkle proof, installs it
// into the PartsState, and gossips HaveTxChunks for it to peers not already
// known to hold it. Returns (justCompleted, err): justCompleted is true iff
// this chunk crossed the K-of-2K reconstruction threshold; err is non-nil
// only on protocol violations (bad proof, out-of-range index, malformed proof).
func (memR *Reactor) installAndGossipChunk(state *chunked.PartsState, peerID uint16, c *protomem.TxChunk) (bool, error) {
	if c.Index >= state.NumParts {
		return false, chunked.ErrChunkOutOfRange
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
		return false, err
	}
	if err := memR.chunkedStore.ChargeChunk(state, len(c.Data)); err != nil {
		// Memory cap breach is our problem, not the peer's. Drop without
		// disconnecting, evict the partial.
		memR.Logger.Info("chunked: cap breached, evicting partial",
			"err", err, "tx_key", state.TxKey)
		memR.chunkedStore.Remove(state.TxKey)
		return false, nil
	}
	justCompleted, err := state.Install(c.Index, c.Data)
	if err != nil {
		memR.chunkedStore.Release(state.OriginPeer(), int64(len(c.Data)))
		if !errors.Is(err, chunked.ErrChunkAlreadyPresent) {
			memR.Logger.Debug("chunked Install error", "err", err)
		}
		return false, nil
	}
	// Record the sender as having this chunk so subsequent dedup is accurate.
	bit := cmtbits.NewBitArray(int(state.NumParts))
	bit.SetIndex(int(c.Index), true)
	state.RecordHaves(peerID, bit)

	memR.gossipChunkOnReceipt(state, c.Index)
	return justCompleted, nil
}

// ---------------------------------------------------------------------------
// Gossip helpers (HaveTxChunks broadcast)
// ---------------------------------------------------------------------------

// gossipChunkOnReceipt announces a newly-installed chunk to every peer that
// (a) doesn't already hold it according to their HaveTxChunks announcements
// and (b) we haven't already told.
func (memR *Reactor) gossipChunkOnReceipt(state *chunked.PartsState, index uint32) {
	if memR.opts.ListenOnly {
		return
	}
	ba := cmtbits.NewBitArray(int(state.NumParts))
	ba.SetIndex(int(index), true)
	msg := &protomem.HaveTxChunks{
		TxKey: state.TxKey[:],
		Parts: *ba.ToProto(),
	}
	for id, peer := range memR.ids.GetAll() {
		if peer == nil {
			continue
		}
		if state.PeerKnowsChunk(id, index) {
			continue
		}
		if peer.TrySend(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   msg,
		}) {
			state.RecordNotified(id, ba)
		}
	}
}

// broadcastFullHaveAfterAdmit announces a full-bitmap HaveTxChunks to every
// peer that we don't yet believe holds all chunks. Used after reconstruction.
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
		if peer.TrySend(p2p.Envelope{
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
func (memR *Reactor) requestMissingFromPeer(state *chunked.PartsState, peerID uint16, peer p2p.Peer) {
	if peer == nil {
		return
	}
	wanted := state.RequestableFromPeer(peerID, DefaultPerPeerInflightCap, DefaultChunksBatchSize)
	if wanted == nil {
		return
	}
	state.MarkInflight(peerID, wanted)
	_ = peer.TrySend(p2p.Envelope{
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

// broadcastNewLargeTxDefault is Default RPC origination: SeenLargeTx +
// HaveTxChunks(partial bitmap, ~2 chunks per peer redundancy) bundled to
// a random fanout of 15 peers. Receivers pull chunks via Want.
func (memR *Reactor) broadcastNewLargeTxDefault(wtx *wrappedTx) {
	state, enc, ok := memR.encodeAndStore(wtx)
	if !ok {
		return
	}
	peers := memR.shuffleCapablePeers()
	if len(peers) == 0 {
		return
	}
	fanout := DefaultRPCAnnounceFanout
	if fanout > len(peers) {
		fanout = len(peers)
	}
	target := peers[:fanout]

	// Build per-peer HaveTxChunks bitmaps with 2× redundancy across the fanout.
	// chunk i → peers (i*r + 0..r-1) mod fanout, where r = redundancy.
	numParts := int(enc.NumParts())
	bitmaps := make([]*cmtbits.BitArray, fanout)
	for i := range bitmaps {
		bitmaps[i] = cmtbits.NewBitArray(numParts)
	}
	for i := 0; i < numParts; i++ {
		for r := 0; r < DefaultHaveTxChunksRedundancy; r++ {
			pIdx := (i*DefaultHaveTxChunksRedundancy + r) % fanout
			bitmaps[pIdx].SetIndex(i, true)
		}
	}

	seenMsg := buildSeenLargeTxMsg(state)
	for i, cp := range target {
		if !cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   seenMsg,
		}) {
			continue
		}
		memR.mempool.PeerHasTx(cp.id, wtx.key())
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

// broadcastNewLargeTxPush is Push RPC origination: SeenLargeTx +
// HaveTxChunks(full bitmap) to ALL peers, then push each chunk to 6 distinct
// round-robin peers via batched TxChunks.
func (memR *Reactor) broadcastNewLargeTxPush(wtx *wrappedTx) {
	state, enc, ok := memR.encodeAndStore(wtx)
	if !ok {
		return
	}
	peers := memR.shuffleCapablePeers()
	if len(peers) == 0 {
		return
	}

	full := cmtbits.NewBitArray(int(enc.NumParts()))
	full.Fill()
	seenMsg := buildSeenLargeTxMsg(state)
	haveMsg := &protomem.HaveTxChunks{
		TxKey: state.TxKey[:],
		Parts: *full.ToProto(),
	}
	for _, cp := range peers {
		if !cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   seenMsg,
		}) {
			continue
		}
		memR.mempool.PeerHasTx(cp.id, wtx.key())
		_ = cp.peer.Send(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   haveMsg,
		})
		state.RecordNotified(cp.id, full)
	}

	// Round-robin chunk push: chunk i → peers (i*r + 0..r-1) mod P,
	// where r = DefaultPushRPCChunkRedundancy and P = number of peers.
	P := len(peers)
	N := int(enc.NumParts())
	perPeer := make(map[uint16][]protomem.TxChunk, P)
	for i := 0; i < N; i++ {
		for r := 0; r < DefaultPushRPCChunkRedundancy; r++ {
			pIdx := (i*DefaultPushRPCChunkRedundancy + r) % P
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
	for _, cp := range peers {
		chunks := perPeer[cp.id]
		for off := 0; off < len(chunks); off += DefaultChunksBatchSize {
			end := off + DefaultChunksBatchSize
			if end > len(chunks) {
				end = len(chunks)
			}
			memR.sendChunksBatch(cp.peer, state.TxKey, chunks[off:end])
		}
	}
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
