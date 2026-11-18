package types

import (
	"fmt"
	"runtime"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/cometbft/cometbft/types"
)

// UnmarshalledTx is an intermediary type that allows keeping the transaction
// metadata, its Key and the actual tx bytes. This will be used to create the
// parts from the local txs.
type UnmarshalledTx struct {
	MetaData TxMetaData
	Key      types.TxKey
	// Prefix and TxBytes together are the tx's encoding in the block, so the
	// tx bytes can be referenced without copying them behind a prefix.
	Prefix  []byte
	TxBytes []byte
}

func (tx *UnmarshalledTx) encodedLen() int {
	return len(tx.Prefix) + len(tx.TxBytes)
}

// copyEncoded copies the tx's encoding, starting at offset, into dst.
func (tx *UnmarshalledTx) copyEncoded(dst []byte, offset int) {
	if offset < len(tx.Prefix) {
		n := copy(dst, tx.Prefix[offset:])
		dst, offset = dst[n:], 0
	} else {
		offset -= len(tx.Prefix)
	}
	copy(dst, tx.TxBytes[offset:])
}

func TxsToParts(txs []UnmarshalledTx, partCount, partSize, lastPartLen uint32) ([]*types.Part, error) {
	if len(txs) == 0 {
		return nil, nil
	}

	for i, tx := range txs {
		expectedLen := tx.MetaData.End - tx.MetaData.Start
		if uint32(tx.encodedLen()) != expectedLen {
			return nil, fmt.Errorf("transaction %d has inconsistent encoded length: expected %d, got %d",
				i, expectedLen, tx.encodedLen())
		}
	}

	cmp := func(i, j int) bool {
		return txs[i].MetaData.Start < txs[j].MetaData.Start
	}

	if !sort.SliceIsSorted(txs, cmp) {
		sort.Slice(txs, cmp)
	}

	// parts are independent: build them concurrently, then keep index order.
	built := make([]*types.Part, partCount)
	var eg errgroup.Group
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for i := range partCount {
		eg.Go(func() error {
			built[i] = buildPart(txs, i, partCount, partSize, lastPartLen)
			return nil
		})
	}
	_ = eg.Wait()
	result := built[:0]
	for _, part := range built {
		if part != nil {
			result = append(result, part)
		}
	}

	return result, nil
}

// buildPart returns part i if txs cover it entirely, or nil.
func buildPart(txs []UnmarshalledTx, i, partCount, partSize, lastPartLen uint32) *types.Part {
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
		return nil
	}

	partLen := endBoundary - startBoundary
	partBytes := make([]byte, partLen)

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
		if txOffset+length > uint32(tx.encodedLen()) {
			return nil
		}
		if partOffset+length > uint32(len(partBytes)) {
			return nil
		}

		tx.copyEncoded(partBytes[partOffset:partOffset+length], int(txOffset))
	}

	return &types.Part{
		Index: i,
		Bytes: partBytes,
	}
}
