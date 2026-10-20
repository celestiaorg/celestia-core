package types

import (
	"bytes"
	"crypto/sha256"
	"testing"

	blobv4 "github.com/celestiaorg/go-square/v4/proto/blob/v4"
	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/cometbft/cometbft/crypto/tmhash"
)

// TestUnmarshalBlobTxDifferential compares core's UnmarshalBlobTx with the
// go-square decoder that celestia-app uses on the same bytes. Rows where they
// disagree are BlobTxs that core unwraps but go-square rejects.
func TestUnmarshalBlobTxDifferential(t *testing.T) {
	innerTx := []byte("inner-tx")
	nsID := append(make([]byte, share.NamespaceVersionZeroPrefixSize), bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize)...)
	signer := bytes.Repeat([]byte{2}, share.SignerSize)

	validBlob := func() *blobv4.BlobProto {
		return &blobv4.BlobProto{NamespaceId: nsID, Data: []byte("data")}
	}
	marshal := func(t *testing.T, tx []byte, typeID string, blobs ...*blobv4.BlobProto) []byte {
		bz, err := proto.Marshal(&blobv4.BlobTx{Tx: tx, Blobs: blobs, TypeId: typeID})
		require.NoError(t, err)
		return bz
	}
	valid := func(t *testing.T) []byte { return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, validBlob()) }

	testCases := []struct {
		name string
		tx   func(t *testing.T) []byte
		// core and goSquare report whether each decoder accepts the tx as a
		// valid BlobTx.
		core, goSquare bool
	}{
		{
			name: "valid v0 blob",
			tx:   valid,
			core: true, goSquare: true,
		},
		{
			name: "valid v1 blob with signer",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = uint32(share.ShareVersionOne)
				b.Signer = signer
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: true, goSquare: true,
		},
		{
			name: "namespace ID too short",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceId = nsID[1:]
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: false, goSquare: false,
		},
		{
			name: "v0 namespace without zero prefix",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceId = bytes.Repeat([]byte{1}, share.NamespaceIDSize)
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: true, goSquare: false,
		},
		{
			name: "unsupported namespace version",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceVersion = 1
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: true, goSquare: false,
		},
		{
			name: "unsupported share version",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = 7
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: true, goSquare: false,
		},
		{
			name: "v1 blob without signer",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = uint32(share.ShareVersionOne)
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: true, goSquare: false,
		},
		{
			name: "empty blob data",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.Data = nil
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			core: true, goSquare: false,
		},
		{
			name: "no blobs",
			tx:   func(t *testing.T) []byte { return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID) },
			core: false, goSquare: false,
		},
		{
			name: "wrong type ID",
			tx:   func(t *testing.T) []byte { return marshal(t, innerTx, "BLOX", validBlob()) },
			core: false, goSquare: false,
		},
		{
			name: "truncated protobuf",
			tx:   func(t *testing.T) []byte { bz := valid(t); return bz[:len(bz)-1] },
			core: false, goSquare: false,
		},
		{
			name: "non-protobuf bytes",
			tx:   func(*testing.T) []byte { return []byte("sender-193-0=D16B687628035716B1DA53BE1491A1B3D4CEA3AB=1025") },
			core: false, goSquare: false,
		},
		{
			name: "unknown BlobTx field",
			tx: func(t *testing.T) []byte {
				return protowire.AppendVarint(protowire.AppendTag(valid(t), 15, protowire.VarintType), 1)
			},
			core: true, goSquare: false,
		},
		{
			name: "unknown blob field",
			tx: func(t *testing.T) []byte {
				blob, err := proto.Marshal(validBlob())
				require.NoError(t, err)
				blob = protowire.AppendVarint(protowire.AppendTag(blob, 15, protowire.VarintType), 1)
				bz := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), innerTx)
				bz = protowire.AppendBytes(protowire.AppendTag(bz, 2, protowire.BytesType), blob)
				return protowire.AppendString(protowire.AppendTag(bz, 3, protowire.BytesType), squaretx.ProtoBlobTxTypeID)
			},
			core: true, goSquare: false,
		},
		{
			name: "duplicate type ID field",
			tx: func(t *testing.T) []byte {
				return protowire.AppendString(protowire.AppendTag(valid(t), 3, protowire.BytesType), squaretx.ProtoBlobTxTypeID)
			},
			core: true, goSquare: false,
		},
		{
			name: "known field with wrong wire type",
			tx: func(t *testing.T) []byte {
				return protowire.AppendVarint(protowire.AppendTag(valid(t), 1, protowire.VarintType), 1)
			},
			core: false, goSquare: false,
		},
		{
			name: "nested blob tx",
			tx: func(t *testing.T) []byte {
				return marshal(t, valid(t), squaretx.ProtoBlobTxTypeID, validBlob())
			},
			core: true, goSquare: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tx := Tx(tc.tx(t))

			coreTx, coreOK := UnmarshalBlobTx(tx)
			squareTx, isBlob, err := squaretx.UnmarshalBlobTx(tx)
			squareOK := isBlob && err == nil
			require.Equal(t, tc.core, coreOK, "core decoder")
			require.Equal(t, tc.goSquare, squareOK, "go-square decoder")
			if coreOK && squareOK {
				assert.Equal(t, squareTx.Tx, coreTx.Tx)
			}

			// Hash and Key unwrap with core's decoder.
			inner := []byte(tx)
			if coreOK {
				inner = coreTx.Tx
			}
			assert.Equal(t, tmhash.Sum(inner), tx.Hash())
			assert.Equal(t, TxKey(sha256.Sum256(inner)), tx.Key())
		})
	}
}
