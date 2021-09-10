package fraudproofs

import (
	"bytes"
	"fmt"
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

func TestBadEncodingFraudProof(t *testing.T) {
	type test struct {
		name        string
		input       BadEncodingFraudProof
		dah         types.DataAvailabilityHeader
		output      bool
		expectedErr string
	}

	err, dah, dahWithBadEncoding, fraudProof := ValidBadEncodingFraudProof()
	require.NoError(t, err)

	fraudProofPositionOOB := BadEncodingFraudProof{
		Height:      fraudProof.Height,
		ShareProofs: fraudProof.ShareProofs,
		IsCol:       fraudProof.IsCol,
		Position:    uint64(len(fraudProof.ShareProofs)*2 + 1),
	}

	tests := []test{
		{
			name:        "Block with bad encoding",
			input:       fraudProof,
			dah:         dahWithBadEncoding,
			output:      true,
			expectedErr: "",
		},
		{
			name:        "BadEncodingFraudProof for a correct block",
			input:       fraudProof,
			dah:         dah,
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
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErr)
			continue
		}
		require.NoError(t, err)
	}
}

func generateBadEncodedTree(shares [][]byte, index int) (*wrapper.ErasuredNamespacedMerkleTree, [][]byte, error) {
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(len(shares)))
	codec := consts.DefaultCodec()
	extendedShares, err := codec.Encode(shares)
	if err != nil {
		return nil, nil, err
	}
	// pick the erasured share at the given index
	extendedShares[index] = append(append(
		make([]byte, 0, consts.ShareSize+consts.NamespaceSize), consts.ParitySharesNamespaceID...),
		bytes.Repeat([]byte{1}, consts.ShareSize)...)
	for i, share := range extendedShares {
		tree.Push(share, rsmt2d.SquareIndex{Axis: 0, Cell: uint(i)})
	}
	return &tree, extendedShares, nil
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}

func ValidBadEncodingFraudProof() (error, types.DataAvailabilityHeader, types.DataAvailabilityHeader, BadEncodingFraudProof) {

	txCount := 4
	isrCount := 0
	evdCount := 0
	msgCount := 0
	maxSize := 36
	blockData := generateRandomBlockData(txCount, isrCount, evdCount, msgCount, maxSize)

	namespacedShares, _ := blockData.ComputeShares()
	shares := namespacedShares.RawShares()

	// extend the original data with bad encoding
	origSquareSize := uint32(math.Sqrt(float64(len(shares))))

	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(origSquareSize))

	// extend the original data
	extendedDataSquare, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
	}

	// generate the row and col roots of the extended data square
	rowRoots := extendedDataSquare.RowRoots()
	colRoots := extendedDataSquare.ColRoots()

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
	}

	// corrupt the data availability header
	height := int64(0)
	position := uint64(0)
	isCol := false

	sharesToCorrupt := extendedDataSquare.Row(uint(position))[0:origSquareSize]
	treeForRow, corruptedShares, err := generateBadEncodedTree(sharesToCorrupt, int(origSquareSize)*2-1)
	if err != nil {
		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
	}

	rowRootsWithBadEncoding := make([]namespace.IntervalDigest, len(dah.RowsRoots))
	colRootsWithBadEncoding := make([]namespace.IntervalDigest, len(dah.ColumnRoots))
	copy(rowRootsWithBadEncoding, dah.RowsRoots)
	copy(colRootsWithBadEncoding, dah.ColumnRoots)
	dahWithBadEncoding := types.DataAvailabilityHeader{
		RowsRoots:   rowRootsWithBadEncoding,
		ColumnRoots: colRootsWithBadEncoding,
	}

	root := treeForRow.Root()
	rootDigest := namespace.IntervalDigest{
		Min:    root[0:consts.NamespaceSize],
		Max:    root[consts.NamespaceSize : consts.NamespaceSize*2],
		Digest: root[consts.NamespaceSize*2:],
	}
	dahWithBadEncoding.RowsRoots[position] = rootDigest

	// For debugging; delete later
	// -------------------------
	fmt.Println("This is the axisRoot corresponding to the uncorrupted row: ", dah.RowsRoots[position])
	fmt.Println("This is the axisRoot corresponding to the corrupted row: ", rootDigest)
	// -------------------------

	fraudProof, err := CreateBadEncodingFraudProof(height, uint64(origSquareSize), position, corruptedShares, isCol)
	if err != nil {
		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
	}
	return nil, dah, dahWithBadEncoding, fraudProof
}

// func ValidBadEncodingFraudProof() (error, types.DataAvailabilityHeader, types.DataAvailabilityHeader, BadEncodingFraudProof) {
// 	txCount := 5
// 	isrCount := 5
// 	evdCount := 1
// 	msgCount := 25
// 	maxSize := 36
// 	blockData := generateRandomBlockData(txCount, isrCount, evdCount, msgCount, maxSize)

// 	namespacedShares, _ := blockData.ComputeShares()
// 	shares := namespacedShares.RawShares()

// 	// extend the original data with bad encoding
// 	origSquareSize := maxSize
// 	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(origSquareSize))

// 	extendedDataSquareWithBadEncoding, err := ComputeExtendedDataSquareWithBadEncoding(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
// 	if err != nil {
// 		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
// 	}

// 	// extend the original data
// 	extendedDataSquare, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
// 	if err != nil {
// 		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
// 	}

// 	// generate the row and col roots of the extended data square with bad encoding
// 	rowRootsWithBadEncoding := extendedDataSquareWithBadEncoding.RowRoots()
// 	colRootsWithBadEncoding := extendedDataSquareWithBadEncoding.ColRoots()

// 	// generate the row and col roots of the extended data square
// 	rowRoots := extendedDataSquare.RowRoots()
// 	colRoots := extendedDataSquare.ColRoots()

// 	// generate data availability header for the block with bad encoding
// 	refactoredRowRootsWithBadEncoding := make([]namespace.IntervalDigest, len(rowRootsWithBadEncoding))
// 	for i, rowRoot := range rowRootsWithBadEncoding {
// 		refactoredRowRootsWithBadEncoding[i] = namespace.IntervalDigest{
// 			Min:    rowRoot[0:consts.NamespaceSize],
// 			Max:    rowRoot[consts.NamespaceSize : consts.NamespaceSize*2],
// 			Digest: rowRoot[consts.NamespaceSize*2:],
// 		}
// 	}
// 	refactoredColRootsWithBadEncoding := make([]namespace.IntervalDigest, len(colRootsWithBadEncoding))
// 	for i, colRoot := range colRootsWithBadEncoding {
// 		refactoredColRootsWithBadEncoding[i] = namespace.IntervalDigest{
// 			Min:    colRoot[0:consts.NamespaceSize],
// 			Max:    colRoot[consts.NamespaceSize : consts.NamespaceSize*2],
// 			Digest: colRoot[consts.NamespaceSize*2:],
// 		}
// 	}
// 	dahWithBadEncoding := types.DataAvailabilityHeader{
// 		RowsRoots:   refactoredRowRootsWithBadEncoding,
// 		ColumnRoots: refactoredColRootsWithBadEncoding,
// 		//hash:        byte(0),
// 	}

// 	// generate data availability header for the correct block
// 	refactoredRowRoots := make([]namespace.IntervalDigest, len(rowRoots))
// 	for i, rowRoot := range rowRoots {
// 		refactoredRowRoots[i] = namespace.IntervalDigest{
// 			Min:    rowRoot[0:consts.NamespaceSize],
// 			Max:    rowRoot[consts.NamespaceSize : consts.NamespaceSize*2],
// 			Digest: rowRoot[consts.NamespaceSize*2:],
// 		}
// 	}
// 	refactoredColRoots := make([]namespace.IntervalDigest, len(colRoots))
// 	for i, colRoot := range colRoots {
// 		refactoredColRoots[i] = namespace.IntervalDigest{
// 			Min:    colRoot[0:consts.NamespaceSize],
// 			Max:    colRoot[consts.NamespaceSize : consts.NamespaceSize*2],
// 			Digest: colRoot[consts.NamespaceSize*2:],
// 		}
// 	}
// 	dah := types.DataAvailabilityHeader{
// 		RowsRoots:   refactoredRowRoots,
// 		ColumnRoots: refactoredColRoots,
// 		//hash:        byte(0),
// 	}

// 	fraudProof, err := CheckAndCreateBadEncodingFraudProof(blockData, &dahWithBadEncoding)
// 	if err != nil {
// 		return err, types.DataAvailabilityHeader{}, types.DataAvailabilityHeader{}, BadEncodingFraudProof{}
// 	}
// 	return nil, dah, dahWithBadEncoding, fraudProof
// }

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
