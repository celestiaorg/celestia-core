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
type receivedTxBuffer struct {
	mu sync.Mutex
	// buffers contains mapping: signer (as string) -> sequence -> buffered tx
	buffers map[string]map[uint64]*bufferedTx
	// countByPeer tracks how many transactions are buffered from each peer
	countByPeer map[string]int
}

// newReceivedTxBuffer creates a new buffer for out-of-order transactions
func newReceivedTxBuffer() *receivedTxBuffer {
	return &receivedTxBuffer{
		buffers:     make(map[string]map[uint64]*bufferedTx),
		countByPeer: make(map[string]int),
	}
}

// add stores a transaction in the buffer for later processing.
// Returns false if the buffer is full for this signer, peer is at capacity, or tx already exists.
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
	if len(signer) == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	signerKey := string(signer)
	signerBuf, exists := b.buffers[signerKey]
	if !exists {
		return
	}
	for entSeq, entry := range signerBuf {
		if entSeq <= seq {
			// Decrement peer count before removing
			if entry.peerID != "" {
				b.countByPeer[entry.peerID]--
				if b.countByPeer[entry.peerID] <= 0 {
					delete(b.countByPeer, entry.peerID)
				}
			}
			delete(signerBuf, entSeq)
		}
	}

	// Clean up empty signer map
	if len(signerBuf) == 0 {
		delete(b.buffers, signerKey)
	}
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

// SignerBufferStats holds statistics about a signer's buffer
type SignerBufferStats struct {
	Size   int
	MinSeq uint64
	MaxSeq uint64
}

// statsForSigner returns buffer statistics for a specific signer
func (b *receivedTxBuffer) statsForSigner(signer []byte) SignerBufferStats {
	if len(signer) == 0 {
		return SignerBufferStats{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	signerBuf, exists := b.buffers[string(signer)]
	if !exists || len(signerBuf) == 0 {
		return SignerBufferStats{}
	}

	var minSeq, maxSeq uint64
	first := true
	for seq := range signerBuf {
		if first {
			minSeq = seq
			maxSeq = seq
			first = false
		} else {
			if seq < minSeq {
				minSeq = seq
			}
			if seq > maxSeq {
				maxSeq = seq
			}
		}
	}

	return SignerBufferStats{
		Size:   len(signerBuf),
		MinSeq: minSeq,
		MaxSeq: maxSeq,
	}
}

// totalSigners returns the number of signers with buffered transactions
func (b *receivedTxBuffer) totalSigners() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.buffers)
}
