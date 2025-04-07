package types

import (
	"sort"

	"github.com/tendermint/tendermint/types"
)

// UnmarshalledTx is an intermediary type that allows keeping the transaction
// metadata, its Key and the actual tx bytes. This will be used to create the
// parts from the local txs.
type UnmarshalledTx struct {
	MetaData TxMetaData
	Key      types.TxKey
	TxBytes  []byte
}

// TxsToParts calculates the parts that can be reconstructed from
// transactions
func TxsToParts(txs []UnmarshalledTx, partCount, partSize, lastPartLen uint32) []*types.Part {
	result := make([]*types.Part, 0)

	sort.Slice(txs, func(i, j int) bool {
		return txs[i].MetaData.Start < txs[j].MetaData.Start
	})

	for i := uint32(0); i < partCount; i++ {
		startBoundary := uint32(i * partSize)
		endBoundary := startBoundary + uint32(partSize)
		if i == partCount-1 && lastPartLen > 0 && lastPartLen < partSize {
			endBoundary = startBoundary + uint32(lastPartLen)
		}

		var isolatedTxs []UnmarshalledTx
		// isolate the relevant transactions for this part
		for _, tx := range txs {
			if tx.MetaData.End < startBoundary || tx.MetaData.Start >= endBoundary {
				continue
			}
			isolatedTxs = append(isolatedTxs, tx)
		}

		cur := startBoundary
		complete := false
		for _, tx := range isolatedTxs {
			if tx.MetaData.Start > cur {
				break
			}
			if tx.MetaData.End > cur {
				cur = tx.MetaData.End
			}
			if cur >= endBoundary {
				complete = true
				break
			}
		}

		if !complete {
			continue
		}

		partLen := endBoundary - startBoundary
		partBytes := make([]byte, partLen)

		for _, tx := range isolatedTxs {
			overlapStart := max(tx.MetaData.Start, startBoundary)
			overlapEnd := min(tx.MetaData.End, endBoundary)
			if overlapEnd <= overlapStart {
				continue
			}

			txOffset := overlapStart - tx.MetaData.Start
			partOffset := overlapStart - startBoundary
			length := overlapEnd - overlapStart

			copy(partBytes[partOffset:partOffset+length], tx.TxBytes[txOffset:txOffset+length])
		}

		part := &types.Part{
			Index: uint32(i), // this will only need to change when blocks are > 4GiB
			Bytes: partBytes,
		}

		result = append(result, part)
	}

	return result
}
