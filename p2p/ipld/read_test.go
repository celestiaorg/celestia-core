package ipld

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-ipfs/core/coreapi"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	format "github.com/ipfs/go-ipld-format"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
)

var raceDetectorActive = false

func TestLeafPath(t *testing.T) {
	type test struct {
		name         string
		index, total uint32
		expected     []string
	}

	// test cases
	tests := []test{
		{"nil", 0, 0, []string(nil)},
		{"0 index 16 total leaves", 0, 16, strings.Split("0/0/0/0", "/")},
		{"1 index 16 total leaves", 1, 16, strings.Split("0/0/0/1", "/")},
		{"9 index 16 total leaves", 9, 16, strings.Split("1/0/0/1", "/")},
		{"15 index 16 total leaves", 15, 16, strings.Split("1/1/1/1", "/")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result, err := leafPath(tt.index, tt.total)
			if err != nil {
				t.Error(err)
			}
			assert.Equal(t, tt.expected, result)
		},
		)
	}
}

func Test_isPowerOf2(t *testing.T) {
	type test struct {
		input    uint32
		expected bool
	}
	tests := []test{
		{
			input:    2,
			expected: true,
		},
		{
			input:    11,
			expected: false,
		},
		{
			input:    511,
			expected: false,
		},

		{
			input:    0,
			expected: true,
		},
		{
			input:    1,
			expected: true,
		},
		{
			input:    16,
			expected: true,
		},
	}
	for _, tt := range tests {
		res := isPowerOf2(tt.input)
		assert.Equal(t, tt.expected, res, fmt.Sprintf("input was %d", tt.input))
	}
}

func TestGetLeafData(t *testing.T) {
	type test struct {
		name    string
		timeout time.Duration
		rootCid cid.Cid
		leaves  [][]byte
	}

	// create the context and batch needed for node collection from the tree
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// issue a new API object
	ipfsAPI := mockedIpfsAPI(t)
	batch := format.NewBatch(ctx, ipfsAPI.Dag())

	// generate random data for the nmt
	data := generateRandNamespacedRawData(16, types.NamespaceSize, types.ShareSize)

	// create a random tree
	root, err := getNmtRoot(ctx, batch, data)
	if err != nil {
		t.Error(err)
	}

	// compute the root and create a cid for the root hash
	rootCid, err := nodes.CidFromNamespacedSha256(root.Bytes())
	if err != nil {
		t.Error(err)
	}

	// test cases
	tests := []test{
		{"16 leaves", time.Second, rootCid, data},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()
			for i, leaf := range tt.leaves {
				data, err := GetLeafData(ctx, tt.rootCid, uint32(i), uint32(len(tt.leaves)), ipfsAPI)
				if err != nil {
					t.Error(err)
				}
				assert.Equal(t, leaf, data)
			}
		},
		)
	}
}

func TestBlockRecovery(t *testing.T) {
	// adjustedLeafSize describes the size of a leaf that will not get split
	adjustedLeafSize := types.MsgShareSize

	originalSquareWidth := 8
	shareCount := originalSquareWidth * originalSquareWidth
	extendedSquareWidth := 2 * originalSquareWidth
	extendedShareCount := extendedSquareWidth * extendedSquareWidth

	// generate test data
	quarterShares := generateRandNamespacedRawData(shareCount, types.NamespaceSize, adjustedLeafSize)
	allShares := generateRandNamespacedRawData(shareCount, types.NamespaceSize, adjustedLeafSize)

	testCases := []struct {
		name string
		// blockData types.Data
		shares    [][]byte
		expectErr bool
		errString string
		d         int // number of shares to delete
	}{
		{"missing 1/2 shares", quarterShares, false, "", extendedShareCount / 2},
		{"missing 1/4 shares", quarterShares, false, "", extendedShareCount / 4},
		{"max missing data", quarterShares, false, "", ((originalSquareWidth + 1) * (originalSquareWidth + 1))},
		{"missing all but one shares", allShares, true, "failed to solve data square", extendedShareCount - 1},
	}
	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			squareSize := uint64(math.Sqrt(float64(len(tc.shares))))

			// create trees for creating roots
			tree := NewErasuredNamespacedMerkleTree(squareSize)
			recoverTree := NewErasuredNamespacedMerkleTree(squareSize)

			eds, err := rsmt2d.ComputeExtendedDataSquare(tc.shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
			if err != nil {
				t.Error(err)
			}

			// calculate roots using the first complete square
			rowRoots := eds.RowRoots()
			colRoots := eds.ColumnRoots()

			flat := flatten(eds)

			// recover a partially complete square
			reds, err := rsmt2d.RepairExtendedDataSquare(
				rowRoots,
				colRoots,
				removeRandShares(flat, tc.d),
				rsmt2d.NewRSGF8Codec(),
				recoverTree.Constructor,
			)

			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errString)
				return
			}

			assert.NoError(t, err)

			// check that the squares are equal
			assert.Equal(t, flatten(eds), flatten(reds))
		})
	}
}

func TestRetrieveBlockData(t *testing.T) {
	type test struct {
		name       string
		squareSize int
		expectErr  bool
		errStr     string
	}

	// issue a new API object
	ipfsAPI := mockedIpfsAPI(t)

	// the max size of messages that won't get split
	adjustedMsgSize := types.MsgShareSize - 2

	tests := []test{
		{"Empty block", 1, false, ""},
		{"4 KB block", 4, false, ""},
		{"16 KB block", 8, false, ""},
		{"16 KB block timeout expected", 8, true, "timeout"},
		{"max square size", types.MaxSquareSize, false, ""},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(fmt.Sprintf("%s size %d", tc.name, tc.squareSize), func(t *testing.T) {
			// if we're using the race detector, skip some large tests due to time and
			// concurrency constraints
			if raceDetectorActive && tc.squareSize > 8 {
				t.Skip("Not running large test due to time and concurrency constraints while race detector is active.")
			}

			background := context.Background()
			blockData := generateRandomBlockData(tc.squareSize*tc.squareSize, adjustedMsgSize)
			block := types.Block{
				Data:       blockData,
				LastCommit: &types.Commit{},
			}

			// if an error is exected, don't put the block
			if !tc.expectErr {
				err := block.PutBlock(background, ipfsAPI.Dag())
				if err != nil {
					t.Fatal(err)
				}
			}

			shareData, _ := blockData.ComputeShares()
			rawData := shareData.RawShares()

			tree := NewErasuredNamespacedMerkleTree(uint64(tc.squareSize))
			eds, err := rsmt2d.ComputeExtendedDataSquare(rawData, rsmt2d.NewRSGF8Codec(), tree.Constructor)
			if err != nil {
				t.Fatal(err)
			}

			rawRowRoots := eds.RowRoots()
			rawColRoots := eds.ColumnRoots()
			rowRoots := rootsToDigests(rawRowRoots)
			colRoots := rootsToDigests(rawColRoots)

			retrievalCtx, cancel := context.WithTimeout(background, time.Second*2)
			defer cancel()

			rblockData, err := RetrieveBlockData(
				retrievalCtx,
				&types.DataAvailabilityHeader{
					RowsRoots:   rowRoots,
					ColumnRoots: colRoots,
				},
				ipfsAPI,
				rsmt2d.NewRSGF8Codec(),
			)

			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errStr)
				return
			}

			require.NoError(t, err)

			nsShares, _ := rblockData.ComputeShares()

			assert.Equal(t, rawData, nsShares.RawShares())
		})
	}
}

func flatten(eds *rsmt2d.ExtendedDataSquare) [][]byte {
	flattenedEDSSize := eds.Width() * eds.Width()
	out := make([][]byte, flattenedEDSSize)
	count := 0
	for i := uint(0); i < eds.Width(); i++ {
		for _, share := range eds.Row(i) {
			out[count] = share
			count++
		}
	}
	return out
}

// getNmtRoot generates the nmt root of some namespaced data
func getNmtRoot(
	ctx context.Context,
	batch *format.Batch,
	namespacedData [][]byte,
) (namespace.IntervalDigest, error) {
	na := nodes.NewNmtNodeAdder(ctx, batch)
	tree := nmt.New(sha256.New(), nmt.NamespaceIDSize(types.NamespaceSize), nmt.NodeVisitor(na.Visit))
	for _, leaf := range namespacedData {
		err := tree.Push(leaf)
		if err != nil {
			return namespace.IntervalDigest{}, err
		}
	}

	return tree.Root(), na.Commit()
}

// this code is copy pasted from the plugin, and should likely be exported in the plugin instead
func generateRandNamespacedRawData(total int, nidSize int, leafSize int) [][]byte {
	data := make([][]byte, total)
	for i := 0; i < total; i++ {
		nid := make([]byte, nidSize)
		_, err := rand.Read(nid)
		if err != nil {
			panic(err)
		}
		data[i] = nid
	}

	sortByteArrays(data)
	for i := 0; i < total; i++ {
		d := make([]byte, leafSize)
		_, err := rand.Read(d)
		if err != nil {
			panic(err)
		}
		data[i] = append(data[i], d...)
	}

	return data
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}

// removes d shares from data
func removeRandShares(data [][]byte, d int) [][]byte {
	count := len(data)
	// remove shares randomly
	for i := 0; i < d; {
		ind := rand.Intn(count)
		if len(data[ind]) == 0 {
			continue
		}
		data[ind] = nil
		i++
	}
	return data
}

func rootsToDigests(roots [][]byte) []namespace.IntervalDigest {
	out := make([]namespace.IntervalDigest, len(roots))
	for i, root := range roots {
		idigest, err := namespace.IntervalDigestFromBytes(types.NamespaceSize, root)
		if err != nil {
			panic(err)
		}
		out[i] = idigest
	}
	return out
}

func generateRandomBlockData(msgCount, msgSize int) types.Data {
	var out types.Data
	if msgCount == 1 {
		return out
	}
	out.Messages = generateRandomMessages(msgCount-1, msgSize)
	out.Txs = generateRandomContiguousShares(1)
	return out
}

func generateRandomMessages(count, msgSize int) types.Messages {
	shares := generateRandNamespacedRawData(count, types.NamespaceSize, msgSize)
	msgs := make([]types.Message, count)
	for i, s := range shares {
		msgs[i] = types.Message{
			Data:        s[types.NamespaceSize:],
			NamespaceID: s[:types.NamespaceSize],
		}
	}
	return types.Messages{MessagesList: msgs}
}

func generateRandomContiguousShares(count int) types.Txs {
	// the size of a length delimited tx that takes up an entire share
	const adjustedTxSize = types.TxShareSize - 2
	txs := make(types.Txs, count)
	for i := 0; i < count; i++ {
		tx := make([]byte, adjustedTxSize)
		_, err := rand.Read(tx)
		if err != nil {
			panic(err)
		}
		txs[i] = types.Tx(tx)
	}
	return txs
}

func mockedIpfsAPI(t *testing.T) iface.CoreAPI {
	ipfsNode, err := coremock.NewMockNode()
	if err != nil {
		t.Error(err)
	}

	// issue a new API object
	ipfsAPI, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		t.Error(err)
	}

	return ipfsAPI
}
