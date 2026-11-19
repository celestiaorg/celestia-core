package types

import (
	"bytes"
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

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
	original := types.NewPartSetFromHeader(cb.Proposal.BlockID.PartSetHeader, types.BlockPartSizeBytes)
	parity := types.NewPartSetFromHeader(types.PartSetHeader{
		Total: original.Total(),
		Hash:  cb.BpHash,
	}, types.BlockPartSizeBytes)
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
	if size < 0 || uint64(size) > uint64(math.MaxUint32) {
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

// RecoverOriginals rebuilds only the missing original parts, see
// types.DecodeOriginals.
func (cps *CombinedPartSet) RecoverOriginals(proofs []*merkle.Proof) ([]*types.Part, error) {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	parts, err := types.DecodeOriginals(cps.original, cps.parity, int(cps.lastLen), proofs)
	for _, p := range parts {
		cps.totalMap.SetIndex(int(p.Index), true)
	}
	return parts, err
}

// BuildParity encodes the parity of complete originals, as the proposer did,
// and keeps it only if it matches the committed parity root.
func (cps *CombinedPartSet) BuildParity() error {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	eps, lastLen, err := types.Encode(cps.original, types.BlockPartSizeBytes)
	if err != nil {
		return err
	}
	if uint32(lastLen) != cps.lastLen || !bytes.Equal(eps.Hash(), cps.parity.Hash()) {
		return errors.New("built parity does not match the compact block")
	}
	cps.parity = eps
	cps.totalMap.Fill()
	return nil
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

// AddPart adds a recovery part to the combined part set. It assumes that the parts being
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

// AddOriginalPart adds an original part to the combined partset.
func (cps *CombinedPartSet) AddOriginalPart(part *types.Part) (bool, error) {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	added, err := cps.original.AddPart(part)
	if added {
		cps.totalMap.SetIndex(int(part.Index), true)
	}
	return added, err
}

// AddOriginalParts adds original parts with distinct indexes, verifying their
// proofs concurrently. added[i] and errs[i] are what AddOriginalPart returns for parts[i].
func (cps *CombinedPartSet) AddOriginalParts(parts []*types.Part) (added []bool, errs []error) {
	added = make([]bool, len(parts))
	errs = make([]error, len(parts))
	cps.mtx.Lock()
	defer cps.mtx.Unlock()
	var eg errgroup.Group
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for i, part := range parts {
		eg.Go(func() error {
			added[i], errs[i] = cps.original.AddPart(part)
			if added[i] {
				cps.totalMap.SetIndex(int(part.Index), true)
			}
			return nil
		})
	}
	_ = eg.Wait()
	return added, errs
}

func (cps *CombinedPartSet) HasPart(index int) bool {
	return cps.totalMap.GetIndex(index)
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
