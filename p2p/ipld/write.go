package ipld

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
	"github.com/libp2p/go-libp2p-core/routing"
	kbucket "github.com/libp2p/go-libp2p-kbucket"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/libs/log"
	"github.com/lazyledger/lazyledger-core/libs/sync"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
)

// TODO(Wondertan) Improve API

// PutData posts erasured block data to IPFS using the provided ipld.NodeAdder.
func PutData(ctx context.Context, shares NamespacedShares, adder ipld.NodeAdder) (*rsmt2d.ExtendedDataSquare, error) {
	if len(shares) == 0 {
		return nil, fmt.Errorf("empty data") // empty block is not an empty Data
	}
	// create nmt adder wrapping batch adder
	batchAdder := NewNmtNodeAdder(ctx, ipld.NewBatch(ctx, adder))
	// create the nmt wrapper to generate row and col commitments
	squareSize := uint32(math.Sqrt(float64(len(shares))))
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(squareSize), nmt.NodeVisitor(batchAdder.Visit))
	// recompute the eds
	eds, err := rsmt2d.ComputeExtendedDataSquare(shares.Raw(), rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return nil, fmt.Errorf("failure to recompute the extended data square: %w", err)
	}
	// compute roots
	eds.ColumnRoots()
	// commit the batch to ipfs
	return eds, batchAdder.Commit()
}

func ProvideData(
	ctx context.Context,
	dah *DataAvailabilityHeader,
	croute routing.ContentRouting,
	logger log.Logger) error {
	// get row and col roots to be provided
	// this also triggers adding data to DAG
	prov := newProvider(ctx, croute, int32(len(dah.RowsRoots)+len(dah.ColumnRoots)), logger)
	for _, root := range dah.RowsRoots {
		prov.Provide(plugin.MustCidFromNamespacedSha256(root.Bytes()))
	}
	for _, root := range dah.ColumnRoots {
		prov.Provide(plugin.MustCidFromNamespacedSha256(root.Bytes()))
	}
	// wait until we provided all the roots if requested
	return prov.Done()
}

var provideWorkers = 32

type provider struct {
	ctx  context.Context
	done chan struct{}

	err   error
	errLk sync.RWMutex

	jobs  chan cid.Cid
	total int32

	croute    routing.ContentRouting
	log       log.Logger
	startTime time.Time
}

func newProvider(ctx context.Context, croute routing.ContentRouting, toProvide int32, logger log.Logger) *provider {
	p := &provider{
		ctx:    ctx,
		done:   make(chan struct{}),
		jobs:   make(chan cid.Cid, provideWorkers),
		total:  toProvide,
		croute: croute,
		log:    logger,
	}
	for range make([]bool, provideWorkers) {
		go p.worker()
	}
	logger.Info("Started Providing to DHT")
	p.startTime = time.Now()
	return p
}

func (p *provider) Provide(id cid.Cid) {
	select {
	case p.jobs <- id:
	case <-p.ctx.Done():
	}
}

func (p *provider) Done() error {
	<-p.done
	return p.Err()
}

func (p *provider) Err() error {
	p.errLk.RLock()
	defer p.errLk.RUnlock()
	if p.err != nil {
		return p.err
	}
	return p.ctx.Err()
}

func (p *provider) worker() {
	for {
		select {
		case id := <-p.jobs:
			err := p.croute.Provide(p.ctx, id, true)
			// Omit ErrLookupFailure to decrease test log spamming as
			// this simply indicates we haven't connected to other DHT nodes yet.
			if err != nil && err != kbucket.ErrLookupFailure {
				if p.Err() == nil {
					p.errLk.Lock()
					p.err = err
					p.errLk.Unlock()
				}

				p.log.Error("Failed to provide to DHT", "err", err.Error())
			}

			p.provided()
		case <-p.ctx.Done():
			for {
				select {
				case <-p.jobs: // drain chan
					p.provided() // ensure done is closed
				default:
					return
				}
			}
		case <-p.done:
			return
		}
	}
}

func (p *provider) provided() {
	if atomic.AddInt32(&p.total, -1) == 0 {
		p.log.Info("Finished providing to DHT", "took", time.Since(p.startTime).String())
		close(p.done)
	}
}
