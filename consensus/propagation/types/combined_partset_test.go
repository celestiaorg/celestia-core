package types

import (
	"testing"

	"github.com/stretchr/testify/require"

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
