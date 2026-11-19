package types

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmttypes "github.com/cometbft/cometbft/types"
)

// TestAddOriginalPartsMatchesAddOriginalPart checks that adding a batch of
// genuine, corrupted, mismatched and already held parts gives the same results
// and stored parts as adding them one by one.
func TestAddOriginalPartsMatchesAddOriginalPart(t *testing.T) {
	partSize := cmttypes.BlockPartSizeBytes
	ps, err := cmttypes.NewPartSetFromData(cmtrand.Bytes(int(partSize)*20+100), partSize)
	require.NoError(t, err)
	total := ps.Total()

	newSet := func() *CombinedPartSet {
		cps := NewCombinedPartSetFromOriginal(cmttypes.NewPartSetFromHeader(ps.Header(), partSize), false)
		_, err := cps.AddOriginalPart(clonePart(ps.GetPart(0)))
		require.NoError(t, err)
		return cps
	}

	batch := func() []*cmttypes.Part {
		parts := make([]*cmttypes.Part, 0, total)
		for i := range total {
			p := clonePart(ps.GetPart(int(i)))
			switch i % 4 {
			case 1:
				p.Bytes[0] ^= 0xff // corrupted data
			case 2:
				p.Proof = ps.GetPart(0).Proof // another part's proof
			}
			parts = append(parts, p)
		}
		return parts
	}

	serial := newSet()
	parts := batch()
	wantAdded := make([]bool, len(parts))
	wantErrs := make([]error, len(parts))
	for i, p := range parts {
		wantAdded[i], wantErrs[i] = serial.AddOriginalPart(p)
	}

	concurrent := newSet()
	added, errs := concurrent.AddOriginalParts(batch())
	require.Equal(t, wantAdded, added)
	require.Equal(t, wantErrs, errs)
	require.Equal(t, serial.BitArray().String(), concurrent.BitArray().String())
	require.Equal(t, serial.Original().Count(), concurrent.Original().Count())
	for i := range total {
		require.Equal(t, serial.Original().GetPart(int(i)), concurrent.Original().GetPart(int(i)))
	}
}

func clonePart(p *cmttypes.Part) *cmttypes.Part {
	return &cmttypes.Part{
		Index: p.Index,
		Bytes: append([]byte(nil), p.Bytes...),
		Proof: p.Proof,
	}
}

// testCompactBlock returns a compact block committing to data, with its
// original and parity part sets and proofs.
func testCompactBlock(t *testing.T, data []byte) (*CompactBlock, *cmttypes.PartSet, *cmttypes.PartSet, []*merkle.Proof) {
	t.Helper()
	ops, err := cmttypes.NewPartSetFromData(data, cmttypes.BlockPartSizeBytes)
	require.NoError(t, err)
	eps, lastLen, err := cmttypes.Encode(ops, cmttypes.BlockPartSizeBytes)
	require.NoError(t, err)
	hashes := make([][]byte, 0, 2*ops.Total())
	for _, ps := range []*cmttypes.PartSet{ops, eps} {
		for i := 0; i < int(ps.Total()); i++ {
			hashes = append(hashes, ps.GetPart(i).Proof.LeafHash)
		}
	}
	cb := &CompactBlock{
		BpHash:      eps.Hash(),
		LastLen:     uint32(lastLen),
		PartsHashes: hashes,
		Proposal:    cmttypes.Proposal{BlockID: cmttypes.BlockID{PartSetHeader: ops.Header()}},
	}
	proofs, err := cb.Proofs()
	require.NoError(t, err)
	return cb, ops, eps, proofs
}

// addParts adds the given combined indexes from ops and eps to a new set.
func addParts(t *testing.T, cb *CompactBlock, ops, eps *cmttypes.PartSet, proofs []*merkle.Proof, indexes []int) *CombinedPartSet {
	t.Helper()
	cps := NewCombinedSetFromCompactBlock(cb)
	total := int(ops.Total())
	for _, i := range indexes {
		src := ops
		if i >= total {
			src = eps
		}
		added, err := cps.AddPart(&RecoveryPart{Index: uint32(i), Data: src.GetPart(i % total).Bytes}, *proofs[i])
		require.NoError(t, err)
		require.True(t, added)
	}
	return cps
}

func TestRecoverOriginalsMatchesDecode(t *testing.T) {
	partSize := int(cmttypes.BlockPartSizeBytes)
	sizes := map[string]int{
		"one short part":  1000,
		"two full parts":  2 * partSize,
		"five parts":      4*partSize + 123,
		"128 parts (GF8)": 127*partSize + 1,
		"300 parts":       299*partSize + 7, // more than 256 shards
	}
	for name, size := range sizes {
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(size)))
			cb, ops, eps, proofs := testCompactBlock(t, cmtrand.Bytes(size))
			total := int(ops.Total())
			perm := rng.Perm(2 * total)
			patterns := map[string][]int{
				"parity only":         seq(total, 2*total),
				"first original lost": append(seq(1, total), total),
				"last original lost":  append(seq(0, total-1), 2*total-1),
				"random half":         perm[:total],
			}
			for pname, present := range patterns {
				t.Run(pname, func(t *testing.T) {
					decoded := addParts(t, cb, ops, eps, proofs, present)
					require.NoError(t, decoded.Decode())

					recovering := addParts(t, cb, ops, eps, proofs, present)
					missing := recovering.Original().BitArray().Not().GetTrueIndices()
					recovered, err := recovering.RecoverOriginals(proofs)
					require.NoError(t, err)
					require.Len(t, recovered, len(missing))
					require.True(t, recovering.IsComplete())
					for i := 0; i < total; i++ {
						want := decoded.Original().GetPart(i)
						got := recovering.Original().GetPart(i)
						require.Equal(t, want.Bytes, got.Bytes, "part %d", i)
						require.Equal(t, want.Proof, got.Proof, "proof %d", i)
					}
					// parity stays exactly what was received.
					for i := total; i < 2*total; i++ {
						require.Equal(t, slices.Contains(present, i), recovering.HasPart(i), "parity %d", i)
					}
				})
			}
		})
	}
}

func TestRecoverOriginalsRejectsWrongParity(t *testing.T) {
	cb, ops, eps, proofs := testCompactBlock(t, cmtrand.Bytes(5*int(cmttypes.BlockPartSizeBytes)))
	total := int(ops.Total())

	// a byzantine proposer commits to parity that is not the encoding of the block.
	parity := make([]byte, 0, total*int(cmttypes.BlockPartSizeBytes))
	for i := 0; i < total; i++ {
		parity = append(parity, eps.GetPart(i).Bytes...)
	}
	parity[0] ^= 0xff
	badEps, err := cmttypes.NewPartSetFromData(parity, cmttypes.BlockPartSizeBytes)
	require.NoError(t, err)
	hashes := append([][]byte{}, cb.PartsHashes[:total]...)
	for i := 0; i < total; i++ {
		hashes = append(hashes, badEps.GetPart(i).Proof.LeafHash)
	}
	badCb := &CompactBlock{BpHash: badEps.Hash(), LastLen: cb.LastLen, PartsHashes: hashes, Proposal: cb.Proposal}
	badProofs, err := badCb.Proofs()
	require.NoError(t, err)

	cps := addParts(t, badCb, ops, badEps, badProofs, append(seq(1, total), total))
	recovered, err := cps.RecoverOriginals(badProofs)
	require.Error(t, err)
	require.Empty(t, recovered)
	require.False(t, cps.HasPart(0))
	require.Equal(t, uint32(total-1), cps.Original().Count())

	// a wrong last part length fails the same way.
	cb.LastLen--
	cps = addParts(t, cb, ops, eps, proofs, append(seq(0, total-1), total))
	recovered, err = cps.RecoverOriginals(proofs)
	require.Error(t, err)
	require.Empty(t, recovered)
	require.False(t, cps.IsComplete())
}

func seq(from, to int) []int {
	out := make([]int, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}

func TestBuildParityMatchesProposer(t *testing.T) {
	partSize := int(cmttypes.BlockPartSizeBytes)
	for _, size := range []int{1000, 4*partSize + 123, 299*partSize + 7} {
		cb, ops, eps, proofs := testCompactBlock(t, cmtrand.Bytes(size))
		total := int(ops.Total())
		cps := addParts(t, cb, ops, eps, proofs, seq(0, total))
		require.NoError(t, cps.BuildParity())
		for i := 0; i < total; i++ {
			p, has := cps.GetPart(uint32(total + i))
			require.True(t, has)
			require.Equal(t, eps.GetPart(i).Bytes, p.Bytes)
			require.Equal(t, eps.GetPart(i).Proof, p.Proof)
		}

		// parity that does not match the committed root is not kept.
		bad := *cb
		bad.BpHash = cmtrand.Bytes(32)
		cps = addParts(t, &bad, ops, eps, proofs, seq(0, total))
		require.Error(t, cps.BuildParity())
		require.Zero(t, cps.Parity().Count())
	}
}
