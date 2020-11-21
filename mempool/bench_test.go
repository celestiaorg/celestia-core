package mempool

import (
	"testing"

	"github.com/lazyledger/lazyledger-core/abci/example/kvstore"
	"github.com/lazyledger/lazyledger-core/proxy"
	"github.com/lazyledger/lazyledger-core/types"
)

func BenchmarkReap(b *testing.B) {
	app := kvstore.NewApplication()
	cc := proxy.NewLocalClientCreator(app)
	mempool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	size := 10000
	for i := 0; i < size; i++ {
		tx := types.Tx{Value: make([]byte, 8)}
		// binary.BigEndian.PutUint64(tx, uint64(i))
		if err := mempool.CheckTx(tx, nil, TxInfo{}); err != nil {
			b.Error(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mempool.ReapMaxBytesMaxGas(100000000, 10000000)
	}
}

func BenchmarkCheckTx(b *testing.B) {
	app := kvstore.NewApplication()
	cc := proxy.NewLocalClientCreator(app)
	mempool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	for i := 0; i < b.N; i++ {
		tx := types.Tx{Value: make([]byte, 8)}
		// binary.BigEndian.PutUint64(tx, uint64(i))
		if err := mempool.CheckTx(tx, nil, TxInfo{}); err != nil {
			b.Error(err)
		}
	}
}

func BenchmarkCacheInsertTime(b *testing.B) {
	cache := newMapTxCache(b.N)
	txs := make([]types.Tx, b.N)
	for i := 0; i < b.N; i++ {
		txs[i] = types.Tx{Value: make([]byte, 8)}
		// binary.BigEndian.PutUint64(txs[i], uint64(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Push(txs[i])
	}
}

// This benchmark is probably skewed, since we actually will be removing
// txs in parallel, which may cause some overhead due to mutex locking.
func BenchmarkCacheRemoveTime(b *testing.B) {
	cache := newMapTxCache(b.N)
	txs := make([]types.Tx, b.N)
	for i := 0; i < b.N; i++ {
		txs[i] = types.Tx{Value: make([]byte, 8)}
		// binary.BigEndian.PutUint64(txs[i], uint64(i))
		cache.Push(txs[i])
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Remove(txs[i])
	}
}
