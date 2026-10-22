package types

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	share "github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
)

func makeBlobTx(t testing.TB, blobSize int) Tx {
	t.Helper()
	data := make([]byte, blobSize)
	_, _ = rand.Read(data)
	ns := share.MustNewV0Namespace(bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize))
	blob, err := share.NewV0Blob(ns, data)
	require.NoError(t, err)
	inner := make([]byte, 300)
	_, _ = rand.Read(inner)
	tx, err := squaretx.MarshalBlobTx(inner, blob)
	require.NoError(t, err)
	return tx
}

func makePlainTxs(n, size int) Txs {
	txs := make(Txs, n)
	for i := range txs {
		txs[i] = make([]byte, size)
		_, _ = rand.Read(txs[i])
	}
	return txs
}

// mixedTxs returns plain, BlobTx and IndexWrapper txs.
func mixedTxs(t *testing.T) Txs {
	t.Helper()
	txs := makePlainTxs(20, 64)
	for range 5 {
		txs = append(txs, makeBlobTx(t, 4096))
	}
	iw, err := MarshalIndexWrapper(txs[0], 1, 2)
	require.NoError(t, err)
	return append(txs, iw)
}

func expectedHashes(txs Txs) [][]byte {
	hashes := make([][]byte, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash()
	}
	return hashes
}

func TestBlockFillCachedHashes(t *testing.T) {
	txs := mixedTxs(t)
	sentinel := make([][]byte, len(txs))
	for i := range sentinel {
		sentinel[i] = []byte{byte(i)}
	}

	tests := []struct {
		name   string
		txs    Txs
		cached [][]byte
		want   [][]byte
	}{
		{"empty block", Txs{}, nil, nil},
		{"no cached hashes", txs, nil, expectedHashes(txs)},
		{"already filled is kept", txs, sentinel, sentinel},
		{"wrong length is recomputed", txs, sentinel[:3], expectedHashes(txs)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &Block{Data: Data{Txs: tc.txs}}
			b.SetCachedHashes(tc.cached)
			b.FillCachedHashes()
			require.Equal(t, tc.want, b.CachedHashes())
		})
	}
}

// Mempool keys come from CachedTxs, so the cached hash must equal Tx.Key.
func TestBlockCachedTxsMatchKey(t *testing.T) {
	txs := mixedTxs(t)
	b := &Block{Data: Data{Txs: txs}}
	for i, ctx := range b.CachedTxs() {
		require.Equal(t, txs[i], ctx.Tx)
		require.Equal(t, txs[i].Key(), ctx.Key())
	}
}

func TestBlockFillCachedHashesSingleProc(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	txs := mixedTxs(t)
	b := &Block{Data: Data{Txs: txs}}
	b.FillCachedHashes()
	require.Equal(t, expectedHashes(txs), b.CachedHashes())
}

// BenchmarkFillCachedHashes compares hashing a ~30 MB block serially with FillCachedHashes.
func BenchmarkFillCachedHashes(b *testing.B) {
	shapes := []struct {
		name string
		txs  func() Txs
	}{
		{"blob_1MB_x30", func() Txs { return repeatBlobTxs(b, 30, 1<<20) }},
		{"blob_8MB_x4", func() Txs { return repeatBlobTxs(b, 4, 8<<20) }},
		{"plain_300B_x100k", func() Txs { return makePlainTxs(100_000, 300) }},
	}
	for _, s := range shapes {
		txs := s.txs()
		b.Run(fmt.Sprintf("%s/serial", s.name), func(b *testing.B) {
			for b.Loop() {
				_ = expectedHashes(txs)
			}
		})
		b.Run(fmt.Sprintf("%s/fill", s.name), func(b *testing.B) {
			for b.Loop() {
				blk := &Block{Data: Data{Txs: txs}}
				blk.FillCachedHashes()
			}
		})
	}
}

func repeatBlobTxs(b *testing.B, n, size int) Txs {
	txs := make(Txs, n)
	for i := range txs {
		txs[i] = makeBlobTx(b, size)
	}
	return txs
}
