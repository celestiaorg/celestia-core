package propagation

import (
	"cmp"
	"slices"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

// maxPushedParts bounds how many parts the proposer pushes to each peer up
// front. Past this many, a peer is better off recovering the transactions it
// already holds from its mempool and decoding the block than receiving a large
// push it mostly did not need.
const maxPushedParts = 10

// uncoveredParts returns the indices of the parts a peer cannot rebuild from
// the transactions the compact block points at, because some of their bytes
// fall outside every transaction's byte range. Those are the parts worth
// pushing: block metadata, padding, and anything else that is not transaction
// data.
//
// At most maxPushedParts indices are returned, lowest first.
func uncoveredParts(blobs []proptypes.TxMetaData, total, partSize, lastLen uint32) []uint32 {
	covered := mergeRanges(blobs)

	uncovered := make([]uint32, 0, maxPushedParts)
	next := 0
	for part := range total {
		start := part * partSize
		end := start + partSize
		if part == total-1 && lastLen > 0 {
			end = start + lastLen
		}

		// Ranges and parts both ascend, so the cursor only moves forward.
		for next < len(covered) && covered[next][1] <= start {
			next++
		}
		cursor := start
		for i := next; i < len(covered) && cursor < end && covered[i][0] <= cursor; i++ {
			cursor = covered[i][1]
		}

		if cursor < end {
			uncovered = append(uncovered, part)
			if len(uncovered) == maxPushedParts {
				break
			}
		}
	}
	return uncovered
}

// mergeRanges returns the blobs' byte ranges sorted and merged, so overlapping
// or adjacent transactions become one span.
func mergeRanges(blobs []proptypes.TxMetaData) [][2]uint32 {
	ranges := make([][2]uint32, 0, len(blobs))
	for _, blob := range blobs {
		if blob.End > blob.Start {
			ranges = append(ranges, [2]uint32{blob.Start, blob.End})
		}
	}
	slices.SortFunc(ranges, func(a, b [2]uint32) int { return cmp.Compare(a[0], b[0]) })

	merged := make([][2]uint32, 0, len(ranges))
	for _, r := range ranges {
		last := len(merged) - 1
		if last >= 0 && r[0] <= merged[last][1] {
			merged[last][1] = max(merged[last][1], r[1])
			continue
		}
		merged = append(merged, r)
	}
	return merged
}
