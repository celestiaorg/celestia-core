package ipld

import (
	"context"
	"fmt"
	"math"
	stdsync "sync"

	ipld "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
	"github.com/libp2p/go-libp2p-core/routing"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/libs/log"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
	"github.com/lazyledger/lazyledger-core/types"
)

// PutBlock posts and pins erasured block data to IPFS using the provided
// ipld.NodeAdder. Note: the erasured data is currently recomputed
func PutBlock(
	ctx context.Context,
	adder ipld.NodeAdder,
	block *types.Block,
	croute routing.ContentRouting,
	logger log.Logger,
) error {
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

	// setup provide workers
	const workers = 32
	var wg stdsync.WaitGroup
	jobs := make(chan []byte, workers)
	errc := make(chan error, 1)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel() // ensure that this cancel is called

	// start feeding the workers jobs
	go feedJobs(workerCtx, jobs, eds.RowRoots(), eds.ColumnRoots())

	// start each worker
	for i := 0; i < workers; i++ {
		wg.Add(1)
		worker := provideWorker{croute, logger, workerCancel}
		go worker.provide(workerCtx, &wg, jobs, errc)
	}

	// for for all jobs to finish, an error to occur, or a if a timeout is reached
	wg.Wait()
	err = collectErrors(errc)
	if err != nil {
		return err
	}

	// commit the batch to ipfs
	return batchAdder.Commit()
}

func collectErrors(errc chan error) error {
	close(errc)
	err := <-errc
	return err
}

type provideWorker struct {
	croute routing.ContentRouting
	logger log.Logger
	cancel context.CancelFunc
}

func feedJobs(ctx context.Context, jobs chan<- []byte, roots ...[][]byte) {
	defer close(jobs)
	for _, rootSet := range roots {
		for _, root := range rootSet {
			select {
			case <-ctx.Done():
				return
			default:
				jobs <- root
			}
		}
	}
}

func (w *provideWorker) provide(ctx context.Context, wg *stdsync.WaitGroup, jobs <-chan []byte, errc chan<- error) {
	defer wg.Done()
	for job := range jobs {
		rootCid := plugin.MustCidFromNamespacedSha256(job)
		err := w.croute.Provide(ctx, rootCid, true)
		if err != nil {
			w.cancel()
			errc <- err
		}
	}
}
