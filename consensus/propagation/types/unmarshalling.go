package types

import (
	"sort"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/types"
)

// TxsToParts takes a set of mempool transactions and creates parts from them.
// It fixes the memory leak by copying part bytes.
func TxsToParts(txsFound []UnmarshalledTx) []*types.Part {
	// sort the transactions by start index
	sort.Slice(txsFound, func(i, j int) bool {
		return txsFound[i].MetaData.Start < txsFound[j].MetaData.Start
	})

	parts := make([]*types.Part, 0)
	var cumulativeBytes []byte
	// cumulativeStart tracks the starting index of the bytes stored in cumulativeBytes.
	cumulativeStart := -1

	for i, tx := range txsFound {
		// Determine the boundaries of the part in which this transaction belongs.
		partIndex := tx.MetaData.Start / types.BlockPartSizeBytes
		partStart := partIndex * types.BlockPartSizeBytes
		partEnd := (partIndex + 1) * types.BlockPartSizeBytes

		// If the cumulative buffer is empty, set the starting index.
		if len(cumulativeBytes) == 0 {
			cumulativeStart = int(tx.MetaData.Start)
		}

		// Append the current transaction's bytes.
		cumulativeBytes = append(cumulativeBytes, tx.TxBytes...)

		// Check if the cumulative bytes do not start exactly at the beginning of the part.
		// If they don't, adjust the cumulative buffer.
		if int(partStart) < cumulativeStart {
			relativeEnd := int(partEnd) - cumulativeStart
			if relativeEnd > len(cumulativeBytes) {
				// Not enough bytes to flush any part, reset the cumulative buffer.
				cumulativeBytes = cumulativeBytes[:0]
				cumulativeStart = -1
			} else {
				// Trim the cumulative buffer to start exactly at the part's end.
				cumulativeBytes = cumulativeBytes[relativeEnd:]
				cumulativeStart = int(partEnd)
			}
		}

		// Flush complete parts from the cumulative buffer.
		for len(cumulativeBytes) >= int(types.BlockPartSizeBytes) {
			// Create a copy of the part's bytes to prevent memory leaks.
			partData := make([]byte, types.BlockPartSizeBytes)
			copy(partData, cumulativeBytes[:types.BlockPartSizeBytes])

			newPart := &types.Part{
				Index: uint32(cumulativeStart) / types.BlockPartSizeBytes,
				Bytes: partData,
				Proof: merkle.Proof{}, // empty proof as the full Merkle tree is unavailable here
			}
			parts = append(parts, newPart)

			// Remove the flushed part from the cumulative buffer.
			cumulativeBytes = cumulativeBytes[types.BlockPartSizeBytes:]
			cumulativeStart += int(types.BlockPartSizeBytes)
		}

		// If the next transaction is not contiguous with the current one, reset the buffer.
		if i+1 < len(txsFound) {
			nextTx := txsFound[i+1]
			if tx.MetaData.End != nextTx.MetaData.Start {
				cumulativeBytes = cumulativeBytes[:0]
				cumulativeStart = -1
			}
		}
	}

	return parts
}

// UnmarshalledTx is an intermediary type that allows keeping the transaction
// metadata, its Key and the actual tx bytes. This will be used to create the
// parts from the local txs.
type UnmarshalledTx struct {
	MetaData TxMetaData
	Key      types.TxKey
	TxBytes  []byte
}
