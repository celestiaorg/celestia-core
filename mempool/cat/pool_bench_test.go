package cat

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
)

func BenchmarkTxPool_CheckTx(b *testing.B) {
	txmp := setup(b, 10000)
	txmp.config.Size = b.N
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		b.StopTimer()
		prefix := make([]byte, 20)
		_, err := rng.Read(prefix)
		require.NoError(b, err)

		priority := int64(rng.Intn(9999-1000) + 1000)
		tx := []byte(fmt.Sprintf("sender%d=%X=%d", n, prefix, priority))
		b.StartTimer()

		require.NoError(b, txmp.CheckTx(tx, nil, mempool.TxInfo{}))
	}
}

// BenchmarkReapMaxBytesMaxGas reaps half the pool, so the budget boundary falls
// inside a multi-tx signer set unless every set holds one tx.
func BenchmarkReapMaxBytesMaxGas(b *testing.B) {
	for _, sets := range []int{1000, 2000, 4000, 8000} {
		for _, perSet := range []int{1, 4} {
			b.Run(fmt.Sprintf("sets=%d/txs-per-set=%d", sets, perSet), func(b *testing.B) {
				txmp := setup(b, 0)
				txmp.config.Size = sets * perSet
				var txLen int
				for s := 0; s < sets; s++ {
					for i := 0; i < perSet; i++ {
						tx := []byte(fmt.Sprintf("s%06d=%02d=%06d", s, i, sets-s))
						txLen = len(tx)
						require.NoError(b, txmp.CheckTx(tx, nil, mempool.TxInfo{}))
					}
				}
				maxBytes := int64(txLen * (sets*perSet/2 + 1))
				b.ResetTimer()
				var reaped int
				for n := 0; n < b.N; n++ {
					reaped = len(txmp.ReapMaxBytesMaxGas(maxBytes, -1))
				}
				b.ReportMetric(float64(reaped), "txs")
			})
		}
	}
}
