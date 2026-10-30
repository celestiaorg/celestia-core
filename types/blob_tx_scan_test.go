package types

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// The tests in this file hand-encode BlobTxs with protowire so the exact bytes
// are under control, and check that the zero-copy scanner
// (scanLegacyBlobTx/unwrapLegacyBlobTx) agrees with the generated decoder
// (decodeLegacyBlobTx) on every one of them.

var (
	scanInnerTx = []byte("inner-tx")
	// scanNamespaceID is a 28-byte namespace ID, the only thing the legacy
	// rules check about a blob.
	scanNamespaceID = bytes.Repeat([]byte{7}, share.NamespaceIDSize)
)

// field appends field num with wire type bytes and value v.
func field(b []byte, num protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), v)
}

// varintField appends field num with wire type varint and value v.
func varintField(b []byte, num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(b, num, protowire.VarintType), v)
}

// blobBytes encodes a cmtproto.Blob with the given namespace ID and data.
func blobBytes(nsID, data []byte) []byte {
	return field(field(nil, blobNamespaceIDField, nsID), blobDataField, data)
}

// validScanBlob encodes a blob the legacy rules accept.
func validScanBlob() []byte {
	return blobBytes(scanNamespaceID, []byte("data"))
}

// blobTxBytes encodes a BlobTx with fields in canonical order: tx, blobs, type_id.
func blobTxBytes(tx []byte, blobs ...[]byte) []byte {
	b := field(nil, blobTxTxField, tx)
	for _, blob := range blobs {
		b = field(b, blobTxBlobsField, blob)
	}
	return field(b, blobTxTypeIDField, []byte(squaretx.ProtoBlobTxTypeID))
}

// scanResult is what both the scanner and the reference decoder report for a tx.
type scanResult struct {
	inner    []byte
	isBlobTx bool
}

// requireScanMatchesReference asserts that scanLegacyBlobTx (when it decides)
// and unwrapLegacyBlobTx agree with decodeLegacyBlobTx on tx, and returns the
// reference result for further assertions. When wantDecided is non-nil it also
// asserts whether scanLegacyBlobTx decided on its own (true) or must fall back
// to the decoder (false).
func requireScanMatchesReference(t *testing.T, tx []byte, wantDecided *bool) scanResult {
	t.Helper()
	wantInner, wantIsBlobTx := decodeLegacyBlobTx(tx)

	inner, isBlobTx, ok := scanLegacyBlobTx(tx)
	if wantDecided != nil {
		require.Equal(t, *wantDecided, ok, "scanLegacyBlobTx decided (ok) on %x", tx)
	}
	if ok {
		require.Equal(t, wantIsBlobTx, isBlobTx, "scanLegacyBlobTx verdict on %x", tx)
		if isBlobTx {
			require.True(t, bytes.Equal(wantInner, inner), "scanLegacyBlobTx inner tx on %x: want %x, got %x", tx, wantInner, inner)
			require.Equal(t, len(wantInner), len(inner), "scanLegacyBlobTx inner tx length on %x", tx)
			require.Equal(t, wantInner == nil, inner == nil, "scanLegacyBlobTx inner tx nil-ness on %x", tx)
		}
	}

	gotInner, gotIsBlobTx := unwrapLegacyBlobTx(tx)
	require.Equal(t, wantIsBlobTx, gotIsBlobTx, "unwrapLegacyBlobTx verdict on %x", tx)
	require.True(t, bytes.Equal(wantInner, gotInner), "unwrapLegacyBlobTx inner tx on %x: want %x, got %x", tx, wantInner, gotInner)
	require.Equal(t, len(wantInner), len(gotInner), "unwrapLegacyBlobTx inner tx length on %x", tx)
	require.Equal(t, wantInner == nil, gotInner == nil, "unwrapLegacyBlobTx inner tx nil-ness on %x", tx)

	return scanResult{inner: wantInner, isBlobTx: wantIsBlobTx}
}

func boolPtr(b bool) *bool { return &b }

var (
	decided  = boolPtr(true)
	fellBack = boolPtr(false)
)

// scanCase is a hand-encoded tx with the verdict both implementations must
// reach. When isBlobTx is true, inner is the expected inner tx.
type scanCase struct {
	name     string
	tx       []byte
	isBlobTx bool
	inner    []byte
	// decided says whether scanLegacyBlobTx must decide on its own; nil means
	// either is fine as long as the final result matches the reference.
	decided *bool
}

func runScanCases(t *testing.T, cases []scanCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := requireScanMatchesReference(t, tc.tx, tc.decided)
			require.Equal(t, tc.isBlobTx, got.isBlobTx, "verdict")
			if tc.isBlobTx {
				assert.Equal(t, tc.inner, got.inner, "inner tx")
			} else {
				assert.Equal(t, tc.tx, got.inner, "a rejected tx is returned unchanged")
			}
		})
	}
}

// TestScanLegacyBlobTxDuplicateFields checks that, like the generated decoder,
// the last occurrence of a singular field wins.
func TestScanLegacyBlobTxDuplicateFields(t *testing.T) {
	typeID := []byte(squaretx.ProtoBlobTxTypeID)
	shortNS := scanNamespaceID[:5]

	runScanCases(t, []scanCase{
		{
			name: "tx field twice, last wins",
			tx: field(field(field(field(nil,
				blobTxTxField, []byte("first")),
				blobTxTxField, []byte("second")),
				blobTxBlobsField, validScanBlob()),
				blobTxTypeIDField, typeID),
			isBlobTx: true, inner: []byte("second"), decided: decided,
		},
		{
			name: "tx field twice, last is empty",
			tx: field(field(field(field(nil,
				blobTxTxField, []byte("first")),
				blobTxTxField, nil),
				blobTxBlobsField, validScanBlob()),
				blobTxTypeIDField, typeID),
			isBlobTx: true, inner: []byte{}, decided: decided,
		},
		{
			name: "type_id BLOB then XXXX is not a blob tx",
			tx: field(field(field(field(nil,
				blobTxTxField, scanInnerTx),
				blobTxBlobsField, validScanBlob()),
				blobTxTypeIDField, typeID),
				blobTxTypeIDField, []byte("XXXX")),
			isBlobTx: false, decided: decided,
		},
		{
			name: "type_id XXXX then BLOB is a blob tx",
			tx: field(field(field(field(nil,
				blobTxTxField, scanInnerTx),
				blobTxBlobsField, validScanBlob()),
				blobTxTypeIDField, []byte("XXXX")),
				blobTxTypeIDField, typeID),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name: "type_id BLOB then empty is not a blob tx",
			tx: field(field(field(field(nil,
				blobTxTxField, scanInnerTx),
				blobTxBlobsField, validScanBlob()),
				blobTxTypeIDField, typeID),
				blobTxTypeIDField, nil),
			isBlobTx: false, decided: decided,
		},
		{
			name: "namespace_id valid then short is not a blob tx",
			tx: blobTxBytes(scanInnerTx,
				field(field(field(nil,
					blobNamespaceIDField, scanNamespaceID),
					blobNamespaceIDField, shortNS),
					blobDataField, []byte("data"))),
			isBlobTx: false, decided: decided,
		},
		{
			name: "namespace_id short then valid is a blob tx",
			tx: blobTxBytes(scanInnerTx,
				field(field(field(nil,
					blobNamespaceIDField, shortNS),
					blobNamespaceIDField, scanNamespaceID),
					blobDataField, []byte("data"))),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name: "namespace_id valid then valid with data in between is a blob tx",
			tx: blobTxBytes(scanInnerTx,
				field(field(field(nil,
					blobNamespaceIDField, scanNamespaceID),
					blobDataField, []byte("data")),
					blobNamespaceIDField, scanNamespaceID)),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
	})
}

// TestScanLegacyBlobTxAbsentVsEmptyInnerTx checks a BlobTx whose tx field is
// absent against one whose tx field is present with zero length. The
// generated decoder returns nil for the absent field and a non-nil empty slice
// for the present one, and the scanner must reproduce both.
func TestScanLegacyBlobTxAbsentVsEmptyInnerTx(t *testing.T) {
	typeID := []byte(squaretx.ProtoBlobTxTypeID)

	t.Run("absent tx field", func(t *testing.T) {
		tx := field(field(nil, blobTxBlobsField, validScanBlob()), blobTxTypeIDField, typeID)
		got := requireScanMatchesReference(t, tx, decided)
		require.True(t, got.isBlobTx)
		assert.Nil(t, got.inner, "reference returns nil for an absent tx field")
		assert.Len(t, got.inner, 0)

		inner, isBlobTx, ok := scanLegacyBlobTx(tx)
		require.True(t, ok)
		require.True(t, isBlobTx)
		assert.Nil(t, inner)
	})

	t.Run("empty tx field", func(t *testing.T) {
		tx := blobTxBytes(nil, validScanBlob())
		got := requireScanMatchesReference(t, tx, decided)
		require.True(t, got.isBlobTx)
		assert.NotNil(t, got.inner, "reference returns a non-nil empty slice for a present but empty tx field")
		assert.Len(t, got.inner, 0)
		assert.True(t, bytes.Equal([]byte{}, got.inner))

		inner, isBlobTx, ok := scanLegacyBlobTx(tx)
		require.True(t, ok)
		require.True(t, isBlobTx)
		assert.NotNil(t, inner)
		assert.Len(t, inner, 0)
	})

	t.Run("absent and empty hash the same", func(t *testing.T) {
		absent := Tx(field(field(nil, blobTxBlobsField, validScanBlob()), blobTxTypeIDField, typeID))
		empty := Tx(blobTxBytes(nil, validScanBlob()))
		assert.Equal(t, absent.Hash(), empty.Hash())
		assert.Equal(t, absent.Key(), empty.Key())
	})
}

// wireTypeValues lists every wire type the tests encode a field with, and a
// well-formed value for it. Groups are covered separately.
var wireTypeValues = []struct {
	name string
	typ  protowire.Type
	// append appends tag num with this wire type and a value.
	append func(b []byte, num protowire.Number) []byte
}{
	{"varint", protowire.VarintType, func(b []byte, num protowire.Number) []byte {
		return protowire.AppendVarint(protowire.AppendTag(b, num, protowire.VarintType), 1)
	}},
	{"fixed32", protowire.Fixed32Type, func(b []byte, num protowire.Number) []byte {
		return protowire.AppendFixed32(protowire.AppendTag(b, num, protowire.Fixed32Type), 1)
	}},
	{"fixed64", protowire.Fixed64Type, func(b []byte, num protowire.Number) []byte {
		return protowire.AppendFixed64(protowire.AppendTag(b, num, protowire.Fixed64Type), 1)
	}},
	{"bytes", protowire.BytesType, func(b []byte, num protowire.Number) []byte {
		return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), []byte{1})
	}},
}

// TestScanLegacyBlobTxWrongWireTypes encodes every known field of BlobTx and
// Blob with every wire type other than its declared one. A known field with
// the wrong wire type is a decode error, so the tx is rejected.
func TestScanLegacyBlobTxWrongWireTypes(t *testing.T) {
	typeID := []byte(squaretx.ProtoBlobTxTypeID)

	blobTxFields := []struct {
		name string
		num  protowire.Number
		want protowire.Type
	}{
		{"tx", blobTxTxField, protowire.BytesType},
		{"blobs", blobTxBlobsField, protowire.BytesType},
		{"type_id", blobTxTypeIDField, protowire.BytesType},
	}
	blobFields := []struct {
		name string
		num  protowire.Number
		want protowire.Type
	}{
		{"namespace_id", blobNamespaceIDField, protowire.BytesType},
		{"data", blobDataField, protowire.BytesType},
		{"share_version", blobShareVersionField, protowire.VarintType},
		{"namespace_version", blobNamespaceVersionField, protowire.VarintType},
	}

	var cases []scanCase
	for _, f := range blobTxFields {
		for _, wt := range wireTypeValues {
			if wt.typ == f.want {
				continue
			}
			// The mistyped field comes after an otherwise valid BlobTx so
			// that only the wire type makes the tx invalid.
			cases = append(cases, scanCase{
				name:     fmt.Sprintf("BlobTx.%s as %s after valid fields", f.name, wt.name),
				tx:       wt.append(blobTxBytes(scanInnerTx, validScanBlob()), f.num),
				isBlobTx: false, decided: decided,
			})
			// And before them, so the scanner must not stop early with a
			// wrong verdict.
			cases = append(cases, scanCase{
				name: fmt.Sprintf("BlobTx.%s as %s before valid fields", f.name, wt.name),
				tx: field(field(field(wt.append(nil, f.num),
					blobTxTxField, scanInnerTx),
					blobTxBlobsField, validScanBlob()),
					blobTxTypeIDField, typeID),
				isBlobTx: false, decided: decided,
			})
		}
	}
	for _, f := range blobFields {
		for _, wt := range wireTypeValues {
			if wt.typ == f.want {
				continue
			}
			cases = append(cases, scanCase{
				name:     fmt.Sprintf("Blob.%s as %s after valid namespace", f.name, wt.name),
				tx:       blobTxBytes(scanInnerTx, wt.append(validScanBlob(), f.num)),
				isBlobTx: false, decided: decided,
			})
			cases = append(cases, scanCase{
				name:     fmt.Sprintf("Blob.%s as %s before valid namespace", f.name, wt.name),
				tx:       blobTxBytes(scanInnerTx, append(wt.append(nil, f.num), validScanBlob()...)),
				isBlobTx: false, decided: decided,
			})
			// A mistyped field in the second of two blobs.
			cases = append(cases, scanCase{
				name:     fmt.Sprintf("Blob.%s as %s in second blob", f.name, wt.name),
				tx:       blobTxBytes(scanInnerTx, validScanBlob(), wt.append(validScanBlob(), f.num)),
				isBlobTx: false, decided: decided,
			})
		}
	}
	// Known fields encoded as a group start or a bare group end.
	for _, f := range blobTxFields {
		cases = append(cases, scanCase{
			name:     fmt.Sprintf("BlobTx.%s as start group", f.name),
			tx:       protowire.AppendTag(blobTxBytes(scanInnerTx, validScanBlob()), f.num, protowire.StartGroupType),
			isBlobTx: false, decided: decided,
		})
		cases = append(cases, scanCase{
			name:     fmt.Sprintf("BlobTx.%s as end group", f.name),
			tx:       protowire.AppendTag(blobTxBytes(scanInnerTx, validScanBlob()), f.num, protowire.EndGroupType),
			isBlobTx: false, decided: decided,
		})
	}
	for _, f := range blobFields {
		cases = append(cases, scanCase{
			name:     fmt.Sprintf("Blob.%s as start group", f.name),
			tx:       blobTxBytes(scanInnerTx, protowire.AppendTag(validScanBlob(), f.num, protowire.StartGroupType)),
			isBlobTx: false, decided: decided,
		})
		cases = append(cases, scanCase{
			name:     fmt.Sprintf("Blob.%s as end group", f.name),
			tx:       blobTxBytes(scanInnerTx, protowire.AppendTag(validScanBlob(), f.num, protowire.EndGroupType)),
			isBlobTx: false, decided: decided,
		})
	}
	runScanCases(t, cases)
}

// TestScanLegacyBlobTxMalformedVarintsAndTags feeds the scanner non-canonical
// and overflowing varints, invalid tags and bad length prefixes. protowire is
// stricter than the generated decoder here, so the scanner may fall back, but
// the final result must always match the reference.
func TestScanLegacyBlobTxMalformedVarintsAndTags(t *testing.T) {
	// valid returns a fresh buffer each time so appends cannot alias.
	valid := func() []byte { return blobTxBytes(scanInnerTx, validScanBlob()) }
	typeID := []byte(squaretx.ProtoBlobTxTypeID)

	// tenByteVarint encodes v with the continuation bit set on nine bytes and
	// the given tenth byte, so bits beyond the 64th land in the tenth byte.
	tenByteVarint := func(v uint64, tenth byte) []byte {
		b := make([]byte, 0, 10)
		for i := 0; i < 9; i++ {
			b = append(b, byte(v&0x7f)|0x80)
			v >>= 7
		}
		return append(b, tenth)
	}
	// nonCanonicalVarint encodes v on more bytes than needed by adding
	// zero-valued continuation bytes.
	nonCanonicalVarint := func(v uint64, size int) []byte {
		b := protowire.AppendVarint(nil, v)
		for len(b) < size {
			b[len(b)-1] |= 0x80
			b = append(b, 0)
		}
		return b
	}
	tag := func(num protowire.Number, typ protowire.Type) uint64 {
		return protowire.EncodeTag(num, typ)
	}

	runScanCases(t, []scanCase{
		{
			name:     "non-canonical 2-byte tag for tx field",
			tx:       append(append(nonCanonicalVarint(tag(blobTxTxField, protowire.BytesType), 2), protowire.AppendBytes(nil, scanInnerTx)...), field(field(nil, blobTxBlobsField, validScanBlob()), blobTxTypeIDField, typeID)...),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "non-canonical 10-byte tag for tx field",
			tx:       append(append(nonCanonicalVarint(tag(blobTxTxField, protowire.BytesType), 10), protowire.AppendBytes(nil, scanInnerTx)...), field(field(nil, blobTxBlobsField, validScanBlob()), blobTxTypeIDField, typeID)...),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "non-canonical 10-byte length prefix for tx field",
			tx:       append(append(append(protowire.AppendTag(nil, blobTxTxField, protowire.BytesType), nonCanonicalVarint(uint64(len(scanInnerTx)), 10)...), scanInnerTx...), field(field(nil, blobTxBlobsField, validScanBlob()), blobTxTypeIDField, typeID)...),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "non-canonical 10-byte varint for unknown BlobTx field",
			tx:       append(protowire.AppendTag(valid(), 15, protowire.VarintType), nonCanonicalVarint(1, 10)...),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "non-canonical 10-byte varint for share_version",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendTag(validScanBlob(), blobShareVersionField, protowire.VarintType), nonCanonicalVarint(1, 10)...)),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "10-byte varint with overflowing tenth byte for unknown BlobTx field",
			tx:       append(protowire.AppendTag(valid(), 15, protowire.VarintType), tenByteVarint(1, 0x7f)...),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "10-byte varint with overflowing tenth byte for share_version",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendTag(validScanBlob(), blobShareVersionField, protowire.VarintType), tenByteVarint(1, 0x7f)...)),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "10-byte tag with overflowing tenth byte",
			tx:       append(valid(), tenByteVarint(tag(15, protowire.VarintType), 0x7f)...),
			isBlobTx: false,
		},
		{
			name:     "11-byte varint for unknown BlobTx field",
			tx:       append(protowire.AppendTag(valid(), 15, protowire.VarintType), bytes.Repeat([]byte{0x80}, 11)...),
			isBlobTx: false,
		},
		{
			name:     "11-byte varint for share_version",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendTag(validScanBlob(), blobShareVersionField, protowire.VarintType), bytes.Repeat([]byte{0x80}, 11)...)),
			isBlobTx: false,
		},
		{
			name:     "11-byte tag",
			tx:       append(valid(), bytes.Repeat([]byte{0x80}, 11)...),
			isBlobTx: false,
		},
		{
			name:     "unterminated varint at the end",
			tx:       append(protowire.AppendTag(valid(), 15, protowire.VarintType), 0x80, 0x80),
			isBlobTx: false,
		},
		{
			name:     "tag with field number 0 in BlobTx",
			tx:       append(valid(), protowire.AppendVarint(nil, tag(0, protowire.VarintType))...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "tag with field number 0 in Blob",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendVarint(protowire.AppendVarint(validScanBlob(), tag(0, protowire.VarintType)), 1)),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "tag with field number MaxValidNumber in BlobTx",
			tx:       protowire.AppendVarint(protowire.AppendTag(valid(), protowire.MaxValidNumber, protowire.VarintType), 1),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "tag with field number MaxValidNumber+1 in BlobTx",
			tx:       protowire.AppendVarint(protowire.AppendVarint(valid(), tag(protowire.MaxValidNumber+1, protowire.VarintType)), 1),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "tag with field number MaxValidNumber+1 in Blob",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendVarint(protowire.AppendVarint(validScanBlob(), tag(protowire.MaxValidNumber+1, protowire.VarintType)), 1)),
			isBlobTx: true, inner: scanInnerTx,
		},
		{
			name:     "tag with field number 2^31 (negative as int32)",
			tx:       protowire.AppendVarint(protowire.AppendVarint(valid(), uint64(1)<<31<<3|uint64(protowire.VarintType)), 1),
			isBlobTx: false,
		},
		{
			name:     "tag with field number 2^61 (max uint64 tag)",
			tx:       protowire.AppendVarint(protowire.AppendVarint(valid(), math.MaxUint64), 1),
			isBlobTx: false,
		},
		{
			name:     "tx length prefix larger than remaining bytes",
			tx:       append(protowire.AppendVarint(protowire.AppendTag(nil, blobTxTxField, protowire.BytesType), 100), scanInnerTx...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "blobs length prefix larger than remaining bytes",
			tx:       append(protowire.AppendVarint(protowire.AppendTag(field(nil, blobTxTxField, scanInnerTx), blobTxBlobsField, protowire.BytesType), 100), validScanBlob()...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "unknown field length prefix larger than remaining bytes",
			tx:       append(protowire.AppendVarint(protowire.AppendTag(valid(), 15, protowire.BytesType), 100), 1, 2, 3),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "namespace_id length prefix larger than remaining bytes",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendVarint(protowire.AppendTag(nil, blobNamespaceIDField, protowire.BytesType), 100), scanNamespaceID...)),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "data length prefix larger than remaining bytes",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendVarint(protowire.AppendTag(field(nil, blobNamespaceIDField, scanNamespaceID), blobDataField, protowire.BytesType), 100), 1, 2, 3)),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "tx length prefix 2^63 overflows int",
			tx:       append(protowire.AppendVarint(protowire.AppendTag(nil, blobTxTxField, protowire.BytesType), 1<<63), scanInnerTx...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "tx length prefix max uint64",
			tx:       append(protowire.AppendVarint(protowire.AppendTag(nil, blobTxTxField, protowire.BytesType), math.MaxUint64), scanInnerTx...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "tx length prefix 2^32 exceeds remaining bytes on every platform",
			tx:       append(protowire.AppendVarint(protowire.AppendTag(nil, blobTxTxField, protowire.BytesType), 1<<32), scanInnerTx...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "data length prefix 2^63 overflows int",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendVarint(protowire.AppendTag(field(nil, blobNamespaceIDField, scanNamespaceID), blobDataField, protowire.BytesType), 1<<63), 1, 2, 3)),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "fixed64 truncated",
			tx:       append(protowire.AppendTag(valid(), 15, protowire.Fixed64Type), 1, 2, 3),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "fixed32 truncated in Blob",
			tx:       blobTxBytes(scanInnerTx, append(protowire.AppendTag(validScanBlob(), 15, protowire.Fixed32Type), 1)),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "invalid wire type 6 in BlobTx",
			tx:       append(valid(), protowire.AppendVarint(nil, tag(15, 6))...),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "invalid wire type 7 in Blob",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendVarint(validScanBlob(), tag(15, 7))),
			isBlobTx: false, decided: fellBack,
		},
		{
			name:     "lone tag byte",
			tx:       append(valid(), byte(tag(15, protowire.BytesType))),
			isBlobTx: false, decided: fellBack,
		},
	})
}

// TestScanLegacyBlobTxUnknownFieldsAndGroups checks unknown field numbers
// with every wire type, inside BlobTx and inside Blob. Well-formed unknown
// fields are skipped by both implementations. protowire checks that an end
// group matches its start group, which the generated decoder does not, so a
// mismatched or unterminated group makes the scanner fall back rather than
// reject the tx.
func TestScanLegacyBlobTxUnknownFieldsAndGroups(t *testing.T) {
	// valid returns a fresh buffer each time so appends cannot alias.
	valid := func() []byte { return blobTxBytes(scanInnerTx, validScanBlob()) }
	typeID := []byte(squaretx.ProtoBlobTxTypeID)
	unknownNums := []protowire.Number{5, 15, 100, 1 << 20}

	var cases []scanCase
	for _, num := range unknownNums {
		for _, wt := range wireTypeValues {
			cases = append(cases,
				scanCase{
					name:     fmt.Sprintf("unknown BlobTx field %d as %s after", num, wt.name),
					tx:       wt.append(valid(), num),
					isBlobTx: true, inner: scanInnerTx, decided: decided,
				},
				scanCase{
					name:     fmt.Sprintf("unknown BlobTx field %d as %s before", num, wt.name),
					tx:       append(wt.append(nil, num), valid()...),
					isBlobTx: true, inner: scanInnerTx, decided: decided,
				},
				scanCase{
					name: fmt.Sprintf("unknown BlobTx field %d as %s between", num, wt.name),
					tx: field(field(wt.append(field(nil, blobTxTxField, scanInnerTx), num),
						blobTxBlobsField, validScanBlob()),
						blobTxTypeIDField, typeID),
					isBlobTx: true, inner: scanInnerTx, decided: decided,
				},
				scanCase{
					name:     fmt.Sprintf("unknown Blob field %d as %s after", num, wt.name),
					tx:       blobTxBytes(scanInnerTx, wt.append(validScanBlob(), num)),
					isBlobTx: true, inner: scanInnerTx, decided: decided,
				},
				scanCase{
					name:     fmt.Sprintf("unknown Blob field %d as %s before", num, wt.name),
					tx:       blobTxBytes(scanInnerTx, append(wt.append(nil, num), validScanBlob()...)),
					isBlobTx: true, inner: scanInnerTx, decided: decided,
				},
			)
		}
	}

	group := func(b []byte, num protowire.Number, body []byte) []byte {
		b = protowire.AppendTag(b, num, protowire.StartGroupType)
		b = append(b, body...)
		return protowire.AppendTag(b, num, protowire.EndGroupType)
	}
	// Group contents that look like known BlobTx fields must be skipped, not
	// interpreted.
	decoy := field(field(nil, blobTxTxField, []byte("decoy")), blobTxTypeIDField, []byte("XXXX"))

	cases = append(cases,
		scanCase{
			name:     "empty group in BlobTx",
			tx:       group(valid(), 15, nil),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		scanCase{
			name:     "group with decoy fields in BlobTx",
			tx:       group(valid(), 15, decoy),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		scanCase{
			name:     "group with decoy fields before the real ones",
			tx:       append(group(nil, 15, decoy), valid()...),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		scanCase{
			name:     "nested groups in BlobTx",
			tx:       group(valid(), 15, group(nil, 16, varintField(nil, 17, 1))),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		scanCase{
			name:     "empty group in Blob",
			tx:       blobTxBytes(scanInnerTx, group(validScanBlob(), 15, nil)),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		scanCase{
			name:     "group with decoy namespace in Blob",
			tx:       blobTxBytes(scanInnerTx, group(validScanBlob(), 15, field(nil, blobNamespaceIDField, scanNamespaceID[:3]))),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		scanCase{
			name:     "group with decoy namespace in Blob without a real namespace",
			tx:       blobTxBytes(scanInnerTx, group(field(nil, blobDataField, []byte("data")), 15, field(nil, blobNamespaceIDField, scanNamespaceID))),
			isBlobTx: false, decided: decided,
		},
		scanCase{
			name:     "unterminated group in BlobTx",
			tx:       protowire.AppendTag(valid(), 15, protowire.StartGroupType),
			isBlobTx: false, decided: fellBack,
		},
		scanCase{
			name:     "unterminated group with body in BlobTx",
			tx:       append(protowire.AppendTag(valid(), 15, protowire.StartGroupType), decoy...),
			isBlobTx: false, decided: fellBack,
		},
		scanCase{
			name:     "unterminated group in Blob",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendTag(validScanBlob(), 15, protowire.StartGroupType)),
			isBlobTx: false, decided: fellBack,
		},
		scanCase{
			name:     "bare end group in BlobTx",
			tx:       protowire.AppendTag(valid(), 15, protowire.EndGroupType),
			isBlobTx: false, decided: fellBack,
		},
		scanCase{
			name:     "bare end group in Blob",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendTag(validScanBlob(), 15, protowire.EndGroupType)),
			isBlobTx: false, decided: fellBack,
		},
		scanCase{
			// The generated decoder only counts nesting depth, so it accepts
			// this; protowire does not, so the scanner must fall back.
			name:     "group closed with a different field number in BlobTx",
			tx:       protowire.AppendTag(protowire.AppendTag(valid(), 15, protowire.StartGroupType), 16, protowire.EndGroupType),
			isBlobTx: true, inner: scanInnerTx, decided: fellBack,
		},
		scanCase{
			name:     "group closed with a different field number in Blob",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendTag(protowire.AppendTag(validScanBlob(), 15, protowire.StartGroupType), 16, protowire.EndGroupType)),
			isBlobTx: true, inner: scanInnerTx, decided: fellBack,
		},
		scanCase{
			name:     "group closed with a different field number in Blob with bad namespace",
			tx:       blobTxBytes(scanInnerTx, protowire.AppendTag(protowire.AppendTag(blobBytes(scanNamespaceID[:3], nil), 15, protowire.StartGroupType), 16, protowire.EndGroupType)),
			isBlobTx: false, decided: fellBack,
		},
		scanCase{
			name:     "group in BlobTx terminated by a known field number",
			tx:       protowire.AppendTag(protowire.AppendTag(valid(), 15, protowire.StartGroupType), blobTxTxField, protowire.EndGroupType),
			isBlobTx: true, inner: scanInnerTx, decided: fellBack,
		},
	)
	runScanCases(t, cases)
}

// TestScanLegacyBlobTxMultipleBlobs checks the namespace length rule across
// several blobs and that the legacy rules ignore everything else in a blob.
func TestScanLegacyBlobTxMultipleBlobs(t *testing.T) {
	good := validScanBlob()
	short := blobBytes(scanNamespaceID[:share.NamespaceIDSize-1], []byte("data"))
	long := blobBytes(append(scanNamespaceID, 1), []byte("data"))
	none := field(nil, blobDataField, []byte("data"))

	runScanCases(t, []scanCase{
		{
			name:     "two valid blobs",
			tx:       blobTxBytes(scanInnerTx, good, good),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name:     "three valid blobs",
			tx:       blobTxBytes(scanInnerTx, good, good, good),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name:     "first of three blobs has a short namespace",
			tx:       blobTxBytes(scanInnerTx, short, good, good),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "second of three blobs has a short namespace",
			tx:       blobTxBytes(scanInnerTx, good, short, good),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "third of three blobs has a short namespace",
			tx:       blobTxBytes(scanInnerTx, good, good, short),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "first of three blobs has a long namespace",
			tx:       blobTxBytes(scanInnerTx, long, good, good),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "third of three blobs has a long namespace",
			tx:       blobTxBytes(scanInnerTx, good, good, long),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "second of three blobs has no namespace",
			tx:       blobTxBytes(scanInnerTx, good, none, good),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "empty blob message",
			tx:       blobTxBytes(scanInnerTx, nil),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "empty blob message after a valid blob",
			tx:       blobTxBytes(scanInnerTx, good, nil),
			isBlobTx: false, decided: decided,
		},
		{
			name:     "blob with only a namespace",
			tx:       blobTxBytes(scanInnerTx, field(nil, blobNamespaceIDField, scanNamespaceID)),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name: "blob with arbitrary share_version and namespace_version",
			tx: blobTxBytes(scanInnerTx,
				varintField(varintField(good, blobShareVersionField, 7), blobNamespaceVersionField, 255)),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name: "blob with max uint64 share_version and namespace_version",
			tx: blobTxBytes(scanInnerTx,
				varintField(varintField(good, blobShareVersionField, math.MaxUint64), blobNamespaceVersionField, math.MaxUint64)),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name: "blob with versions before the namespace",
			tx: blobTxBytes(scanInnerTx,
				append(varintField(varintField(nil, blobShareVersionField, 1), blobNamespaceVersionField, 1), good...)),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
		{
			name:     "blob with large data",
			tx:       blobTxBytes(scanInnerTx, blobBytes(scanNamespaceID, bytes.Repeat([]byte{9}, 1<<16))),
			isBlobTx: true, inner: scanInnerTx, decided: decided,
		},
	})
}

// TestScanLegacyBlobTxTruncation compares the scanner with the reference on
// every prefix of a valid two-blob BlobTx.
func TestScanLegacyBlobTxTruncation(t *testing.T) {
	// Give the blobs versions so every field kind appears in the tx.
	blob := varintField(varintField(validScanBlob(), blobShareVersionField, 1), blobNamespaceVersionField, 0)
	full := blobTxBytes(scanInnerTx, blob, blob)

	got := requireScanMatchesReference(t, full, decided)
	require.True(t, got.isBlobTx)
	require.Equal(t, scanInnerTx, got.inner)

	for n := 0; n < len(full); n++ {
		t.Run(fmt.Sprintf("prefix_%d", n), func(t *testing.T) {
			// Clone so a scanner result cannot alias beyond the prefix.
			prefix := bytes.Clone(full[:n])
			got := requireScanMatchesReference(t, prefix, nil)
			// No prefix carries the type ID, which is the last field, so
			// none is a BlobTx.
			assert.False(t, got.isBlobTx, "prefix of length %d", n)
		})
	}

	// With type_id first, prefixes that cut inside the blobs are decidable
	// and must still match.
	reordered := field(field(field(field(nil,
		blobTxTypeIDField, []byte(squaretx.ProtoBlobTxTypeID)),
		blobTxBlobsField, blob),
		blobTxTxField, scanInnerTx),
		blobTxBlobsField, blob)
	for n := 0; n < len(reordered); n++ {
		t.Run(fmt.Sprintf("type_id_first_prefix_%d", n), func(t *testing.T) {
			requireScanMatchesReference(t, bytes.Clone(reordered[:n]), nil)
		})
	}
}

// TestScanLegacyBlobTxFieldOrder checks that the inner tx is found whatever
// order the fields come in.
func TestScanLegacyBlobTxFieldOrder(t *testing.T) {
	typeID := []byte(squaretx.ProtoBlobTxTypeID)
	blob1 := validScanBlob()
	blob2 := blobBytes(scanNamespaceID, []byte("other"))

	type encodedField struct {
		name string
		num  protowire.Number
		val  []byte
	}
	tx := encodedField{"tx", blobTxTxField, scanInnerTx}
	b1 := encodedField{"blob1", blobTxBlobsField, blob1}
	b2 := encodedField{"blob2", blobTxBlobsField, blob2}
	tid := encodedField{"type_id", blobTxTypeIDField, typeID}

	orders := [][]encodedField{
		{tx, b1, b2, tid},
		{tx, b1, tid, b2},
		{tx, tid, b1, b2},
		{b1, tx, b2, tid},
		{b1, b2, tx, tid},
		{b1, b2, tid, tx},
		{b1, tx, tid, b2},
		{b1, tid, tx, b2},
		{b1, tid, b2, tx},
		{tid, tx, b1, b2},
		{tid, b1, tx, b2},
		{tid, b1, b2, tx},
		{b2, b1, tx, tid},
	}
	for _, order := range orders {
		name := ""
		var bz []byte
		for i, f := range order {
			if i > 0 {
				name += ","
			}
			name += f.name
			bz = field(bz, f.num, f.val)
		}
		t.Run(name, func(t *testing.T) {
			got := requireScanMatchesReference(t, bz, decided)
			require.True(t, got.isBlobTx, "field order %s", name)
			assert.Equal(t, scanInnerTx, got.inner)

			unwrapped, isBlobTx := UnwrapBlobTx(bz, BlobTxValidationAppVersion-1)
			require.True(t, isBlobTx)
			assert.Equal(t, Tx(scanInnerTx), unwrapped)
		})
	}
}

// TestUnwrapBlobTxReturnsIndependentBuffer checks that the inner tx returned
// by UnwrapBlobTx under the legacy rules is a copy, and that the subslice
// returned by unwrapLegacyBlobTx has no spare capacity, so appending to it
// cannot overwrite the blob data that follows it in the original buffer.
func TestUnwrapBlobTxReturnsIndependentBuffer(t *testing.T) {
	const appVersion = BlobTxValidationAppVersion - 1
	require.Less(t, appVersion, BlobTxValidationAppVersion)

	newTx := func() Tx {
		return Tx(blobTxBytes(scanInnerTx, validScanBlob(), validScanBlob()))
	}

	t.Run("UnwrapBlobTx copies the inner tx", func(t *testing.T) {
		tx := newTx()
		inner, isBlobTx := UnwrapBlobTx(tx, appVersion)
		require.True(t, isBlobTx)
		require.Equal(t, Tx(scanInnerTx), inner)

		for i := range tx {
			tx[i] = 0xff
		}
		assert.Equal(t, Tx(scanInnerTx), inner, "inner tx changed with the original buffer")
		assert.Equal(t, len(inner), cap(inner))
	})

	t.Run("UnwrapBlobTx copy does not alias blob data", func(t *testing.T) {
		tx := newTx()
		inner, isBlobTx := UnwrapBlobTx(tx, appVersion)
		require.True(t, isBlobTx)
		before := bytes.Clone(tx)
		_ = append(inner, bytes.Repeat([]byte{0xee}, 64)...)
		assert.Equal(t, before, []byte(tx), "appending to the copy changed the original buffer")
	})

	t.Run("unwrapLegacyBlobTx subslice has no spare capacity", func(t *testing.T) {
		tx := newTx()
		inner, isBlobTx := unwrapLegacyBlobTx(tx)
		require.True(t, isBlobTx)
		require.Equal(t, Tx(scanInnerTx), inner)
		assert.Equal(t, len(inner), cap(inner), "subslice must be capped at its length")

		before := bytes.Clone(tx)
		grown := append(inner, bytes.Repeat([]byte{0xee}, 64)...)
		assert.Equal(t, before, []byte(tx), "appending to the subslice changed the original buffer")
		assert.Equal(t, Tx(append(bytes.Clone(scanInnerTx), bytes.Repeat([]byte{0xee}, 64)...)), grown)
	})

	t.Run("unwrapLegacyBlobTx subslice aliases the original buffer", func(t *testing.T) {
		// Documents the zero-copy behavior UnwrapBlobTx guards against.
		tx := newTx()
		inner, isBlobTx := unwrapLegacyBlobTx(tx)
		require.True(t, isBlobTx)
		require.NotEmpty(t, inner)
		tx[len(field(nil, blobTxTxField, nil))] ^= 0xff // first byte of the inner tx
		assert.NotEqual(t, Tx(scanInnerTx), inner, "subslice should see writes to the original buffer")
	})

	t.Run("empty inner tx has no spare capacity", func(t *testing.T) {
		tx := Tx(blobTxBytes(nil, validScanBlob()))
		inner, isBlobTx := unwrapLegacyBlobTx(tx)
		require.True(t, isBlobTx)
		require.NotNil(t, inner)
		assert.Equal(t, 0, cap(inner))

		before := bytes.Clone(tx)
		_ = append(inner, 1, 2, 3)
		assert.Equal(t, before, []byte(tx))
	})
}
