package types

import (
	"errors"
	"fmt"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

// TxMetaData keeps track of the hash of a transaction and its location within the
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

// ValidateBasic checks if the CompactBlock is valid. It fails if the height is
// negative, if the round is negative, if the BpHash is invalid, or if any of
// the Blobs are invalid.
func (c *CompactBlock) ValidateBasic() error {
	if c.Height < 0 {
		return errors.New("CompactBlock: Height cannot be negative")
	}
	if c.Round < 0 {
		return errors.New("CompactBlock: Round cannot be negative")
	}
	if err := types.ValidateHash(c.BpHash); err != nil {
		return err
	}
	for _, blob := range c.Blobs {
		if err := blob.ValidateBasic(); err != nil {
			return err
		}
	}
	if len(c.Signature) > types.MaxSignatureSize {
		return errors.New("CompactBlock: Signature is too big")
	}
	return nil
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
func CompactBlockFromProto(c *protoprop.CompactBlock) (*CompactBlock, error) {
	blobs := make([]*TxMetaData, len(c.Blobs))
	for i, blob := range c.Blobs {
		blobs[i] = TxMetaDataFromProto(blob)
	}
	cb := &CompactBlock{
		Height:    c.Height,
		Round:     c.Round,
		BpHash:    c.BpHash,
		Blobs:     blobs,
		Signature: c.Signature,
	}
	return cb, cb.ValidateBasic()
}

// PartMetaData keeps track of the hash of each part, its location via the
// index, along with the proof of inclusion to either the PartSetHeader hash or
// the BPRoot in the CompactBlock.
type PartMetaData struct {
	Index uint32       `json:"index,omitempty"`
	Hash  []byte       `json:"hash,omitempty"`
	Proof merkle.Proof `json:"proof"`
}

// ValidateBasic checks if the PartMetaData is valid. It fails if the hash or
// the proof is invalid.
func (p *PartMetaData) ValidateBasic() error {
	err := p.Proof.ValidateBasic()
	if err != nil {
		return err
	}
	return types.ValidateHash(p.Hash)
}

// HaveParts is the go representation of the wire message for determining the
// route of parts.
type HaveParts struct {
	Height int64          `json:"height,omitempty"`
	Round  int32          `json:"round,omitempty"`
	Parts  []PartMetaData `json:"parts,omitempty"`
}

// ValidateBasic checks if the HaveParts is valid. It fails if Parts is nil or
// empty, or if any of the parts are invalid.
func (h *HaveParts) ValidateBasic() error {
	if len(h.Parts) == 0 {
		return errors.New("HaveParts: Parts cannot be nil or empty")
	}
	if h.Height < 0 || h.Round < 0 {
		return errors.New("HaveParts: Height and Round cannot be negative")
	}
	for _, part := range h.Parts {
		err := part.ValidateBasic()
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *HaveParts) SetIndex(i uint32, Hash []byte, Proof *merkle.Proof) {
	// TODO set the parts in an ordered way and support getting them faster.
	h.Parts = append(h.Parts, PartMetaData{i, Hash, *Proof})
}

func (h *HaveParts) RemoveIndex(i uint32) {
	parts := make([]PartMetaData, 0)
	for _, part := range h.Parts {
		if part.Index != i {
			parts = append(parts, part)
		}
	}
	h.Parts = parts
}

func (h *HaveParts) IsEmpty() bool {
	return len(h.Parts) == 0
}

func (h *HaveParts) GetIndex(i uint32) bool {
	// TODO set the parts in an ordered way and support getting them faster and also get the proof and verify it
	for _, part := range h.Parts {
		if part.Index == i {
			return true
		}
	}
	return false
}

func (h *HaveParts) Copy() *HaveParts {
	partsCopy := make([]PartMetaData, len(h.Parts))
	for i, part := range h.Parts {
		hashCopy := make([]byte, len(part.Hash))
		copy(hashCopy, part.Hash)

		partsCopy[i] = PartMetaData{
			Index: part.Index,
			Hash:  hashCopy,
			Proof: merkle.Proof{
				Total:    part.Proof.Total,
				Index:    part.Proof.Index,
				LeafHash: part.Proof.LeafHash,
				Aunts:    part.Proof.Aunts, // TODO also deep copy this
			},
		}
	}

	return &HaveParts{
		Height: h.Height,
		Round:  h.Round,
		Parts:  partsCopy,
	}
}

// Sub
// TODO document that this makes changes on the receiving object
func (h *HaveParts) Sub(parts *bits.BitArray) {
	size := min(len(h.Parts), parts.Size())
	newParts := make([]PartMetaData, 0)
	// TODO improve this implementation not to iterate this way on all possibilities
	for i := 0; i < size; i++ {
		if !parts.GetIndex(int(h.Parts[i].Index)) {
			newParts = append(newParts, h.Parts[i])
		}
	}
	h.Parts = newParts
}

func (h *HaveParts) GetTrueIndices() []int {
	// TODO make this not iterate all over the elements
	indices := make([]int, len(h.Parts))
	for i, part := range h.Parts {
		indices[i] = int(part.Index)
	}
	return indices
}

// ToProto converts HaveParts to its protobuf representation.
func (h *HaveParts) ToProto() *protoprop.HaveParts {
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

func (h *HaveParts) ToBitArray() *bits.BitArray {
	array := bits.NewBitArray(len(h.Parts))
	for _, part := range h.Parts {
		array.SetIndex(int(part.Index), true)
	}
	return array
}

// HavePartFromProto converts a protobuf HaveParts to its Go representation.
func HavePartFromProto(h *protoprop.HaveParts) (*HaveParts, error) {
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
	hp := &HaveParts{
		Height: h.Height,
		Round:  h.Round,
		Parts:  parts,
	}
	return hp, hp.ValidateBasic()
}

// WantParts is a message that requests a set of parts from a peer.
type WantParts struct {
	Parts  *bits.BitArray `json:"parts"`
	Height int64          `json:"height,omitempty"`
	Round  int32          `json:"round,omitempty"`
}

func (w *WantParts) ValidateBasic() error {
	if w.Parts == nil {
		return errors.New("WantParts: Parts cannot be nil")
	}
	return nil
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
	wp := &WantParts{
		Parts:  ba,
		Height: w.Height,
		Round:  w.Round,
	}
	return wp, wp.ValidateBasic()
}

type RecoveryPart struct {
	Height int64
	Round  int32
	Index  uint32
	Data   []byte
}

func (p *RecoveryPart) ValidateBasic() error {
	// TODO: implement
	return nil
}

// MsgFromProto takes a consensus proto message and returns the native go type
func MsgFromProto(p *protoprop.Message) (Message, error) {
	if p == nil {
		return nil, errors.New("propagation: nil message")
	}
	var pb Message
	um, err := p.Unwrap()
	if err != nil {
		return nil, err
	}

	switch msg := um.(type) {
	case *protoprop.TxMetaData:
		pb = &TxMetaData{
			Hash:  msg.Hash,
			Start: msg.Start,
			End:   msg.End,
		}
	case *protoprop.CompactBlock:
		blobs := make([]*TxMetaData, len(msg.Blobs))
		for i, blob := range msg.Blobs {
			blobs[i] = &TxMetaData{
				Hash:  blob.Hash,
				Start: blob.Start,
				End:   blob.End,
			}
		}
		pb = &CompactBlock{
			Height:    msg.Height,
			Round:     msg.Round,
			BpHash:    msg.BpHash,
			Blobs:     blobs,
			Signature: msg.Signature,
		}
	case *protoprop.PartMetaData:
		pb = &PartMetaData{
			Index: msg.Index,
			Hash:  msg.Hash,
			Proof: merkle.Proof{
				Total:    msg.Proof.Total,
				Index:    msg.Proof.Index,
				LeafHash: msg.Proof.LeafHash,
				Aunts:    msg.Proof.Aunts,
			},
		}
	case *protoprop.HaveParts:
		parts := make([]PartMetaData, len(msg.Parts))
		for i, part := range msg.Parts {
			parts[i] = PartMetaData{
				Index: part.Index,
				Hash:  part.Hash,
				Proof: merkle.Proof{
					Total:    part.Proof.Total,
					Index:    part.Proof.Index,
					LeafHash: part.Proof.LeafHash,
					Aunts:    part.Proof.Aunts,
				},
			}
		}
		pb = &HaveParts{
			Height: msg.Height,
			Round:  msg.Round,
			Parts:  parts,
		}
	case *protoprop.WantParts:
		array := bits.NewBitArray(msg.Parts.Size())
		array.FromProto(&msg.Parts)
		pb = &WantParts{
			Parts:  array,
			Height: msg.Height,
			Round:  msg.Round,
		}
	case *protoprop.RecoveryPart:
		pb = &RecoveryPart{
			Height: msg.Height,
			Round:  msg.Round,
			Index:  msg.Index,
			Data:   msg.Data,
		}
	default:
		return nil, fmt.Errorf("propagation: message not recognized: %T", msg)
	}

	if err := pb.ValidateBasic(); err != nil {
		return nil, err
	}

	return pb, nil
}

// Message is a message that can be sent and received on the Reactor
type Message interface {
	ValidateBasic() error
}

// TODO: register all the underlying types in an init
