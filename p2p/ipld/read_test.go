package ipld

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	format "github.com/ipfs/go-ipld-format"
	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/ipfs"
	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/libs/log"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/lazyledger-core/types/consts"
)

func TestGetLeafData(t *testing.T) {
	const leaves = 16

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// generate random data for the nmt
	data := generateRandNamespacedRawData(leaves, consts.NamespaceSize, consts.ShareSize)

	// create a random tree
	dag := mdutils.Mock()
	root, err := getNmtRoot(ctx, dag, data)
	require.NoError(t, err)

	// compute the root and create a cid for the root hash
	rootCid, err := plugin.CidFromNamespacedSha256(root.Bytes())
	require.NoError(t, err)

	for i, leaf := range data {
		data, err := GetLeafData(ctx, rootCid, uint32(i), uint32(len(data)), dag)
		assert.NoError(t, err)
		assert.Equal(t, leaf, data)
	}
}

func TestBlockRecovery(t *testing.T) {
	originalSquareWidth := 8
	shareCount := originalSquareWidth * originalSquareWidth
	extendedSquareWidth := 2 * originalSquareWidth
	extendedShareCount := extendedSquareWidth * extendedSquareWidth

	// generate test data
	quarterShares := generateRandNamespacedRawData(shareCount, consts.NamespaceSize, consts.MsgShareSize)
	allShares := generateRandNamespacedRawData(shareCount, consts.NamespaceSize, consts.MsgShareSize)

	testCases := []struct {
		name      string
		shares    [][]byte
		expectErr bool
		errString string
		d         int // number of shares to delete
	}{
		{"missing 1/2 shares", quarterShares, false, "", extendedShareCount / 2},
		{"missing 1/4 shares", quarterShares, false, "", extendedShareCount / 4},
		{"max missing data", quarterShares, false, "", (originalSquareWidth + 1) * (originalSquareWidth + 1)},
		{"missing all but one shares", allShares, true, "failed to solve data square", extendedShareCount - 1},
	}
	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			squareSize := uint64(math.Sqrt(float64(len(tc.shares))))

			// create trees for creating roots
			tree := wrapper.NewErasuredNamespacedMerkleTree(squareSize)
			recoverTree := wrapper.NewErasuredNamespacedMerkleTree(squareSize)

			eds, err := rsmt2d.ComputeExtendedDataSquare(tc.shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
			require.NoError(t, err)

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
	logger := log.TestingLogger()
	type test struct {
		name       string
		squareSize int
		expectErr  bool
		errStr     string
	}
	tests := []test{
		{"Empty block", 1, false, ""},
		{"4 KB block", 4, false, ""},
		{"16 KB block", 8, false, ""},
		{"16 KB block timeout expected", 8, true, "not found"},
		{"max square size", consts.MaxSquareSize, false, ""},
	}

	for _, tc := range tests {
		// TODO(Wondertan): remove this
		if tc.squareSize > 8 {
			continue
		}

		tc := tc
		t.Run(fmt.Sprintf("%s size %d", tc.name, tc.squareSize), func(t *testing.T) {
			ctx := context.Background()
			dag := mdutils.Mock()
			croute := ipfs.MockRouting()

			blockData := generateRandomBlockData(tc.squareSize*tc.squareSize, consts.MsgShareSize-2)
			block := &types.Block{
				Data:       blockData,
				LastCommit: &types.Commit{},
			}

			// if an error is exected, don't put the block
			if !tc.expectErr {
				err := PutBlock(ctx, dag, block, croute, logger)
				require.NoError(t, err)
			}

			shareData, _ := blockData.ComputeShares()
			rawData := shareData.RawShares()

			tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(tc.squareSize))
			eds, err := rsmt2d.ComputeExtendedDataSquare(rawData, rsmt2d.NewRSGF8Codec(), tree.Constructor)
			require.NoError(t, err)

			rawRowRoots := eds.RowRoots()
			rawColRoots := eds.ColumnRoots()
			rowRoots := rootsToDigests(rawRowRoots)
			colRoots := rootsToDigests(rawColRoots)

			// limit with deadline retrieval specifically
			ctx, cancel := context.WithTimeout(ctx, time.Second*2)
			defer cancel()

			rblockData, err := RetrieveBlockData(
				ctx,
				&types.DataAvailabilityHeader{
					RowsRoots:   rowRoots,
					ColumnRoots: colRoots,
				},
				dag,
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
	dag format.NodeAdder,
	namespacedData [][]byte,
) (namespace.IntervalDigest, error) {
	na := NewNmtNodeAdder(ctx, format.NewBatch(ctx, dag))
	tree := nmt.New(sha256.New, nmt.NamespaceIDSize(consts.NamespaceSize), nmt.NodeVisitor(na.Visit))
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
		idigest, err := namespace.IntervalDigestFromBytes(consts.NamespaceSize, root)
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
	shares := generateRandNamespacedRawData(count, consts.NamespaceSize, msgSize)
	msgs := make([]types.Message, count)
	for i, s := range shares {
		msgs[i] = types.Message{
			Data:        s[consts.NamespaceSize:],
			NamespaceID: s[:consts.NamespaceSize],
		}
	}
	return types.Messages{MessagesList: msgs}
}

func generateRandomContiguousShares(count int) types.Txs {
	// the size of a length delimited tx that takes up an entire share
	const adjustedTxSize = consts.TxShareSize - 2
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
