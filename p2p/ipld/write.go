package ipld

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	format "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/types"
)

// PutBlock posts and pins erasured block data to IPFS using the provided
// ipld.NodeAdder. Note: the erasured data is currently recomputed
func PutBlock(ctx context.Context, adder format.NodeAdder, block *types.Block) error {
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

	// recompute the eds
	eds, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), rsmt2d.NewDefaultTree)
	if err != nil {
		return fmt.Errorf("failure to recompute the extended data square: %w", err)
	}

	// add namespaces to erasured shares and flatten the eds
	leaves := types.FlattenNamespacedEDS(namespacedShares, eds)

	// create nmt adder wrapping batch adder
	batchAdder := NewNmtNodeAdder(ctx, format.NewBatch(ctx, adder))

	// iterate through each set of col and row leaves
	for _, leafSet := range leaves {
		tree := nmt.New(sha256.New(), nmt.NodeVisitor(batchAdder.Visit))
		for _, share := range leafSet {
			err = tree.Push(share)
			if err != nil {
				return err
			}
		}

		// compute the root in order to collect the ipld.Nodes
		tree.Root()
	}

	// commit the batch to ipfs
	return batchAdder.Commit()
}
