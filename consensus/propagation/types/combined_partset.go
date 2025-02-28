package types

import (
	"github.com/tendermint/tendermint/types"
)

// CombinedPartSet wraps two PartSet instances: one for original block data and one for parity data.
type CombinedPartSet struct {
	original *types.PartSet // holds the original parts (indexes: 0 to original.Total()-1)
	parity   *types.PartSet // holds parity parts (logical indexes start at original.Total())
	lastLen  uint32
}

// NewCombinedSetFromCompactBlock creates a new CombinedPartSet from a
// CompactBlock using the PartSetHeader in the proposal and the BpHash from the
// CompactBlock.
func NewCombinedSetFromCompactBlock(cb *CompactBlock) *CombinedPartSet {
	original := types.NewPartSetFromHeader(cb.Proposal.BlockID.PartSetHeader)
	parity := types.NewPartSetFromHeader(types.PartSetHeader{
		Total: original.Total(),
		Hash:  cb.BpHash,
	})

	return &CombinedPartSet{
		original: original,
		parity:   parity,
		lastLen:  cb.LastLen,
	}
}

// CanDecode determines if enough parts have been added to decode the block.
func (cps *CombinedPartSet) CanDecode() bool {
	return (cps.original.Count() + cps.parity.Count()) >= cps.original.Total()
}

func (cps *CombinedPartSet) Decode() error {
	_, _, err := types.Decode(cps.original, cps.parity, int(cps.lastLen))
	return err
}

// AddPart adds a part to the combined part set. It assumes that the parts being
// added have already been verified.
func (cps *CombinedPartSet) AddPart(part RecoveryPart) (bool, error) {
	p := &types.Part{
		Index: part.Index,
		Bytes: part.Data,
	}

	if part.Index < cps.original.Total() {
		return cps.original.AddPartWithoutProof(p)
	}

	// Adjust the index to be relative to the parity set.
	p.Index -= cps.original.Total()
	return cps.parity.AddPartWithoutProof(p)
}
