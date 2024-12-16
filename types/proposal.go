package types

import (
	"errors"
	"fmt"
	"time"

	"github.com/tendermint/tendermint/libs/bits"
	cmtbytes "github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/libs/protoio"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
	cmttime "github.com/tendermint/tendermint/types/time"
)

var (
	ErrInvalidBlockPartSignature = errors.New("error invalid block part signature")
	ErrInvalidBlockPartHash      = errors.New("error invalid block part hash")
)

type PartState struct {
	Parts  *bits.BitArray
	Height int64
	Round  int32
	Have   bool
}

func (wp *PartState) ValidateBasic() error {
	if wp.Height < 0 {
		return errors.New("want parts for invalid height below zero")
	}
	return nil
}

func (wp *PartState) ToProto() *cmtproto.PartState {
	return &cmtproto.PartState{
		Parts:  *wp.Parts.ToProto(),
		Height: wp.Height,
		Round:  wp.Round,
		Have:   wp.Have,
	}
}

func PartStateFromProto(pwp *cmtproto.PartState) *PartState {
	ba := new(bits.BitArray)
	ba.FromProto(&pwp.Parts)
	return &PartState{
		Parts:  ba,
		Height: pwp.Height,
		Round:  pwp.Round,
		Have:   pwp.Have,
	}
}

// Proposal defines a block proposal for the consensus.
// It refers to the block by BlockID field.
// It must be signed by the correct proposer for the given Height/Round
// to be considered valid. It may depend on votes from a previous round,
// a so-called Proof-of-Lock (POL) round, as noted in the POLRound.
// If POLRound >= 0, then BlockID corresponds to the block that is locked in POLRound.
type Proposal struct {
	Type         cmtproto.SignedMsgType
	Height       int64                  `json:"height"`
	Round        int32                  `json:"round"`     // there can not be greater than 2_147_483_647 rounds
	POLRound     int32                  `json:"pol_round"` // -1 if null.
	BlockID      BlockID                `json:"block_id"`
	Timestamp    time.Time              `json:"timestamp"`
	Signature    []byte                 `json:"signature"`
	CompactBlock *cmtproto.CompactBlock `json:"compact_block"` // todo(evan): use a separate type
	HaveParts    *bits.BitArray         `json:"have_parts"`    // todo(evan): use a separate type
}

// NewProposal returns a new Proposal.
// If there is no POLRound, polRound should be -1.
func NewProposal(height int64, round int32, polRound int32, blockID BlockID) *Proposal {
	return &Proposal{
		Type:      cmtproto.ProposalType,
		Height:    height,
		Round:     round,
		BlockID:   blockID,
		POLRound:  polRound,
		Timestamp: cmttime.Now(),
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

	// todo(evan) validate the compact block
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
	pb.CompactBlock = p.CompactBlock
	pb.HaveParts = p.HaveParts.ToProto()

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
	p.CompactBlock = pp.CompactBlock

	pbBits := new(bits.BitArray)
	pbBits.FromProto(pp.HaveParts)
	p.HaveParts = pbBits

	return p, p.ValidateBasic()
}
