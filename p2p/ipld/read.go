package ipld

import (
	"context"
	"fmt"
	"math"
	"math/rand"

	"github.com/ipfs/go-cid"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
)

const baseErrorMsg = "failure to retrieve block data:"

var ErrEncounteredTooManyErrors = fmt.Errorf("%s %s", baseErrorMsg, "encountered too many errors")
var ErrTimeout = fmt.Errorf("%s %s", baseErrorMsg, "timeout")

// RetrieveBlockData asynchronously fetches block data using the minimum number
// of requests to IPFS. It fails if one of the random samples sampled is not available.
func RetrieveBlockData(
	ctx context.Context,
	dah *types.DataAvailabilityHeader,
	api coreiface.CoreAPI,
	codec rsmt2d.Codec,
) (types.Data, error) {
	edsWidth := len(dah.RowsRoots)
	sc := newshareCounter(ctx, uint32(edsWidth))

	// convert the row and col roots into Cids
	rowRoots := dah.RowsRoots.Bytes()
	colRoots := dah.ColumnRoots.Bytes()

	// sample 1/4 of the total extended square by sampling half of the leaves in
	// half of the rows
	for _, row := range uniqueRandNumbers(edsWidth/2, edsWidth) {
		for _, col := range uniqueRandNumbers(edsWidth/2, edsWidth) {
			rootCid, err := nodes.CidFromNamespacedSha256(rowRoots[row])
			if err != nil {
				return types.Data{}, err
			}

			go sc.retrieveShare(rootCid, true, row, col, api)
		}
	}

	// wait until enough data has been collected, too many errors encountered,
	// or the timeout is reached
	err := sc.wait()
	if err != nil {
		return types.Data{}, err
	}

	// flatten the square
	flattened := sc.flatten()

	tree := NewErasuredNamespacedMerkleTree(uint64(edsWidth) / 2)

	// repair the square
	eds, err := rsmt2d.RepairExtendedDataSquare(rowRoots, colRoots, flattened, codec, tree.Constructor)
	if err != nil {
		return types.Data{}, err
	}

	blockData, err := types.DataFromSquare(eds)
	if err != nil {
		return types.Data{}, err
	}

	return blockData, nil
}

// uniqueRandNumbers generates count unique random numbers with a max of max
func uniqueRandNumbers(count, max int) []uint32 {
	if count > max {
		panic(fmt.Sprintf("cannot create %d unique samples from a max of %d", count, max))
	}
	samples := make(map[uint32]struct{}, count)
	for i := 0; i < count; {
		// nolint:gosec // G404: Use of weak random number generator
		sample := uint32(rand.Intn(max))
		if _, has := samples[sample]; has {
			continue
		}
		samples[sample] = struct{}{}
		i++
	}
	out := make([]uint32, count)
	counter := 0
	for s := range samples {
		out[counter] = s
		counter++
	}
	return out
}

type index struct {
	row uint32
	col uint32
}

type indexedShare struct {
	data []byte
	index
}

// shareCounter is a thread safe tallying mechanism for share retrieval
type shareCounter struct {
	// all shares
	shares map[index][]byte
	// number of shares successfully collected
	counter uint32
	// the width of the extended data square
	edsWidth uint32
	// the minimum shares needed to repair the extended data square
	minSharesNeeded uint32

	shareChan chan indexedShare
	ctx       context.Context
	cancel    context.CancelFunc
	// any errors encountered when attempting to retrieve shares
	errc chan error
}

func newshareCounter(parentCtx context.Context, edsWidth uint32) *shareCounter {
	ctx, cancel := context.WithCancel(parentCtx)

	// calculate the min number of shares needed to repair the square
	minSharesNeeded := (edsWidth * edsWidth / 4)

	return &shareCounter{
		shares:          make(map[index][]byte),
		edsWidth:        edsWidth,
		minSharesNeeded: minSharesNeeded,
		shareChan:       make(chan indexedShare, 1),
		errc:            make(chan error, 1),
		ctx:             ctx,
		cancel:          cancel,
	}
}

// retrieveLeaf uses GetLeafData to fetch a single leaf and counts that leaf
func (sc *shareCounter) retrieveShare(
	rootCid cid.Cid,
	isRow bool,
	axisIdx uint32,
	idx uint32,
	api coreiface.CoreAPI,
) {
	data, err := GetLeafData(sc.ctx, rootCid, idx, sc.edsWidth, api)
	if err != nil {
		select {
		case <-sc.ctx.Done():
		case sc.errc <- err:
		}
	}

	if len(data) < types.ShareSize {
		return
	}

	// switch the row and col indexes if needed
	rowIdx := idx
	colIdx := axisIdx
	if isRow {
		rowIdx = axisIdx
		colIdx = idx
	}

	select {
	case <-sc.ctx.Done():
	default:
		sc.shareChan <- indexedShare{data: data[types.NamespaceSize:], index: index{row: rowIdx, col: colIdx}}
	}
}

// wait until enough data has been collected, the timeout has been reached, or
// too many errors are encountered
func (sc *shareCounter) wait() error {
	defer sc.cancel()

	for {
		select {
		case <-sc.ctx.Done():
			return ErrTimeout

		case share := <-sc.shareChan:
			_, has := sc.shares[share.index]
			// add iff it does not already exists
			if !has {
				sc.shares[share.index] = share.data
				sc.counter++
				// check finishing condition
				if sc.counter >= sc.minSharesNeeded {
					return nil
				}
			}

		case err := <-sc.errc:
			return fmt.Errorf("failure to retrieve data square: %w", err)
		}
	}
}

func (sc *shareCounter) flatten() [][]byte {
	flattended := make([][]byte, sc.edsWidth*sc.edsWidth)
	for index, data := range sc.shares {
		flattended[(index.row*sc.edsWidth)+index.col] = data
	}
	return flattended
}

// GetLeafData fetches and returns the data for leaf leafIndex of root rootCid.
// It stops and returns an error if the provided context is cancelled before
// finishing
func GetLeafData(
	ctx context.Context,
	rootCid cid.Cid,
	leafIndex uint32,
	totalLeafs uint32, // this corresponds to the extended square width
	api coreiface.CoreAPI,
) ([]byte, error) {
	// calculate the path to the leaf
	leafPath, err := leafPath(leafIndex, totalLeafs)
	if err != nil {
		return nil, err
	}

	// use the root cid and the leafPath to create an ipld path
	p := path.Join(path.IpldPath(rootCid), leafPath...)

	// resolve the path
	node, err := api.ResolveNode(ctx, p)
	if err != nil {
		return nil, err
	}

	// return the leaf, without the nmt-leaf-or-node byte
	return node.RawData()[1:], nil
}

func leafPath(index, total uint32) ([]string, error) {
	// ensure that the total is a power of two
	if !isPowerOf2(total) {
		return nil, fmt.Errorf("expected total to be a power of 2, got %d", total)
	}

	if total == 0 {
		return nil, nil
	}

	depth := int(math.Log2(float64(total)))
	cursor := index
	path := make([]string, depth)
	for i := depth - 1; i >= 0; i-- {
		if cursor%2 == 0 {
			path[i] = "0"
		} else {
			path[i] = "1"
		}
		cursor /= 2
	}

	return path, nil
}

// isPowerOf2 returns checks if a given number is a power of two
func isPowerOf2(v uint32) bool {
	return math.Ceil(math.Log2(float64(v))) == math.Floor(math.Log2(float64(v)))
}
