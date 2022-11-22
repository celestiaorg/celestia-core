package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tmrand "github.com/tendermint/tendermint/libs/rand"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

func makeTxs(cnt, size int) Txs {
	txs := make(Txs, cnt)
	for i := 0; i < cnt; i++ {
		txs[i] = tmrand.Bytes(size)
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

func TestUnwrapMalleatedTx(t *testing.T) {
	// perform a simple test for being unable to decode a non
	// malleated transaction
	tx := Tx{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0}
	_, ok := UnwrapMalleatedTx(tx)
	require.False(t, ok)

	data := Data{
		Txs: []Tx{tx},
		Evidence: EvidenceData{
			Evidence: nil,
		},
		Blobs: []Blob{
			{
				NamespaceID: []byte{1, 2, 3, 4, 5, 6, 7, 8},
				Data:        []byte{1, 2, 3, 4, 5, 6, 7, 8, 9},
			},
		},
	}

	// create a proto message that used to be decoded when it shouldn't have
	randomBlock := MakeBlock(
		1,
		data,
		&Commit{},
	)

	protoB, err := randomBlock.ToProto()
	require.NoError(t, err)

	rawBlock, err := protoB.Marshal()
	require.NoError(t, err)

	// due to protobuf not actually requiring type compatibility
	// we need to make sure that there is some check
	_, ok = UnwrapMalleatedTx(rawBlock)
	require.False(t, ok)

	MalleatedTx, err := WrapMalleatedTx(0, rawBlock)
	require.NoError(t, err)

	// finally, ensure that the unwrapped bytes are identical to the input
	malleated, ok := UnwrapMalleatedTx(MalleatedTx)
	require.True(t, ok)
	require.Equal(t, rawBlock, malleated.Tx)
}

func TestWrapUnwrapBlobTx(t *testing.T) {
	tx := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	blob := tmproto.Blob{
		NamespaceId:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Data:         []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		ShareVersion: 0,
	}

	bTx, err := WrapBlobTx(tx, &blob)
	require.NoError(t, err)

	resTx, isBlob := UnwrapBlobTx(bTx)
	require.True(t, isBlob)

	assert.Equal(t, tx, resTx.Tx)
	require.Len(t, resTx.Blobs, 1)
	assert.Equal(t, blob, *resTx.Blobs[0])
}

// todo: add fuzzing
func TestUnwrapBlobTxFalsePositive(t *testing.T) {
	tx := []byte("sender-193-0=D16B687628035716B1DA53BE1491A1B3D4CEA3AB=1025")
	_, isBlob := UnwrapBlobTx(tx)
	require.False(t, isBlob)
}
