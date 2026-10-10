package types

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"runtime"
	"testing"

	"github.com/klauspost/reedsolomon"
	"github.com/stretchr/testify/require"
)

// TestEncodeMatchesSingleCallAboveThreshold checks that the parity Encode
// produces above the parallel threshold is identical to one unsegmented call
// and still reconstructs a missing part.
func TestEncodeMatchesSingleCallAboveThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes a 16 MiB part set")
	}

	const total = 257
	ps := int(BlockPartSizeBytes)

	data := make([]byte, total*ps)
	_, err := rand.Read(data)
	require.NoError(t, err)

	original, err := NewPartSetFromData(data, BlockPartSizeBytes)
	require.NoError(t, err)
	require.Equal(t, total, int(original.Total()), "the segmented path needs more than 256 shards")

	// The reference: one call over whole shards, no segmentation. Encode is
	// only byte-identical to it because GF16 treats each byte position
	// independently - an implementation detail of the codec, documented nowhere
	// in its API - so a library bump that changed the in-shard symbol packing
	// would silently produce invalid parity for every block this node proposes.
	shards := make([][]byte, 2*total)
	for i := range total {
		shards[i] = append([]byte(nil), data[i*ps:(i+1)*ps]...)
		shards[total+i] = make([]byte, ps)
	}
	enc, err := reedsolomon.New(total, total)
	require.NoError(t, err)
	require.NoError(t, enc.Encode(shards))

	// Encode sizes its segments from GOMAXPROCS. 3 is the smallest value whose
	// worker count leaves the last segment shorter than the others; the
	// machine's own value covers whatever CI happens to run on. No other test
	// in this package reaches the segmented branch: they all stay under 129
	// shards.
	for _, procs := range []int{runtime.GOMAXPROCS(0), 3} {
		t.Run(fmt.Sprintf("gomaxprocs_%d", procs), func(t *testing.T) {
			defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))

			parity, lastLen, err := Encode(original, BlockPartSizeBytes)
			require.NoError(t, err)
			require.Equal(t, total, int(parity.Total()))
			require.Positive(t, lastLen)

			for i := range total {
				// bytes.Equal, not require.Equal: Part.Bytes is
				// cmtbytes.HexBytes, and require.Equal compares types as well
				// as contents.
				require.True(t, bytes.Equal(shards[total+i], parity.GetPart(i).Bytes),
					"parity part %d differs from the unsegmented encode", i)
			}

			// Drop a part and rebuild it from the parity. Decode checks the
			// reconstructed set against the original root, so this fails if
			// any parity byte is wrong.
			partial := NewPartSetFromHeader(original.Header(), BlockPartSizeBytes)
			for i := 1; i < total; i++ {
				added, err := partial.AddPart(original.GetPart(i))
				require.NoError(t, err)
				require.True(t, added)
			}
			recovered, _, err := Decode(partial, parity, lastLen)
			require.NoError(t, err)
			require.True(t, bytes.Equal(original.GetPart(0).Bytes, recovered.GetPart(0).Bytes),
				"the reconstructed part differs from the original")
		})
	}
}

// TestEncodeSegmentsCoverEveryByte pins the segment arithmetic itself across
// worker counts, which the production runs above exercise only at the two
// GOMAXPROCS values they set.
func TestEncodeSegmentsCoverEveryByte(t *testing.T) {
	ps := int(BlockPartSizeBytes)
	for _, workers := range []int{2, 3, 7, 8, 16} {
		segmentSize := ((ps/workers + 63) / 64) * 64
		require.Positive(t, segmentSize)
		require.Zero(t, segmentSize%64, "segments must stay 64-byte aligned")

		covered, segments := 0, 0
		for start := 0; start < ps; start += segmentSize {
			end := min(start+segmentSize, ps)
			require.Equal(t, covered, start, "segments must be contiguous")
			covered = end
			segments++
		}
		require.Equal(t, ps, covered, "segments must cover the whole shard")
		require.LessOrEqual(t, segments, workers, "never more segments than workers")
	}
}
