package cat

import (
	"sync"
	"time"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

// bufferedTx holds a transaction that are requested out-of-order within certain bounds, waiting to be processed
type bufferedTx struct {
	tx       *types.CachedTx
	txKey    types.TxKey
	txInfo   mempool.TxInfo
	peerID   string
	sequence uint64
	addedAt  time.Time
}

// receivedTxBuffer holds transactions that are requested out-of-order.
// Transactions are buffered by (signer, sequence) and processed in order
// once earlier sequences complete.
//
// Buffered txs have not been through CheckTx, so the buffer is bounded
// independently of peer identity: total bytes are capped by maxBytes, and
// entries are dropped once they expire (pruneOlderThan) or fall below the
// signer's expected sequence (removeBelow).
type receivedTxBuffer struct {
	mu sync.Mutex
	// buffers contains mapping: signer (as string) -> sequence -> buffered tx
	buffers map[string]map[uint64]*bufferedTx
	// countByPeer tracks how many transactions are buffered from each peer
	countByPeer map[string]int
	// totalBytes is the summed size of all buffered txs.
	totalBytes int64
	// maxBytes caps totalBytes.
	maxBytes int64
}

// newReceivedTxBuffer creates a new buffer for out-of-order transactions
// holding at most maxBytes of tx data.
func newReceivedTxBuffer(maxBytes int64) *receivedTxBuffer {
	return &receivedTxBuffer{
		buffers:     make(map[string]map[uint64]*bufferedTx),
		countByPeer: make(map[string]int),
		maxBytes:    maxBytes,
	}
}

// add stores a transaction in the buffer for later processing.
// Returns false if the buffer is full for this signer, peer is at capacity,
// the byte limit would be exceeded, or tx already exists.
func (b *receivedTxBuffer) add(signer []byte, seq uint64, tx *types.CachedTx, txKey types.TxKey, txInfo mempool.TxInfo, peerID string) bool {
	if len(signer) == 0 {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Check buffer limit per peer
	if b.countByPeer[peerID] >= maxRequestsPerPeer {
		return false
	}

	// Check the total byte limit
	txSize := int64(len(tx.Tx))
	if b.totalBytes+txSize > b.maxBytes {
		return false
	}

	signerKey := string(signer)
	signerBuf, exists := b.buffers[signerKey]
	if !exists {
		signerBuf = make(map[uint64]*bufferedTx)
		b.buffers[signerKey] = signerBuf
	}

	// Check if already buffered
	if _, exists := signerBuf[seq]; exists {
		return false
	}

	// Check buffer limit per signer
	if len(signerBuf) >= maxReceivedBufferSize {
		return false
	}

	signerBuf[seq] = &bufferedTx{
		tx:       tx,
		txKey:    txKey,
		txInfo:   txInfo,
		peerID:   peerID,
		sequence: seq,
		addedAt:  time.Now(),
	}
	b.countByPeer[peerID]++
	b.totalBytes += txSize
	return true
}

// get retrieves a buffered transaction for the given signer and sequence.
// Returns nil if not found.
func (b *receivedTxBuffer) get(signer []byte, seq uint64) *bufferedTx {
	if len(signer) == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	signerBuf, exists := b.buffers[string(signer)]
	if !exists {
		return nil
	}

	return signerBuf[seq]
}

// removeLowerSeqs deletes all buffered transactions with sequence <= seq
func (b *receivedTxBuffer) removeLowerSeqs(signer []byte, seq uint64) {
	b.removeIf(signer, func(e *bufferedTx) bool { return e.sequence <= seq })
}

// removeBelow deletes all buffered transactions with sequence < seq and
// returns their keys. Such txs can never be applied once the signer's
// expected sequence has reached seq.
func (b *receivedTxBuffer) removeBelow(signer []byte, seq uint64) []types.TxKey {
	return b.removeIf(signer, func(e *bufferedTx) bool { return e.sequence < seq })
}

// pruneOlderThan deletes all buffered transactions added before cutoff and
// returns their keys.
func (b *receivedTxBuffer) pruneOlderThan(cutoff time.Time) []types.TxKey {
	b.mu.Lock()
	defer b.mu.Unlock()

	removed := make([]types.TxKey, 0, len(b.buffers))
	for signerKey := range b.buffers {
		removed = append(removed, b.removeIfLocked(signerKey, func(e *bufferedTx) bool {
			return e.addedAt.Before(cutoff)
		})...)
	}
	return removed
}

func (b *receivedTxBuffer) removeIf(signer []byte, match func(*bufferedTx) bool) []types.TxKey {
	if len(signer) == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.removeIfLocked(string(signer), match)
}

// removeIfLocked deletes the signer's buffered txs that match and returns
// their keys. b.mu must be held.
func (b *receivedTxBuffer) removeIfLocked(signerKey string, match func(*bufferedTx) bool) []types.TxKey {
	signerBuf, exists := b.buffers[signerKey]
	if !exists {
		return nil
	}
	var removed []types.TxKey
	for entSeq, entry := range signerBuf {
		if !match(entry) {
			continue
		}
		// Decrement peer count before removing
		if entry.peerID != "" {
			b.countByPeer[entry.peerID]--
			if b.countByPeer[entry.peerID] <= 0 {
				delete(b.countByPeer, entry.peerID)
			}
		}
		b.totalBytes -= int64(len(entry.tx.Tx))
		delete(signerBuf, entSeq)
		removed = append(removed, entry.txKey)
	}

	// Clean up empty signer map
	if len(signerBuf) == 0 {
		delete(b.buffers, signerKey)
	}
	return removed
}

// signerKeys returns all signers that have buffered transactions
func (b *receivedTxBuffer) signerKeys() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffers) == 0 {
		return nil
	}

	signers := make([][]byte, 0, len(b.buffers))
	for signerKey := range b.buffers {
		signers = append(signers, []byte(signerKey))
	}
	return signers
}

// countForPeer returns the number of buffered transactions from a specific peer
func (b *receivedTxBuffer) countForPeer(peerID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.countByPeer[peerID]
}

// size returns the number of buffered transactions and their total bytes.
func (b *receivedTxBuffer) size() (int, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := 0
	for _, signerBuf := range b.buffers {
		n += len(signerBuf)
	}
	return n, b.totalBytes
}
