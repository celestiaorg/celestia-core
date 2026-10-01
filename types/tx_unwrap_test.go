package types

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/celestiaorg/go-square/v3/share"
	square "github.com/celestiaorg/go-square/v3/tx"
	"github.com/cosmos/gogoproto/proto"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// unwrapReference is the implementation the scan replaced: decode as an
// IndexWrapper, then as a BlobTx, then give up. Every test below asserts the
// scan agrees with it.
func unwrapReference(tx Tx) []byte {
	if indexWrapper, isIndexWrapper := UnmarshalIndexWrapper(tx); isIndexWrapper {
		return indexWrapper.Tx
	}
	if blobTx, isBlobTx := UnmarshalBlobTx(tx); isBlobTx {
		return blobTx.Tx
	}
	return tx
}

func blobTxBytes(t *testing.T, inner []byte, blobs ...*cmtproto.Blob) []byte {
	t.Helper()
	bz, err := proto.Marshal(&cmtproto.BlobTx{Tx: inner, Blobs: blobs, TypeId: square.ProtoBlobTxTypeID})
	require.NoError(t, err)
	return bz
}

// blobBytes encodes one blob on its own.
func blobBytes(t *testing.T, b *cmtproto.Blob) []byte {
	t.Helper()
	bz, err := proto.Marshal(b)
	require.NoError(t, err)
	return bz
}

// blobTxWithRawBlob frames inner and an already encoded, possibly malformed,
// blob as a BlobTx by hand, so the test can reach bytes proto.Marshal would
// never produce.
func blobTxWithRawBlob(t *testing.T, inner, rawBlob []byte) []byte {
	t.Helper()
	var out []byte
	out = protowire.AppendTag(out, wrapperTxField, protowire.BytesType)
	out = protowire.AppendBytes(out, inner)
	out = protowire.AppendTag(out, wrapperPayloadFld, protowire.BytesType)
	out = protowire.AppendBytes(out, rawBlob)
	out = protowire.AppendTag(out, wrapperTypeIDField, protowire.BytesType)
	out = protowire.AppendString(out, square.ProtoBlobTxTypeID)
	return out
}

func blob(namespaceLen, dataLen int) *cmtproto.Blob {
	return &cmtproto.Blob{
		NamespaceId:      make([]byte, namespaceLen),
		Data:             make([]byte, dataLen),
		ShareVersion:     0,
		NamespaceVersion: 0,
	}
}

func TestUnwrapMatchesReference(t *testing.T) {
	inner := []byte("the inner sdk transaction")

	indexWrapper, err := proto.Marshal(&cmtproto.IndexWrapper{
		Tx:           inner,
		ShareIndexes: []uint32{1, 2, 3},
		TypeId:       square.ProtoIndexWrapperTypeID,
	})
	require.NoError(t, err)

	wrongTypeID, err := proto.Marshal(&cmtproto.BlobTx{
		Tx:     inner,
		Blobs:  []*cmtproto.Blob{blob(share.NamespaceIDSize, 8)},
		TypeId: "NOPE",
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		tx   []byte
	}{
		{"plain tx", []byte("not a wrapper at all")},
		{"empty tx", []byte{}},
		{"random bytes", []byte{0xff, 0x01, 0x02, 0x7f}},
		{"blob tx", blobTxBytes(t, inner, blob(share.NamespaceIDSize, 32))},
		{"blob tx, several blobs", blobTxBytes(t, inner, blob(share.NamespaceIDSize, 4), blob(share.NamespaceIDSize, 1024))},
		{"blob tx, no blobs", blobTxBytes(t, inner)},
		{"blob tx, short namespace", blobTxBytes(t, inner, blob(share.NamespaceIDSize-1, 32))},
		{"blob tx, empty namespace", blobTxBytes(t, inner, blob(0, 32))},
		{"blob tx, no inner tx", blobTxBytes(t, nil, blob(share.NamespaceIDSize, 32))},
		{"index wrapper", indexWrapper},
		{"wrong type id", wrongTypeID},
		{"truncated blob tx", blobTxBytes(t, inner, blob(share.NamespaceIDSize, 32))[:10]},
		{"blob with wrong wire type on a known field", blobTxWithRawBlob(t, inner, append(blobBytes(t, blob(share.NamespaceIDSize, 4)), 0x22, 0x02, 0x30, 0x30))},
		{"blob with a group field", blobTxWithRawBlob(t, inner, append(blobBytes(t, blob(share.NamespaceIDSize, 4)), 0x2b, 0x2c))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, unwrapReference(tc.tx), unwrap(tc.tx))
		})
	}
}

// TestHashAndKeyMatchReference checks the two exported entry points, not just
// the helper, because they are what the mempool key and the tx index are built
// from.
func TestHashAndKeyMatchReference(t *testing.T) {
	inner := []byte("inner")
	txs := [][]byte{
		[]byte("plain"),
		blobTxBytes(t, inner, blob(share.NamespaceIDSize, 64)),
		blobTxBytes(t, inner),
	}

	for _, tx := range txs {
		expected := sha256.Sum256(unwrapReference(tx))
		require.Equal(t, expected[:], Tx(tx).Hash())
		require.Equal(t, TxKey(expected), Tx(tx).Key())
	}
}

// FuzzUnwrap throws arbitrary bytes at the scan and requires it to agree with
// the decoder on every one of them. The scan is only allowed to be faster, not
// different.
func FuzzUnwrap(f *testing.F) {
	inner := []byte("inner tx bytes")
	bz, err := proto.Marshal(&cmtproto.BlobTx{
		Tx:     inner,
		Blobs:  []*cmtproto.Blob{{NamespaceId: make([]byte, share.NamespaceIDSize), Data: []byte("blob")}},
		TypeId: square.ProtoBlobTxTypeID,
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(bz)

	wrapped, err := proto.Marshal(&cmtproto.IndexWrapper{
		Tx:           inner,
		ShareIndexes: []uint32{7},
		TypeId:       square.ProtoIndexWrapperTypeID,
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(wrapped)
	f.Add([]byte("plain transaction"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, tx []byte) {
		require.Equal(t, unwrapReference(tx), unwrap(tx))
	})
}
