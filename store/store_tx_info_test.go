package store

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	share "github.com/celestiaorg/go-square/v3/share"
	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func makeBlobTx(t testing.TB, blobSize int) types.Tx {
	t.Helper()
	data := make([]byte, blobSize)
	_, _ = rand.Read(data)
	blob := &cmtproto.Blob{NamespaceId: bytes.Repeat([]byte{1}, share.NamespaceIDSize), Data: data}
	inner := make([]byte, 300)
	_, _ = rand.Read(inner)
	tx, err := types.MarshalBlobTx(inner, blob)
	require.NoError(t, err)
	return tx
}

func okResults(n int) []*abci.ExecTxResult {
	res := make([]*abci.ExecTxResult, n)
	for i := range res {
		res[i] = &abci.ExecTxResult{Code: abci.CodeTypeOK, GasWanted: 10, GasUsed: 5}
	}
	return res
}

func TestSaveTxInfoCachedHashes(t *testing.T) {
	txs := types.Txs{[]byte("a"), []byte("b"), makeBlobTx(t, 1024)}
	sentinel := [][]byte{[]byte("h0"), []byte("h1"), []byte("h2")}
	computed := make([][]byte, len(txs))
	for i, tx := range txs {
		computed[i] = tx.Hash()
	}

	tests := []struct {
		name    string
		cached  [][]byte
		indexed [][]byte
	}{
		// SaveTxInfo must index under the cached hashes without rehashing.
		{"cached hashes are used as is", sentinel, sentinel},
		{"no cached hashes are computed", nil, computed},
		{"wrong length is recomputed", sentinel[:1], computed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bs, _ := newInMemoryBlockStore()
			block := &types.Block{Header: types.Header{Height: 3}, Data: types.Data{Txs: txs}}
			block.SetCachedHashes(tc.cached)

			require.NoError(t, bs.SaveTxInfo(block, okResults(len(txs))))
			for i, h := range tc.indexed {
				info := bs.LoadTxInfo(h)
				require.NotNil(t, info, "tx %d not indexed under %X", i, h)
				require.Equal(t, uint32(i), info.Index)
				require.Equal(t, int64(3), info.Height)
			}
		})
	}
}

// BenchmarkSaveTxInfo measures SaveTxInfo on a ~30 MB block over pebble, with
// and without hashes cached at block assembly.
func BenchmarkSaveTxInfo(b *testing.B) {
	shapes := []struct {
		name string
		txs  func() types.Txs
	}{
		{"blob_1MB_x30", func() types.Txs { return blobTxs(b, 30, 1<<20) }},
		{"blob_8MB_x4", func() types.Txs { return blobTxs(b, 4, 8<<20) }},
		{"plain_300B_x100k", func() types.Txs { return plainTxs(100_000, 300) }},
	}
	db, err := dbm.NewDB("blockstore", dbm.PebbleDBBackend, b.TempDir())
	require.NoError(b, err)
	defer db.Close()
	bs := NewBlockStore(db)

	for _, s := range shapes {
		txs := s.txs()
		results := okResults(len(txs))
		for _, cached := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/cached=%t", s.name, cached), func(b *testing.B) {
				block := &types.Block{Header: types.Header{Height: 1}, Data: types.Data{Txs: txs}}
				if cached {
					block.FillCachedHashes()
				}
				hashes := block.CachedHashes()
				for b.Loop() {
					block.SetCachedHashes(hashes)
					require.NoError(b, bs.SaveTxInfo(block, results))
				}
			})
		}
	}
}

func blobTxs(b *testing.B, n, size int) types.Txs {
	txs := make(types.Txs, n)
	for i := range txs {
		txs[i] = makeBlobTx(b, size)
	}
	return txs
}

func plainTxs(n, size int) types.Txs {
	txs := make(types.Txs, n)
	for i := range txs {
		txs[i] = make([]byte, size)
		_, _ = rand.Read(txs[i])
	}
	return txs
}
