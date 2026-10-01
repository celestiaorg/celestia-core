package types

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/klauspost/reedsolomon"

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

// RecoverOriginals reconstructs only the original parts that are still
// missing. Parity parts are never generated: completing the original set is
// what consensus needs, and a node must not advertise parity it does not hold.
// The proofs committed in the compact block are reused, and each reconstructed
// part is verified against its committed hash. It returns the indexes it
// recovered.
func (cps *CombinedPartSet) RecoverOriginals(proofs []*merkle.Proof) ([]uint32, error) {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()

	total := int(cps.original.Total())
	parityTotal := int(cps.parity.Total())
	missing := cps.original.BitArray().Not().GetTrueIndices()
	if len(missing) == 0 {
		return nil, nil
	}
	if len(proofs) < total {
		return nil, fmt.Errorf("compact block has %d proofs, need %d", len(proofs), total)
	}

	partSize := int(types.BlockPartSizeBytes)
	shards := make([][]byte, total+parityTotal)
	for i := 0; i < total; i++ {
		chunk := cps.original.GetPartBytes(i)
		if chunk == nil {
			continue
		}
		// reed-solomon needs every shard the same size, so the short last part
		// is padded back out with the zeros the encoder used.
		if len(chunk) != partSize {
			padded := make([]byte, partSize)
			copy(padded, chunk)
			chunk = padded
		}
		shards[i] = chunk
	}
	for i := 0; i < parityTotal; i++ {
		shards[total+i] = cps.parity.GetPartBytes(i)
	}

	enc, err := reedsolomon.New(total, parityTotal)
	if err != nil {
		return nil, err
	}
	// ReconstructData recreates the missing data shards only and leaves the
	// missing parity shards nil.
	if err := enc.ReconstructData(shards); err != nil {
		return nil, err
	}

	// drop the padding the encoder added to the last part.
	if cps.lastLen > 0 && int(cps.lastLen) < len(shards[total-1]) {
		shards[total-1] = shards[total-1][:cps.lastLen]
	}

	// Verify every reconstructed part before adding any, so a failure leaves
	// the set exactly as it was. Adding as we go would strand the parts added
	// before the failure: recorded as held, never forwarded to consensus, and
	// never requested again.
	root := cps.original.Hash()
	for _, i := range missing {
		if proofs[i].Total != int64(total) {
			return nil, fmt.Errorf("proof for part %d has total %d, expected %d", i, proofs[i].Total, total)
		}
		if err := proofs[i].Verify(root, shards[i]); err != nil {
			return nil, fmt.Errorf("reconstructed part %d does not match its committed proof: %w", i, err)
		}
	}

	recovered := make([]uint32, 0, len(missing))
	for _, i := range missing {
		added, err := cps.original.AddPart(&types.Part{
			Index: uint32(i),
			Bytes: shards[i],
			Proof: *proofs[i],
		})
		if err != nil {
			return recovered, err
		}
		if !added {
			return recovered, fmt.Errorf("failed to add reconstructed part %d", i)
		}
		cps.totalMap.SetIndex(i, true)
		recovered = append(recovered, uint32(i))
	}
	return recovered, nil
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

// AddOriginalParts adds several recovered original parts at once, verifying
// their proofs concurrently. It returns the parts that were added and, index
// aligned with parts, the error that stopped each of the others.
func (cps *CombinedPartSet) AddOriginalParts(parts []*types.Part) ([]*types.Part, []error) {
	cps.mtx.Lock()
	defer cps.mtx.Unlock()

	added, errs := cps.original.AddParts(parts)
	out := make([]*types.Part, 0, len(parts))
	for i, ok := range added {
		if !ok {
			continue
		}
		cps.totalMap.SetIndex(int(parts[i].Index), true)
		out = append(out, parts[i])
	}
	return out, errs
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
