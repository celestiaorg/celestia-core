package types

import (
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/celestiaorg/go-square/v3/share"
	square "github.com/celestiaorg/go-square/v3/tx"
)

// Field numbers shared by BlobTx and IndexWrapper. Both carry the transaction
// they wrap in field 1 and their type marker in field 3. Field 2 is the blob
// list in a BlobTx and the share indexes in an IndexWrapper.
const (
	wrapperTxField     = 1
	wrapperPayloadFld  = 2
	wrapperTypeIDField = 3

	// blobNamespaceField is the namespace id inside a Blob.
	blobNamespaceField = 1
)

// unwrapVerdict is what a scan of a transaction's top level fields concluded.
type unwrapVerdict int

const (
	// unwrapPlain means the transaction parsed cleanly and carries neither type
	// marker, so no decode can treat it as a wrapper and its own bytes are what
	// gets hashed.
	unwrapPlain unwrapVerdict = iota
	// unwrapInner means the transaction is a wrapper and the scan extracted the
	// transaction it carries.
	unwrapInner
	// unwrapUndecided means the scan could not settle it. The caller must fall
	// back to decoding the transaction, which is authoritative.
	unwrapUndecided
)

// scanWrapper looks for the transaction inside a BlobTx or an IndexWrapper by
// walking the encoded fields rather than decoding them.
//
// Decoding costs a copy of every blob, which on this chain is most of the
// block, and all that is needed is the type marker and a subslice of the
// original bytes. The scan is deliberately timid: anything it is not sure about
// comes back as unwrapUndecided, so it can only ever save work, never change
// which transaction a hash is taken over.
func scanWrapper(tx []byte) ([]byte, unwrapVerdict) {
	var (
		inner      []byte
		typeID     []byte
		blobCount  int
		blobsValid = true
	)

	buf := tx
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if n < 0 {
			return nil, unwrapUndecided
		}
		buf = buf[n:]

		switch num {
		case wrapperTxField, wrapperPayloadFld, wrapperTypeIDField:
			// A known field with an unexpected wire type is a decode error for
			// the generated unmarshaller, which the scan must not paper over.
			if typ != protowire.BytesType {
				return nil, unwrapUndecided
			}
			value, n := protowire.ConsumeBytes(buf)
			if n < 0 {
				return nil, unwrapUndecided
			}
			buf = buf[n:]

			switch num {
			case wrapperTxField:
				inner = value
			case wrapperTypeIDField:
				typeID = value
			default:
				// Only meaningful for a BlobTx; an IndexWrapper's packed share
				// indexes land here too and are ignored below.
				blobCount++
				if !blobHasNamespace(value) {
					blobsValid = false
				}
			}
		default:
			// Groups are skipped differently by the generated decoder than
			// by protowire, so leave them to the decoder.
			if typ == protowire.StartGroupType || typ == protowire.EndGroupType {
				return nil, unwrapUndecided
			}
			n := protowire.ConsumeFieldValue(num, typ, buf)
			if n < 0 {
				return nil, unwrapUndecided
			}
			buf = buf[n:]
		}
	}

	switch string(typeID) {
	case square.ProtoIndexWrapperTypeID:
		// An IndexWrapper's share indexes are packed varints, and deciding
		// whether they parse costs as much as decoding the message. Index
		// wrappers only exist inside the square, never in a block's
		// transaction list, so the slow path here is not a path that runs.
		return nil, unwrapUndecided
	case square.ProtoBlobTxTypeID:
		// UnmarshalBlobTx also requires at least one blob, each with a full
		// length namespace id.
		if blobCount == 0 || !blobsValid {
			return nil, unwrapUndecided
		}
		return inner, unwrapInner
	default:
		// Neither decode can succeed without its type marker.
		return nil, unwrapPlain
	}
}

// Blob field numbers and the wire type each must carry. A known field with
// another wire type is a decode error for the generated unmarshaller, so the
// scan must not skip over it.
var blobFieldWireTypes = map[protowire.Number]protowire.Type{
	blobNamespaceField: protowire.BytesType,  // namespace_id
	2:                  protowire.BytesType,  // data
	3:                  protowire.VarintType, // share_version
	4:                  protowire.VarintType, // namespace_version
}

// blobHasNamespace reports whether an encoded Blob would decode and carries a
// namespace id of the expected length, which is the only part of a blob the
// wrapper check looks at. It returns false whenever the generated decoder
// could reject the blob, so the caller falls back to decoding.
func blobHasNamespace(blob []byte) bool {
	found := false
	for len(blob) > 0 {
		num, typ, n := protowire.ConsumeTag(blob)
		if n < 0 {
			return false
		}
		blob = blob[n:]

		if want, known := blobFieldWireTypes[num]; known && typ != want {
			return false
		}
		if typ == protowire.StartGroupType || typ == protowire.EndGroupType {
			return false
		}

		if num == blobNamespaceField {
			namespace, n := protowire.ConsumeBytes(blob)
			if n < 0 {
				return false
			}
			blob = blob[n:]
			found = len(namespace) == share.NamespaceIDSize
			continue
		}

		n = protowire.ConsumeFieldValue(num, typ, blob)
		if n < 0 {
			return false
		}
		blob = blob[n:]
	}
	return found
}

// unwrap returns the transaction that tx carries if it is a BlobTx or an
// IndexWrapper, and tx itself otherwise. It is what Hash and Key are taken
// over.
func unwrap(tx Tx) []byte {
	switch inner, verdict := scanWrapper(tx); verdict {
	case unwrapInner:
		return inner
	case unwrapPlain:
		return tx
	}

	// The scan was not sure, so decode. BlobTx is tried first because it is
	// what almost every transaction on this chain is.
	if blobTx, isBlobTx := UnmarshalBlobTx(tx); isBlobTx {
		return blobTx.Tx
	}
	if indexWrapper, isIndexWrapper := UnmarshalIndexWrapper(tx); isIndexWrapper {
		return indexWrapper.Tx
	}
	return tx
}
