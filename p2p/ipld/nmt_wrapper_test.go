package ipld

import (
	"crypto/sha256"
	"testing"

	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
	"github.com/stretchr/testify/assert"
)

func TestPushErasuredNamespacedMerkleTree(t *testing.T) {
	testCases := []struct {
		name       string
		squareSize int
	}{
		{"extendedSquareSize = 16", 8},
		{"extendedSquareSize = 256", 128},
	}
	for _, tc := range testCases {
		tc := tc
		n := NewErasuredNamespacedMerkleTree(uint64(tc.squareSize))
		tree := n.Constructor()

		// push test data to the tree
		for i, d := range generateErasuredData(t, tc.squareSize, rsmt2d.NewRSGF8Codec()) {
			// push will panic if there's an error
			tree.Push(d, rsmt2d.SquareIndex{Axis: uint(0), Cell: uint(i)})
		}
	}
}

func TestRootErasuredNamespacedMerkleTree(t *testing.T) {
	// check that the root is different from a standard nmt tree this should be
	// the case, because the ErasuredNamespacedMerkleTree should add namespaces
	// to the second half of the tree
	size := 8
	data := generateRandNamespacedRawData(size, types.NamespaceSize, types.MsgShareSize)
	n := NewErasuredNamespacedMerkleTree(uint64(size))
	tree := n.Constructor()
	nmtTree := nmt.New(sha256.New())

	for i, d := range data {
		tree.Push(d, rsmt2d.SquareIndex{Axis: uint(0), Cell: uint(i)})
		err := nmtTree.Push(d)
		if err != nil {
			t.Error(err)
		}
	}

	assert.NotEqual(t, nmtTree.Root().Bytes(), tree.Root())
}

func TestErasureNamespacedMerkleTreePanics(t *testing.T) {
	testCases := []struct {
		name  string
		pFunc assert.PanicTestFunc
	}{
		{
			"push over square size",
			assert.PanicTestFunc(
				func() {
					data := generateErasuredData(t, 16, rsmt2d.NewRSGF8Codec())
					n := NewErasuredNamespacedMerkleTree(uint64(15))
					tree := n.Constructor()
					for i, d := range data {
						tree.Push(d, rsmt2d.SquareIndex{Axis: uint(0), Cell: uint(i)})
					}
				}),
		},
		{
			"push in incorrect lexigraphic order",
			assert.PanicTestFunc(
				func() {
					data := generateErasuredData(t, 16, rsmt2d.NewRSGF8Codec())
					n := NewErasuredNamespacedMerkleTree(uint64(16))
					tree := n.Constructor()
					for i := len(data) - 1; i > 0; i-- {
						tree.Push(data[i], rsmt2d.SquareIndex{Axis: uint(0), Cell: uint(i)})
					}
				},
			),
		},
		{
			"Prove non existent leaf",
			assert.PanicTestFunc(
				func() {
					size := 16
					data := generateErasuredData(t, size, rsmt2d.NewRSGF8Codec())
					n := NewErasuredNamespacedMerkleTree(uint64(size))
					tree := n.Constructor()
					for i, d := range data {
						tree.Push(d, rsmt2d.SquareIndex{Axis: uint(0), Cell: uint(i)})
					}
					tree.Prove(size + 100)
				},
			),
		},
	}
	for _, tc := range testCases {
		tc := tc
		assert.Panics(t, tc.pFunc, tc.name)

	}
}

func TestExtendedDataSquare(t *testing.T) {
	squareSize := 4
	// data for a 4X4 square
	raw := generateRandNamespacedRawData(
		squareSize*squareSize,
		types.NamespaceSize,
		types.MsgShareSize,
	)

	tree := NewErasuredNamespacedMerkleTree(uint64(squareSize))

	_, err := rsmt2d.ComputeExtendedDataSquare(raw, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	assert.NoError(t, err)
}

// generateErasuredData produces a slice that is twice as long as it erasures
// the data
func generateErasuredData(t *testing.T, numLeaves int, codec rsmt2d.Codec) [][]byte {
	raw := generateRandNamespacedRawData(
		numLeaves,
		types.NamespaceSize,
		types.MsgShareSize,
	)
	erasuredData, err := codec.Encode(raw)
	if err != nil {
		t.Error(err)
	}
	return append(raw, erasuredData...)
}
