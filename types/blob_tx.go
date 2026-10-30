package types

import (
	"bytes"

	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"google.golang.org/protobuf/encoding/protowire"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// BlobTxValidationAppVersion is the first app version whose blocks are unwrapped
// with go-square's validating BlobTx decoder. Blocks from earlier app versions
// keep the legacy rules so historical replay is unchanged.
const BlobTxValidationAppVersion uint64 = 11

// UnwrapBlobTx returns the inner transaction of a BlobTx and true. It returns
// tx and false if tx is not a BlobTx under the rules for appVersion.
func UnwrapBlobTx(tx Tx, appVersion uint64) (Tx, bool) {
	if appVersion < BlobTxValidationAppVersion {
		inner, isBlobTx := unwrapLegacyBlobTx(tx)
		if !isBlobTx {
			return tx, false
		}
		// Copy so callers that keep the inner tx, such as queued tx events,
		// do not keep the blobs alive.
		return bytes.Clone(inner), true
	}
	// The no-copy decoder gives the same verdict as UnmarshalBlobTx but
	// aliases tx instead of copying the inner tx and every blob. The blobs are
	// dropped here, so only the inner tx is copied, for the same reason as
	// above: fireEvents queues it in tx events, and an alias would keep the
	// whole BlobTx, blobs included, alive until every subscriber drained it.
	bTx, isBlob, err := squaretx.UnmarshalBlobTxNoCopy(tx)
	if !isBlob || err != nil {
		return tx, false
	}
	return bytes.Clone(bTx.Tx), true
}

// unwrapLegacyBlobTx extracts the inner transaction using the legacy rules,
// which only check the type ID, the blob count and namespace ID lengths. They
// define Tx.Hash, Tx.Key and the bytes sent to the application before
// BlobTxValidationAppVersion, so they must not change.
//
// It scans the protobuf fields in place and returns a subslice of tx, so it
// copies no blob data. Whenever the scan cannot be sure it agrees with the
// generated decoder, it falls back to decodeLegacyBlobTx.
func unwrapLegacyBlobTx(tx Tx) (Tx, bool) {
	inner, isBlobTx, ok := scanLegacyBlobTx(tx)
	if !ok {
		return decodeLegacyBlobTx(tx)
	}
	if !isBlobTx {
		return tx, false
	}
	return inner, true
}

// decodeLegacyBlobTx is the reference implementation of the legacy rules: it
// decodes tx with the generated BlobTx decoder.
func decodeLegacyBlobTx(tx Tx) (Tx, bool) {
	var bTx cmtproto.BlobTx //nolint:staticcheck // legacy rules need the legacy message
	if err := bTx.Unmarshal(tx); err != nil {
		return tx, false
	}
	if bTx.TypeId != squaretx.ProtoBlobTxTypeID || len(bTx.Blobs) == 0 {
		return tx, false
	}
	for _, b := range bTx.Blobs {
		if len(b.NamespaceId) != share.NamespaceIDSize {
			return tx, false
		}
	}
	return bTx.Tx, true
}

// Field numbers shared by cmtproto.BlobTx and cmtproto.IndexWrapper, and those
// of cmtproto.Blob.
const (
	blobTxTxField     protowire.Number = 1
	blobTxBlobsField  protowire.Number = 2
	blobTxTypeIDField protowire.Number = 3

	blobNamespaceIDField      protowire.Number = 1
	blobDataField             protowire.Number = 2
	blobShareVersionField     protowire.Number = 3
	blobNamespaceVersionField protowire.Number = 4
)

// scanLegacyBlobTx applies the legacy rules without copying. ok is false when
// the scan cannot decide, and the caller must then decode tx instead.
//
// protowire is stricter than the generated decoder (field number range, varint
// length, group nesting), so bytes protowire accepts decode to the same fields,
// and a protowire error is reported as !ok rather than as "not a BlobTx". Like
// the generated decoder, the last occurrence of a singular field wins. A known
// field with the wrong wire type is a decode error, which rejects the tx just
// like a mismatched type ID does, so neither needs a fallback.
func scanLegacyBlobTx(tx []byte) (inner []byte, isBlobTx, ok bool) {
	var typeID []byte
	for b := tx; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, false, false
		}
		b = b[n:]
		switch num {
		case blobTxTxField, blobTxBlobsField, blobTxTypeIDField:
			if typ != protowire.BytesType {
				return nil, false, true
			}
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, false, false
			}
			switch num {
			case blobTxTxField:
				inner = v[:len(v):len(v)]
			case blobTxTypeIDField:
				typeID = v
			}
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, false, false
			}
			b = b[n:]
		}
	}
	if string(typeID) != squaretx.ProtoBlobTxTypeID {
		return nil, false, true
	}

	// The first pass checked the framing, so this one only visits the blobs.
	blobs := 0
	for b := tx; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		b = b[n:]
		if num != blobTxBlobsField {
			b = b[protowire.ConsumeFieldValue(num, typ, b):]
			continue
		}
		v, n := protowire.ConsumeBytes(b)
		b = b[n:]
		valid, ok := scanLegacyBlob(v)
		if !ok {
			return nil, false, false
		}
		if !valid {
			return nil, false, true
		}
		blobs++
	}
	if blobs == 0 {
		return nil, false, true
	}
	return inner, true, true
}

// scanLegacyBlob reports whether blob decodes as a cmtproto.Blob whose
// namespace ID has the expected length. ok is false when the scan cannot
// decide.
func scanLegacyBlob(blob []byte) (valid, ok bool) {
	nsLen := 0
	for b := blob; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false, false
		}
		b = b[n:]
		var want protowire.Type
		switch num {
		case blobNamespaceIDField, blobDataField:
			want = protowire.BytesType
		case blobShareVersionField, blobNamespaceVersionField:
			want = protowire.VarintType
		default:
			want = typ
		}
		if typ != want {
			return false, true
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return false, false
		}
		if num == blobNamespaceIDField {
			v, _ := protowire.ConsumeBytes(b)
			nsLen = len(v)
		}
		b = b[n:]
	}
	return nsLen == share.NamespaceIDSize, true
}
