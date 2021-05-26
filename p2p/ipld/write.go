package ipld

import (
	"context"
	"errors"
	"fmt"
	"math"

	ipld "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
	"github.com/lazyledger/lazyledger-core/types"
)

// PutBlock posts and pins erasured block data to IPFS using the provided
// ipld.NodeAdder. Note: the erasured data is currently recomputed
func PutBlock(ctx context.Context, adder ipld.NodeAdder, block *types.Block) error {
	if adder == nil {
		return errors.New("no ipfs node adder provided")
	}

	// recompute the shares
	namespacedShares, _ := block.Data.ComputeShares()
	shares := namespacedShares.RawShares()

	// don't do anything if there is no data to put on IPFS
	if len(shares) == 0 {
		return nil
	}

	// create nmt adder wrapping batch adder
	batchAdder := NewNmtNodeAdder(ctx, ipld.NewBatch(ctx, adder))

	// create the nmt wrapper to generate row and col commitments
	squareSize := uint32(math.Sqrt(float64(len(shares))))
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(squareSize), nmt.NodeVisitor(batchAdder.Visit))

	// recompute the eds
	eds, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return fmt.Errorf("failure to recompute the extended data square: %w", err)
	}

	// thanks to the batchAdder.Visit func we added to the nmt wrapper,
	// generating the roots will start adding the data to IPFS
	eds.RowRoots()
	eds.ColumnRoots()

	// commit the batch to ipfs
	return batchAdder.Commit()
}
