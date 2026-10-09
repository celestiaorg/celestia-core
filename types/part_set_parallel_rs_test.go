package types

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/klauspost/reedsolomon"
	"github.com/stretchr/testify/require"
)

// TestEncodeMatchesSingleCallAboveThreshold checks that the parity Encode
// produces above the parallel threshold is identical to one unsegmented call.
//
// Encode splits each shard into 64-byte-aligned ranges and encodes them
// concurrently. That is only byte-identical because GF16 treats each byte
// position independently - an implementation detail of the codec, documented
// nowhere in its API - so a library bump that changed the in-shard symbol
// packing would silently produce invalid parity for every block this node
// proposes. No other test in this package reaches the branch: they all stay
// under 129 shards.
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

	parity, lastLen, err := Encode(original, BlockPartSizeBytes)
	require.NoError(t, err)
	require.Equal(t, total, int(parity.Total()))
	require.Positive(t, lastLen)

	// the reference: one call over whole shards, no segmentation
	shards := make([][]byte, 2*total)
	for i := range total {
		shards[i] = append([]byte(nil), data[i*ps:(i+1)*ps]...)
		shards[total+i] = make([]byte, ps)
	}
	enc, err := reedsolomon.New(total, total)
	require.NoError(t, err)
	require.NoError(t, enc.Encode(shards))

	for i := range total {
		// bytes.Equal, not require.Equal: Part.Bytes is cmtbytes.HexBytes, and
		// require.Equal compares types as well as contents.
		require.True(t, bytes.Equal(shards[total+i], parity.GetPart(i).Bytes),
			"parity part %d differs from the unsegmented encode", i)
	}
}

// TestEncodeSegmentsCoverEveryByte pins the segment arithmetic itself across
// worker counts, which the single production run above exercises only at the
// machine's own GOMAXPROCS.
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
