package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"
)

// newTestCombinedPartSet builds an empty combined part set for a block of the
// given size, along with the original and parity part sets holding the real
// data and the proofs committed in the compact block.
func newTestCombinedPartSet(t *testing.T, blockSize int) (*CombinedPartSet, *types.PartSet, *types.PartSet, []*merkle.Proof) {
	t.Helper()

	ops, err := types.NewPartSetFromData(cmtrand.Bytes(blockSize), types.BlockPartSizeBytes)
	require.NoError(t, err)
	eps, lastLen, err := types.Encode(ops, types.BlockPartSizeBytes)
	require.NoError(t, err)

	hashes := make([][]byte, 0, ops.Total()+eps.Total())
	for _, ps := range []*types.PartSet{ops, eps} {
		for i := uint32(0); i < ps.Total(); i++ {
			hashes = append(hashes, ps.GetPart(int(i)).Proof.LeafHash)
		}
	}

	cb := &CompactBlock{
		BpHash:      eps.Hash(),
		LastLen:     uint32(lastLen),
		PartsHashes: hashes,
		Proposal:    types.Proposal{BlockID: types.BlockID{PartSetHeader: ops.Header()}},
	}
	proofs, err := cb.Proofs()
	require.NoError(t, err)

	return NewCombinedSetFromCompactBlock(cb), ops, eps, proofs
}

// addTestPart copies a part from the source part set into the combined part
// set. Parity parts use logical indexes starting at the original total.
func addTestPart(t *testing.T, cps *CombinedPartSet, src *types.PartSet, index, logicalIndex uint32, proofs []*merkle.Proof) {
	t.Helper()
	part := src.GetPart(int(index))
	require.NotNil(t, part)
	added, err := cps.AddPart(&RecoveryPart{Index: logicalIndex, Data: part.Bytes}, *proofs[logicalIndex])
	require.NoError(t, err)
	require.True(t, added)
}

func TestRecoverOriginals_AllOriginalsPresent(t *testing.T) {
	cps, ops, _, proofs := newTestCombinedPartSet(t, 3*int(types.BlockPartSizeBytes)+1000)

	for i := uint32(0); i < ops.Total(); i++ {
		addTestPart(t, cps, ops, i, i, proofs)
	}
	require.True(t, cps.IsComplete())

	// passing nil proofs proves no reconstruction was attempted: it would fail
	// the proof length check before touching reed-solomon.
	recovered, err := cps.RecoverOriginals(nil)
	require.NoError(t, err)
	assert.Empty(t, recovered)

	// no parity was materialized, so none may be advertised.
	assert.Zero(t, cps.Parity().Count())
	assert.Equal(t, []int{0, 1, 2, 3}, cps.BitArray().GetTrueIndices())
}

func TestRecoverOriginals_SomeOriginalsMissing(t *testing.T) {
	cps, ops, eps, proofs := newTestCombinedPartSet(t, 3*int(types.BlockPartSizeBytes)+1000)
	total := ops.Total()
	require.Equal(t, uint32(4), total)

	// two originals and two parity parts: exactly enough to reconstruct.
	addTestPart(t, cps, ops, 0, 0, proofs)
	addTestPart(t, cps, ops, 1, 1, proofs)
	addTestPart(t, cps, eps, 0, total, proofs)
	addTestPart(t, cps, eps, 1, total+1, proofs)
	require.True(t, cps.CanDecode())

	recovered, err := cps.RecoverOriginals(proofs)
	require.NoError(t, err)
	assert.Equal(t, []uint32{2, 3}, recovered)

	require.True(t, cps.IsComplete())
	assert.Equal(t, ops.GetPartBytes(2), cps.Original().GetPartBytes(2))
	// the last part is short, so the reconstruction padding must be stripped.
	assert.Equal(t, ops.GetPartBytes(3), cps.Original().GetPartBytes(3))

	// only the two parity parts that were received exist locally.
	assert.Equal(t, uint32(2), cps.Parity().Count())
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5}, cps.BitArray().GetTrueIndices())
}

func TestRecoverOriginals_NotEnoughShards(t *testing.T) {
	cps, ops, _, proofs := newTestCombinedPartSet(t, 3*int(types.BlockPartSizeBytes)+1000)

	addTestPart(t, cps, ops, 0, 0, proofs)
	addTestPart(t, cps, ops, 1, 1, proofs)
	require.False(t, cps.CanDecode())

	recovered, err := cps.RecoverOriginals(proofs)
	require.Error(t, err)
	assert.Empty(t, recovered)
	assert.False(t, cps.IsComplete())
	assert.Equal(t, []int{0, 1}, cps.BitArray().GetTrueIndices())
}

// TestRecoverOriginals_WrongLastLenAddsNothing has the proposer commit a wrong
// LastLen, so the reconstructed last part fails its proof. Nothing may be
// added: a part added before the failure would be held but never forwarded.
func TestRecoverOriginals_WrongLastLenAddsNothing(t *testing.T) {
	ops, err := types.NewPartSetFromData(cmtrand.Bytes(3*int(types.BlockPartSizeBytes)+1000), types.BlockPartSizeBytes)
	require.NoError(t, err)
	eps, _, err := types.Encode(ops, types.BlockPartSizeBytes)
	require.NoError(t, err)

	hashes := make([][]byte, 0, ops.Total()+eps.Total())
	for _, ps := range []*types.PartSet{ops, eps} {
		for i := uint32(0); i < ps.Total(); i++ {
			hashes = append(hashes, ps.GetPart(int(i)).Proof.LeafHash)
		}
	}
	cb := &CompactBlock{
		BpHash:      eps.Hash(),
		LastLen:     types.BlockPartSizeBytes, // the real last part is shorter
		PartsHashes: hashes,
		Proposal:    types.Proposal{BlockID: types.BlockID{PartSetHeader: ops.Header()}},
	}
	proofs, err := cb.Proofs()
	require.NoError(t, err)
	cps := NewCombinedSetFromCompactBlock(cb)

	total := ops.Total()
	// parts 0 and 3 are missing; 0 would verify, 3 cannot because of LastLen.
	addTestPart(t, cps, ops, 1, 1, proofs)
	addTestPart(t, cps, ops, 2, 2, proofs)
	addTestPart(t, cps, eps, 0, total, proofs)
	addTestPart(t, cps, eps, 1, total+1, proofs)
	require.True(t, cps.CanDecode())

	recovered, err := cps.RecoverOriginals(proofs)
	require.Error(t, err)
	assert.Empty(t, recovered)
	assert.False(t, cps.Original().HasPart(0))
	assert.Equal(t, []int{1, 2, 4, 5}, cps.BitArray().GetTrueIndices())
}

func TestSetParityValidatesCommitment(t *testing.T) {
	cps, ops, eps, proofs := newTestCombinedPartSet(t, 3*int(types.BlockPartSizeBytes)+1001)
	require.Error(t, cps.SetParity(eps), "incomplete originals")
	for i := uint32(0); i < ops.Total(); i++ {
		addTestPart(t, cps, ops, i, i, proofs)
	}
	addTestPart(t, cps, eps, 0, ops.Total(), proofs)
	previous := cps.Parity()
	indices := cps.BitArray().GetTrueIndices()

	wrongRoot, err := types.NewPartSetFromData(cmtrand.Bytes(int(eps.Total()*types.BlockPartSizeBytes)), types.BlockPartSizeBytes)
	require.NoError(t, err)
	wrongTotal, err := types.NewPartSetFromData(cmtrand.Bytes(int(types.BlockPartSizeBytes)), types.BlockPartSizeBytes)
	require.NoError(t, err)
	for _, invalid := range []*types.PartSet{nil, types.NewPartSetFromHeader(eps.Header(), types.BlockPartSizeBytes), wrongRoot, wrongTotal} {
		require.Error(t, cps.SetParity(invalid))
		require.Same(t, previous, cps.Parity())
		require.Equal(t, indices, cps.BitArray().GetTrueIndices())
	}
	require.NoError(t, cps.SetParity(eps))
	require.Same(t, eps, cps.Parity())
	require.True(t, cps.BitArray().IsFull())
}
