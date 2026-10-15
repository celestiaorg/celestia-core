package propagation

import (
	"cmp"
	"slices"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

// maxPushedParts bounds the original parts a proposer pushes to each peer, and
// that a node accepts unrequested from a peer that sent it the compact block.
// The header takes 1 part and evidence plus last commit (~110 B per validator) up to
// 5 more, which fits ~2,300 validators with no evidence. Above it, nothing is
// pushed and peers use have/want.
const maxPushedParts = 6

// uncoveredParts returns the original parts with bytes outside every
// transaction's range, which no mempool can rebuild. It returns nil when there
// are more than maxPushedParts of them.
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
			if len(uncovered) == maxPushedParts {
				return nil
			}
			uncovered = append(uncovered, part)
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
