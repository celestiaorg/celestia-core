package da

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/celestiaorg/celestia-core/crypto/merkle"
	"github.com/celestiaorg/celestia-core/crypto/tmhash"
	daproto "github.com/celestiaorg/celestia-core/proto/tendermint/da"
)

// DataAvailabilityHeader (DAHeader) contains the row and column roots of the erasure
// coded version of the data in Block.Data.
// Therefor the original Block.Data is arranged in a
// k × k matrix, which is then "extended" to a
// 2k × 2k matrix applying multiple times Reed-Solomon encoding.
// For details see Section 5.2: https://arxiv.org/abs/1809.09044
// or the Celestia specification:
// https://github.com/celestiaorg/celestia-specs/blob/master/specs/data_structures.md#availabledataheader
// Note that currently we list row and column roots in separate fields
// (different from the spec).
type DataAvailabilityHeader struct {
	// RowRoot_j 	= root((M_{j,1} || M_{j,2} || ... || M_{j,2k} ))
	RowsRoots [][]byte `json:"row_roots"`
	// ColumnRoot_j = root((M_{1,j} || M_{2,j} || ... || M_{2k,j} ))
	ColumnRoots [][]byte `json:"column_roots"`
	// cached result of Hash() not to be recomputed
	hash []byte
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
		slices[i] = rowRoot
	}
	for i, colRoot := range dah.ColumnRoots {
		slices[i+colsCount] = colRoot
	}
	// The single data root is computed using a simple binary merkle tree.
	// Effectively being root(rowRoots || columnRoots):
	dah.hash = merkle.HashFromByteSlices(slices)
	return dah.hash
}

func (dah *DataAvailabilityHeader) ToProto() (*daproto.DataAvailabilityHeader, error) {
	if dah == nil {
		return nil, errors.New("nil DataAvailabilityHeader")
	}

	dahp := new(daproto.DataAvailabilityHeader)
	dahp.RowRoots = dah.RowsRoots
	dahp.ColumnRoots = dah.ColumnRoots
	return dahp, nil
}

func DataAvailabilityHeaderFromProto(dahp *daproto.DataAvailabilityHeader) (dah *DataAvailabilityHeader, err error) {
	if dahp == nil {
		return nil, errors.New("nil DataAvailabilityHeader")
	}

	dah = new(DataAvailabilityHeader)
	dah.RowsRoots = dahp.RowRoots
	dah.ColumnRoots = dahp.ColumnRoots

	return
}

// ValidateBasic runs stateless checks on the DataAvailabilityHeader. Calls Hash() if not already called
func (dah *DataAvailabilityHeader) ValidateBasic() error {
	if dah == nil {
		return errors.New("nil data availability header is not valid")
	}
	const minDAHSize = 2
	if len(dah.ColumnRoots) < minDAHSize || len(dah.RowsRoots) < minDAHSize {
		return fmt.Errorf(
			"Minimum valid DataAvailabilityHeader has at least %d row and column roots",
			minDAHSize,
		)
	}
	if len(dah.ColumnRoots) != len(dah.RowsRoots) {
		return fmt.Errorf(
			"unequal number of row and column roots: row %d col %d",
			len(dah.RowsRoots),
			len(dah.ColumnRoots),
		)
	}
	if err := ValidateHash(dah.hash); err != nil {
		return fmt.Errorf("wrong hash: %v", err)
	}

	return nil
}

func (dah *DataAvailabilityHeader) IsZero() bool {
	if dah == nil {
		return true
	}
	return len(dah.ColumnRoots) == 0 || len(dah.RowsRoots) == 0
}

// MinDataAvailabilityHeader returns a hard coded copy of a data availability
// header from empty block data
func MinDataAvailabilityHeader() *DataAvailabilityHeader {
	firstRoot := []byte{
		255, 255, 255, 255, 255, 255, 255, 254, 255, 255, 255, 255, 255, 255, 255, 254, 102, 154, 168, 240,
		216, 82, 33, 160, 91, 111, 9, 23, 136, 77, 48, 97, 106, 108, 125, 83, 48, 165, 100, 10, 8, 160, 77,
		204, 91, 9, 47, 79,
	}

	secondRoot := []byte{
		255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 41, 52, 55, 243,
		182, 165, 97, 30, 37, 201, 13, 90, 68, 184, 76, 196, 179, 114, 12, 219, 166, 133, 83, 222, 254,
		139, 113, 154, 241, 245, 195, 149,
	}

	dah := &DataAvailabilityHeader{
		RowsRoots:   [][]byte{firstRoot, secondRoot},
		ColumnRoots: [][]byte{firstRoot, secondRoot},
		hash: []byte{
			4, 122, 211, 141, 172, 30, 22, 215, 241, 73, 77, 225, 174, 40, 53, 252, 106, 158, 117, 238,
			88, 77, 86, 66, 235, 146, 121, 62, 161, 36, 160, 111,
		},
	}
	return dah
}

// ValidateHash returns an error if the hash is not empty, but its
// size != tmhash.Size.
func ValidateHash(h []byte) error {
	if len(h) > 0 && len(h) != tmhash.Size {
		return fmt.Errorf("expected size to be %d bytes, got %d bytes",
			tmhash.Size,
			len(h),
		)
	}
	return nil
}
