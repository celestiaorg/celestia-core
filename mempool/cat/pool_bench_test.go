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
