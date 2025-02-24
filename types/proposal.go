package types

import (
	"errors"
	"fmt"
	"time"

	"github.com/tendermint/tendermint/crypto"
	cmtbytes "github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/libs/protoio"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
	cmttime "github.com/tendermint/tendermint/types/time"
)

var (
	ErrInvalidBlockPartSignature = errors.New("error invalid block part signature")
	ErrInvalidBlockPartHash      = errors.New("error invalid block part hash")
)

// TxMetaData keeps track of the hash of a transaction and its location within the
// protobuf encoded block.
type TxMetaData struct {
	Hash  []byte `protobuf:"bytes,1,opt,name=hash,proto3" json:"hash,omitempty"`
	Start uint32 // todo(evan): fill in start values in and use them to reconstruct the block
	End   uint32 // todo(evan): fill in end values in and use them to reconstruct the block
}

// ToProto converts TxMetaData to its protobuf representation.
func (t *TxMetaData) ToProto() *cmtproto.TxMetaData {
	return &cmtproto.TxMetaData{
		Hash:  t.Hash,
		Start: t.Start,
		End:   t.End,
	}
}

// TxMetaDataFromProto converts a protobuf TxMetaData to its Go representation.
func TxMetaDataFromProto(t *cmtproto.TxMetaData) *TxMetaData {
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

	return ValidateHash(t.Hash)
}

// CompactBlock contains commitments and metadata for reusing transactions that
// have already been distributed.
type CompactBlock struct {
	Height    int64         `json:"height,omitempty"`
	Round     int32         `json:"round,omitempty"`
	BpHash    []byte        `json:"bp_hash,omitempty"`
	Blobs     []*TxMetaData `json:"blobs,omitempty"`
	Signature []byte        `json:"signature,omitempty"`
	LastLen   uint32        `json:"last_length,omitempty"`
}

// NewCompactBlock wraps struct creation and calls validate basic before returning.
func NewCompactBlock(height int64, round int32, lastLen uint32, extendedPS *PartSet, blobHashes []*TxMetaData) (CompactBlock, error) {
	compBlock := CompactBlock{
		Height:  height,
		Round:   round,
		BpHash:  extendedPS.Hash(),
		Blobs:   blobHashes,
		LastLen: lastLen,
	}
	return compBlock, compBlock.ValidateBasic()
}

func (c *CompactBlock) SignBytes() []byte {
	// todo: implement
	return []byte{}
}

// VerifySignature verifies the signature over the compact block.
// WARN: not implemented.
func (c *CompactBlock) VerifySig(pubK crypto.PubKey) bool {
	// todo: implement
	return pubK.VerifySignature(c.SignBytes(), c.Signature)
}

// ValidateBasic checks if the CompactBlock is valid. It fails if the height is
// negative, if the round is negative, if the BpHash is invalid, or if any of
// the Blobs are invalid.
func (c *CompactBlock) ValidateBasic() error {
	if c.Height < 0 {
		return errors.New("CompactBlock: Height cannot be negative")
	}
	if err := ValidateHash(c.BpHash); err != nil {
		return err
	}
	for _, blob := range c.Blobs {
		if err := blob.ValidateBasic(); err != nil {
			return err
		}
	}
	// todo(evan): uncomment when adding the signature
	// if len(c.Signature) > MaxSignatureSize {
	// 	return errors.New("CompactBlock: Signature is too big")
	// }
	return nil
}

// ToProto converts CompactBlock to its protobuf representation.
func (c *CompactBlock) ToProto() *cmtproto.CompactBlock {
	blobs := make([]*cmtproto.TxMetaData, len(c.Blobs))
	for i, blob := range c.Blobs {
		blobs[i] = blob.ToProto()
	}
	return &cmtproto.CompactBlock{
		Height:     c.Height,
		Round:      c.Round,
		BpHash:     c.BpHash,
		Blobs:      blobs,
		Signature:  c.Signature,
		LastLength: c.LastLen,
	}
}

// CompactBlockFromProto converts a protobuf CompactBlock to its Go representation.
func CompactBlockFromProto(c *cmtproto.CompactBlock) (*CompactBlock, error) {
	if c == nil {
		return &CompactBlock{}, nil
	}
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
		LastLen:   c.LastLength,
	}
	return cb, cb.ValidateBasic()
}

// Proposal defines a block proposal for the consensus.
// It refers to the block by BlockID field.
// It must be signed by the correct proposer for the given Height/Round
// to be considered valid. It may depend on votes from a previous round,
// a so-called Proof-of-Lock (POL) round, as noted in the POLRound.
// If POLRound >= 0, then BlockID corresponds to the block that is locked in POLRound.
type Proposal struct {
	Type         cmtproto.SignedMsgType
	Height       int64        `json:"height"`
	Round        int32        `json:"round"`     // there can not be greater than 2_147_483_647 rounds
	POLRound     int32        `json:"pol_round"` // -1 if null.
	BlockID      BlockID      `json:"block_id"`
	Timestamp    time.Time    `json:"timestamp"`
	Signature    []byte       `json:"signature"`
	CompactBlock CompactBlock `json:"compact_block"`
}

// NewProposal returns a new Proposal.
// If there is no POLRound, polRound should be -1.
func NewProposal(height int64, round int32, polRound int32, blockID BlockID, compBlock CompactBlock) *Proposal {
	return &Proposal{
		Type:         cmtproto.ProposalType,
		Height:       height,
		Round:        round,
		BlockID:      blockID,
		POLRound:     polRound,
		Timestamp:    cmttime.Now(),
		CompactBlock: compBlock,
	}
}

// ValidateBasic performs basic validation.
func (p *Proposal) ValidateBasic() error {
	if p.Type != cmtproto.ProposalType {
		return errors.New("invalid Type")
	}
	if p.Height < 0 {
		return errors.New("negative Height")
	}
	if p.Round < 0 {
		return errors.New("negative Round")
	}
	if p.POLRound < -1 {
		return errors.New("negative POLRound (exception: -1)")
	}
	if err := p.BlockID.ValidateBasic(); err != nil {
		return fmt.Errorf("wrong BlockID: %v", err)
	}
	// ValidateBasic above would pass even if the BlockID was empty:
	if !p.BlockID.IsComplete() {
		return fmt.Errorf("expected a complete, non-empty BlockID, got: %v", p.BlockID)
	}

	// NOTE: Timestamp validation is subtle and handled elsewhere.

	if len(p.Signature) == 0 {
		return errors.New("signature is missing")
	}

	if len(p.Signature) > MaxSignatureSize {
		return fmt.Errorf("signature is too big (max: %d)", MaxSignatureSize)
	}

	// TODO: Handle the compblock failures in a backwards compatible way
	if err := p.CompactBlock.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

// String returns a string representation of the Proposal.
//
// 1. height
// 2. round
// 3. block ID
// 4. POL round
// 5. first 6 bytes of signature
// 6. timestamp
//
// See BlockID#String.
func (p *Proposal) String() string {
	return fmt.Sprintf("Proposal{%v/%v (%v, %v) %X @ %s}",
		p.Height,
		p.Round,
		p.BlockID,
		p.POLRound,
		cmtbytes.Fingerprint(p.Signature),
		CanonicalTime(p.Timestamp))
}

// ProposalSignBytes returns the proto-encoding of the canonicalized Proposal,
// for signing. Panics if the marshaling fails.
//
// The encoded Protobuf message is varint length-prefixed (using MarshalDelimited)
// for backwards-compatibility with the Amino encoding, due to e.g. hardware
// devices that rely on this encoding.
//
// See CanonicalizeProposal
func ProposalSignBytes(chainID string, p *cmtproto.Proposal) []byte {
	pb := CanonicalizeProposal(chainID, p)
	bz, err := protoio.MarshalDelimited(&pb)
	if err != nil {
		panic(err)
	}

	return bz
}

// ToProto converts Proposal to protobuf
func (p *Proposal) ToProto() *cmtproto.Proposal {
	if p == nil {
		return &cmtproto.Proposal{}
	}
	pb := new(cmtproto.Proposal)

	pb.BlockID = p.BlockID.ToProto()
	pb.Type = p.Type
	pb.Height = p.Height
	pb.Round = p.Round
	pb.PolRound = p.POLRound
	pb.Timestamp = p.Timestamp
	pb.Signature = p.Signature

	return pb
}

// FromProto sets a protobuf Proposal to the given pointer.
// It returns an error if the proposal is invalid.
func ProposalFromProto(pp *cmtproto.Proposal) (*Proposal, error) {
	if pp == nil {
		return nil, errors.New("nil proposal")
	}

	p := new(Proposal)

	blockID, err := BlockIDFromProto(&pp.BlockID)
	if err != nil {
		return nil, err
	}

	p.BlockID = *blockID
	p.Type = pp.Type
	p.Height = pp.Height
	p.Round = pp.Round
	p.POLRound = pp.PolRound
	p.Timestamp = pp.Timestamp
	p.Signature = pp.Signature

	return p, p.ValidateBasic()
}
