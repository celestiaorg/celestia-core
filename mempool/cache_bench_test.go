package mempool

import (
	"encoding/binary"
	"testing"

	"github.com/cometbft/cometbft/types"
)

func BenchmarkCacheInsertTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	txKeys := make([]types.TxKey, b.N)
	for i := 0; i < b.N; i++ {
		tx := make([]byte, 8)
		binary.BigEndian.PutUint64(tx, uint64(i))
		txKeys[i] = types.Tx(tx).Key()
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cache.Push(txKeys[i])
	}
}

// This benchmark is probably skewed, since we actually will be removing
// txs in parallel, which may cause some overhead due to mutex locking.
func BenchmarkCacheRemoveTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	txKeys := make([]types.TxKey, b.N)
	for i := 0; i < b.N; i++ {
		tx := make([]byte, 8)
		binary.BigEndian.PutUint64(tx, uint64(i))
		txKeys[i] = types.Tx(tx).Key()
		cache.Push(txKeys[i])
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cache.Remove(txKeys[i])
	}
}
