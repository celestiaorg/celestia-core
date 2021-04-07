package ipld

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/ipfs/go-cid"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/rsmt2d"
)

// NOTE: I'm making the assumption that bandwidth is more expensive than
// compute, this means that when retrieving data I'm stopping as soon as enough
// data is retrieved to recompute

/////////////////////////////////////////
//	Retrieve Block Data
///////////////////////////////////////

const baseErrorMsg = "failure to retrieve block data:"

var ErrEncounteredTooManyErrors = fmt.Errorf("%s %s", baseErrorMsg, "encountered too many errors")
var ErrTimeout = fmt.Errorf("%s %s", baseErrorMsg, "timeout")

// RetrieveBlockData asynchronously fetches block data until the underlying extended
// data square can be restored and returned.
func RetrieveBlockData(
	ctx context.Context,
	dah *types.DataAvailabilityHeader,
	api coreiface.CoreAPI,
	codec rsmt2d.CodecType,
) (types.Data, error) {
	edsWidth := uint32(len(dah.ColumnRoots))
	originalSquareWidth := edsWidth / 2

	// keep track of leaves using thread safe counter
	sc := newshareCounter(edsWidth)

	// convert the row and col roots into Cids
	rowRoots := dah.RowsRoots.Bytes()
	rowCids, err := rootsToCids(rowRoots)
	if err != nil {
		return types.Data{}, err
	}

	colRoots := dah.ColumnRoots.Bytes()
	colCids, err := rootsToCids(colRoots)
	if err != nil {
		return types.Data{}, err
	}

	// add a local cancel to the parent ctx
	ctx, cancel := context.WithCancel(ctx)

	// async fetch each leaf
	for outer := uint32(0); outer < edsWidth; outer++ {
		for inner := uint32(0); inner < edsWidth; inner++ {
			// async fetch leaves.
			go sc.retrieveShare(ctx, colCids[inner], false, outer, inner, api)
			go sc.retrieveShare(ctx, rowCids[outer], true, outer, inner, api)
		}
	}

	// wait until enough data has been collected, too many errors encountered,
	// or the timeout is reached
	err = sc.wait(ctx)
	// there is now either enough data collected or it is impossible
	// therefore cancel any ongoing requests.
	cancel()
	if err != nil {
		return types.Data{}, err
	}

	// flatten the square
	flattened := sc.flatten()

	var eds *rsmt2d.ExtendedDataSquare
	// don't repair the square if all the data is there
	if sc.counter == sc.edsWidth*sc.edsWidth {
		e, err := rsmt2d.ImportExtendedDataSquare(flattened, codec, rsmt2d.NewDefaultTree)
		if err != nil {
			return types.Data{}, err
		}
		eds = e
	} else {
		tree := NewErasuredNamespacedMerkleTree(uint64(originalSquareWidth))
		// repair the square
		e, err := rsmt2d.RepairExtendedDataSquare(rowRoots, colRoots, flattened, codec, tree.Constructor)
		if err != nil {
			return types.Data{}, err
		}
		eds = e
	}

	blockData, err := types.DataFromSquare(eds)
	if err != nil {
		return types.Data{}, err
	}

	return blockData, nil
}

// rootsToCids converts roots to cids
func rootsToCids(roots [][]byte) ([]cid.Cid, error) {
	cids := make([]cid.Cid, len(roots))
	for i, root := range roots {
		rootCid, err := nodes.CidFromNamespacedSha256(root)
		if err != nil {
			return nil, err
		}
		cids[i] = rootCid
	}
	return cids, nil
}

// shareCounter is a thread safe tallying mechanism for leaf retrieval
type shareCounter struct {
	// all shares
	shares [][][]byte
	// number of shares successfully collected
	counter uint32
	// the width of the extended data square
	edsWidth uint32
	// the minimum shares needed to repair the extended data square
	minSharesNeeded uint32
	mut             sync.Mutex
	finished        chan error
	// any errors encountered when attempting to retrieve shares
	errors []error
}

func newshareCounter(edsWidth uint32) *shareCounter {
	shares := make([][][]byte, edsWidth)
	for i := uint32(0); i < edsWidth; i++ {
		shares[i] = make([][]byte, edsWidth)
	}
	originalSquareWidth := edsWidth / 2
	minSharesNeeded := (originalSquareWidth + 1) * (originalSquareWidth + 1)
	return &shareCounter{
		shares:          shares,
		edsWidth:        edsWidth,
		mut:             sync.Mutex{},
		minSharesNeeded: minSharesNeeded,
		finished:        make(chan error, 10), //TODO: evan fix this
	}
}

// retrieveLeaf uses GetLeafData to fetch a single leaf and counts that leaf
func (sc *shareCounter) retrieveShare(
	ctx context.Context,
	rootCid cid.Cid,
	isRow bool,
	rowIdx uint32,
	colIdx uint32,
	api coreiface.CoreAPI,
) {
	idx := colIdx
	if isRow {
		idx = rowIdx
	}

	data, err := GetLeafData(ctx, rootCid, idx, sc.edsWidth, api)
	if err != nil {
		sc.addError(err)
		return
	}

	sc.addShare(rowIdx, colIdx, data[types.NamespaceSize:])
}

// addShare adds shares data to the underlying square in a thread safe fashion
func (sc *shareCounter) addShare(rowIdx, colIdx uint32, data []byte) {
	// avoid panics by not doing anything if the share doesn't belong in the share
	// counter
	if colIdx > sc.edsWidth || rowIdx > sc.edsWidth {
		return
	}

	sc.mut.Lock()
	defer sc.mut.Unlock()

	// add the share if it does not exist
	if i := sc.shares[rowIdx][colIdx]; i == nil {
		sc.shares[rowIdx][colIdx] = data
		sc.counter++

		// signal to stop retrieving data from ipfs as there is now enough data
		// to complete the data square
		if sc.minSharesNeeded <= sc.counter {
			sc.finished <- nil
		}
	}
}

// addError simply adds an error to the underlying errors in a thread safe manor
func (sc *shareCounter) addError(err error) {
	sc.mut.Lock()
	defer sc.mut.Unlock()

	sc.errors = append(sc.errors, err)

	// signal to close processes as there have been so many errors that it is
	// impossible to recover the data square
	if uint32(len(sc.errors)) > ((sc.edsWidth * sc.edsWidth) - sc.minSharesNeeded) {
		sc.finished <- ErrEncounteredTooManyErrors
	}
}

// wait until enough data has been collected. check every interval
func (sc *shareCounter) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ErrTimeout
	case err := <-sc.finished:
		return err
	}
}

func (sc *shareCounter) flatten() [][]byte {
	sc.mut.Lock()
	defer sc.mut.Unlock()

	flattended := make([][]byte, (sc.edsWidth * sc.edsWidth))
	for i := uint32(0); i < sc.edsWidth; i++ {
		copy(flattended[i*sc.edsWidth:(i+1)*sc.edsWidth], sc.shares[i])
	}

	return flattended
}

/////////////////////////////////////////
//	Get Leaf Data
///////////////////////////////////////

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
	if total != nextPowerOf2(total) {
		return nil, errors.New("expected total to be a power of 2")
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

// nextPowerOf2 returns the next lowest power of 2 unless the input is a power
// of two, in which case it returns the input
func nextPowerOf2(v uint32) uint32 {
	if v == 1 {
		return 1
	}
	// keep track of the input
	i := v

	// find the next highest power using bit mashing
	v--
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v++

	// check if the input was the next highest power
	if i == v {
		return v
	}

	// return the next lowest power
	return v / 2
}
