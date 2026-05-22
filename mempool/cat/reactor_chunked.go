package cat

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"time"

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

// Chunked-propagation handlers (ADR-012). Each handler is responsible for the
// path from one wire message to its effect on the chunked.Store. The
// scheduler (graduated fanout, per-peer cap, retry ticker) lives in
// scheduler_chunked.go in a follow-up phase; this file implements only the
// per-message logic.

const (
	// maxLeafHashes caps the array carried in a SeenLargeTx announce. With
	// 64 KiB chunks and K-of-2K coding, MaxBlockSizeBytes / ChunkSize * 2
	// fits well under this; we stay conservative to bound DoS surface.
	maxChunkedLeafHashes = 4096

	// minChunkedTxSize is the smallest body we accept via the chunked path.
	// Smaller-than-1-byte announces are nonsensical.
	minChunkedTxSize = 1
)

var (
	errChunkedBadKey        = errors.New("chunked: malformed tx_key")
	errChunkedBadRoot       = errors.New("chunked: malformed parts_root")
	errChunkedBadLeafCount  = errors.New("chunked: leaf_hashes count != num_parts")
	errChunkedBadNumParts   = errors.New("chunked: invalid num_parts")
	errChunkedBadLastLength = errors.New("chunked: invalid last_length")
	errChunkedSignerLen     = errors.New("chunked: signer too long")
)

func (memR *Reactor) handleSeenLargeTx(src p2p.Peer, msg *protomem.SeenLargeTx) {
	if err := validateSeenLargeTx(msg); err != nil {
		memR.Logger.Error("malformed SeenLargeTx", "err", err, "src", src)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}

	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Logger.Error("SeenLargeTx with bad tx_key", "err", err, "src", src)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())

	// If we already have the tx in the legacy/cat mempool, ignore. The chunked
	// store may still want to know who else has it, but we don't need to fetch.
	if memR.mempool.Has(txKey) {
		memR.mempool.PeerHasTx(peerID, txKey)
		return
	}

	// If we already track this tx in the chunked store, just merge peer haves.
	if existing := memR.chunkedStore.Get(txKey); existing != nil {
		full := cmtbits.NewBitArray(int(existing.NumParts))
		full.Fill()
		existing.RecordHaves(peerID, full)
		return
	}

	params := chunked.InsertParams{
		TxKey:      txKey,
		PartsRoot:  msg.PartsRoot,
		NumParts:   msg.NumParts,
		LastLength: msg.LastLength,
		LeafHashes: msg.LeafHashes,
		OriginPeer: peerID,
	}
	state, err := memR.chunkedStore.Insert(params)
	if err != nil {
		memR.Logger.Debug("chunked Insert rejected", "err", err, "tx_key", txKey)
		return
	}

	// The announcer implicitly has every chunk.
	full := cmtbits.NewBitArray(int(state.NumParts))
	full.Fill()
	state.RecordHaves(peerID, full)

	// Schedule a WantTxChunks for everything we're missing. Per-peer cap is
	// enforced inside requestChunksFrom.
	memR.requestChunksFrom(state, peerID, src)
}

func (memR *Reactor) handleHaveTxChunks(src p2p.Peer, msg *protomem.HaveTxChunks) {
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Logger.Error("HaveTxChunks with bad tx_key", "err", err, "src", src)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	state := memR.chunkedStore.Get(txKey)
	if state == nil {
		// We have no record of this tx (yet); ignore. The announcing peer
		// will resend HaveTxChunks once we issue a SeenLargeTx or the peer
		// sends its own SeenLargeTx.
		return
	}
	peerID := memR.ids.GetIDForPeer(src.ID())
	if msg.Parts.Bits != int64(state.NumParts) {
		memR.Logger.Error("HaveTxChunks bitarray size mismatch",
			"got", msg.Parts.Bits, "want", state.NumParts)
		memR.Switch.StopPeerForError(src, errChunkedBadNumParts, memR.String())
		return
	}
	parts := protoBitArrayToInternal(&msg.Parts, int(state.NumParts))
	state.RecordHaves(peerID, parts)
	memR.requestChunksFrom(state, peerID, src)
}

func (memR *Reactor) handleWantTxChunks(src p2p.Peer, msg *protomem.WantTxChunks) {
	if memR.opts.ListenOnly {
		return
	}
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Logger.Error("WantTxChunks with bad tx_key", "err", err, "src", src)
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

func (memR *Reactor) handleTxChunk(src p2p.Peer, msg *protomem.TxChunk) {
	txKey, err := types.TxKeyFromBytes(msg.TxKey)
	if err != nil {
		memR.Logger.Error("TxChunk with bad tx_key", "err", err, "src", src)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}
	state := memR.chunkedStore.Get(txKey)
	if state == nil {
		return
	}
	if msg.Index >= state.NumParts {
		memR.Switch.StopPeerForError(src, chunked.ErrChunkOutOfRange, memR.String())
		return
	}

	var proof *merkle.Proof
	if state.NumParts > 1 {
		var p merkle.Proof
		p.Total = msg.Proof.Total
		p.Index = msg.Proof.Index
		p.LeafHash = msg.Proof.LeafHash
		p.Aunts = msg.Proof.Aunts
		proof = &p
	}
	if err := chunked.VerifyChunk(state.PartsRoot, state.NumParts, msg.Index, msg.Data, proof); err != nil {
		memR.Logger.Error("TxChunk failed verification", "err", err, "src", src, "tx_key", txKey)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return
	}

	if err := memR.chunkedStore.ChargeChunk(state, len(msg.Data)); err != nil {
		// Memory cap breach: drop the chunk and evict the partial.
		memR.Logger.Info("chunked partial evicted due to memory cap", "err", err, "tx_key", txKey)
		memR.chunkedStore.Remove(txKey)
		return
	}
	justCompleted, err := state.Install(msg.Index, msg.Data)
	if err != nil {
		// ErrChunkAlreadyPresent is a race with another peer; refund the
		// reservation we just made and move on.
		memR.chunkedStore.Release(state.OriginPeer(), int64(len(msg.Data)))
		if !errors.Is(err, chunked.ErrChunkAlreadyPresent) {
			memR.Logger.Debug("chunked Install error", "err", err, "tx_key", txKey)
		}
		return
	}
	if !justCompleted {
		// Forward HaveTxChunks to peers so they know we now hold this chunk.
		memR.broadcastHaveChunk(state, msg.Index)
		return
	}

	memR.reconstructAndAdmit(state, src)
}

// reconstructAndAdmit runs RS decode, verifies the body hash, and submits
// the result through CheckTx. On success the chunked state advances to
// StateReconstructed and the legacy CAT pool admits the tx.
func (memR *Reactor) reconstructAndAdmit(state *chunked.PartsState, src p2p.Peer) {
	body, err := memR.chunkedStore.ReconstructAndVerify(state)
	if err != nil {
		memR.Logger.Error("chunked reconstruct failed", "err", err, "tx_key", state.TxKey)
		// Body did not hash to the announced tx_key, or RS decode failed. The
		// origin peer attested to this announce; ban-score them.
		memR.Switch.StopPeerForError(src, err, memR.String())
		memR.chunkedStore.Remove(state.TxKey)
		return
	}

	peerID := memR.ids.GetIDForPeer(src.ID())
	cachedTx := types.Tx(body).ToCachedTx()
	txInfo := mempool.TxInfo{SenderID: peerID, SenderP2PID: src.ID()}
	if _, err := memR.tryAddNewTx(cachedTx, state.TxKey, txInfo, string(src.ID())); err != nil {
		// CheckTx rejected the body. Drop the chunked state; the tx is
		// already in the rejected cache via the existing CAT pool.
		memR.chunkedStore.Remove(state.TxKey)
		return
	}
}

// requestChunksFrom issues a WantTxChunks to peer for chunks we still need
// that the peer advertises, bounded by the per-peer concurrency cap.
func (memR *Reactor) requestChunksFrom(state *chunked.PartsState, peerID uint16, peer p2p.Peer) {
	if peer == nil {
		return
	}
	missing := state.MissingFromPeer(peerID)
	if missing == nil {
		return
	}
	want := boundByCap(missing, chunked.PerPeerInflightCap(state.NumParts, memR.ids.Len()))
	if want == nil {
		return
	}
	state.MarkInflight(peerID, want)
	peer.TrySend(p2p.Envelope{
		ChannelID: MempoolChunkChannel,
		Message: &protomem.WantTxChunks{
			TxKey: state.TxKey[:],
			Parts: *want.ToProto(),
		},
	})
}

// serveChunks responds with TxChunk messages for each requested chunk we hold.
func (memR *Reactor) serveChunks(state *chunked.PartsState, peer p2p.Peer, wanted *cmtbits.BitArray) {
	if wanted == nil {
		return
	}
	indices := wanted.GetTrueIndices()
	for _, idx := range indices {
		if uint32(idx) >= state.NumParts {
			continue
		}
		data, proofProto, ok := chunkPayloadFor(state, uint32(idx))
		if !ok {
			continue
		}
		msg := &protomem.TxChunk{
			TxKey: state.TxKey[:],
			Index: uint32(idx),
			Data:  data,
			Proof: proofProto,
		}
		peer.TrySend(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   msg,
		})
	}
}

// broadcastHaveChunk advertises ownership of a newly-installed chunk to all
// peers, scoped by the chunked channel's natural fan-out. For now we send to
// every connected peer; the graduated fanout (chunked.AnnounceFanout) is
// applied at SeenLargeTx time and forwarding HaveTxChunks for individual
// chunks is cheap (a 1-bit BitArray).
func (memR *Reactor) broadcastHaveChunk(state *chunked.PartsState, index uint32) {
	if memR.opts.ListenOnly {
		return
	}
	ba := cmtbits.NewBitArray(int(state.NumParts))
	ba.SetIndex(int(index), true)
	msg := &protomem.HaveTxChunks{
		TxKey: state.TxKey[:],
		Parts: *ba.ToProto(),
	}
	for _, peer := range memR.ids.GetAll() {
		peer.TrySend(p2p.Envelope{
			ChannelID: MempoolChunkChannel,
			Message:   msg,
		})
	}
}

// broadcastNewLargeTx encodes a newly-admitted tx and gossips it via the
// chunked path. The chunked store retains the encoded chunks so we can serve
// peer WantTxChunks requests. Peers without channel MempoolChunkChannel
// still get a legacy SeenTx so they can pull the body the old way.
func (memR *Reactor) broadcastNewLargeTx(wtx *wrappedTx) {
	if memR.opts.ListenOnly {
		return
	}
	body := wtx.tx.Tx
	if len(body) == 0 {
		return
	}
	txKey := wtx.key()

	enc, err := chunked.Encode(body)
	if err != nil {
		memR.Logger.Error("chunked encode failed; falling back to legacy SeenTx",
			"err", err, "tx_key", txKey)
		memR.broadcastSeenTxWithHeight(txKey, wtx.height, wtx.sender, wtx.sequence)
		return
	}

	// Retain chunks in the chunked store as already-reconstructed so we can
	// answer WantTxChunks from peers. If the state already exists (re-broadcast,
	// recheck, etc.) just continue with the existing state.
	params := chunked.InsertParams{
		TxKey:      txKey,
		PartsRoot:  enc.PartsRoot,
		NumParts:   enc.NumParts(),
		LastLength: enc.LastLength,
		LeafHashes: enc.LeafHashes,
		OriginPeer: 0, // self-originated
	}
	if _, err := memR.chunkedStore.InsertReconstructed(params, enc); err != nil &&
		!errors.Is(err, chunked.ErrAlreadyExists) {
		memR.Logger.Error("chunked InsertReconstructed failed",
			"err", err, "tx_key", txKey)
	}

	memR.announceLargeTxAndPush(enc, wtx)
}

// announceLargeTxAndPush sends SeenLargeTx to chunked-capable peers using the
// graduated-fanout formula, optionally pushes K chunks across a handful of
// bootstrap peers, and sends legacy SeenTx to peers that do not support
// MempoolChunkChannel.
func (memR *Reactor) announceLargeTxAndPush(enc *chunked.EncodedTx, wtx *wrappedTx) {
	peers := memR.ids.GetAll()
	if len(peers) == 0 {
		return
	}
	txKey := wtx.key()

	seenLargeMsg := &protomem.Message{
		Sum: &protomem.Message_SeenLargeTx{
			SeenLargeTx: &protomem.SeenLargeTx{
				TxKey:      txKey[:],
				PartsRoot:  enc.PartsRoot,
				NumParts:   enc.NumParts(),
				LastLength: enc.LastLength,
				Signer:     wtx.sender,
				Sequence:   wtx.sequence,
				LeafHashes: enc.LeafHashes,
			},
		},
	}
	legacySeenMsg := &protomem.Message{
		Sum: &protomem.Message_SeenTx{
			SeenTx: &protomem.SeenTx{
				TxKey:    txKey[:],
				Signer:   wtx.sender,
				Sequence: wtx.sequence,
			},
		},
	}

	// Graduated fanout for SeenLargeTx; legacy SeenTx still respects the
	// existing maxSeenTxBroadcast (15) bound for non-chunked peers.
	chunkedFanout := chunked.AnnounceFanout(enc.NumParts(), chunked.DefaultAnnounceTarget)
	chunkedSent := 0
	legacySent := 0

	orderedPeers := selectStickyPeers(wtx.sender, peers, len(peers), memR.currentStickyPeerSalt())
	maxPersistent := memR.opts.MaxPersistentStickyPeers
	chunkedPersistentSent := 0
	legacyPersistentSent := 0

	// Track chunked-capable peers for the bootstrap push.
	var chunkedPeers []capablePeer

	for _, peerInfo := range orderedPeers {
		id := peerInfo.id
		peer := peerInfo.peer
		if p, ok := peer.Get(types.PeerStateKey).(PeerState); ok {
			if p.GetHeight() < wtx.height-peerHeightDiff {
				continue
			}
		}
		if memR.mempool.seenByPeersSet.Has(txKey, id) {
			continue
		}
		isPersistent := peer.IsPersistent()
		supports := peerSupportsChunked(peer)

		if supports {
			chunkedPeers = append(chunkedPeers, capablePeer{id: id, peer: peer})
			canSend := chunkedSent < chunkedFanout ||
				(isPersistent && chunkedPersistentSent < maxPersistent)
			if canSend && peer.Send(p2p.Envelope{
				ChannelID: MempoolChunkChannel,
				Message:   seenLargeMsg,
			}) {
				memR.mempool.PeerHasTx(id, txKey)
				chunkedSent++
				if isPersistent {
					chunkedPersistentSent++
				}
			}
			continue
		}

		canSendLegacy := legacySent < maxSeenTxBroadcast ||
			(isPersistent && legacyPersistentSent < maxPersistent)
		if canSendLegacy && peer.Send(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message:   legacySeenMsg,
		}) {
			memR.mempool.PeerHasTx(id, txKey)
			legacySent++
			if isPersistent {
				legacyPersistentSent++
			}
		}
	}

	memR.bootstrapPushChunks(enc, txKey, chunkedPeers)
}

// bootstrapPushChunks distributes K chunks across up to DefaultBootstrapPushPeers
// chunked-capable peers. This guarantees K-of-2K is reachable within one RTT
// of admission without depending on the pull loop.
func (memR *Reactor) bootstrapPushChunks(enc *chunked.EncodedTx, txKey types.TxKey, peers []capablePeer) {
	if len(peers) == 0 {
		return
	}
	numPushPeers := chunked.DefaultBootstrapPushPeers
	if numPushPeers > len(peers) {
		numPushPeers = len(peers)
	}
	k := int(enc.NumOriginals())
	if numPushPeers > k {
		// More peers than chunks needed; cap so each gets at least one chunk.
		numPushPeers = k
	}
	if numPushPeers <= 0 {
		return
	}

	// Pick K random distinct indices out of 0..2K-1, partition across peers.
	total := int(enc.NumParts())
	indices := rand.Perm(total)[:k]
	chunksPerPeer := (k + numPushPeers - 1) / numPushPeers

	pos := 0
	for i := 0; i < numPushPeers && pos < k; i++ {
		end := pos + chunksPerPeer
		if end > k {
			end = k
		}
		for j := pos; j < end; j++ {
			idx := uint32(indices[j])
			var proof cmtcrypto.Proof
			if enc.NumParts() > 1 {
				proof = *enc.Proofs[idx].ToProto()
			}
			peers[i].peer.TrySend(p2p.Envelope{
				ChannelID: MempoolChunkChannel,
				Message: &protomem.TxChunk{
					TxKey: txKey[:],
					Index: idx,
					Data:  enc.Chunk(idx),
					Proof: proof,
				},
			})
		}
		pos = end
	}
}

type capablePeer struct {
	id   uint16
	peer p2p.Peer
}

// peerSupportsChunked reports whether the peer advertises MempoolChunkChannel.
// Falls back to the legacy SeenTx path when false. Uses the same
// type-assert-to-DefaultNodeInfo pattern as the consensus and propagation
// reactors.
func peerSupportsChunked(peer p2p.Peer) bool {
	if peer == nil {
		return false
	}
	ni, ok := peer.NodeInfo().(p2p.DefaultNodeInfo)
	if !ok {
		return false
	}
	return ni.HasChannel(MempoolChunkChannel)
}

// startChunkedSweeper runs the TTL/eviction sweep until the reactor stops.
// Started from OnStart.
func (memR *Reactor) startChunkedSweeper() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-memR.Quit():
			return
		case <-tick.C:
			evicted := memR.chunkedStore.SweepExpired(time.Now())
			if len(evicted) > 0 {
				memR.Logger.Debug("chunked sweeper evicted partials", "count", len(evicted))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
	if msg.NumParts == 1 {
		if msg.LastLength == 0 || msg.LastLength > uint32(chunked.ChunkSize) {
			return errChunkedBadLastLength
		}
	} else {
		if msg.LastLength == 0 || msg.LastLength > uint32(chunked.ChunkSize) {
			return errChunkedBadLastLength
		}
	}
	if len(msg.Signer) > maxSignerLength {
		return errChunkedSignerLen
	}
	// parts_root must equal leaf_hashes[0] when num_parts == 1; otherwise
	// it must match the root computed from leaf_hashes.
	if msg.NumParts == 1 {
		if !bytes.Equal(msg.PartsRoot, msg.LeafHashes[0]) {
			return fmt.Errorf("%w: single-chunk root != leaf_hashes[0]", errChunkedBadRoot)
		}
	}
	return nil
}

// protoBitArrayToInternal converts the wire BitArray (proto/tendermint/libs/bits)
// to the internal libs/bits BitArray. Caller must have validated that the
// proto BitArray's Bits field equals numParts.
func protoBitArrayToInternal(p *protobits.BitArray, numParts int) *cmtbits.BitArray {
	ba := cmtbits.NewBitArray(numParts)
	if p == nil || p.Bits == 0 {
		return ba
	}
	ba.FromProto(p)
	return ba
}

// chunkPayloadFor returns the data and proof for a chunk index, ready to put
// on the wire. ok is false if we don't hold the chunk.
//
// For the initial wiring we only serve chunks for txs that have already been
// reconstructed: we re-encode the body to recover the proofs. A follow-up
// patch will retain the Merkle proofs alongside the chunks in PartsState so
// we can also serve from collecting state.
func chunkPayloadFor(state *chunked.PartsState, index uint32) (data []byte, proof cmtcrypto.Proof, ok bool) {
	body := stateBody(state)
	if body == nil {
		return nil, cmtcrypto.Proof{}, false
	}
	enc, err := chunked.Encode(body)
	if err != nil {
		return nil, cmtcrypto.Proof{}, false
	}
	if index >= enc.NumParts() {
		return nil, cmtcrypto.Proof{}, false
	}
	chunk := enc.Chunk(index)
	if state.NumParts == 1 {
		return chunk, cmtcrypto.Proof{}, true
	}
	return chunk, *enc.Proofs[index].ToProto(), true
}

// stateBody returns the reconstructed body if the state has progressed to
// StateReconstructed, otherwise nil. Collecting partials are not served:
// at the network level there's no point sending a chunk that we cannot
// re-Merkle-prove against the same parts_root the requester knows.
func stateBody(state *chunked.PartsState) []byte {
	if state.State() != chunked.StateReconstructed {
		return nil
	}
	return state.Body
}

// boundByCap returns a BitArray whose true bits are at most n.
// Returns nil if the input has no true bits.
func boundByCap(in *cmtbits.BitArray, n int) *cmtbits.BitArray {
	if in == nil {
		return nil
	}
	indices := in.GetTrueIndices()
	if len(indices) == 0 {
		return nil
	}
	if n <= 0 {
		n = 1
	}
	if len(indices) > n {
		indices = indices[:n]
	}
	out := cmtbits.NewBitArray(in.Size())
	for _, i := range indices {
		out.SetIndex(i, true)
	}
	return out
}
