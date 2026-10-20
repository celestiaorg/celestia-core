package main

import (
	"bytes"
	"testing"

	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestScanBlock(t *testing.T) {
	ns := share.MustNewV0Namespace(bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize))
	blob, err := share.NewV0Blob(ns, []byte("data"))
	require.NoError(t, err)
	valid, err := squaretx.MarshalBlobTx([]byte("inner"), blob)
	require.NoError(t, err)
	unknownField := append(append([]byte{}, valid...), 0x78, 0x01)

	block := &types.Block{
		Header: types.Header{Height: 7},
		Data:   types.Data{Txs: types.Txs{[]byte("plain-tx"), valid, unknownField}},
	}
	block.Version.App = 3
	s := &stats{ByAppVersion: map[uint64]int64{}, ByReason: map[string]int64{}}

	findings := scanBlock(block, 4096, s)
	require.Len(t, findings, 1)
	f := findings[0]
	require.Equal(t, int64(7), f.Height)
	require.Equal(t, 2, f.Index)
	require.Equal(t, uint64(3), f.AppVersion)
	require.True(t, f.LegacyOK)
	require.False(t, f.SquareOK)
	require.Equal(t, hash([]byte("inner")), f.LegacyHash)
	require.Equal(t, hash(unknownField), f.SquareHash)
	require.NotEmpty(t, f.RawTx)

	require.Equal(t, int64(3), s.Txs)
	require.Equal(t, int64(2), s.LegacyBlobs)
	require.Equal(t, int64(1), s.SquareBlobs)
	require.Equal(t, int64(1), s.ByReason[squaretx.ErrNonCanonicalBlobTx.Error()])
}
