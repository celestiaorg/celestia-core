package types

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/crypto/tmhash"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
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
	if t.Start >= t.End {
		fmt.Println(fmt.Errorf("TxMetaData: start is greater than end %d >= %d", t.Start, t.End).Error())
		os.Exit(8)
	}

	if len(t.Hash) != tmhash.Size {
		fmt.Println(fmt.Errorf("TxMetaData: hash size is invalid %X", t.Hash).Error())
		os.Exit(9)
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
	// the original + parity part set parts hashes.
	PartsHashes [][]byte `json:"parts_hashes,omitempty"`

	mtx sync.Mutex
	// proofsCache is local storage from generated proofs from the PartsHashes.
	// It must not be included in any serialization.
	proofsCache []*merkle.Proof
}

func (cb *CompactBlock) ToString() string {
	cb.mtx.Lock()
	defer cb.mtx.Unlock()

	// Format PartsHashes
	var partsHashes []string
	for _, ph := range cb.PartsHashes {
		partsHashes = append(partsHashes, fmt.Sprintf("%X", ph))
	}

	// Format Blobs (TxMetaData)
	var blobs []string
	for _, b := range cb.Blobs {
		blobs = append(blobs, fmt.Sprintf(`TxMetaData{
    Hash: %X,
    Start: %d,
    End: %d
  }`, b.Hash, b.Start, b.End))
	}

	// Format Proposal.BlockID.PartSetHeader
	psh := cb.Proposal.BlockID.PartSetHeader
	partSetHeaderStr := fmt.Sprintf(`PartSetHeader{
      Total: %d,
      Hash: %X
    }`, psh.Total, psh.Hash)

	// Format Proposal.BlockID
	bid := cb.Proposal.BlockID
	blockIDStr := fmt.Sprintf(`BlockID{
      Hash: %X,
      PartSetHeader: %s
    }`, bid.Hash, partSetHeaderStr)

	// Format Proposal
	proposalStr := fmt.Sprintf(`Proposal{
    Type: %v,
    Height: %d,
    Round: %d,
    POLRound: %d,
    BlockID: %s,
    Timestamp: %s,
    Signature: %X
  }`,
		cb.Proposal.Type,
		cb.Proposal.Height,
		cb.Proposal.Round,
		cb.Proposal.POLRound,
		blockIDStr,
		cb.Proposal.Timestamp.Format(time.RFC3339Nano),
		cb.Proposal.Signature,
	)

	// Final CompactBlock String
	return fmt.Sprintf(`CompactBlock{
  BpHash: %X,
  Blobs: [%s],
  Signature: %X,
  Proposal: %s,
  LastLen: %d,
  PartsHashes: [%s]
}`,
		cb.BpHash,
		strings.Join(blobs, ", "),
		cb.Signature,
		proposalStr,
		cb.LastLen,
		strings.Join(partsHashes, ", "),
	)
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

	// validate tx metadata
	err = hasOverlappingRanges(c.Blobs)
	if err != nil {
		return err
	}
	return nil
}

// hasOverlappingRanges checks whether any ranges in the provided slice of TxMetaData overlap.
// Returns an error if overlapping ranges are found, otherwise returns nil.
func hasOverlappingRanges(blobs []TxMetaData) error {
	if len(blobs) == 0 {
		return nil
	}
	// Create a copy of the blobs slice to avoid mutating the original
	blobsCopy := make([]TxMetaData, len(blobs))
	copy(blobsCopy, blobs)
	sort.Slice(blobsCopy, func(i, j int) bool {
		return blobsCopy[i].Start < blobsCopy[j].Start
	})

	for i := 1; i < len(blobsCopy); i++ {
		prev := blobsCopy[i-1]
		curr := blobsCopy[i]

		// If current range starts before previous range ends, there's an overlap
		if curr.Start < prev.End { // using < instead of <= because the ranges are [start:end)
			return fmt.Errorf("overlapping tx metadata ranges: %d:[%d-%d) and %d:[%d-%d)", i-1, prev.Start, prev.End, i, curr.Start, curr.End)
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
	if c == nil {
		return nil, errors.New("propagation: nil compact block")
	}
	blobs := make([]TxMetaData, len(c.Blobs))
	for i, blob := range c.Blobs {
		if blob == nil {
			return nil, errors.New("CompactBlock: nil blob")
		}
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

// BitArrary returns a bit array of the provided size with the indexes of the
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

// ValidatePartHashes verifies that each part's hash in the HaveParts struct matches the corresponding expected hash.
// Returns an error if any hash does not match, indicating the index of the first mismatch.
func (h *HaveParts) ValidatePartHashes(expectedHashes [][]byte) error {
	for index, part := range h.Parts {
		if int(part.Index) >= len(expectedHashes) {
			return fmt.Errorf("non existing part hash index %d", index)
		}
		if !bytes.Equal(part.Hash, expectedHashes[part.Index]) {
			return fmt.Errorf("invalid part hash at index %d", index)
		}
	}
	return nil
}

func (h *HaveParts) IsEmpty() bool {
	return len(h.Parts) == 0
}

func (h *HaveParts) GetIndex(i uint32) bool {
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
	if h == nil {
		return nil, errors.New("propagation: nil have parts")
	}
	parts := make([]PartMetaData, len(h.Parts))
	for i, part := range h.Parts {
		if part == nil {
			return nil, fmt.Errorf("HaveParts: nil part at index %d", i)
		}
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
	Parts             *bits.BitArray `json:"parts"`
	Height            int64          `json:"height,omitempty"`
	Round             int32          `json:"round,omitempty"`
	Prove             bool           `json:"prove,omitempty"`
	MissingPartsCount int32          `json:"missing_parts_count,omitempty"`
}

func (w *WantParts) ValidateBasic() error {
	if w.Parts == nil {
		return errors.New("WantParts: Parts cannot be nil")
	}
	if w.MissingPartsCount <= 0 {
		return errors.New("WantParts: MissingPartsCount cannot be negative or zero")
	}
	return nil
}

// ToProto converts WantParts to its protobuf representation.
func (w *WantParts) ToProto() *protoprop.WantParts {
	return &protoprop.WantParts{
		Parts:             *w.Parts.ToProto(),
		Height:            w.Height,
		Round:             w.Round,
		Prove:             w.Prove,
		MissingPartsCount: w.MissingPartsCount,
	}
}

// WantPartsFromProto converts a protobuf WantParts to its Go representation.
func WantPartsFromProto(w *protoprop.WantParts) (*WantParts, error) {
	if w == nil {
		return nil, errors.New("propagation: nil want parts")
	}

	array := bits.NewBitArray(w.Parts.Size())
	if array == nil {
		return nil, errors.New("WantParts: nil parts")
	}
	array.FromProto(&w.Parts)
	wp := &WantParts{
		Parts:             array,
		Height:            w.Height,
		Round:             w.Round,
		Prove:             w.Prove,
		MissingPartsCount: w.MissingPartsCount,
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
	if p == nil {
		return errors.New("propagation: nil recovery part")
	}
	if p.Height < 0 || p.Round < 0 {
		return errors.New("RecoveryPart: Height and Round cannot be negative")
	}
	if len(p.Data) == 0 {
		return errors.New("RecoveryPart: Data cannot be nil or empty")
	}
	if p.Proof != nil {
		if err := p.Proof.ValidateBasic(); err != nil {
			return fmt.Errorf("RecoveryPart: invalid proof: %w", err)
		}
		hash := merkle.LeafHash(p.Data)
		if !bytes.Equal(hash, p.Proof.LeafHash) {
			return errors.New("RecoveryPart: invalid proof leaf hash")
		}
	}
	return nil
}

func RecoveryPartFromProto(r *protoprop.RecoveryPart) (*RecoveryPart, error) {
	if r == nil {
		return nil, errors.New("propagation: nil recovery part")
	}
	proof, err := merkle.ProofFromProto(&r.Proof, true)
	if err != nil {
		return nil, err
	}
	rp := &RecoveryPart{
		Height: r.Height,
		Round:  r.Round,
		Index:  r.Index,
		Data:   r.Data,
		Proof:  proof,
	}
	return rp, rp.ValidateBasic()
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
	case *protoprop.CompactBlock:
		compactBlock, err := CompactBlockFromProto(msg)
		if err != nil {
			return nil, err
		}
		pb = compactBlock
	case *protoprop.HaveParts:
		haveParts, err := HavePartFromProto(msg)
		if err != nil {
			return nil, err
		}
		pb = haveParts
	case *protoprop.WantParts:
		wantParts, err := WantPartsFromProto(msg)
		if err != nil {
			return nil, err
		}
		pb = wantParts
	case *protoprop.RecoveryPart:
		recoveryPart, err := RecoveryPartFromProto(msg)
		if err != nil {
			return nil, err
		}
		pb = recoveryPart
	default:
		return nil, fmt.Errorf("propagation: message not recognized: %T", msg)
	}

	return pb, nil
}

// Message is a message that can be sent and received on the Reactor
type Message interface {
	ValidateBasic() error
}
