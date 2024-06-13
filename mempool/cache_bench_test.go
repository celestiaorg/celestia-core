package mempool

import (
	"crypto/rand"
	"encoding/binary"
	"testing"

	"github.com/cometbft/cometbft/types"
)

func BenchmarkCacheInsertTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	keys := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = make([]byte, 32)
		_, err := rand.Read(keys[i])
		if err != nil {
			b.Fatal(err)
		}
		binary.BigEndian.PutUint64(keys[i], uint64(i))
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cache.Push(types.TxKey(keys[i]))
	}
}

// This benchmark is probably skewed, since we actually will be removing
// txs in parallel, which may cause some overhead due to mutex locking.
func BenchmarkCacheRemoveTime(b *testing.B) {
	cache := NewLRUTxCache(b.N)

	keys := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = make([]byte, 32)
		_, err := rand.Read(keys[i])
		if err != nil {
			b.Fatal(err)
		}
		cache.Push(types.TxKey(keys[i]))
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cache.Remove(types.TxKey(keys[i]))
	}
}
