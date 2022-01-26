package types

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tmrand "github.com/tendermint/tendermint/libs/rand"
)

func makeTxs(cnt, size int) Txs {
	txs := make(Txs, cnt)
	for i := 0; i < cnt; i++ {
		txs[i] = tmrand.Bytes(size)
	}
	return txs
}

func randInt(low, high int) int {
	off := tmrand.Int() % (high - low)
	return low + off
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

func TestUnwrapMalleatedTx(t *testing.T) {
	// perform a simple test for being unable to decode a non
	// malleated transaction
	tx := Tx{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0}
	_, _, ok := UnwrapMalleatedTx(tx)
	require.False(t, ok)

	// create a proto message that used to be decoded when it shouldn't have
	randomBlock := MakeBlock(
		1,
		[]Tx{tx},
		nil,
		nil,
		[]Message{
			{
				NamespaceID: []byte{1, 2, 3, 4, 5, 6, 7, 8},
				Data:        []byte{1, 2, 3, 4, 5, 6, 7, 8, 9},
			},
		},
		&Commit{},
	)

	protoB, err := randomBlock.ToProto()
	require.NoError(t, err)

	rawBlock, err := protoB.Marshal()
	require.NoError(t, err)

	// due to protobuf not actually requiring type compatibility
	// we need to make sure that there is some check
	_, _, ok = UnwrapMalleatedTx(rawBlock)
	require.False(t, ok)

	pHash := sha256.Sum256(rawBlock)
	MalleatedTx, err := WrapMalleatedTx(pHash[:], rawBlock)
	require.NoError(t, err)

	// finally, ensure that the unwrapped bytes are identical to the input
	unwrappedHash, unwrapped, ok := UnwrapMalleatedTx(MalleatedTx)
	require.True(t, ok)
	assert.Equal(t, 32, len(unwrappedHash))
	require.Equal(t, rawBlock, []byte(unwrapped))
}
