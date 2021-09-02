package da

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/celestiaorg/celestia-core/crypto/merkle"
	"github.com/celestiaorg/celestia-core/crypto/tmhash"
	"github.com/celestiaorg/celestia-core/pkg/consts"
	"github.com/celestiaorg/celestia-core/pkg/wrapper"
	daproto "github.com/celestiaorg/celestia-core/proto/tendermint/da"
	"github.com/celestiaorg/rsmt2d"
)

const (
	maxDAHSize = consts.MaxSquareSize * 2
	minDAHSize = consts.MinSquareSize * 2
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

// NewDataAvailabilityHeader generates a DataAvailability header using the provided square size and shares
func NewDataAvailabilityHeader(squareSize uint64, shares [][]byte) (DataAvailabilityHeader, error) {
	// Check that square size is with range
	if squareSize < consts.MinSquareSize || squareSize > consts.MaxSquareSize {
		return DataAvailabilityHeader{}, fmt.Errorf(
			"invalid square size: min %d max %d provided %d",
			consts.MinSquareSize,
			consts.MaxSquareSize,
			squareSize,
		)
	}
	// check that valid number of shares have been provided
	if squareSize*squareSize != uint64(len(shares)) {
		return DataAvailabilityHeader{}, fmt.Errorf(
			"must provide valid number of shares for square size: got %d wanted %d",
			len(shares),
			squareSize*squareSize,
		)
	}

	tree := wrapper.NewErasuredNamespacedMerkleTree(squareSize)

	// TODO(ismail): for better efficiency and a larger number shares
	// we should switch to the rsmt2d.LeopardFF16 codec:
	extendedDataSquare, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return DataAvailabilityHeader{}, err
	}

	// generate the row and col roots using the EDS
	dah := DataAvailabilityHeader{
		RowsRoots:   extendedDataSquare.RowRoots(),
		ColumnRoots: extendedDataSquare.ColRoots(),
	}

	// generate the hash of the data using the new roots
	dah.Hash()

	return dah, nil
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

	return dah, dah.ValidateBasic()
}

// ValidateBasic runs stateless checks on the DataAvailabilityHeader. Calls Hash() if not already called
func (dah *DataAvailabilityHeader) ValidateBasic() error {
	if dah == nil {
		return errors.New("nil data availability header is not valid")
	}
	if len(dah.ColumnRoots) < minDAHSize || len(dah.RowsRoots) < minDAHSize {
		return fmt.Errorf(
			"minimum valid DataAvailabilityHeader has at least %d row and column roots",
			minDAHSize,
		)
	}
	if len(dah.ColumnRoots) > maxDAHSize || len(dah.RowsRoots) > maxDAHSize {
		return fmt.Errorf(
			"maximum valid DataAvailabilityHeader has at most %d row and column roots",
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
	if err := validateHash(dah.hash); err != nil {
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

// tail is filler for all tail padded shares
// it is allocated once and used everywhere
var tailPaddingShare = append(
	append(make([]byte, 0, consts.ShareSize), consts.TailPaddingNamespaceID...),
	bytes.Repeat([]byte{0}, consts.ShareSize-consts.NamespaceSize)...,
)

// MinDataAvailabilityHeader returns the minimum valid data availability header.
// It is equal to the data availability header for an empty block
func MinDataAvailabilityHeader() DataAvailabilityHeader {
	shares := make([][]byte, consts.MinSharecount)
	for i := 0; i < consts.MinSharecount; i++ {
		shares[i] = tailPaddingShare
	}
	dah, err := NewDataAvailabilityHeader(
		consts.MinSquareSize,
		shares,
	)
	if err != nil {
		panic(err)
	}
	return dah
}

// validateHash returns an error if the hash is not empty, but its
// size != tmhash.Size. copy pasted from `types` package as to not import
func validateHash(h []byte) error {
	if len(h) > 0 && len(h) != tmhash.Size {
		return fmt.Errorf("expected size to be %d bytes, got %d bytes",
			tmhash.Size,
			len(h),
		)
	}
	return nil
}
