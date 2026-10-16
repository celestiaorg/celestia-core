package types

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEncodeSegmentedMatchesSequential is what the segmented path rests on:
// encoding a block both ways must produce identical parity, byte for byte.
// That only holds because GF16 treats each byte position independently, which
// the library documents nowhere, so a version bump could silently break every
// block this node proposes.
//
// leopard decomposes work over m = ceilPow2(parityShards) buffers, so the
// shard counts below are the smallest in each of the first three m regimes,
// plus a 32 MiB block.
func TestEncodeSegmentedMatchesSequential(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes up to a 32 MiB part set")
	}

	for _, tc := range []struct {
		name  string
		total int
	}{
		{"8 MiB, m=256", minSegmentedShards},
		{"16 MiB, m=512", 257},
		{"32 MiB", 512},
		{"32 MiB, m=1024", 513},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := randomPartSet(t, tc.total)

			sequential, seqLast := encodeWithProcs(t, ops, 1)
			segmented, segLast := encodeWithProcs(t, ops, 4)

			require.Equal(t, seqLast, segLast)
			require.Equal(t, sequential.Total(), segmented.Total())
			for i := range tc.total {
				// bytes.Equal, not require.Equal: Part.Bytes is cmtbytes.HexBytes.
				require.True(t, bytes.Equal(sequential.GetPart(i).Bytes, segmented.GetPart(i).Bytes),
					"parity part %d differs between the sequential and segmented encodes", i)
			}
		})
	}
}

// TestEncodeParityDecodes rebuilds a dropped part from segmented parity.
// Decode checks the rebuilt set against the original root, so this fails if
// any parity byte is wrong.
func TestEncodeParityDecodes(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes an 8 MiB part set")
	}
	const total = minSegmentedShards
	ops := randomPartSet(t, total)

	parity, lastLen := encodeWithProcs(t, ops, 4)

	partial := NewPartSetFromHeader(ops.Header(), BlockPartSizeBytes)
	for i := 1; i < total; i++ {
		added, err := partial.AddPart(ops.GetPart(i))
		require.NoError(t, err)
		require.True(t, added)
	}
	recovered, _, err := Decode(partial, parity, lastLen)
	require.NoError(t, err)
	require.True(t, bytes.Equal(ops.GetPart(0).Bytes, recovered.GetPart(0).Bytes))
}

func randomPartSet(t *testing.T, total int) *PartSet {
	t.Helper()
	data := make([]byte, total*int(BlockPartSizeBytes))
	_, err := rand.Read(data)
	require.NoError(t, err)
	ops, err := NewPartSetFromData(data, BlockPartSizeBytes)
	require.NoError(t, err)
	require.Equal(t, total, int(ops.Total()))
	return ops
}

// encodeWithProcs encodes at a given GOMAXPROCS and restores the previous
// setting. One P is how the single-call path is selected; more than one takes
// the segmented path, which the assertion here pins.
func encodeWithProcs(t *testing.T, ops *PartSet, procs int) (*PartSet, int) {
	t.Helper()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))
	require.Equal(t, procs > 1, segmentedEncodeOK(int(ops.Total()), procs))
	parity, lastLen, err := Encode(ops, BlockPartSizeBytes)
	require.NoError(t, err)
	require.Positive(t, lastLen)
	return parity, lastLen
}

// TestSegmentedEncodeBounds pins the lower end, a codec property, so crossing
// it by one shard flips the decision. There is no upper bound.
func TestSegmentedEncodeBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int
		procs int
		want  bool
	}{
		{"one below the lower bound", 128, 8, false},
		{"the lower bound", 129, 8, true},
		{"across the work-buffer doubling at 513", 513, 8, true},
		{"a 64 MiB block", 1024, 8, true},
		{"a single core cannot overlap anything", 300, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, segmentedEncodeOK(tc.total, tc.procs))
		})
	}
}

// TestEncodeSegmentsCoverEveryByte pins the segment arithmetic, including part
// sizes that leave a short final segment.
func TestEncodeSegmentsCoverEveryByte(t *testing.T) {
	for _, ps := range []int{int(BlockPartSizeBytes), 96 * 1024, 1 << 20, 64*1024 + 192} {
		t.Run(fmt.Sprintf("part_size_%d", ps), func(t *testing.T) {
			require.Zero(t, segmentBytes%64, "segments must stay 64-byte aligned")

			covered, segments := 0, 0
			for start := 0; start < ps; start += segmentBytes {
				end := min(start+segmentBytes, ps)
				require.Equal(t, covered, start, "segments must be contiguous")
				covered = end
				segments++
			}
			require.Equal(t, ps, covered, "segments must cover the whole shard")
			require.Positive(t, segments)
		})
	}
}
