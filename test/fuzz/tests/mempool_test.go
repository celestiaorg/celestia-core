//go:build gofuzz || go1.21

package tests

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	abciclient "github.com/cometbft/cometbft/abci/client"
	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat"
	cmttypes "github.com/cometbft/cometbft/types"
)

func FuzzMempool(f *testing.F) {
	const maxTxBytes = 4096
	for _, seed := range [][]byte{nil, []byte("key=value"), []byte("key:value"), []byte("val=invalid"), bytes.Repeat([]byte("x"), maxTxBytes+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		// Start each input with an empty pool and caches. Otherwise accepted
		// inputs eventually fill the pool and stop reaching ABCI CheckTx.
		app := kvstore.NewInMemoryApplication()
		t.Cleanup(func() { require.NoError(t, app.Close()) })
		conn := abciclient.NewLocalClient(new(cmtsync.Mutex), app)
		require.NoError(t, conn.Start())
		t.Cleanup(func() { require.NoError(t, conn.Stop()) })
		cfg := cmtcfg.DefaultMempoolConfig()
		cfg.Broadcast = false
		cfg.MaxTxBytes = maxTxBytes
		mp := cat.NewTxPool(log.NewNopLogger(), cfg, conn, 0)
		var response *abci.ResponseCheckTx
		err := mp.CheckTx(data, func(r *abci.ResponseCheckTx) { response = r }, mempool.TxInfo{})
		if len(data) > maxTxBytes {
			var tooLarge mempool.ErrTxTooLarge
			require.ErrorAs(t, err, &tooLarge)
			require.Nil(t, response)
		} else {
			require.NoError(t, err)
			require.NotNil(t, response)
			if response.Code == abci.CodeTypeOK {
				require.Equal(t, 1, mp.Size())
				require.Equal(t, int64(len(data)), mp.SizeBytes())
				tx, ok := mp.GetTxByKey(cmttypes.Tx(data).Key())
				require.True(t, ok)
				require.Equal(t, data, []byte(tx.Tx))
				require.ErrorIs(t, mp.CheckTx(data, nil, mempool.TxInfo{}), cat.ErrTxInMempool)
			} else {
				require.Zero(t, mp.Size())
				require.Zero(t, mp.SizeBytes())
			}
		}
		mp.Flush()
		require.Zero(t, mp.Size())
		require.Zero(t, mp.SizeBytes())
	})
}
