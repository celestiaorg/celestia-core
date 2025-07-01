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
<<<<<<< HEAD
		cache.Push(&types.CachedTx{Tx: txs[i]})
=======
		cache.Push(txKeys[i])
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
	}
}

// This benchmark is probably skewed, since we actually will be removing
// txs in parallel, which may cause some overhead due to mutex locking.
func BenchmarkCacheRemoveTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	txKeys := make([]types.TxKey, b.N)
	for i := 0; i < b.N; i++ {
<<<<<<< HEAD
		txs[i] = make([]byte, 8)
		binary.BigEndian.PutUint64(txs[i], uint64(i))
		cache.Push(&types.CachedTx{Tx: txs[i]})
=======
		tx := make([]byte, 8)
		binary.BigEndian.PutUint64(tx, uint64(i))
		txKeys[i] = types.Tx(tx).Key()
		cache.Push(txKeys[i])
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
<<<<<<< HEAD
		cache.Remove(&types.CachedTx{Tx: txs[i]})
=======
		cache.Remove(txKeys[i])
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
	}
}
