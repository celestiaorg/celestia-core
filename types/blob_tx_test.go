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

// TestUnwrapBlobTxDifferential compares the legacy and go-square BlobTx rules
// on the same bytes. Rows where the rules disagree are the transactions whose
// handling changes at BlobTxValidationAppVersion.
func TestUnwrapBlobTxDifferential(t *testing.T) {
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
		// legacy and strict report whether each rule set unwraps the tx.
		legacy, strict bool
		// inner returns the tx the legacy rules unwrap to. It defaults to
		// innerTx when they accept the tx, and to the tx itself otherwise.
		inner func(t *testing.T) []byte
	}{
		{
			name:   "valid v0 blob",
			tx:     valid,
			legacy: true, strict: true,
		},
		{
			name: "valid v1 blob with signer",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = uint32(share.ShareVersionOne)
				b.Signer = signer
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: true, strict: true,
		},
		{
			name: "namespace ID too short",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceId = nsID[1:]
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: false, strict: false,
		},
		{
			name: "v0 namespace without zero prefix",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceId = bytes.Repeat([]byte{1}, share.NamespaceIDSize)
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: true, strict: false,
		},
		{
			name: "unsupported namespace version",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.NamespaceVersion = 1
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: true, strict: false,
		},
		{
			name: "unsupported share version",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = 7
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: true, strict: false,
		},
		{
			name: "v1 blob without signer",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.ShareVersion = uint32(share.ShareVersionOne)
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: true, strict: false,
		},
		{
			name: "empty blob data",
			tx: func(t *testing.T) []byte {
				b := validBlob()
				b.Data = nil
				return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID, b)
			},
			legacy: true, strict: false,
		},
		{
			name:   "no blobs",
			tx:     func(t *testing.T) []byte { return marshal(t, innerTx, squaretx.ProtoBlobTxTypeID) },
			legacy: false, strict: false,
		},
		{
			name:   "wrong type ID",
			tx:     func(t *testing.T) []byte { return marshal(t, innerTx, "BLOX", validBlob()) },
			legacy: false, strict: false,
		},
		{
			name:   "truncated protobuf",
			tx:     func(t *testing.T) []byte { bz := valid(t); return bz[:len(bz)-1] },
			legacy: false, strict: false,
		},
		{
			name:   "non-protobuf bytes",
			tx:     func(*testing.T) []byte { return []byte("sender-193-0=D16B687628035716B1DA53BE1491A1B3D4CEA3AB=1025") },
			legacy: false, strict: false,
		},
		{
			name: "unknown BlobTx field",
			tx: func(t *testing.T) []byte {
				return protowire.AppendVarint(protowire.AppendTag(valid(t), 15, protowire.VarintType), 1)
			},
			legacy: true, strict: false,
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
			legacy: true, strict: false,
		},
		{
			name: "duplicate type ID field",
			tx: func(t *testing.T) []byte {
				return protowire.AppendString(protowire.AppendTag(valid(t), 3, protowire.BytesType), squaretx.ProtoBlobTxTypeID)
			},
			legacy: true, strict: false,
		},
		{
			name: "known field with wrong wire type",
			tx: func(t *testing.T) []byte {
				return protowire.AppendVarint(protowire.AppendTag(valid(t), 1, protowire.VarintType), 1)
			},
			legacy: false, strict: false,
		},
		{
			name: "nested blob tx",
			tx: func(t *testing.T) []byte {
				return marshal(t, valid(t), squaretx.ProtoBlobTxTypeID, validBlob())
			},
			legacy: true, strict: false,
			inner: valid,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tx := Tx(tc.tx(t))

			legacyTx, legacyOK := UnwrapBlobTx(tx, BlobTxValidationAppVersion-1)
			strictTx, strictOK := UnwrapBlobTx(tx, BlobTxValidationAppVersion)
			require.Equal(t, tc.legacy, legacyOK, "legacy rules")
			require.Equal(t, tc.strict, strictOK, "go-square rules")

			// The legacy rules unwrap exactly one layer.
			wantLegacy := []byte(tx)
			switch {
			case tc.inner != nil:
				wantLegacy = tc.inner(t)
			case tc.legacy:
				wantLegacy = innerTx
			}
			assert.Equal(t, Tx(wantLegacy), legacyTx)

			wantStrict := []byte(tx)
			if tc.strict {
				wantStrict = innerTx
			}
			assert.Equal(t, Tx(wantStrict), strictTx)

			// Hash and Key always use the legacy rules.
			assert.Equal(t, tmhash.Sum(wantLegacy), tx.Hash())
			assert.Equal(t, TxKey(sha256.Sum256(wantLegacy)), tx.Key())
		})
	}
}

// legacyBlobTxSeeds returns BlobTxs, IndexWrappers and malformed envelopes to
// seed the differential fuzz test.
func legacyBlobTxSeeds(tb testing.TB) [][]byte {
	tb.Helper()
	nsID := append(make([]byte, share.NamespaceVersionZeroPrefixSize), bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize)...)
	blob := &blobv4.BlobProto{NamespaceId: nsID, Data: []byte("data")}
	marshal := func(msg proto.Message) []byte {
		bz, err := proto.Marshal(msg)
		require.NoError(tb, err)
		return bz
	}
	valid := marshal(&blobv4.BlobTx{Tx: []byte("inner-tx"), Blobs: []*blobv4.BlobProto{blob, blob}, TypeId: squaretx.ProtoBlobTxTypeID})
	indexWrapper, err := MarshalIndexWrapper([]byte("inner-tx"), 1, 2, 3)
	require.NoError(tb, err)
	return [][]byte{
		nil,
		[]byte("not a protobuf"),
		valid,
		valid[:len(valid)-1],
		marshal(&blobv4.BlobTx{Tx: []byte("inner-tx"), TypeId: squaretx.ProtoBlobTxTypeID}),
		marshal(&blobv4.BlobTx{Blobs: []*blobv4.BlobProto{blob}, TypeId: squaretx.ProtoBlobTxTypeID}),
		marshal(&blobv4.BlobTx{Tx: valid, Blobs: []*blobv4.BlobProto{blob}, TypeId: squaretx.ProtoBlobTxTypeID}),
		marshal(&blobv4.BlobTx{Tx: []byte("inner-tx"), Blobs: []*blobv4.BlobProto{{NamespaceId: nsID[1:]}}, TypeId: squaretx.ProtoBlobTxTypeID}),
		protowire.AppendVarint(protowire.AppendTag(valid, 15, protowire.VarintType), 1),
		protowire.AppendVarint(protowire.AppendTag(valid, 1, protowire.VarintType), 1),
		protowire.AppendTag(protowire.AppendTag(valid, 15, protowire.StartGroupType), 15, protowire.EndGroupType),
		indexWrapper,
	}
}

// FuzzUnwrapLegacyBlobTx checks that the zero-copy scan agrees with the
// generated decoder on every input, so Tx.Hash, Tx.Key and the bytes sent to
// the application before BlobTxValidationAppVersion are unchanged.
func FuzzUnwrapLegacyBlobTx(f *testing.F) {
	for _, seed := range legacyBlobTxSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, bz []byte) {
		tx := Tx(bz)
		wantTx, wantOK := decodeLegacyBlobTx(tx)

		// When the scan decides on its own, it must agree with the decoder.
		if inner, isBlobTx, ok := scanLegacyBlobTx(tx); ok {
			require.Equal(t, wantOK, isBlobTx, "scan verdict")
			if isBlobTx {
				require.Equal(t, []byte(wantTx), inner, "scan inner tx")
				require.Equal(t, wantTx == nil, inner == nil, "scan inner tx nil-ness")
			}
		}

		gotTx, gotOK := unwrapLegacyBlobTx(tx)
		require.Equal(t, wantOK, gotOK)
		require.Equal(t, wantTx, gotTx)

		// Hash and Key as computed before the scan was introduced.
		want := []byte(tx)
		if wantOK {
			want = wantTx
		} else if iw, ok := UnmarshalIndexWrapper(tx); ok {
			want = iw.Tx
		}
		require.Equal(t, tmhash.Sum(want), tx.Hash())
		require.Equal(t, TxKey(sha256.Sum256(want)), tx.Key())
	})
}

// BenchmarkTxHashBlobTx hashes an 8 MiB BlobTx carrying many blobs with the
// zero-copy scan and with the generated decoder it replaces.
func BenchmarkTxHashBlobTx(b *testing.B) {
	const (
		blobCount = 64
		blobSize  = 8 << 20 / blobCount
	)
	nsID := append(make([]byte, share.NamespaceVersionZeroPrefixSize), bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize)...)
	blobs := make([]*blobv4.BlobProto, blobCount)
	for i := range blobs {
		blobs[i] = &blobv4.BlobProto{NamespaceId: nsID, Data: bytes.Repeat([]byte{byte(i)}, blobSize)}
	}
	bz, err := proto.Marshal(&blobv4.BlobTx{Tx: bytes.Repeat([]byte{1}, 400), Blobs: blobs, TypeId: squaretx.ProtoBlobTxTypeID})
	require.NoError(b, err)
	tx := Tx(bz)

	b.Run("scan", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			inner, _ := unwrapLegacyBlobTx(tx)
			tmhash.Sum(inner)
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			inner, _ := decodeLegacyBlobTx(tx)
			tmhash.Sum(inner)
		}
	})
}

// TestUnwrapBlobTxDoesNotAlias checks that the inner tx UnwrapBlobTx returns
// does not share memory with the BlobTx at any app version, so callers that
// keep it, such as queued tx events, do not keep the blobs alive.
func TestUnwrapBlobTxDoesNotAlias(t *testing.T) {
	nsID := append(make([]byte, share.NamespaceVersionZeroPrefixSize), bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize)...)
	blob := &blobv4.BlobProto{NamespaceId: nsID, Data: []byte("data")}
	tx, err := proto.Marshal(&blobv4.BlobTx{Tx: []byte("inner-tx"), Blobs: []*blobv4.BlobProto{blob}, TypeId: squaretx.ProtoBlobTxTypeID})
	require.NoError(t, err)

	for _, appVersion := range []uint64{BlobTxValidationAppVersion - 1, BlobTxValidationAppVersion} {
		bz := bytes.Clone(tx)
		inner, isBlobTx := UnwrapBlobTx(bz, appVersion)
		require.True(t, isBlobTx)
		require.Equal(t, Tx("inner-tx"), inner)

		for i := range bz {
			bz[i] = 0
		}
		require.Equal(t, Tx("inner-tx"), inner, "app version %d: inner tx aliases the BlobTx", appVersion)
	}
}

// BenchmarkUnwrapBlobTx unwraps an 8 MiB BlobTx carrying many blobs with the
// rules of each app version, and with the copying go-square decoder that the
// no-copy one replaces at BlobTxValidationAppVersion.
func BenchmarkUnwrapBlobTx(b *testing.B) {
	const (
		blobCount = 64
		blobSize  = 8 << 20 / blobCount
	)
	nsID := append(make([]byte, share.NamespaceVersionZeroPrefixSize), bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize)...)
	blobs := make([]*blobv4.BlobProto, blobCount)
	for i := range blobs {
		blobs[i] = &blobv4.BlobProto{NamespaceId: nsID, Data: bytes.Repeat([]byte{byte(i)}, blobSize)}
	}
	bz, err := proto.Marshal(&blobv4.BlobTx{Tx: bytes.Repeat([]byte{1}, 400), Blobs: blobs, TypeId: squaretx.ProtoBlobTxTypeID})
	require.NoError(b, err)
	tx := Tx(bz)

	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			UnwrapBlobTx(tx, BlobTxValidationAppVersion-1)
		}
	})
	b.Run("v11", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			UnwrapBlobTx(tx, BlobTxValidationAppVersion)
		}
	})
	b.Run("v11 copying decoder", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			bTx, _, err := squaretx.UnmarshalBlobTx(tx)
			if err != nil {
				b.Fatal(err)
			}
			_ = bytes.Clone(bTx.Tx)
		}
	})
}
