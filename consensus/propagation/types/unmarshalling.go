package types

import (
	"sort"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/types"
)

// TxsToParts takes a set of mempool transactions and tries to create parts from them.
func TxsToParts(txsFound []UnmarshalledTx) []*types.Part {
	// sort the txs found by start index
	sort.Slice(txsFound, func(i, j int) bool {
		return txsFound[i].MetaData.Start < txsFound[j].MetaData.Start
	})

	// the part slice we will return
	parts := make([]*types.Part, 0)

	// the cumulative bytes slice will contain the transaction bytes along with
	// any left bytes from previous contiguous transactions
	cumulativeBytes := make([]byte, 0)
	// the start index of where the cumulative bytes start
	cumulativeBytesStartIndex := -1

	for index := 0; index < len(txsFound); index++ {
		// the transaction we're parsing
		currentTx := txsFound[index]
		// the inclusive part index where the transaction starts
		currentPartIndex := currentTx.MetaData.Start / types.BlockPartSizeBytes
		currentPartStartIndex := currentPartIndex * types.BlockPartSizeBytes
		// the exclusive index of the byte where the current part ends
		currentPartEndIndex := (currentPartIndex + 1) * types.BlockPartSizeBytes

		if len(cumulativeBytes) == 0 {
			// an empty cumulative bytes means the current transaction start
			// is where the cumulative bytes will start
			cumulativeBytesStartIndex = int(currentTx.MetaData.Start)
		}
		// append the current transaction bytes to the cumulative bytes slice
		cumulativeBytes = append(cumulativeBytes, currentTx.TxBytes...)

		// This case checks whether the cumulative bytes start index
		// starts at the current part.
		// If not, this means the current part, even if we might have some of its data,
		// is not recoverable, and we can truncate it.
		if int(currentPartStartIndex) < cumulativeBytesStartIndex {
			// relative part end index
			relativePartEndIndex := int(currentPartEndIndex) - cumulativeBytesStartIndex
			if relativePartEndIndex > len(cumulativeBytes) {
				// case where the cumulative bytes length is small.
				// this happens with small transactions.
				cumulativeBytes = cumulativeBytes[:0]
				cumulativeBytesStartIndex = -1
			} else {
				// slice the cumulative bytes to start at exactly the part end index
				cumulativeBytes = cumulativeBytes[relativePartEndIndex:]
				// set the cumulative bytes start index to the current part end index
				cumulativeBytesStartIndex = int(currentPartEndIndex)
			}
		}

		// parse the parts we gathered so far
		for len(cumulativeBytes) >= int(types.BlockPartSizeBytes) {
			// get the part's bytes
			partBz := cumulativeBytes[:types.BlockPartSizeBytes]
			// create the part
			part := &types.Part{
				Index: uint32(cumulativeBytesStartIndex) / types.BlockPartSizeBytes,
				Bytes: partBz,
				Proof: merkle.Proof{}, // empty proof because we don't have the other leaves to create a valid one
			}
			parts = append(parts, part)
			// slice this part off the cumulative bytes
			cumulativeBytes = cumulativeBytes[types.BlockPartSizeBytes:]
			// set cumulative start index
			cumulativeBytesStartIndex += int(types.BlockPartSizeBytes)
		}

		// check whether the next transaction is a contingent to the current one.
		if index+1 < len(txsFound) {
			nextTx := txsFound[index+1]
			if currentTx.MetaData.End != nextTx.MetaData.Start {
				// the next transaction is not contingent, we can reset the cumulative bytes.
				cumulativeBytes = cumulativeBytes[:0]
				cumulativeBytesStartIndex = -1
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
