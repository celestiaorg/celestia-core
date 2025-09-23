package types

import (
	"fmt"
	"sort"

	"github.com/cometbft/cometbft/types"
)

// UnmarshalledTx is an intermediary type that allows keeping the transaction
// metadata, its Key and the actual tx bytes. This will be used to create the
// parts from the local txs.
type UnmarshalledTx struct {
	MetaData TxMetaData
	Key      types.TxKey
	TxBytes  []byte
}

func TxsToParts(txs []UnmarshalledTx, partCount, partSize, lastPartLen uint32) ([]*types.Part, error) {
	if len(txs) == 0 {
		return nil, nil
	}
	result := make([]*types.Part, 0, partCount)

	for i, tx := range txs {
		expectedLen := tx.MetaData.End - tx.MetaData.Start
		if uint32(len(tx.TxBytes)) != expectedLen {
			return nil, fmt.Errorf("transaction %d has inconsistent TxBytes length: expected %d, got %d",
				i, expectedLen, len(tx.TxBytes))
		}
	}

	cmp := func(i, j int) bool {
		return txs[i].MetaData.Start < txs[j].MetaData.Start
	}

	if !sort.SliceIsSorted(txs, cmp) {
		sort.Slice(txs, cmp)
	}

	for i := uint32(0); i < partCount; i++ {
		startBoundary := i * partSize
		endBoundary := startBoundary + partSize
		// Adjust for a short final part if necessary.
		if i == partCount-1 && lastPartLen > 0 && lastPartLen < partSize {
			endBoundary = startBoundary + lastPartLen
		}

		first, _ := sort.Find(len(txs), func(n int) int {
			return int(startBoundary) - int(txs[n].MetaData.End)
		})

		last, _ := sort.Find(len(txs), func(n int) int {
			return int(endBoundary) - int(txs[n].MetaData.Start)
		})

		boundary := startBoundary
		for idx := first; idx < last; idx++ {
			if txs[idx].MetaData.Start <= boundary {
				boundary = txs[idx].MetaData.End
			}
		}
		complete := boundary >= endBoundary

		if !complete {
			continue
		}

		partLen := endBoundary - startBoundary
		partBytes := make([]byte, partLen)

		var skip bool
		for idx := first; idx < last; idx++ {
			tx := &txs[idx]
			overlapStart := max(tx.MetaData.Start, startBoundary)
			overlapEnd := min(tx.MetaData.End, endBoundary)
			if overlapEnd <= overlapStart {
				continue // No overlap.
			}

			txOffset := overlapStart - tx.MetaData.Start
			partOffset := overlapStart - startBoundary
			length := overlapEnd - overlapStart

			// Defensive check: Ensure the computed bounds are within the slice lengths.
			if txOffset+length > uint32(len(tx.TxBytes)) {
				skip = true
				break
			}
			if partOffset+length > uint32(len(partBytes)) {
				skip = true
				break
			}

			copy(partBytes[partOffset:partOffset+length], tx.TxBytes[txOffset:txOffset+length])
		}

		if skip {
			continue
		}

		part := &types.Part{
			Index: i,
			Bytes: partBytes,
		}
		result = append(result, part)
	}

	return result, nil
}
