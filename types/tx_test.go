package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
)

func makeTxs(cnt, size int) Txs {
	txs := make(Txs, cnt)
	for i := 0; i < cnt; i++ {
		txs[i] = cmtrand.Bytes(size)
	}
	return txs
}

func TestTxIndex(t *testing.T) {
	for i := 0; i < 20; i++ {
		txs := makeTxs(15, 60)
		for j := 0; j < len(txs); j++ {
			tx := txs[j]
			idx := txs.Index(tx)
			assert.Equal(t, j, idx)
		}
		assert.Equal(t, -1, txs.Index(nil))
		assert.Equal(t, -1, txs.Index(Tx("foodnwkf")))
	}
}

func TestTxIndexByHash(t *testing.T) {
	for i := 0; i < 20; i++ {
		txs := makeTxs(15, 60)
		for j := 0; j < len(txs); j++ {
			tx := txs[j]
			idx := txs.IndexByHash(tx.Hash())
			assert.Equal(t, j, idx)
		}
		assert.Equal(t, -1, txs.IndexByHash(nil))
		assert.Equal(t, -1, txs.IndexByHash(Tx("foodnwkf").Hash()))
	}
}

func TestUnmarshalIndexWrapper(t *testing.T) {
	// perform a simple test for being unable to decode a non
	// IndexWrapper transaction
	tx := Tx{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0}
	_, ok := UnmarshalIndexWrapper(tx)
	require.False(t, ok)

	// create a proto message that used to be decoded when it shouldn't have
	randomBlock := MakeBlock(
		1,
		[]Tx{tx},
		&Commit{},
		[]Evidence{},
	)

	protoB, err := randomBlock.ToProto()
	require.NoError(t, err)

	rawBlock, err := protoB.Marshal()
	require.NoError(t, err)

	// due to protobuf not actually requiring type compatibility
	// we need to make sure that there is some check
	_, ok = UnmarshalIndexWrapper(rawBlock)
	require.False(t, ok)

	IndexWrapper, err := MarshalIndexWrapper(rawBlock, 0)
	require.NoError(t, err)

	// finally, ensure that the unwrapped bytes are identical to the input
	indexWrapper, ok := UnmarshalIndexWrapper(IndexWrapper)
	require.True(t, ok)
	require.Equal(t, rawBlock, indexWrapper.Tx)
}

func TestTxKeyFromBytes(t *testing.T) {
	tx := Tx("hello")
	key := tx.Key()
	key2, err := TxKeyFromBytes(key[:])
	require.NoError(t, err)
	require.Equal(t, key, key2)
	_, err = TxKeyFromBytes([]byte("foo"))
	require.Error(t, err)
}
