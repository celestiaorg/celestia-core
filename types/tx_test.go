package types

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/celestiaorg/go-square/v2/share"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
	ctest "github.com/cometbft/cometbft/libs/test"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
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

func TestValidTxProof(t *testing.T) {
	cases := []struct {
		txs Txs
	}{
		{Txs{{1, 4, 34, 87, 163, 1}}},
		{Txs{{5, 56, 165, 2}, {4, 77}}},
		{Txs{Tx("foo"), Tx("bar"), Tx("baz")}},
		{makeTxs(20, 5)},
		{makeTxs(7, 81)},
		{makeTxs(61, 15)},
	}

	for h, tc := range cases {
		txs := tc.txs
		root := txs.Hash()
		// make sure valid proof for every tx
		for i := range txs {
			tx := []byte(txs[i])
			proof := txs.Proof(i)
			assert.EqualValues(t, i, proof.Proof.Index, "%d: %d", h, i)
			assert.EqualValues(t, len(txs), proof.Proof.Total, "%d: %d", h, i)
			assert.EqualValues(t, root, proof.RootHash, "%d: %d", h, i)
			assert.EqualValues(t, tx, proof.Data, "%d: %d", h, i)
			assert.EqualValues(t, txs[i].Hash(), proof.Leaf(), "%d: %d", h, i)
			assert.Nil(t, proof.Validate(root), "%d: %d", h, i)
			assert.NotNil(t, proof.Validate([]byte("foobar")), "%d: %d", h, i)

			// read-write must also work
			var (
				p2  TxProof
				pb2 cmtproto.TxProof
			)
			pbProof := proof.ToProto()
			bin, err := pbProof.Marshal()
			require.NoError(t, err)

			err = pb2.Unmarshal(bin)
			require.NoError(t, err)

			p2, err = TxProofFromProto(pb2)
			if assert.Nil(t, err, "%d: %d: %+v", h, i, err) {
				assert.Nil(t, p2.Validate(root), "%d: %d", h, i)
			}
		}
	}
}

func TestTxProofUnchangable(t *testing.T) {
	// run the other test a bunch...
	for i := 0; i < 40; i++ {
		testTxProofUnchangable(t)
	}
}

func testTxProofUnchangable(t *testing.T) {
	// make some proof
	txs := makeTxs(randInt(2, 100), randInt(16, 128))
	root := txs.Hash()
	i := randInt(0, len(txs)-1)
	proof := txs.Proof(i)

	// make sure it is valid to start with
	assert.Nil(t, proof.Validate(root))
	pbProof := proof.ToProto()
	bin, err := pbProof.Marshal()
	require.NoError(t, err)

	// try mutating the data and make sure nothing breaks
	for j := 0; j < 500; j++ {
		bad := ctest.MutateByteSlice(bin)
		if !bytes.Equal(bad, bin) {
			assertBadProof(t, root, bad, proof)
		}
	}
}

// This makes sure that the proof doesn't deserialize into something valid.
func assertBadProof(t *testing.T, root []byte, bad []byte, good TxProof) {

	var (
		proof   TxProof
		pbProof cmtproto.TxProof
	)
	err := pbProof.Unmarshal(bad)
	if err == nil {
		proof, err = TxProofFromProto(pbProof)
		if err == nil {
			err = proof.Validate(root)
			if err == nil {
				// XXX Fix simple merkle proofs so the following is *not* OK.
				// This can happen if we have a slightly different total (where the
				// path ends up the same). If it is something else, we have a real
				// problem.
				assert.NotEqual(t, proof.Proof.Total, good.Proof.Total, "bad: %#v\ngood: %#v", proof, good)
			}
		}
	}
}

func randInt(low, high int) int {
	return rand.Intn(high-low) + low
}

func TestUnmarshalIndexWrapper(t *testing.T) {
	// perform a simple test for being unable to decode a non
	// IndexWrapper transaction
	tx := Tx{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0}
	_, ok := UnmarshalIndexWrapper(tx)
	require.False(t, ok)

	data := Data{Txs: []Tx{tx}}

	// create a proto message that used to be decoded when it shouldn't have
	randomBlock := MakeBlock(
		1,
		data,
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

func TestUnmarshalBlobTx(t *testing.T) {
	tx := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	namespaceOne := bytes.Repeat([]byte{1}, share.NamespaceIDSize)
	blob := cmtproto.Blob{
		NamespaceId:      namespaceOne,
		Data:             []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		ShareVersion:     0,
		NamespaceVersion: 0,
	}
	bTx, err := MarshalBlobTx(tx, &blob)
	require.NoError(t, err)

	resTx, isBlob := UnmarshalBlobTx(bTx)
	require.True(t, isBlob)

	assert.Equal(t, tx, resTx.Tx)
	require.Len(t, resTx.Blobs, 1)
	assert.Equal(t, blob, *resTx.Blobs[0])
}

// todo: add fuzzing
func TestUnmarshalBlobTxFalsePositive(t *testing.T) {
	tx := []byte("sender-193-0=D16B687628035716B1DA53BE1491A1B3D4CEA3AB=1025")
	_, isBlob := UnmarshalBlobTx(tx)
	require.False(t, isBlob)
}
