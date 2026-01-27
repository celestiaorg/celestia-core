package cat

import (
	"strconv"
	"sync"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

type txKeyEntry struct {
	txKey    types.TxKey
	signer   []byte
	sequence uint64
	peer     uint16
}

// legacyTxKeyTracker maintains legacy txKey <-> (signer, sequence) mappings.
// It mirrors pendingSeen cleanup behavior: on peer loss or timeout, clear peer association only.
type legacyTxKeyTracker struct {
	mu sync.Mutex

	// txKey -> (signer, sequence) for receving `Tx` messages
	txKeyToSeqSigner map[types.TxKey]*txKeyEntry
	// (signer, sequence) -> txKey for sending `SeenTx` messages
	seqSignerToTxKey map[string]types.TxKey
}

func newLegacyTxKeyTracker() *legacyTxKeyTracker {
	return &legacyTxKeyTracker{
		txKeyToSeqSigner: make(map[types.TxKey]*txKeyEntry),
		seqSignerToTxKey: make(map[string]types.TxKey),
	}
}

func signerSequenceKey(signer []byte, sequence uint64) string {
	return string(signer) + ":" + strconv.FormatUint(sequence, 10)
}

// recordSeenTx records a legacy SeenTx message (txKey-based).
func (t *legacyTxKeyTracker) recordSeenTx(msg *protomem.SeenTx, txKey types.TxKey, peerID uint16) {
	if len(msg.Signer) == 0 || msg.Sequence == 0 || peerID == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.txKeyToSeqSigner[txKey]; ok {
		return
	}

	seqKey := signerSequenceKey(msg.Signer, msg.Sequence)
	if _, ok := t.seqSignerToTxKey[seqKey]; ok {
		return
	}

	entry := &txKeyEntry{
		txKey:    txKey,
		signer:   append([]byte(nil), msg.Signer...),
		sequence: msg.Sequence,
		peer:     peerID,
	}
	t.txKeyToSeqSigner[txKey] = entry
	t.seqSignerToTxKey[seqKey] = txKey
}

// getByTxKey returns a defensive copy of the entry for a txKey.
func (t *legacyTxKeyTracker) getByTxKey(txKey types.TxKey) *txKeyEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.txKeyToSeqSigner[txKey]
	if !ok {
		return nil
	}
	clone := *entry
	clone.signer = append([]byte(nil), entry.signer...)
	return &clone
}

// txKeyForSignerSequence returns the txKey for a signer/sequence pair.
func (t *legacyTxKeyTracker) txKeyForSignerSequence(signer []byte, sequence uint64) (types.TxKey, bool) {
	if len(signer) == 0 || sequence == 0 {
		return types.TxKey{}, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	txKey, ok := t.seqSignerToTxKey[signerSequenceKey(signer, sequence)]
	return txKey, ok
}

// entriesForSigner returns all tracked entries for a signer.
// note: currently used for testing, will prob delete later
func (t *legacyTxKeyTracker) entriesForSigner(signer []byte) []*txKeyEntry {
	if len(signer) == 0 {
		return nil
	}

	signerKey := string(signer)

	t.mu.Lock()
	defer t.mu.Unlock()

	var out []*txKeyEntry
	for _, entry := range t.txKeyToSeqSigner {
		if string(entry.signer) != signerKey {
			continue
		}
		clone := *entry
		clone.signer = append([]byte(nil), entry.signer...)
		out = append(out, &clone)
	}
	return out
}

// entries returns all tracked entries (defensive copies).
func (t *legacyTxKeyTracker) entries() []*txKeyEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]*txKeyEntry, 0, len(t.txKeyToSeqSigner))
	for _, entry := range t.txKeyToSeqSigner {
		clone := *entry
		clone.signer = append([]byte(nil), entry.signer...)
		out = append(out, &clone)
	}
	return out
}

// removeByTxKey removes tx metadata for a txKey.
// note: removes from both maps!
func (t *legacyTxKeyTracker) removeByTxKey(txKey types.TxKey) *txKeyEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.txKeyToSeqSigner[txKey]
	if !ok {
		return nil
	}
	delete(t.seqSignerToTxKey, signerSequenceKey(entry.signer, entry.sequence))
	delete(t.txKeyToSeqSigner, txKey)
	return entry
}

// removeBySignerSequence removes tx metadata for a signer and sequence.
// note: removes from both maps!
func (t *legacyTxKeyTracker) removeBySignerSequence(signer []byte, sequence uint64) {
	if len(signer) == 0 || sequence == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	seqKey := signerSequenceKey(signer, sequence)
	txKey, ok := t.seqSignerToTxKey[seqKey]
	if !ok {
		return
	}
	delete(t.seqSignerToTxKey, seqKey)
	delete(t.txKeyToSeqSigner, txKey)
}

// removePeer clears peer associations for a disconnected peer.
func (t *legacyTxKeyTracker) removePeer(peerID uint16) {
	if peerID == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for _, entry := range t.txKeyToSeqSigner {
		if entry.peer == peerID {
			entry.peer = 0
		}
	}
}

// markRequestFailed clears peer association when a request fails.
func (t *legacyTxKeyTracker) markRequestFailed(txKey types.TxKey, peerID uint16) {
	if peerID == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.txKeyToSeqSigner[txKey]
	if !ok {
		return
	}
	if entry.peer == peerID {
		entry.peer = 0
	}
}
