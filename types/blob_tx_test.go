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

// TestUnwrapBlobTx checks which BlobTxs UnwrapBlobTx accepts. The v042x column
// records what core v0.42.x and earlier accepted with their own decoder; rows
// where it differs are the BlobTxs whose hash and FinalizeBlock bytes change.
func TestUnwrapBlobTx(t *testing.T) {
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
		// v042x reports whether core v0.42.x unwrapped the tx; goSquare whether
		// UnwrapBlobTx does.
		v042x, goSquare bool
	}{
		{
			name:  "valid v0 blob",
			tx:    valid,
			v042x: true, goSquare: true,
		},
		{
			name: "valid v1 blob with signer",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = uint32(share.ShareVersionOne)
				b.Signer = signer
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: true, goSquare: true,
		},
		{
			name: "namespace ID too short",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceId = nsID[1:]
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: false, goSquare: false,
		},
		{
			name: "v0 namespace without zero prefix",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceId = bytes.Repeat([]byte{1}, share.NamespaceIDSize)
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: true, goSquare: false,
		},
		{
			name: "unsupported namespace version",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceVersion = 1
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: true, goSquare: false,
		},
		{
			name: "unsupported share version",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = 7
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: true, goSquare: false,
		},
		{
			name: "v1 blob without signer",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = uint32(share.ShareVersionOne)
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: true, goSquare: false,
		},
		{
			name: "empty blob data",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.Data = nil
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			v042x: true, goSquare: false,
		},
		{
			name:  "no blobs",
			tx:    func(t *testing.T) []byte { return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID) },
			v042x: false, goSquare: false,
		},
		{
			name:  "wrong type ID",
			tx:    func(t *testing.T) []byte { return marshal(t, innerTx, "BLOX", validBlob()) },
			v042x: false, goSquare: false,
		},
		{
			name:  "truncated protobuf",
			tx:    func(t *testing.T) []byte { bz := valid(t); return bz[:len(bz)-1] },
			v042x: false, goSquare: false,
		},
		{
			name:  "non-protobuf bytes",
			tx:    func(*testing.T) []byte { return []byte("sender-193-0=D16B687628035716B1DA53BE1491A1B3D4CEA3AB=1025") },
			v042x: false, goSquare: false,
		},
		{
			name: "unknown BlobTx field",
			tx: func(t *testing.T) []byte {
				return protowire.AppendVarint(protowire.AppendTag(valid(t), 15, protowire.VarintType), 1)
			},
			v042x: true, goSquare: false,
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
			v042x: true, goSquare: false,
		},
		{
			name: "duplicate type ID field",
			tx: func(t *testing.T) []byte {
				return protowire.AppendString(protowire.AppendTag(valid(t), 3, protowire.BytesType), squaretx.ProtoBlobTxTypeID)
			},
			v042x: true, goSquare: false,
		},
		{
			name: "known field with wrong wire type",
			tx: func(t *testing.T) []byte {
				return protowire.AppendVarint(protowire.AppendTag(valid(t), 1, protowire.VarintType), 1)
			},
			v042x: false, goSquare: false,
		},
		{
			name: "nested blob tx",
			tx: func(t *testing.T) []byte {
				return marshal(t, valid(t), squaretx.ProtoBlobTxTypeID, validBlob())
			},
			v042x: true, goSquare: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tx := Tx(tc.tx(t))

			got, ok := UnwrapBlobTx(tx)
			require.Equal(t, tc.goSquare, ok)
			_, isBlob, err := squaretx.UnmarshalBlobTx(tx)
			require.Equal(t, isBlob && err == nil, ok, "must match go-square")

			want := []byte(tx)
			if ok {
				want = innerTx
			}
			assert.Equal(t, Tx(want), got)
			assert.Equal(t, tmhash.Sum(want), tx.Hash())
			assert.Equal(t, TxKey(sha256.Sum256(want)), tx.Key())
		})
	}
}
