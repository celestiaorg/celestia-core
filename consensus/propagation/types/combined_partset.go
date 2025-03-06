package types

import (
	"fmt"

	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/types"
)

// CombinedPartSet wraps two PartSet instances: one for original block data and one for parity data.
type CombinedPartSet struct {
	totalMap *bits.BitArray
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
	total := bits.NewBitArray(int(original.Total() * 2))

	return &CombinedPartSet{
		original: original,
		parity:   parity,
		lastLen:  cb.LastLen,
		totalMap: total,
	}
}

func NewCombinedPartSetFromOriginal(original *types.PartSet) *CombinedPartSet {
	return &CombinedPartSet{
		original: original,
	}
}

func (cps *CombinedPartSet) Original() *types.PartSet {
	return cps.original
}

func (cps *CombinedPartSet) Parity() *types.PartSet {
	return cps.parity
}

func (cps *CombinedPartSet) BitArray() *bits.BitArray {
	return cps.totalMap
}

func (cps *CombinedPartSet) Total() uint32 {
	return cps.original.Total() + cps.parity.Total()
}

func (cps *CombinedPartSet) IsComplete() bool {
	return cps.original.IsComplete() && cps.parity.IsComplete()
}

// CanDecode determines if enough parts have been added to decode the block.
func (cps *CombinedPartSet) CanDecode() bool {
	return (cps.original.Count() + cps.parity.Count()) >= cps.original.Total()
}

func (cps *CombinedPartSet) Decode() error {
	_, _, err := types.Decode(cps.original, cps.parity, int(cps.lastLen))
	if err == nil {
		cps.totalMap.Fill()
	}
	return err
}

// AddPart adds a part to the combined part set. It assumes that the parts being
// added have already been verified.
func (cps *CombinedPartSet) AddPart(part *RecoveryPart) (bool, error) {
	p := &types.Part{
		Index: part.Index,
		Bytes: part.Data,
	}

	if part.Index < cps.original.Total() {
		added, err := cps.original.AddPartWithoutProof(p)
		if added {
			cps.totalMap.SetIndex(int(part.Index), true)
		}
		return added, err
	}

	// Adjust the index to be relative to the parity set.
	p.Index -= cps.original.Total()
	added, err := cps.parity.AddPartWithoutProof(p)
	if added {
		cps.totalMap.SetIndex(int(part.Index), true)
	}
	return added, err
}

// AddPartSet adds a whole part set of parts
func (cps *CombinedPartSet) AddPartSet(parts *types.PartSet) (bool, error) {
	for index := 0; index < int(parts.Count()); index++ {
		part := parts.GetPart(index)
		added, err := cps.AddPart(&RecoveryPart{Index: part.Index, Data: part.Bytes})
		if err != nil {
			return false, err
		}
		if !added {
			return false, fmt.Errorf("failed to add part %d", index)
		}
	}
	return true, nil
}

func (cps *CombinedPartSet) GetPart(index uint32) (*types.Part, bool) {
	if !cps.totalMap.GetIndex(int(index)) {
		return nil, false
	}

	if index < cps.original.Total() {
		part := cps.original.GetPart(int(index))
		return part, part != nil
	}

	part := cps.parity.GetPart(int(index - cps.original.Total()))
	return part, part != nil
}
