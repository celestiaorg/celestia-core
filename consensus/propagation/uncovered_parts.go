package propagation

import (
	"cmp"
	"slices"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

// maxPushedParts is how many parts the proposer pushes per peer, and how many
// unrequested parts we accept from a peer that sent us the compact block.
// 1 for the header, 5 for evidence and last commit (good for ~2,300 validators).
// If more are needed we push nothing and peers just ask via have/want.
const maxPushedParts = 6

// byteRange is a span of bytes in the block: start is included, end is not.
type byteRange struct {
	start uint32
	end   uint32
}

// uncoveredParts returns the indexes of the original parts that hold bytes no
// transaction covers (header, evidence, last commit). No mempool can rebuild
// those parts. It returns nil when there are more than maxPushedParts of them.
func uncoveredParts(txs []proptypes.TxMetaData, totalParts, partSize, lastPartSize uint32) []uint32 {
	txRanges := mergedTxRanges(txs)

	uncovered := make([]uint32, 0, maxPushedParts)
	for partIndex := range totalParts {
		partStart := partIndex * partSize
		partEnd := partStart + partSize

		// the last part is usually shorter than a full part
		isLastPart := partIndex == totalParts-1
		if isLastPart && lastPartSize > 0 {
			partEnd = partStart + lastPartSize
		}

		if isCoveredByTxs(txRanges, partStart, partEnd) {
			continue
		}

		// too many to push, so push nothing
		if len(uncovered) == maxPushedParts {
			return nil
		}
		uncovered = append(uncovered, partIndex)
	}
	return uncovered
}

// isCoveredByTxs reports whether every byte of the part belongs to a
// transaction. txRanges must be merged, so that is true only when a single
// range spans the whole part.
func isCoveredByTxs(txRanges []byteRange, partStart, partEnd uint32) bool {
	for _, txRange := range txRanges {
		startsBeforePart := txRange.start <= partStart
		endsAfterPart := partEnd <= txRange.end
		if startsBeforePart && endsAfterPart {
			return true
		}
	}
	return false
}

// mergedTxRanges returns the byte ranges of the transactions, sorted, with
// overlapping or touching ranges joined into one.
func mergedTxRanges(txs []proptypes.TxMetaData) []byteRange {
	txRanges := make([]byteRange, 0, len(txs))
	for _, tx := range txs {
		isEmpty := tx.End <= tx.Start
		if isEmpty {
			continue
		}
		txRanges = append(txRanges, byteRange{start: tx.Start, end: tx.End})
	}
	slices.SortFunc(txRanges, func(a, b byteRange) int {
		return cmp.Compare(a.start, b.start)
	})

	merged := make([]byteRange, 0, len(txRanges))
	for _, txRange := range txRanges {
		if len(merged) == 0 {
			merged = append(merged, txRange)
			continue
		}

		previous := &merged[len(merged)-1]
		touchesPrevious := txRange.start <= previous.end
		if touchesPrevious {
			previous.end = max(previous.end, txRange.end)
			continue
		}
		merged = append(merged, txRange)
	}
	return merged
}
