// Package blobtx provides wire fixtures for consensus compatibility tests.
package blobtx

import (
	"bytes"
	"testing"

	blobproto "github.com/celestiaorg/go-square/v3/proto/blob/v2"
	"github.com/celestiaorg/go-square/v3/share"
	squaretx "github.com/celestiaorg/go-square/v3/tx"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type Case struct {
	Name       string
	Wire       []byte
	Inner      []byte
	Recognized bool
	Valid      bool
}

func bytesField(wire []byte, field protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(wire, field, protowire.BytesType), value)
}

// Cases includes valid domain objects and intentionally invalid protobufs.
// Expected extraction is specified independently of either decoder.
func Cases(t testing.TB) []Case {
	t.Helper()
	inner := []byte("inner-tx-bytes")
	ns, err := share.NewV0Namespace([]byte{1})
	require.NoError(t, err)
	validBlob, err := share.NewBlob(ns, []byte("blob-data"), 0, nil)
	require.NoError(t, err)
	valid, err := squaretx.MarshalBlobTx(inner, validBlob)
	require.NoError(t, err)
	signedBlob, err := share.NewBlob(ns, []byte("signed-data"), 1, bytes.Repeat([]byte{1}, 20))
	require.NoError(t, err)
	signed, err := squaretx.MarshalBlobTx(inner, signedBlob)
	require.NoError(t, err)
	cases := []Case{
		{"plain", inner, inner, false, false},
		{"legacy", valid, inner, true, true},
		{"signer", signed, inner, true, true},
	}
	add := func(name string, wire []byte, recognized, valid bool, extracted []byte) {
		if !recognized {
			extracted = wire
		}
		cases = append(cases, Case{name, wire, extracted, recognized, valid})
	}
	mutate := func(name string, change func(*blobproto.BlobTx), recognized, valid bool) {
		var envelope blobproto.BlobTx
		require.NoError(t, proto.Unmarshal(cases[1].Wire, &envelope))
		change(&envelope)
		wire, err := proto.Marshal(&envelope)
		require.NoError(t, err)
		add(name, wire, recognized, valid, envelope.Tx)
	}
	mutate("namespace-length", func(b *blobproto.BlobTx) { b.Blobs[0].NamespaceId = []byte{1} }, false, false)
	mutate("namespace-prefix", func(b *blobproto.BlobTx) { b.Blobs[0].NamespaceId[0] = 1 }, true, false)
	mutate("namespace-version", func(b *blobproto.BlobTx) { b.Blobs[0].NamespaceVersion = 1 }, true, false)
	mutate("share-version", func(b *blobproto.BlobTx) { b.Blobs[0].ShareVersion = 2 }, true, false)
	mutate("empty-data", func(b *blobproto.BlobTx) { b.Blobs[0].Data = nil }, true, false)
	mutate("no-blobs", func(b *blobproto.BlobTx) { b.Blobs = nil }, false, false)
	mutate("empty-blob", func(b *blobproto.BlobTx) { b.Blobs = []*blobproto.BlobProto{{}} }, false, false)
	mutate("empty-inner", func(b *blobproto.BlobTx) { b.Tx = nil }, true, true)
	mutate("wrong-type", func(b *blobproto.BlobTx) { b.TypeId = "OTHER" }, false, false)
	add("truncated", valid[:len(valid)-1], false, false, nil)
	add("unknown-field", bytesField(bytes.Clone(valid), 99, []byte("unknown")), true, true, inner)
	add("duplicate-tx", bytesField(bytes.Clone(valid), 1, []byte("last-tx")), true, true, []byte("last-tx"))
	add("duplicate-type", bytesField(bytes.Clone(valid), 3, []byte("OTHER")), false, false, nil)
	add("invalid-wire-type", append(bytes.Clone(valid), 0x0f), false, false, nil)
	// The old schema treats signer as unknown, so even a varint signer is skipped.
	// The go-square protobuf runtime also skips mismatched known wire types.
	var envelope blobproto.BlobTx
	require.NoError(t, proto.Unmarshal(valid, &envelope))
	blob, err := proto.Marshal(envelope.Blobs[0])
	require.NoError(t, err)
	wrap := func(blob []byte) []byte {
		wire := bytesField(nil, 1, inner)
		wire = bytesField(wire, 2, blob)
		return bytesField(wire, 3, []byte(squaretx.ProtoBlobTxTypeID))
	}
	add("signer-wrong-wire", wrap(append(bytes.Clone(blob), 0x28, 0x01)), true, true, inner)
	add("namespace-wrong-wire", wrap(append(bytes.Clone(blob), 0x08, 0x01)), false, true, nil)
	add("duplicate-namespace", wrap(bytesField(bytes.Clone(blob), 1, []byte{1})), false, false, nil)
	add("duplicate-blob", bytesField(bytes.Clone(valid), 2, blob), true, true, inner)
	return cases
}
