package ipld

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"github.com/lazyledger/nmt"
	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/types"
)

// emptyNamepsaceID occurs when a share is empty and indicates that
var emptyNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 0}

// Fulfills the rsmt2d.Tree interface and rsmt2d.TreeConstructorFn function
var _ rsmt2d.TreeConstructorFn = ErasuredNamespacedMerkleTree{}.Constructor
var _ rsmt2d.Tree = &ErasuredNamespacedMerkleTree{}

// ErasuredNamespacedMerkleTree wraps NamespaceMerkleTree to conform to the
// rsmt2d.Tree interface while also providing the correct namespaces to the
// underlying NamespaceMerkleTree. It does this by adding the already included
// namespace to the first half of the tree, and then uses the parity namespace
// ID for each share pushed to the second half of the tree. This allows for the
// namespaces to be included in the erasure data, while also keeping the nmt
// library sufficiently general
type ErasuredNamespacedMerkleTree struct {
	squareSize uint64 // note: this refers to the width of the original square before erasure-coded
	options    []nmt.Option
	tree       *nmt.NamespacedMerkleTree
}

// NewErasuredNamespacedMerkleTree issues a new ErasuredNamespacedMerkleTree. squareSize must be greater than zero
func NewErasuredNamespacedMerkleTree(squareSize uint64, setters ...nmt.Option) ErasuredNamespacedMerkleTree {
	if squareSize == 0 {
		panic("cannot create a ErasuredNamespacedMerkleTree of squareSize == 0")
	}
	tree := nmt.New(sha256.New(), setters...)
	return ErasuredNamespacedMerkleTree{squareSize: squareSize, options: setters, tree: tree}
}

// Constructor acts as the rsmt2d.TreeConstructorFn for
// ErasuredNamespacedMerkleTree
func (w ErasuredNamespacedMerkleTree) Constructor() rsmt2d.Tree {
	newTree := NewErasuredNamespacedMerkleTree(w.squareSize, w.options...)
	return &newTree
}

// Push adds the provided data to the underlying NamespaceMerkleTree, and
// automatically uses the first DefaultNamespaceIDLen number of bytes as the
// namespace unless the data pushed to the second half of the tree. Fulfills the
// rsmt.Tree interface. NOTE: panics if an error is encountered while pushing or
// if the tree size is exceeded.
func (w *ErasuredNamespacedMerkleTree) Push(data []byte, idx rsmt2d.SquareIndex) {
	// determine the namespace based on where in the tree we're pushing
	nsID := make(namespace.ID, types.NamespaceSize)

	if idx.Axis+1 > 2*uint(w.squareSize) || idx.Cell+1 > 2*uint(w.squareSize) {
		panic(fmt.Sprintf("pushed past predetermined square size: boundary at %d index at %+v", 2*w.squareSize, idx))
	}

	// use the parity namespace if the cell is not in Q0 of the extended
	// datasquare if the cell is empty it means we got an empty block so we need
	// to use TailPaddingNamespaceID
	if idx.Axis+1 > uint(w.squareSize) || idx.Cell+1 > uint(w.squareSize) {
		copy(nsID, types.ParitySharesNamespaceID)
	} else {
		// empty shares use the TailPaddingNamespaceID if the data is empty, so
		// here we check if the share is empty (namepsace == [0,0,0,0,0,0,0,0])
		if bytes.Equal(data[:types.NamespaceSize], emptyNamespaceID) {
			copy(nsID, types.TailPaddingNamespaceID)
		} else {
			copy(nsID, data[:types.NamespaceSize])
		}
	}
	nidAndData := append(append(make([]byte, 0, types.NamespaceSize+len(data)), nsID...), data...)
	// push to the underlying tree
	err := w.tree.Push(nidAndData)
	// panic on error
	if err != nil {
		panic(err)
	}
}

// Prove fulfills the rsmt.Tree interface by generating and returning a single
// leaf proof using the underlying NamespacedMerkleTree. NOTE: panics if the
// underlying NamespaceMerkleTree errors.
func (w *ErasuredNamespacedMerkleTree) Prove(
	idx int,
) (merkleRoot []byte, proofSet [][]byte, proofIndex uint64, numLeaves uint64) {
	proof, err := w.tree.Prove(idx)
	if err != nil {
		panic(err)
	}
	nodes := proof.Nodes()
	return w.Root(), nodes, uint64(proof.Start()), uint64(len(nodes))
}

// Root fulfills the rsmt.Tree interface by generating and returning the
// underlying NamespaceMerkleTree Root.
func (w *ErasuredNamespacedMerkleTree) Root() []byte {
	return w.tree.Root().Bytes()
}
