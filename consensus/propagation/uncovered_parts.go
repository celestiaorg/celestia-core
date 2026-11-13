package propagation

import (
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

// maxPushedParts is how many parts the proposer pushes per peer, and how many
// unrequested parts we accept from a peer that sent us the compact block.
// 1 for the header, 5 for evidence and last commit (good for ~2,300 validators).
// If more are needed we push nothing and peers just ask via have/want.
const maxPushedParts = 6

// uncoveredParts returns the indexes of the original parts that hold bytes
// outside the transactions (header, evidence, last commit). No mempool can
// rebuild those parts. It returns nil when there are more than maxPushedParts
// of them.
func uncoveredParts(txs []proptypes.TxMetaData, totalParts, partSize, lastPartSize uint32) []uint32 {
	// the math below only works if the txs are one continuous range. If they
	// ever aren't, push nothing and let peers ask via have/want.
	if !txsAreBackToBack(txs) {
		return nil
	}
	txsStart, txsEnd := txsByteRange(txs)

	uncovered := make([]uint32, 0, maxPushedParts)
	for partIndex := range totalParts {
		partStart := partIndex * partSize
		partEnd := partStart + partSize

		// the last part is usually shorter than a full part
		isLastPart := partIndex == totalParts-1
		if isLastPart && lastPartSize > 0 {
			partEnd = partStart + lastPartSize
		}

		isOnlyTxBytes := txsStart <= partStart && partEnd <= txsEnd
		if isOnlyTxBytes {
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

// txsAreBackToBack reports whether every transaction starts exactly where the
// previous one ends, with no gap or overlap.
func txsAreBackToBack(txs []proptypes.TxMetaData) bool {
	for i := 1; i < len(txs); i++ {
		previousTx := txs[i-1]
		currentTx := txs[i]
		if currentTx.Start != previousTx.End {
			return false
		}
	}
	return true
}

// txsByteRange returns where the transactions start and end in the block.
// The caller must check txsAreBackToBack first, so they are one continuous
// range from the first transaction's start to the last one's end.
func txsByteRange(txs []proptypes.TxMetaData) (start, end uint32) {
	if len(txs) == 0 {
		return 0, 0
	}
	firstTx := txs[0]
	lastTx := txs[len(txs)-1]
	return firstTx.Start, lastTx.End
}
