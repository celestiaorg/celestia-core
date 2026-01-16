package cat

import (
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// seenTxMetadata is a single entry in the sequence tracker.
type seenTxMetadata struct {
	signerKey string
	signer    []byte
	txKey     types.TxKey
	sequence  uint64
	peer      uint16
	addedAt   time.Time
}

type peerSet map[uint16]struct{}

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
	// maxSeenBySigner caches the maximum seen sequence per signer.
	maxSeenBySigner map[string]uint64
	// peersBySignerSeq tracks which peers have seen a specific (signer, sequence).
	// Format: signer -> sequence -> peerID set.
	// For O(1) lookup for peers during sequence gossip.
	peersBySignerSeq map[string]map[uint64]peerSet
}

// newSequenceTracker constructs an empty sequence tracker.
func newSequenceTracker() *sequenceTracker {
	return &sequenceTracker{
		seenByTxKey:      make(map[types.TxKey]*seenTxMetadata),
		maxSeenBySigner:  make(map[string]uint64),
		peersBySignerSeq: make(map[string]map[uint64]peerSet),
	}
}

// recordSeenTx records a SeenTx message, tracking which peers have seen which sequences
// and linking txKey to signer/sequence metadata for later processing.
func (st *sequenceTracker) recordSeenTx(msg *protomem.SeenTx, txKey types.TxKey, peerID uint16) {
	if len(msg.Signer) == 0 || msg.Sequence == 0 || peerID == 0 {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	signerKey := string(msg.Signer)
	st.dropPeerSequencesBelowMinLocked(signerKey, msg.MinSequence, peerID)
	st.updateMaxSeenBySigner(signerKey, msg.Sequence)
	st.addPeerForSequence(signerKey, msg.Sequence, peerID)

	// If we already have the transaction, we don't need to record it.
	if _, ok := st.seenByTxKey[txKey]; ok {
		return
	}

	// Record the transaction metadata inside the txkey tracker.
	entry := &seenTxMetadata{
		signerKey: signerKey,
		signer:    append([]byte(nil), msg.Signer...),
		txKey:     txKey,
		sequence:  msg.Sequence,
		peer:      peerID,
		addedAt:   time.Now(),
	}
	st.seenByTxKey[txKey] = entry
}

// addPeerForSequence records that a peer has seen a specific sequence.
// Assumes that the caller has already acquired the lock.
func (st *sequenceTracker) addPeerForSequence(signerKey string, sequence uint64, peerID uint16) {
	// Initialize the map if it doesn't exist.
	if st.peersBySignerSeq[signerKey] == nil {
		st.peersBySignerSeq[signerKey] = make(map[uint64]peerSet)
	}
	// Initialize the peer set if it doesn't exist.
	peers := st.peersBySignerSeq[signerKey][sequence]
	if peers == nil {
		peers = make(peerSet)
		st.peersBySignerSeq[signerKey][sequence] = peers
	}
	// Add the peer to the set.
	peers[peerID] = struct{}{}
}

// deleteSequenceLocked clears peer tracking for a specific (signer, sequence).
// Assumes that the caller has already acquired the lock.
func (st *sequenceTracker) deleteSequenceLocked(signerKey string, sequence uint64) {
	sequences := st.peersBySignerSeq[signerKey]
	if sequences == nil {
		return
	}
	delete(sequences, sequence)
	if len(sequences) == 0 {
		delete(st.peersBySignerSeq, signerKey)
		delete(st.maxSeenBySigner, signerKey)
		return
	}
	st.recomputeMaxSeenBySignerLocked(signerKey, sequences)
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
	st.deleteSequenceLocked(entry.signerKey, entry.sequence)
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

// removeBelowSequence drops entries for a signer that are below the expected sequence.
func (st *sequenceTracker) removeBelowSequence(signer []byte, expectedSeq uint64) int {
	// todo: see if we need to sanity check the expectedSeq
	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	removed := 0
	for key, entry := range st.seenByTxKey {
		if entry.signerKey != signerKey {
			continue
		}
		// If the sequence is greater than or equal to the expected sequence
		// we don't need to remove it.
		if entry.sequence >= expectedSeq {
			continue
		}
		delete(st.seenByTxKey, key)
		st.deleteSequenceLocked(entry.signerKey, entry.sequence)
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

	// Clear the peer ID from all sequences.
	for signerKey, sequences := range st.peersBySignerSeq {
		for sequence, peers := range sequences {
			// Clear the peer ID from the sequence.
			delete(peers, peerID)
			// If the sequence is now empty, delete it.
			if len(peers) == 0 {
				delete(sequences, sequence)
			}
		}
		// If the sequence is now empty, delete it.
		if len(sequences) == 0 {
			delete(st.peersBySignerSeq, signerKey)
			delete(st.maxSeenBySigner, signerKey)
			continue
		}
		// Recalculate maxSeenBySigner for this signer after peer removal.
		var maxSeq uint64
		for seq := range sequences {
			if seq > maxSeq {
				maxSeq = seq
			}
		}
		if maxSeq == 0 {
			delete(st.maxSeenBySigner, signerKey)
			continue
		}
		st.maxSeenBySigner[signerKey] = maxSeq
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
		seen[entry.signerKey] = struct{}{}
	}

	out := make([][]byte, 0, len(seen))
	for signerKey := range seen {
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

// getPeersForSignerSequence returns peers that have reported seeing the sequence.
func (st *sequenceTracker) getPeersForSignerSequence(signer []byte, sequence uint64) []uint16 {
	// todo: see if we need this sanity check.
	if len(signer) == 0 || sequence == 0 {
		return nil
	}

	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	sequences := st.peersBySignerSeq[signerKey]
	if len(sequences) == 0 {
		return nil
	}
	peers := sequences[sequence]
	if len(peers) == 0 {
		return nil
	}

	out := make([]uint16, 0, len(peers))
	for peerID := range peers {
		out = append(out, peerID)
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

	return st.maxSeenBySigner[signerKey]
}

// pruneEntriesOlderThan removes tx metadata older than maxAge and returns how many were removed.
func (st *sequenceTracker) pruneEntriesOlderThan(maxAge time.Duration) int {
	if maxAge <= 0 {
		return 0
	}

	cutoff := time.Now().Add(-maxAge)

	st.mu.Lock()
	defer st.mu.Unlock()

	removed := 0
	for key, entry := range st.seenByTxKey {
		if entry.addedAt.After(cutoff) {
			continue
		}
		delete(st.seenByTxKey, key)
		st.deleteSequenceLocked(entry.signerKey, entry.sequence)
		removed++
	}
	return removed
}

// recomputeMaxSeenBySignerLocked recalculates the max seen sequence for a signer.
// Assumes that the caller has already acquired the lock.
func (st *sequenceTracker) recomputeMaxSeenBySignerLocked(signerKey string, sequences map[uint64]peerSet) {
	var maxSeq uint64
	for seq := range sequences {
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	if maxSeq == 0 {
		delete(st.maxSeenBySigner, signerKey)
		return
	}
	st.maxSeenBySigner[signerKey] = maxSeq
}

// dropPeerSequencesBelowMinLocked removes a peer from sequences below minSequence.
// Assumes that the caller has already acquired the lock.
func (st *sequenceTracker) dropPeerSequencesBelowMinLocked(signerKey string, minSequence uint64, peerID uint16) {
	if minSequence == 0 || peerID == 0 {
		return
	}

	// Fast path: nothing to prune for this signer.
	sequences := st.peersBySignerSeq[signerKey]
	if sequences == nil {
		return
	}

	// Remove the peer from sequences it can no longer serve.
	for seq, peers := range sequences {
		if seq >= minSequence {
			continue
		}
		delete(peers, peerID)
		if len(peers) == 0 {
			delete(sequences, seq)
		}
	}

	// Drop empty signer entry or recompute max seen sequence after pruning.
	if len(sequences) == 0 {
		delete(st.peersBySignerSeq, signerKey)
		delete(st.maxSeenBySigner, signerKey)
	} else {
		st.recomputeMaxSeenBySignerLocked(signerKey, sequences)
	}

	// Clear per-tx peer hints that point to sequences the peer no longer has.
	for _, entry := range st.seenByTxKey {
		if entry.signerKey != signerKey {
			continue
		}
		if entry.peer == peerID && entry.sequence < minSequence {
			entry.peer = 0
		}
	}
}

// updateMaxSeenBySigner updates the max sequence for a signer if the new sequence is higher.
// Assumes that the caller has already acquired the lock.
func (st *sequenceTracker) updateMaxSeenBySigner(signerKey string, sequence uint64) {
	if sequence > st.maxSeenBySigner[signerKey] {
		st.maxSeenBySigner[signerKey] = sequence
	}
}
