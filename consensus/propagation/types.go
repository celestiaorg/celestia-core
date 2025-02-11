package types

import (
	"errors"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

// TxmetaData keeps track of the hash of a transaction and its location with the
// protobuf encoded block.
type TxMetaData struct {
	Hash  []byte `protobuf:"bytes,1,opt,name=hash,proto3" json:"hash,omitempty"`
	Start uint32
	End   uint32
}

// ToProto converts TxMetaData to its protobuf representation.
func (t *TxMetaData) ToProto() *protoprop.TxMetaData {
	return &protoprop.TxMetaData{
		Hash:  t.Hash,
		Start: t.Start,
		End:   t.End,
	}
}

// TxMetaDataFromProto converts a protobuf TxMetaData to its Go representation.
func TxMetaDataFromProto(t *protoprop.TxMetaData) *TxMetaData {
	return &TxMetaData{
		Hash:  t.Hash,
		Start: t.Start,
		End:   t.End,
	}
}

// ValidateBasic checks if the TxMetaData is valid. It fails if Start > End or
// if the hash is invalid.
func (t *TxMetaData) ValidateBasic() error {
	if t.Start > t.End {
		return errors.New("TxMetaData: Start > End")
	}

	return types.ValidateHash(t.Hash)
}

// CompactBlock contains commitments and metadata for reusing transactions that
// have already been distributed.
type CompactBlock struct {
	Height    int64         `json:"height,omitempty"`
	Round     int32         `json:"round,omitempty"`
	BpHash    []byte        `json:"bp_hash,omitempty"`
	Blobs     []*TxMetaData `json:"blobs,omitempty"`
	Signature []byte        `json:"signature,omitempty"`
}

// ToProto converts CompactBlock to its protobuf representation.
func (c *CompactBlock) ToProto() *protoprop.CompactBlock {
	blobs := make([]*protoprop.TxMetaData, len(c.Blobs))
	for i, blob := range c.Blobs {
		blobs[i] = blob.ToProto()
	}
	return &protoprop.CompactBlock{
		Height:    c.Height,
		Round:     c.Round,
		BpHash:    c.BpHash,
		Blobs:     blobs,
		Signature: c.Signature,
	}
}

// CompactBlockFromProto converts a protobuf CompactBlock to its Go representation.
func CompactBlockFromProto(c *protoprop.CompactBlock) *CompactBlock {
	blobs := make([]*TxMetaData, len(c.Blobs))
	for i, blob := range c.Blobs {
		blobs[i] = TxMetaDataFromProto(blob)
	}
	return &CompactBlock{
		Height:    c.Height,
		Round:     c.Round,
		BpHash:    c.BpHash,
		Blobs:     blobs,
		Signature: c.Signature,
	}
}

// PartMetaData keeps track of the hash of each part, its location via the
// index, along with the proof of inclusion to either the PartSetHeader hash or
// the BPRoot in the CompactBlock.
type PartMetaData struct {
	Index uint32       `json:"index,omitempty"`
	Hash  []byte       `json:"hash,omitempty"`
	Proof merkle.Proof `json:"proof"`
}

// ToProto converts PartMetaData to its protobuf representation.
type HavePart struct {
	Height int64          `json:"height,omitempty"`
	Round  int32          `json:"round,omitempty"`
	Parts  []PartMetaData `json:"parts,omitempty"`
}

// ToProto converts HavePart to its protobuf representation.
func (h *HavePart) ToProto() *protoprop.HaveParts {
	parts := make([]*protoprop.PartMetaData, len(h.Parts))
	for i, part := range h.Parts {
		parts[i] = &protoprop.PartMetaData{
			Index: part.Index,
			Hash:  part.Hash,
			Proof: *part.Proof.ToProto(),
		}
	}
	return &protoprop.HaveParts{
		Height: h.Height,
		Round:  h.Round,
		Parts:  parts,
	}
}

// HavePartFromProto converts a protobuf HavePart to its Go representation.
func HavePartFromProto(h *protoprop.HaveParts) (*HavePart, error) {
	parts := make([]PartMetaData, len(h.Parts))
	for i, part := range h.Parts {
		proof, err := merkle.ProofFromProto(&part.Proof)
		if err != nil {
			return nil, err
		}
		parts[i] = PartMetaData{
			Index: part.Index,
			Hash:  part.Hash,
			Proof: *proof,
		}
	}
	return &HavePart{
		Height: h.Height,
		Round:  h.Round,
		Parts:  parts,
	}, nil
}

// WantParts is a message that requests a set of parts from a peer.
type WantParts struct {
	Parts  *bits.BitArray `json:"parts"`
	Height int64          `json:"height,omitempty"`
	Round  int32          `json:"round,omitempty"`
}

// ToProto converts WantParts to its protobuf representation.
func (w *WantParts) ToProto() *protoprop.WantParts {
	return &protoprop.WantParts{
		Parts:  *w.Parts.ToProto(),
		Height: w.Height,
		Round:  w.Round,
	}
}

// WantPartsFromProto converts a protobuf WantParts to its Go representation.
func WantPartsFromProto(w *protoprop.WantParts) (*WantParts, error) {
	ba := new(bits.BitArray)
	ba.FromProto(&w.Parts)
	return &WantParts{
		Parts:  ba,
		Height: w.Height,
		Round:  w.Round,
	}, nil
}
