package cat

import (
	"sync"
	"time"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

const (
	// maxBufferedPerSigner limits buffered txs per signer to prevent memory attacks
	maxBufferedPerSigner = 64

	// bufferEntryTTL is how long a buffered tx lives before expiry
	bufferEntryTTL = 30 * time.Second
)

// bufferedTx holds a transaction that arrived out-of-order, waiting to be processed
type bufferedTx struct {
	tx       *types.CachedTx
	txKey    types.TxKey
	txInfo   mempool.TxInfo
	sequence uint64
	addedAt  time.Time
}

// receivedTxBuffer holds transactions that arrived out-of-order.
// Transactions are buffered by (signer, sequence) and processed in order
// once earlier sequences complete.
type receivedTxBuffer struct {
	mu sync.Mutex
	// signer (as string) -> sequence -> buffered tx
	buffers map[string]map[uint64]*bufferedTx
}

// newReceivedTxBuffer creates a new buffer for out-of-order transactions
func newReceivedTxBuffer() *receivedTxBuffer {
	return &receivedTxBuffer{
		buffers: make(map[string]map[uint64]*bufferedTx),
	}
}

// add stores a transaction in the buffer for later processing.
// Returns false if the buffer is full for this signer or tx already exists.
func (b *receivedTxBuffer) add(signer []byte, seq uint64, tx *types.CachedTx, txKey types.TxKey, txInfo mempool.TxInfo) bool {
	if len(signer) == 0 {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

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
	if len(signerBuf) >= maxBufferedPerSigner {
		return false
	}

	signerBuf[seq] = &bufferedTx{
		tx:       tx,
		txKey:    txKey,
		txInfo:   txInfo,
		sequence: seq,
		addedAt:  time.Now(),
	}
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

	signerKey := string(signer)
	signerBuf, exists := b.buffers[signerKey]
	if !exists {
		return nil
	}

	return signerBuf[seq]
}

// remove deletes a buffered transaction
func (b *receivedTxBuffer) remove(signer []byte, seq uint64) {
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

	delete(signerBuf, seq)

	// Clean up empty signer map
	if len(signerBuf) == 0 {
		delete(b.buffers, signerKey)
	}
}

// cleanup removes expired entries from all buffers
func (b *receivedTxBuffer) cleanup() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	for signerKey, signerBuf := range b.buffers {
		for seq, entry := range signerBuf {
			if now.Sub(entry.addedAt) > bufferEntryTTL {
				delete(signerBuf, seq)
			}
		}
		if len(signerBuf) == 0 {
			delete(b.buffers, signerKey)
		}
	}
}

// size returns total number of buffered transactions across all signers
func (b *receivedTxBuffer) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	total := 0
	for _, signerBuf := range b.buffers {
		total += len(signerBuf)
	}
	return total
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

// signerSize returns number of buffered transactions for a specific signer
func (b *receivedTxBuffer) signerSize(signer []byte) int {
	if len(signer) == 0 {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	signerKey := string(signer)
	if signerBuf, exists := b.buffers[signerKey]; exists {
		return len(signerBuf)
	}
	return 0
}
