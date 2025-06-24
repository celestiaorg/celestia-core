package types

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/types"
)

// CombinedPartSet wraps two PartSet instances: one for original block data and one for parity data.
type CombinedPartSet struct {
	mtx      *sync.Mutex
	totalMap *bits.BitArray
	original *types.PartSet // holds the original parts (indexes: 0 to original.Total()-1)
	parity   *types.PartSet // holds parity parts (logical indexes start at original.Total())
	lastLen  uint32
	catchup  bool

	IsDecoding atomic.Bool
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
		mtx:      &sync.Mutex{},
	}
}

func NewCombinedPartSetFromOriginal(original *types.PartSet, catchup bool) *CombinedPartSet {
	ps := &CombinedPartSet{
		mtx:      &sync.Mutex{},
		original: original,
		parity:   &types.PartSet{},
		catchup:  catchup,
		totalMap: bits.NewBitArray(int(original.Total() * 2)),
	}
	for _, ind := range original.BitArray().GetTrueIndices() {
		ps.totalMap.SetIndex(ind, true)
	}
	return ps
}

func (cps *CombinedPartSet) SetProposalData(original, parity *types.PartSet) {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	cps.original = original
	cps.parity = parity
	cps.totalMap = bits.NewBitArray(int(original.Total() + parity.Total()))
	cps.totalMap.Fill()
}

func (cps *CombinedPartSet) Original() *types.PartSet {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	return cps.original
}

func (cps *CombinedPartSet) Parity() *types.PartSet {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	return cps.parity
}

func (cps *CombinedPartSet) BitArray() *bits.BitArray {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	return cps.totalMap
}

// OringinalBitArray returns a BitArray that only missing parts if they are in the original
// part set.
func (cps *CombinedPartSet) MissingOriginal() *bits.BitArray {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	out := bits.NewBitArray(int(cps.original.Total() * 2))
	missOrig := cps.original.BitArray().Not()
	for _, ind := range missOrig.GetTrueIndices() {
		out.SetIndex(ind, true)
	}
	return out
}

func (cps *CombinedPartSet) Total() uint32 {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	size := cps.totalMap.Size()
	if size > math.MaxUint32 {
		return 0
	}
	return uint32(size)
}

func (cps *CombinedPartSet) IsComplete() bool {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	return cps.original.IsComplete()
}

// CanDecode determines if enough parts have been added to decode the block.
func (cps *CombinedPartSet) CanDecode() bool {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	return (cps.original.Count()+cps.parity.Count()) >= cps.original.Total() &&
		!cps.catchup
}

func (cps *CombinedPartSet) Decode() error {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	ops, eps, err := types.Decode(cps.original, cps.parity, int(cps.lastLen))
	if err != nil {
		return err
	}
	cps.totalMap.Fill()
	cps.original = ops
	cps.parity = eps
	return nil
}

// AddPart adds a part to the combined part set. It assumes that the parts being
// added have already been verified.
func (cps *CombinedPartSet) AddPart(part *RecoveryPart, proof merkle.Proof) (bool, error) {
	p := &types.Part{
		Index: part.Index,
		Bytes: part.Data,
		Proof: proof,
	}

	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	if part.Index < cps.original.Total() {
		added, err := cps.original.AddPart(p)
		if added {
			cps.totalMap.SetIndex(int(part.Index), true)
		}
		return added, err
	}

	// Adjust the index to be relative to the parity set.
	encodedIndex := p.Index
	p.Index -= cps.original.Total()
	added, err := cps.parity.AddPart(p)
	if added {
		cps.totalMap.SetIndex(int(encodedIndex), true)
	}
	return added, err
}

func (cps *CombinedPartSet) GetPart(index uint32) (*types.Part, bool) {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
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
