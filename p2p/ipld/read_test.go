package ipld

import (
	"bytes"
	"context"
	"crypto/sha256"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	format "github.com/ipfs/go-ipld-format"
	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
)

func TestGetLeafData(t *testing.T) {
	const leaves = 16

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dag := mdutils.Mock()

	// generate random shares for the nmt
	shares := RandNamespacedShares(t, leaves)

	// create a random tree
	root, err := getNmtRoot(ctx, dag, shares.Raw())
	require.NoError(t, err)

	for i, leaf := range shares {
		data, err := GetLeafData(ctx, root, uint32(i), uint32(len(shares)), dag)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(leaf.Share, data))
	}
}

func TestBlockRecovery(t *testing.T) {
	originalSquareWidth := 8
	shareCount := originalSquareWidth * originalSquareWidth
	extendedSquareWidth := 2 * originalSquareWidth
	extendedShareCount := extendedSquareWidth * extendedSquareWidth

	// generate test data
	quarterShares := RandNamespacedShares(t, shareCount)
	allShares := RandNamespacedShares(t, shareCount)

	testCases := []struct {
		name      string
		shares    NamespacedShares
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

			eds, err := rsmt2d.ComputeExtendedDataSquare(tc.shares.Raw(), rsmt2d.NewRSGF8Codec(), tree.Constructor)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dag := mdutils.Mock()

	type test struct {
		name       string
		squareSize int
	}
	tests := []test{
		{"1x1(min)", 1},
		{"32x32(med)", 32},
		{"128x128(max)", MaxSquareSize},
	}
	for _, tc := range tests {
		// TODO(Wondertan): remove this
		if tc.squareSize > 8 {
			continue
		}

		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			shares := RandNamespacedShares(t, tc.squareSize*tc.squareSize)
			in, err := PutData(ctx, shares, dag)
			require.NoError(t, err)

			// limit with deadline, specifically retrieval
			ctx, cancel := context.WithTimeout(ctx, time.Second*2)
			defer cancel()

			out, err := RetrieveData(ctx, MakeDataHeader(in), dag, rsmt2d.NewRSGF8Codec())
			require.NoError(t, err)
			assert.True(t, EqualEDS(in, out))
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
) (cid.Cid, error) {
	na := NewNmtNodeAdder(ctx, format.NewBatch(ctx, dag))
	tree := nmt.New(sha256.New, nmt.NamespaceIDSize(NamespaceSize), nmt.NodeVisitor(na.Visit))
	for _, leaf := range namespacedData {
		err := tree.Push(leaf)
		if err != nil {
			return cid.Undef, err
		}
	}

	// call Root early as it initiates saving
	root := tree.Root()
	if err := na.Commit(); err != nil {
		return cid.Undef, err
	}

	return plugin.CidFromNamespacedSha256(root.Bytes())
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
