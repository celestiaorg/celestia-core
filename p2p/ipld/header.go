package ipld

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/crypto/merkle"
	"github.com/lazyledger/lazyledger-core/proto/tendermint/types"
)

// DataAvailabilityHeader (DAHeader) contains the row and column roots of the erasure
// coded version of the data in Block.Data.
// Therefor the original Block.Data is arranged in a
// k × k matrix, which is then "extended" to a
// 2k × 2k matrix applying multiple times Reed-Solomon encoding.
// For details see Section 5.2: https://arxiv.org/abs/1809.09044
// or the LazyLedger specification:
// https://github.com/lazyledger/lazyledger-specs/blob/master/specs/data_structures.md#availabledataheader
// Note that currently we list row and column roots in separate fields
// (different from the spec).
type DataAvailabilityHeader struct {
	// RowRoot_j 	= root((M_{j,1} || M_{j,2} || ... || M_{j,2k} ))
	RowsRoots NmtRoots `json:"row_roots"`
	// ColumnRoot_j = root((M_{1,j} || M_{2,j} || ... || M_{2k,j} ))
	ColumnRoots NmtRoots `json:"column_roots"`
	// cached result of Hash() not to be recomputed
	hash []byte
}

// TODO(Wondertan): Should return single Hash/CID instead
func MakeDataHeader(eds *rsmt2d.ExtendedDataSquare) *DataAvailabilityHeader {
	// generate the row and col roots using the EDS and nmt wrapper
	rowRoots, colRoots := eds.RowRoots(), eds.ColumnRoots()
	// create DAH
	dah := &DataAvailabilityHeader{
		RowsRoots:   make([]namespace.IntervalDigest, eds.Width()),
		ColumnRoots: make([]namespace.IntervalDigest, eds.Width()),
	}
	// todo(evan): remove interval digests
	// convert the roots to interval digests
	for i := 0; i < len(rowRoots); i++ {
		rowRoot, err := namespace.IntervalDigestFromBytes(NamespaceSize, rowRoots[i])
		if err != nil {
			panic(err)
		}
		colRoot, err := namespace.IntervalDigestFromBytes(NamespaceSize, colRoots[i])
		if err != nil {
			panic(err)
		}
		dah.RowsRoots[i] = rowRoot
		dah.ColumnRoots[i] = colRoot
	}
	return dah
}

type NmtRoots []namespace.IntervalDigest

func (roots NmtRoots) Bytes() [][]byte {
	res := make([][]byte, len(roots))
	for i := 0; i < len(roots); i++ {
		res[i] = roots[i].Bytes()
	}
	return res
}

func NmtRootsFromBytes(in [][]byte) (roots NmtRoots, err error) {
	roots = make([]namespace.IntervalDigest, len(in))
	for i := 0; i < len(in); i++ {
		roots[i], err = namespace.IntervalDigestFromBytes(NamespaceSize, in[i])
		if err != nil {
			return roots, err
		}
	}
	return
}

// String returns hex representation of merkle hash of the DAHeader.
func (dah *DataAvailabilityHeader) String() string {
	if dah == nil {
		return "<nil DAHeader>"
	}
	return fmt.Sprintf("%X", dah.Hash())
}

// Equals checks equality of two DAHeaders.
func (dah *DataAvailabilityHeader) Equals(to *DataAvailabilityHeader) bool {
	return bytes.Equal(dah.Hash(), to.Hash())
}

// Hash computes and caches the merkle root of the row and column roots.
func (dah *DataAvailabilityHeader) Hash() []byte {
	if dah == nil {
		return merkle.HashFromByteSlices(nil)
	}
	if len(dah.hash) != 0 {
		return dah.hash
	}

	colsCount := len(dah.ColumnRoots)
	rowsCount := len(dah.RowsRoots)
	slices := make([][]byte, colsCount+rowsCount)
	for i, rowRoot := range dah.RowsRoots {
		slices[i] = rowRoot.Bytes()
	}
	for i, colRoot := range dah.ColumnRoots {
		slices[i+colsCount] = colRoot.Bytes()
	}
	// The single data root is computed using a simple binary merkle tree.
	// Effectively being root(rowRoots || columnRoots):
	dah.hash = merkle.HashFromByteSlices(slices)
	return dah.hash
}

func (dah *DataAvailabilityHeader) ToProto() (*types.DataAvailabilityHeader, error) {
	if dah == nil {
		return nil, errors.New("nil DataAvailabilityHeader")
	}

	dahp := new(types.DataAvailabilityHeader)
	dahp.RowRoots = dah.RowsRoots.Bytes()
	dahp.ColumnRoots = dah.ColumnRoots.Bytes()
	return dahp, nil
}

func DataAvailabilityHeaderFromProto(dahp *types.DataAvailabilityHeader) (dah *DataAvailabilityHeader, err error) {
	if dahp == nil {
		return nil, errors.New("nil DataAvailabilityHeader")
	}

	dah = new(DataAvailabilityHeader)
	dah.RowsRoots, err = NmtRootsFromBytes(dahp.RowRoots)
	if err != nil {
		return
	}

	dah.ColumnRoots, err = NmtRootsFromBytes(dahp.ColumnRoots)
	if err != nil {
		return
	}

	return
}
