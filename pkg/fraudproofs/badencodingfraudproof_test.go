package fraudproofs

import (
	"bytes"
	"errors"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/celestiaorg/celestia-core/pkg/consts"
	"github.com/celestiaorg/celestia-core/pkg/wrapper"
	"github.com/celestiaorg/celestia-core/types"
	"github.com/celestiaorg/nmt/namespace"

	tmbytes "github.com/celestiaorg/celestia-core/libs/bytes"
	"github.com/celestiaorg/rsmt2d"
	"github.com/stretchr/testify/require"
)

type BadEncodingError int

func TestBadEncodingFraudProof(t *testing.T) error {
	type test struct {
		name        string
		input       BadEncodingFraudProof
		dah         types.DataAvailabilityHeader
		output      bool
		expectedErr string
	}

	err, dah, dahWithBadEncoding, fraudProof := ValidBadEncodingFraudProof()
	if err != nil {
		return err
	}

	fraudProofPositionOOB := BadEncodingFraudProof{
		Height:      fraudProof.Height,
		ShareProofs: fraudProof.ShareProofs,
		IsCol:       fraudProof.IsCol,
		Position:    uint64(len(fraudProof.ShareProofs) * 2),
	}

	tests := []test{
		{
			name:        "Block with bad encoding",
			input:       fraudProof,
			dah:         dah,
			output:      true,
			expectedErr: "",
		},
		{
			name:        "BadEncodingFraudProof for a correct block",
			input:       fraudProof,
			dah:         dahWithBadEncoding,
			output:      false,
			expectedErr: "There is no bad encoding!",
		},
		// {
		// 	name: "Incorrect number of shares",
		// 	input: BadEncodingFraudProof{
		// 		Height:      10,
		// 		ShareProofs: []tmproto.ShareProof{},
		// 		IsCol:       true,
		// 		Position:    12,
		// 	},
		// 	output: false,
		// 	expectedErr:    nil,
		// },
		{
			name:        "Position Out of Bound",
			input:       fraudProofPositionOOB,
			dah:         dahWithBadEncoding,
			output:      false,
			expectedErr: "Position is out of bound.",
		},
		// {
		// 	name: "Non committed shares",
		// 	input: BadEncodingFraudProof{
		// 		Height:      10,
		// 		ShareProofs: []tmproto.ShareProof{},
		// 		IsCol:       true,
		// 		Position:    12,
		// 	},
		// 	output: false,
		// 	expectedErr:    nil,
		// },
		// {
	}

	for _, tt := range tests {
		res, err := VerifyBadEncodingFraudProof(tt.input, &tt.dah)
		require.Equal(t, tt.output, res)
		if tt.expectedErr != "" {
			require.Contains(t, err.Error(), tt.expectedErr)
		}
	}

	return nil
}

// type dataSquare struct {
// 	squareRow    [][][]byte // row-major
// 	squareCol    [][][]byte // col-major
// 	width        uint
// 	chunkSize    uint
// 	rowRoots     [][]byte
// 	colRoots     [][]byte
// 	createTreeFn rsmt2d.TreeConstructorFn
// }

// type ExtendedDataSquare struct {
// 	*dataSquare
// 	originalDataWidth uint
// }

// func newDataSquare(data [][]byte, treeCreator rsmt2d.TreeConstructorFn) (*dataSquare, error) {
// 	width := int(math.Ceil(math.Sqrt(float64(len(data)))))
// 	if width*width != len(data) {
// 		return nil, errors.New("number of chunks must be a square number")
// 	}

// 	chunkSize := len(data[0])

// 	squareRow := make([][][]byte, width)
// 	for i := 0; i < width; i++ {
// 		squareRow[i] = data[i*width : i*width+width]

// 		for j := 0; j < width; j++ {
// 			if len(squareRow[i][j]) != chunkSize {
// 				return nil, errors.New("all chunks must be of equal size")
// 			}
// 		}
// 	}

// 	squareCol := make([][][]byte, width)
// 	for j := 0; j < width; j++ {
// 		squareCol[j] = make([][]byte, width)
// 		for i := 0; i < width; i++ {
// 			squareCol[j][i] = data[i*width+j]
// 		}
// 	}

// 	return &dataSquare{
// 		squareRow:    squareRow,
// 		squareCol:    squareCol,
// 		width:        uint(width),
// 		chunkSize:    uint(chunkSize),
// 		createTreeFn: treeCreator,
// 	}, nil
// }

// func (ds *dataSquare) resetRoots() {
// 	ds.rowRoots = nil
// 	ds.colRoots = nil
// }

// func (ds *dataSquare) extendSquare(extendedWidth uint, fillerChunk []byte) error {
// 	if uint(len(fillerChunk)) != ds.chunkSize {
// 		return errors.New("filler chunk size does not match data square chunk size")
// 	}

// 	newWidth := ds.width + extendedWidth
// 	newSquareRow := make([][][]byte, newWidth)

// 	fillerExtendedRow := make([][]byte, extendedWidth)
// 	for i := uint(0); i < extendedWidth; i++ {
// 		fillerExtendedRow[i] = fillerChunk
// 	}

// 	fillerRow := make([][]byte, newWidth)
// 	for i := uint(0); i < newWidth; i++ {
// 		fillerRow[i] = fillerChunk
// 	}

// 	row := make([][]byte, ds.width)
// 	for i := uint(0); i < ds.width; i++ {
// 		copy(row, ds.squareRow[i])
// 		newSquareRow[i] = append(row, fillerExtendedRow...)
// 	}

// 	for i := ds.width; i < newWidth; i++ {
// 		newSquareRow[i] = make([][]byte, newWidth)
// 		copy(newSquareRow[i], fillerRow)
// 	}

// 	ds.squareRow = newSquareRow

// 	newSquareCol := make([][][]byte, newWidth)
// 	for j := uint(0); j < newWidth; j++ {
// 		newSquareCol[j] = make([][]byte, newWidth)
// 		for i := uint(0); i < newWidth; i++ {
// 			newSquareCol[j][i] = newSquareRow[i][j]
// 		}
// 	}
// 	ds.squareCol = newSquareCol
// 	ds.width = newWidth

// 	ds.resetRoots()

// 	return nil
// }

// func (ds *dataSquare) rowSlice(x uint, y uint, length uint) [][]byte {
// 	return ds.squareRow[x][y : y+length]
// }

// func (ds *dataSquare) colSlice(x uint, y uint, length uint) [][]byte {
// 	return ds.squareCol[y][x : x+length]
// }

// func (ds *dataSquare) setRowSlice(x uint, y uint, newRow [][]byte) error {
// 	for i := uint(0); i < uint(len(newRow)); i++ {
// 		if len(newRow[i]) != int(ds.chunkSize) {
// 			return errors.New("invalid chunk size")
// 		}
// 	}

// 	for i := uint(0); i < uint(len(newRow)); i++ {
// 		ds.squareRow[x][y+i] = newRow[i]
// 		ds.squareCol[y+i][x] = newRow[i]
// 	}

// 	ds.resetRoots()

// 	return nil
// }

// func (ds *dataSquare) setColSlice(x uint, y uint, newCol [][]byte) error {
// 	for i := uint(0); i < uint(len(newCol)); i++ {
// 		if len(newCol[i]) != int(ds.chunkSize) {
// 			return errors.New("invalid chunk size")
// 		}
// 	}

// 	for i := uint(0); i < uint(len(newCol)); i++ {
// 		ds.squareRow[x+i][y] = newCol[i]
// 		ds.squareCol[y][x+i] = newCol[i]
// 	}

// 	ds.resetRoots()

// 	return nil
// }

// // getRowRoot calculates and returns the root of the selected row. Note: unlike the
// // getRowRoots method, getRowRoot uses the built-in cache when available.
// func (ds *dataSquare) getRowRoot(x uint) []byte {
// 	if ds.rowRoots != nil {
// 		return ds.rowRoots[x]
// 	}

// 	tree := ds.createTreeFn()
// 	for i, d := range ds.row(x) {
// 		tree.Push(d, SquareIndex{Cell: uint(i), Axis: x})
// 	}

// 	return tree.Root()
// }

// // getColRoots returns the Merkle roots of all the columns in the square.
// func (ds *dataSquare) getColRoots() [][]byte {
// 	if ds.colRoots == nil {
// 		ds.computeRoots()
// 	}

// 	return ds.colRoots
// }

// // getColRoot calculates and returns the root of the selected row. Note: unlike the
// // getColRoots method, getColRoot uses the built-in cache when available.
// func (ds *dataSquare) getColRoot(y uint) []byte {
// 	if ds.colRoots != nil {
// 		return ds.colRoots[y]
// 	}

// 	tree := ds.createTreeFn()
// 	for i, d := range ds.col(y) {
// 		tree.Push(d, SquareIndex{Axis: y, Cell: uint(i)})
// 	}

// 	return tree.Root()
// }

// func (ds *dataSquare) computeRoots() {
// 	rowRoots := make([][]byte, ds.width)
// 	colRoots := make([][]byte, ds.width)
// 	for i := uint(0); i < ds.width; i++ {
// 		rowRoots[i] = ds.getRowRoot(i)
// 		colRoots[i] = ds.getColRoot(i)
// 	}

// 	ds.rowRoots = rowRoots
// 	ds.colRoots = colRoots
// }

func erasureExtendSquareWithBadEncoding(eds rsmt2d.ExtendedDataSquare, codec rsmt2d.Codec) error {
	width := eds.Width()
	if err := eds.extendSquare(width, bytes.Repeat([]byte{0}, int(eds.chunkSize))); err != nil {
		return err
	}

	var shares [][]byte
	var err error

	// Extend original square horizontally and vertically
	//  ------- -------
	// |       |       |
	// |   O → |   E   |
	// |   ↓   |       |
	//  ------- -------
	// |       |
	// |   E   |
	// |       |
	//  -------
	for i := uint(0); i < eds.originalDataWidth; i++ {
		// Extend horizontally
		shares, err = codec.Encode(eds.rowSlice(i, 0, eds.originalDataWidth))
		if err != nil {
			return err
		}
		if err := eds.setRowSlice(i, eds.originalDataWidth, shares[len(shares)-int(eds.originalDataWidth):]); err != nil {
			return err
		}

		// Extend vertically
		shares, err = codec.Encode(eds.colSlice(0, i, eds.originalDataWidth))

		// Introduce bad encoding
		// ----------------------------------------------------
		incorrectShare := make([]byte, len(shares[0]))
		incorrectShare[0:consts.NamespaceSize] = consts.ParitySharesNamespaceID
		incorrectShare[consts.NamespaceSize:] = byte(0)
		shares[len(shares)-1] = incorrectShare
		// ----------------------------------------------------

		if err != nil {
			return err
		}
		if err := eds.setColSlice(eds.originalDataWidth, i, shares[len(shares)-int(eds.originalDataWidth):]); err != nil {
			return err
		}
	}

	// Extend extended square horizontally
	//  ------- -------
	// |       |       |
	// |   O   |   E   |
	// |       |       |
	//  ------- -------
	// |       |       |
	// |   E → |   E   |
	// |       |       |
	//  ------- -------
	for i := eds.originalDataWidth; i < eds.width; i++ {
		// Extend horizontally
		shares, err = codec.Encode(eds.rowSlice(i, 0, eds.originalDataWidth))
		if err != nil {
			return err
		}
		if err := eds.setRowSlice(i, eds.originalDataWidth, shares[len(shares)-int(eds.originalDataWidth):]); err != nil {
			return err
		}
	}

	return nil
}

// ComputeExtendedDataSquareWithBadEncoding computes an extended data square with bad encoding.
func ComputeExtendedDataSquareWithBadEncoding(
	data [][]byte,
	codec rsmt2d.Codec,
	treeCreatorFn rsmt2d.TreeConstructorFn,
) (*rsmt2d.ExtendedDataSquare, error) {
	if len(data) > codec.maxChunks() {
		return nil, errors.New("number of chunks exceeds the maximum")
	}

	ds, err := rsmt2d.newDataSquare(data, treeCreatorFn)
	if err != nil {
		return nil, err
	}

	eds := rsmt2d.ExtendedDataSquare{dataSquare: ds}
	err = eds.erasureExtendSquareWithBadEncoding(codec)
	if err != nil {
		return nil, err
	}

	return &eds, nil
}

func ValidBadEncodingFraudProof() (error, types.DataAvailabilityHeader, types.DataAvailabilityHeader, BadEncodingFraudProof) {
	txCount := 5
	isrCount := 5
	evdCount := 1
	msgCount := 25
	maxSize := 36
	blockData := generateRandomBlockData(txCount, isrCount, evdCount, msgCount, maxSize)

	namespacedShares, _ := blockData.ComputeShares()
	shares := namespacedShares.RawShares()

	// extend the original data with bad encoding
	origSquareSize := maxSize
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(origSquareSize))

	extendedDataSquareWithBadEncoding, err := ComputeExtendedDataSquareWithBadEncoding(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
	}

	// extend the original data
	extendedDataSquare, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
	}

	// generate the row and col roots of the extended data square with bad encoding
	rowRootsWithBadEncoding := extendedDataSquareWithBadEncoding.RowRoots()
	colRootsWithBadEncoding := extendedDataSquareWithBadEncoding.ColRoots()

	// generate the row and col roots of the extended data square
	rowRoots := extendedDataSquare.RowRoots()
	colRoots := extendedDataSquare.ColRoots()

	// generate data availability header for the block with bad encoding
	refactoredRowRootsWithBadEncoding := make([]namespace.IntervalDigest, len(rowRootsWithBadEncoding))
	for i, rowRoot := range rowRootsWithBadEncoding {
		refactoredRowRootsWithBadEncoding[i] = namespace.IntervalDigest{
			Min:    rowRoot[0:consts.NamespaceSize],
			Max:    rowRoot[consts.NamespaceSize : consts.NamespaceSize*2],
			Digest: rowRoot[consts.NamespaceSize*2:],
		}
	}
	refactoredColRootsWithBadEncoding := make([]namespace.IntervalDigest, len(colRootsWithBadEncoding))
	for i, colRoot := range colRootsWithBadEncoding {
		refactoredColRootsWithBadEncoding[i] = namespace.IntervalDigest{
			Min:    colRoot[0:consts.NamespaceSize],
			Max:    colRoot[consts.NamespaceSize : consts.NamespaceSize*2],
			Digest: colRoot[consts.NamespaceSize*2:],
		}
	}
	dahWithBadEncoding := types.DataAvailabilityHeader{
		RowsRoots:   refactoredRowRootsWithBadEncoding,
		ColumnRoots: refactoredColRootsWithBadEncoding,
		//hash:        byte(0),
	}

	// generate data availability header for the correct block
	refactoredRowRoots := make([]namespace.IntervalDigest, len(rowRoots))
	for i, rowRoot := range rowRoots {
		refactoredRowRoots[i] = namespace.IntervalDigest{
			Min:    rowRoot[0:consts.NamespaceSize],
			Max:    rowRoot[consts.NamespaceSize : consts.NamespaceSize*2],
			Digest: rowRoot[consts.NamespaceSize*2:],
		}
	}
	refactoredColRoots := make([]namespace.IntervalDigest, len(colRoots))
	for i, colRoot := range colRoots {
		refactoredColRoots[i] = namespace.IntervalDigest{
			Min:    colRoot[0:consts.NamespaceSize],
			Max:    colRoot[consts.NamespaceSize : consts.NamespaceSize*2],
			Digest: colRoot[consts.NamespaceSize*2:],
		}
	}
	dah := types.DataAvailabilityHeader{
		RowsRoots:   refactoredRowRoots,
		ColumnRoots: refactoredColRoots,
		//hash:        byte(0),
	}

	fraudProof, err := CheckAndCreateBadEncodingFraudProof(blockData, &dahWithBadEncoding)
	if err != nil {
		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
	}
	return nil, dah, dahWithBadEncoding, fraudProof
}

// generateRandomBlockData returns randomly generated block data for testing purposes
func generateRandomBlockData(txCount, isrCount, evdCount, msgCount, maxSize int) types.Data {
	var out types.Data
	out.Txs = generateRandomlySizedContiguousShares(txCount, maxSize)
	out.IntermediateStateRoots = generateRandomISR(isrCount)
	out.Evidence = generateIdenticalEvidence(evdCount)
	out.Messages = generateRandomlySizedMessages(msgCount, maxSize)
	return out
}

func generateRandomlySizedContiguousShares(count, max int) types.Txs {
	txs := make(types.Txs, count)
	for i := 0; i < count; i++ {
		size := rand.Intn(max)
		if size == 0 {
			size = 1
		}
		txs[i] = generateRandomContiguousShares(1, size)[0]
	}
	return txs
}

func generateRandomContiguousShares(count, size int) types.Txs {
	txs := make(types.Txs, count)
	for i := 0; i < count; i++ {
		tx := make([]byte, size)
		_, err := rand.Read(tx)
		if err != nil {
			panic(err)
		}
		txs[i] = tx
	}
	return txs
}

func generateRandomISR(count int) types.IntermediateStateRoots {
	roots := make([]tmbytes.HexBytes, count)
	for i := 0; i < count; i++ {
		roots[i] = tmbytes.HexBytes(generateRandomContiguousShares(1, 32)[0])
	}
	return types.IntermediateStateRoots{RawRootsList: roots}
}

func generateIdenticalEvidence(count int) types.EvidenceData {
	evidence := make([]types.Evidence, count)
	for i := 0; i < count; i++ {
		ev := types.NewMockDuplicateVoteEvidence(math.MaxInt64, time.Now(), "chainID")
		evidence[i] = ev
	}
	return types.EvidenceData{Evidence: evidence}
}

func generateRandomlySizedMessages(count, maxMsgSize int) types.Messages {
	msgs := make([]types.Message, count)
	for i := 0; i < count; i++ {
		msgs[i] = generateRandomMessage(rand.Intn(maxMsgSize))
	}

	// this is just to let us use assert.Equal
	if count == 0 {
		msgs = nil
	}

	return types.Messages{MessagesList: msgs}
}

func generateRandomMessage(size int) types.Message {
	share := generateRandomNamespacedShares(1, size)[0]
	msg := types.Message{
		NamespaceID: share.NamespaceID(),
		Data:        share.Data(),
	}
	return msg
}

func generateRandomNamespacedShares(count, msgSize int) types.NamespacedShares {
	shares := generateRandNamespacedRawData(uint32(count), consts.NamespaceSize, uint32(msgSize))
	msgs := make([]types.Message, count)
	for i, s := range shares {
		msgs[i] = types.Message{
			Data:        s[consts.NamespaceSize:],
			NamespaceID: s[:consts.NamespaceSize],
		}
	}
	return types.Messages{MessagesList: msgs}.SplitIntoShares()
}

func generateRandNamespacedRawData(total, nidSize, leafSize uint32) [][]byte {
	data := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		nid := make([]byte, nidSize)
		rand.Read(nid)
		data[i] = nid
	}
	sortByteArrays(data)
	for i := uint32(0); i < total; i++ {
		d := make([]byte, leafSize)
		rand.Read(d)
		data[i] = append(data[i], d...)
	}

	return data
}

// note: index should point to erasured data
func generateBadEncodedTree(shares [][]byte, index int) (*wrapper.ErasuredNamespacedMerkleTree, error) {
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(len(shares)))
	codec := consts.DefaultCodec()
	extendedShares, err := codec.Encode(shares)
	if err != nil {
		return nil, err
	}
	// pick a random erasured share
	extendedShares[index] = append(append(
		make([]byte, 0, consts.ShareSize+consts.NamespaceSize), consts.ParitySharesNamespaceID...),
		bytes.Repeat([]byte{1}, consts.ShareSize)...)
	for i, share := range extendedShares {
		tree.Push(share, rsmt2d.SquareIndex{Axis: 0, Cell: uint(i)})
	}
	return &tree, nil
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}
