package mempool

import (
	"encoding/binary"
	"testing"

	"github.com/cometbft/cometbft/types"
)

func BenchmarkCacheInsertTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	txs := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		txs[i] = make([]byte, 8)
		binary.BigEndian.PutUint64(txs[i], uint64(i))
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cache.Push(&types.CachedTx{Tx: txs[i]})
	}
}

// This benchmark is probably skewed, since we actually will be removing
// txs in parallel, which may cause some overhead due to mutex locking.
func BenchmarkCacheRemoveTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	txs := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		txs[i] = make([]byte, 8)
		binary.BigEndian.PutUint64(txs[i], uint64(i))
		cache.Push(&types.CachedTx{Tx: txs[i]})
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cache.Remove(&types.CachedTx{Tx: txs[i]})
	}
}
