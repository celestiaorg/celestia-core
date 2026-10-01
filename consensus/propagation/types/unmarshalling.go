package types

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/cometbft/cometbft/libs/parallel"
	"github.com/cometbft/cometbft/types"
)

// txFieldKey is the protobuf key of Data.txs: field number 1, wire type 2. A
// transaction appears inside the encoded block as this key, then its length as
// a varint, then the transaction bytes.
const txFieldKey = byte(1<<3 | 2)

// partGrain is the smallest number of block parts worth giving a goroutine.
// Building one part is a 64 KiB copy, so the fan-out pays for itself quickly.
const partGrain = 4

// UnmarshalledTx is an intermediary type that allows keeping the transaction
// metadata, its Key and the actual tx bytes. This will be used to create the
// parts from the local txs.
//
// The framed transaction is held as two pieces, the protobuf prefix and the
// transaction bytes the mempool already owns, rather than as one contiguous
// buffer. Rebuilding a block then copies each transaction exactly once, from
// the mempool straight into the part buffer.
type UnmarshalledTx struct {
	MetaData TxMetaData
	Key      types.TxKey
	// prefix is the protobuf key and length varint that precede the
	// transaction inside the block.
	prefix []byte
	// tx is the transaction itself, referenced rather than copied.
	tx []byte
}

// NewUnmarshalledTx frames tx as it appears inside the block's transaction
// list. tx is referenced, not copied, so it must not be modified afterwards.
func NewUnmarshalledTx(metaData TxMetaData, key types.TxKey, tx []byte) UnmarshalledTx {
	var buf [1 + binary.MaxVarintLen64]byte
	buf[0] = txFieldKey
	n := binary.PutUvarint(buf[1:], uint64(len(tx)))
	prefix := make([]byte, 1+n)
	copy(prefix, buf[:1+n])
	return UnmarshalledTx{MetaData: metaData, Key: key, prefix: prefix, tx: tx}
}

// Len is the number of bytes the framed transaction occupies in the block.
func (u UnmarshalledTx) Len() uint32 {
	return uint32(len(u.prefix) + len(u.tx))
}

// Bytes returns the framed transaction as one contiguous buffer. It allocates,
// so it is for tests and diagnostics; the part builder copies the pieces
// directly instead.
func (u UnmarshalledTx) Bytes() []byte {
	out := make([]byte, 0, u.Len())
	out = append(out, u.prefix...)
	return append(out, u.tx...)
}

// copyInto copies length bytes of the framed transaction, starting at offset
// within it, to the front of dst. It reports whether the range was in bounds.
func (u UnmarshalledTx) copyInto(dst []byte, offset, length uint32) bool {
	if uint64(offset)+uint64(length) > uint64(u.Len()) || uint32(len(dst)) < length {
		return false
	}

	prefixLen := uint32(len(u.prefix))
	written := uint32(0)
	if offset < prefixLen {
		n := min(length, prefixLen-offset)
		copy(dst[:n], u.prefix[offset:offset+n])
		written = n
	}
	if written < length {
		// Anything left comes from the transaction body, which starts where
		// the prefix ends.
		bodyOffset := uint32(0)
		if offset > prefixLen {
			bodyOffset = offset - prefixLen
		}
		n := length - written
		copy(dst[written:written+n], u.tx[bodyOffset:bodyOffset+n])
	}
	return true
}

func TxsToParts(txs []UnmarshalledTx, partCount, partSize, lastPartLen uint32) ([]*types.Part, error) {
	if len(txs) == 0 {
		return nil, nil
	}

	for i, tx := range txs {
		expectedLen := tx.MetaData.End - tx.MetaData.Start
		if tx.Len() != expectedLen {
			return nil, fmt.Errorf("transaction %d has inconsistent TxBytes length: expected %d, got %d",
				i, expectedLen, tx.Len())
		}
	}

	cmp := func(i, j int) bool {
		return txs[i].MetaData.Start < txs[j].MetaData.Start
	}

	if !sort.SliceIsSorted(txs, cmp) {
		sort.Slice(txs, cmp)
	}

	// Parts are independent of one another, so they are built concurrently and
	// then collected in index order, which keeps the output identical to the
	// serial build.
	built := make([]*types.Part, partCount)
	parallel.For(int(partCount), partGrain, func(i int) {
		built[i] = buildPart(txs, uint32(i), partCount, partSize, lastPartLen)
	})

	result := make([]*types.Part, 0, partCount)
	for _, part := range built {
		if part != nil {
			result = append(result, part)
		}
	}

	return result, nil
}

// buildPart rebuilds the part at index from the transactions that cover it, or
// returns nil when they do not cover every byte of it. txs must be sorted by
// start offset.
func buildPart(txs []UnmarshalledTx, index, partCount, partSize, lastPartLen uint32) *types.Part {
	startBoundary := index * partSize
	endBoundary := startBoundary + partSize
	// Adjust for a short final part if necessary.
	if index == partCount-1 && lastPartLen > 0 && lastPartLen < partSize {
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
	if boundary < endBoundary {
		return nil
	}

	partBytes := make([]byte, endBoundary-startBoundary)
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

		if !tx.copyInto(partBytes[partOffset:], txOffset, length) {
			return nil
		}
	}

	return &types.Part{
		Index: index,
		Bytes: partBytes,
	}
}
