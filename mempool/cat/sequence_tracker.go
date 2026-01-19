package cat

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"sync"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// seenTxMetadata for backwards compatibility for nodes that don't support the new version.
type seenTxMetadata struct {
	signer   []byte
	sequence uint64
	peer     uint16
}

func (e *seenTxMetadata) peerIDs() []uint16 {
	if e.peer == 0 {
		return nil
	}
	return []uint16{e.peer}
}

// sequenceTracker maintains per-signer sequence ranges and per-tx metadata
// derived from SeenTx messages.
type sequenceTracker struct {
	mu sync.Mutex
	// seenByTxKey maps transaction keys to their sequence metadata (signer, sequence, peer).
	// Used to look up signer/sequence when a tx arrives so we can buffer or mark requests complete.
	seenByTxKey map[types.TxKey]*seenTxMetadata
	// peerMaxSeqBySigner tracks the maximum sequence seen per peer per signer.
	// Format: signer -> peerID -> max sequence.
	peerMaxSeqBySigner map[string]map[uint16]uint64
}

// newSequenceTracker constructs an empty sequence tracker.
func newSequenceTracker() *sequenceTracker {
	return &sequenceTracker{
		seenByTxKey:         make(map[types.TxKey]*seenTxMetadata),
		peerMaxSeqBySigner:  make(map[string]map[uint16]uint64),
	}
}

// recordSeenTx records a SeenTx message, tracking which peers have seen which sequences
// and linking txKey to signer/sequence metadata for later processing.
func (st *sequenceTracker) recordSeenTx(msg *protomem.SeenTx, txKey types.TxKey, peerID uint16) {
	if len(msg.Signer) == 0 || msg.Sequence == 0 || peerID == 0 {
		return
	}
	// todo: see if we even need this at all.
	if msg.MinSequence > 0 && msg.Sequence < msg.MinSequence {
		return
	}
	if msg.MaxSequence > 0 && msg.Sequence > msg.MaxSequence {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if msg.MaxSequence > 0 {
		signerKey := string(msg.Signer)
		if st.peerMaxSeqBySigner[signerKey] == nil {
			st.peerMaxSeqBySigner[signerKey] = make(map[uint16]uint64)
		}
		if msg.MaxSequence > st.peerMaxSeqBySigner[signerKey][peerID] {
			st.peerMaxSeqBySigner[signerKey][peerID] = msg.MaxSequence
		}
	}

	// If we already have the transaction, we don't need to record it.
	if _, ok := st.seenByTxKey[txKey]; ok {
		return
	}

	// Record the transaction metadata inside the txkey tracker.
	entry := &seenTxMetadata{
		signer:   append([]byte(nil), msg.Signer...),
		sequence: msg.Sequence,
		peer:     peerID,
	}
	st.seenByTxKey[txKey] = entry
}

// removeByTxKey removes tx metadata and returns the removed entry when present.
func (st *sequenceTracker) removeByTxKey(txKey types.TxKey) *seenTxMetadata {
	st.mu.Lock()
	defer st.mu.Unlock()

	entry, ok := st.seenByTxKey[txKey]
	if !ok {
		return nil
	}
	delete(st.seenByTxKey, txKey)
	return entry
}

// getByTxKey returns a defensive copy of the entry for a txKey.
func (st *sequenceTracker) getByTxKey(txKey types.TxKey) *seenTxMetadata {
	st.mu.Lock()
	defer st.mu.Unlock()

	entry, ok := st.seenByTxKey[txKey]
	if !ok {
		return nil
	}
	// Clone the entry to avoid returning a pointer to the internal data.
	clone := *entry
	clone.signer = slices.Clone(entry.signer)
	return &clone
}

// entriesForSigner returns all entries for a signer.
func (st *sequenceTracker) entriesForSigner(signer []byte) []*seenTxMetadata {
	if len(signer) == 0 {
		return nil
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	var out []*seenTxMetadata
	for _, entry := range st.seenByTxKey {
		if !bytes.Equal(entry.signer, signer) {
			continue
		}
		clone := *entry
		clone.signer = slices.Clone(entry.signer)
		out = append(out, &clone)
	}
	return out
}

// removeBySignerSequence removes entries from seenByTxKey map for a signer and sequence.
func (st *sequenceTracker) removeBySignerSequence(signer []byte, sequence uint64) int {
	if len(signer) == 0 || sequence == 0 {
		return 0
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	removed := 0
	// iterate over all entries in the seenByTxKey map
	for key, entry := range st.seenByTxKey {
		// if the entry is not for the signer or sequence, skip it
		if !bytes.Equal(entry.signer, signer) || entry.sequence != sequence {
			continue
		}
		// if the entry is for the signer and sequence, delete it
		delete(st.seenByTxKey, key)
		removed++
	}
	return removed
}

// removeBelowSequence drops entries for a signer that are below the expected sequence.
func (st *sequenceTracker) removeBelowSequence(signer []byte, expectedSeq uint64) int {
	// todo: see if we need to sanity check the expectedSeq
	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	removed := 0
	for key, entry := range st.seenByTxKey {
		entrySignerKey := string(entry.signer)
		if entrySignerKey != signerKey {
			continue
		}
		// If the sequence is greater than or equal to the expected sequence
		// we don't need to remove it.
		if entry.sequence >= expectedSeq {
			continue
		}
		delete(st.seenByTxKey, key)
		removed++
	}
	return removed
}

// removePeer clears peer associations and range tracking for a disconnected peer.
func (st *sequenceTracker) removePeer(peerID uint16) {
	if peerID == 0 {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	// Clear the peer ID from all entries.
	for _, entry := range st.seenByTxKey {
		if entry.peer == peerID {
			entry.peer = 0
		}
	}

	// Clear the peer ID from max sequence tracking.
	for signerKey, peers := range st.peerMaxSeqBySigner {
		delete(peers, peerID)
		if len(peers) == 0 {
			delete(st.peerMaxSeqBySigner, signerKey)
		}
	}
}

// signerKeys returns all signer keys that currently have pending entries.
func (st *sequenceTracker) signerKeys() [][]byte {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.seenByTxKey) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	for _, entry := range st.seenByTxKey {
		seen[string(entry.signer)] = struct{}{}
	}

	out := make([][]byte, 0, len(seen))
	for signerKey := range seen {
		out = append(out, []byte(signerKey))
	}
	return out
}

// signerKeysFromPeerMax returns signer keys that peers are tracking the max sequence for.
func (st *sequenceTracker) signerKeysFromPeerMax() [][]byte {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.peerMaxSeqBySigner) == 0 {
		return nil
	}

	out := make([][]byte, 0, len(st.peerMaxSeqBySigner))
	for signerKey := range st.peerMaxSeqBySigner {
		out = append(out, []byte(signerKey))
	}
	return out
}

// markRequestFailed clears peer association when a request fails.
func (st *sequenceTracker) markRequestFailed(txKey types.TxKey, peerID uint16) {
	if peerID == 0 {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	entry, ok := st.seenByTxKey[txKey]
	if !ok {
		return
	}
	if entry.peer == peerID {
		entry.peer = 0
	}
}

// getPeersForSignerSequence returns peers whose max seen sequence is >= sequence.
func (st *sequenceTracker) getPeersForSignerSequence(signer []byte, sequence uint64) []uint16 {
	// todo: see if we need this sanity check.
	if len(signer) == 0 || sequence == 0 {
		return nil
	}

	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	peers := st.peerMaxSeqBySigner[signerKey]
	if len(peers) == 0 {
		return nil
	}

	out := make([]uint16, 0, len(peers))
	for peerID, maxSeq := range peers {
		if maxSeq >= sequence {
			out = append(out, peerID)
		}
	}
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// getMaxSeenSeqForSigner returns the highest sequence observed in SeenTx messages.
// NOTE currently used for debugging.
func (st *sequenceTracker) getMaxSeenSeqForSigner(signer []byte) uint64 {
	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	var maxSeq uint64
	for _, seq := range st.peerMaxSeqBySigner[signerKey] {
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq
}
