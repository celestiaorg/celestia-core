package ipld

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
	"github.com/libp2p/go-libp2p-core/routing"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
	"github.com/lazyledger/lazyledger-core/types"
)

// PutBlock posts and pins erasured block data to IPFS using the provided
// ipld.NodeAdder. Note: the erasured data is currently recomputed
func PutBlock(ctx context.Context, adder ipld.NodeAdder, block *types.Block, croute routing.ContentRouting) error {
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
	// get row and col roots to be provided
	// this also triggers adding data to DAG
	prov := newProvider(ctx, croute)
	for _, root := range eds.RowRoots() {
		prov.Provide(plugin.MustCidFromNamespacedSha256(root))
	}
	for _, root := range eds.ColumnRoots() {
		prov.Provide(plugin.MustCidFromNamespacedSha256(root))
	}
	// wait until we provided all the roots
	select {
	case err = <-prov.Done():
		if err != nil {
			return err
		}
		// commit the batch to ipfs
		return batchAdder.Commit()
	case <-ctx.Done():
		return ctx.Err()
	}
}

var provideWorkers = 32

type provider struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan error
	jobs   chan cid.Cid
	total  int32
	croute routing.ContentRouting
}

func newProvider(ctx context.Context, croute routing.ContentRouting) *provider {
	ctx, cancel := context.WithCancel(ctx)
	p := &provider{
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan error, 1),
		jobs:   make(chan cid.Cid, provideWorkers),
		croute: croute,
	}
	for range make([]bool, provideWorkers) {
		go p.worker()
	}
	return p
}

func (p *provider) Provide(id cid.Cid) {
	atomic.AddInt32(&p.total, 1)
	select {
	case p.jobs <- id:
	case <-p.ctx.Done():
	}
}

func (p *provider) Done() <-chan error {
	return p.done
}

func (p *provider) worker() {
	for {
		select {
		case id := <-p.jobs:
			err := p.croute.Provide(p.ctx, id, true)
			if err != nil {
				select {
				case p.done <- err:
				case <-p.ctx.Done():
				}
			}

			if atomic.AddInt32(&p.total, -1) == 0 {
				p.cancel()
				close(p.done)
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}
