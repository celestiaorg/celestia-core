package types

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/crypto/tmhash"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

const (
	ParityRatio = 2
)

// TxMetaData keeps track of the hash of a transaction and its location within the
// protobuf encoded block.
// Range is [start, end).
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
	// BpHash is the block propagation hash. It's the root of the mekle tree where the leaves are the concatenation of the original partset elements and the parity one.
	BpHash    []byte         `json:"bp_hash,omitempty"`
	Blobs     []TxMetaData   `json:"blobs,omitempty"`
	Signature []byte         `json:"signature,omitempty"`
	Proposal  types.Proposal `json:"proposal,omitempty"`
	// length of the last part
	LastLen uint32 `json:"last_len,omitempty"`
	// the original part set parts hashes.
	PartsHashes [][]byte `json:"parts_hashes,omitempty"`

	mtx sync.Mutex
	// proofsCache is local storage from generated proofs from the PartsHashes.
	// It must not be included in any serialization.
	proofsCache []*merkle.Proof
}

// SignBytes returns the compact block commitment data that
// needs to be signed.
// The sign bytes are the sha256 hash over the following
// concatenated data:
// - BpHash
// - Blobs
// - Proposal.Signature
// - Big endian encoding of LastLen
// - PartsHashes
func (c *CompactBlock) SignBytes() ([]byte, error) {
	bytes := make([]byte, 0)
	bytes = append(bytes, c.BpHash...)
	for _, md := range c.Blobs {
		pb, err := proto.Marshal(md.ToProto())
		if err != nil {
			return nil, err
		}
		bytes = append(bytes, pb...)
	}
	bytes = append(bytes, c.Proposal.Signature...)

	// big endian encode the last len
	lastLenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lastLenBytes, c.LastLen)
	bytes = append(bytes, lastLenBytes...)

	for _, ph := range c.PartsHashes {
		bytes = append(bytes, ph...)
	}

	signBytes := tmhash.Sum(bytes)
	return signBytes, nil
}

// ValidateBasic checks if the CompactBlock is valid. It fails if the height is
// negative, if the round is negative, if the BpHash is invalid, or if any of
// the Blobs are invalid.
func (c *CompactBlock) ValidateBasic() error {
	err := c.Proposal.ValidateBasic()
	if err != nil {
		return err
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

	for index, partHash := range c.PartsHashes {
		if err := types.ValidateHash(partHash); err != nil {
			return fmt.Errorf("invalid part hash height %d round %d index %d: %w", c.Proposal.Height, c.Proposal.Round, index, err)
		}
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
		BpHash:      c.BpHash,
		Blobs:       blobs,
		Signature:   c.Signature,
		Proposal:    c.Proposal.ToProto(),
		LastLength:  c.LastLen,
		PartsHashes: c.PartsHashes,
	}
}

// Proofs returns the proofs to each part. If the proofs are not already
// generated, then they are done so during the first call. An error is only
// thrown if the proofs are generated and the resulting hashes don't match those
// in the compact block. This method should be called upon first receiving a
// compact block.
func (c *CompactBlock) Proofs() ([]*merkle.Proof, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.proofsCache != nil {
		return c.proofsCache, nil
	}

	total := c.Proposal.BlockID.PartSetHeader.Total

	if len(c.PartsHashes) != (ParityRatio * int(total)) {
		return nil, errors.New("invalid number of partset hashes")
	}

	c.proofsCache = make([]*merkle.Proof, 0, len(c.PartsHashes))

	root, proofs := merkle.ProofsFromLeafHashes(c.PartsHashes[:total])
	c.proofsCache = append(c.proofsCache, proofs...)

	if !bytes.Equal(root, c.Proposal.BlockID.PartSetHeader.Hash) {
		return c.proofsCache, fmt.Errorf("incorrect PartsHash: original root")
	}

	parityRoot, eproofs := merkle.ProofsFromLeafHashes(c.PartsHashes[total:])
	c.proofsCache = append(c.proofsCache, eproofs...)

	if !bytes.Equal(c.BpHash, parityRoot) {
		return c.proofsCache, fmt.Errorf("incorrect PartsHash: parity root")
	}

	return c.proofsCache, nil
}

func (c *CompactBlock) GetProof(i uint32) *merkle.Proof {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if i < uint32(len(c.proofsCache)) {
		return c.proofsCache[i]
	}
	return nil
}

func (c *CompactBlock) SetProofCache(proofs []*merkle.Proof) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.proofsCache = proofs
}

// CompactBlockFromProto converts a protobuf CompactBlock to its Go representation.
func CompactBlockFromProto(c *protoprop.CompactBlock) (*CompactBlock, error) {
	blobs := make([]TxMetaData, len(c.Blobs))
	for i, blob := range c.Blobs {
		blobs[i] = *TxMetaDataFromProto(blob)
	}

	prop, err := types.ProposalFromProto(c.Proposal)
	if err != nil {
		return nil, err
	}

	cb := &CompactBlock{
		BpHash:      c.BpHash,
		Blobs:       blobs,
		Signature:   c.Signature,
		Proposal:    *prop,
		LastLen:     c.LastLength,
		PartsHashes: c.PartsHashes,
	}

	return cb, cb.ValidateBasic()
}

// PartMetaData keeps track of the hash of each part, its location via the
// index, along with the proof of inclusion to either the PartSetHeader hash or
// the BPRoot in the CompactBlock.
type PartMetaData struct {
	Index uint32 `json:"index,omitempty"`
	Hash  []byte `json:"hash,omitempty"`
}

// ValidateBasic checks if the PartMetaData is valid. It fails if the hash or
// the proof is invalid.
func (p *PartMetaData) ValidateBasic() error {
	return types.ValidateHash(p.Hash)
}

// HaveParts is the go representation of the wire message for determining the
// route of parts.
type HaveParts struct {
	Height int64          `json:"height,omitempty"`
	Round  int32          `json:"round,omitempty"`
	Parts  []PartMetaData `json:"parts,omitempty"`
}

// BitArray returns a bit array of the provided size with the indexes of the
// parts set to true.
func (h *HaveParts) BitArray(size int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, part := range h.Parts {
		ba.SetIndex(int(part.Index), true)
	}
	return ba
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

// ToProto converts HaveParts to its protobuf representation.
func (h *HaveParts) ToProto() *protoprop.HaveParts {
	parts := make([]*protoprop.PartMetaData, len(h.Parts))
	for i, part := range h.Parts {
		parts[i] = &protoprop.PartMetaData{
			Index: part.Index,
			Hash:  part.Hash,
		}
	}
	return &protoprop.HaveParts{
		Height: h.Height,
		Round:  h.Round,
		Parts:  parts,
	}
}

// HavePartFromProto converts a protobuf HaveParts to its Go representation.
func HavePartFromProto(h *protoprop.HaveParts) (*HaveParts, error) {
	parts := make([]PartMetaData, len(h.Parts))
	for i, part := range h.Parts {
		parts[i] = PartMetaData{
			Index: part.Index,
			Hash:  part.Hash,
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
	Prove  bool           `json:"prove,omitempty"`
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
		Prove:  w.Prove,
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
		Prove:  w.Prove,
	}
	return wp, wp.ValidateBasic()
}

type RecoveryPart struct {
	Height int64
	Round  int32
	Index  uint32
	Data   []byte
	Proof  *merkle.Proof
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
		blobs := make([]TxMetaData, len(msg.Blobs))
		for i, blob := range msg.Blobs {
			blobs[i] = TxMetaData{
				Hash:  blob.Hash,
				Start: blob.Start,
				End:   blob.End,
			}
		}
		prop, err := types.ProposalFromProto(msg.Proposal)
		if err != nil {
			return nil, err
		}
		pb = &CompactBlock{
			BpHash:      msg.BpHash,
			Blobs:       blobs,
			Signature:   msg.Signature,
			Proposal:    *prop,
			LastLen:     msg.LastLength,
			PartsHashes: msg.PartsHashes,
		}
	case *protoprop.PartMetaData:
		pb = &PartMetaData{
			Index: msg.Index,
			Hash:  msg.Hash,
		}
	case *protoprop.HaveParts:
		parts := make([]PartMetaData, len(msg.Parts))
		for i, part := range msg.Parts {
			parts[i] = PartMetaData{
				Index: part.Index,
				Hash:  part.Hash,
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
			Prove:  msg.Prove,
		}
	case *protoprop.RecoveryPart:
		proof, err := merkle.ProofFromProto(&msg.Proof, true)
		if err != nil {
			return pb, err
		}
		pb = &RecoveryPart{
			Height: msg.Height,
			Round:  msg.Round,
			Index:  msg.Index,
			Data:   msg.Data,
			Proof:  proof,
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
