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
		for _, d := range generateErasuredData(t, tc.squareSize) {
			// push will panic if there's an error
			tree.Push(d)
		}
	}
}

func TestRootErasuredNamespacedMerkleTree(t *testing.T) {
	// check that the root is different from a standard nmt tree this should be
	// the case, because the ErasuredNamespacedMerkleTree should add namespaces
	// to the second half of the tree
	size := 16
	data := generateRandNamespacedRawData(size, types.NamespaceSize, AdjustedMessageSize)
	n := NewErasuredNamespacedMerkleTree(uint64(16))
	tree := n.Constructor()
	nmtTree := nmt.New(sha256.New())

	for _, d := range data {
		tree.Push(d)
		err := nmtTree.Push(d[:types.NamespaceSize], d[types.NamespaceSize:])
		if err != nil {
			t.Error(err)
		}
	}

	assert.NotEqual(t, nmtTree.Root().Bytes(), tree.Root())
}

func TestErasureNamespacedMerkleTreePanics(t *testing.T) {
	testCases := []struct {
		name  string
		pFucn assert.PanicTestFunc
	}{
		{
			"push over square size",
			assert.PanicTestFunc(
				func() {
					data := generateErasuredData(t, 16)
					n := NewErasuredNamespacedMerkleTree(uint64(15))
					tree := n.Constructor()
					for _, d := range data {
						tree.Push(d)
					}
				}),
		},
		{
			"push in incorrect lexigraphic order",
			assert.PanicTestFunc(
				func() {
					data := generateErasuredData(t, 16)
					n := NewErasuredNamespacedMerkleTree(uint64(16))
					tree := n.Constructor()
					for i := len(data) - 1; i > 0; i-- {
						tree.Push(data[i])
					}
				},
			),
		},
		{
			"Prove non existent leaf",
			assert.PanicTestFunc(
				func() {
					size := 16
					data := generateErasuredData(t, size)
					n := NewErasuredNamespacedMerkleTree(uint64(size))
					tree := n.Constructor()
					for _, d := range data {
						tree.Push(d)
					}
					tree.Prove(size + 100)
				},
			),
		},
	}
	for _, tc := range testCases {
		assert.Panics(t, tc.pFucn)

	}
}

// generateErasuredData produces a slice that is twice as long as it erasures
// the data
func generateErasuredData(t *testing.T, numLeaves int) [][]byte {
	raw := generateRandNamespacedRawData(
		numLeaves,
		types.NamespaceSize,
		AdjustedMessageSize,
	)
	erasuredData, err := rsmt2d.Encode(raw, rsmt2d.RSGF8)
	if err != nil {
		t.Error(err)
	}
	return append(raw, erasuredData...)
}
