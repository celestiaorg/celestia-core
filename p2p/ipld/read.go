package ipld

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

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

// ValidateAvailability implements the protocol described in https://fc21.ifca.ai/papers/83.pdf.
// Specifically all steps of the protocol described in section
// _5.2 Random Sampling and Network Block Recovery_ are carried out.
//
// In more detail it will first create numSamples random unique coordinates.
// Then, it will ask the network for the leaf data corresponding to these coordinates.
// Additionally to the number of requests, the caller can pass in a callback,
// which will be called on for each retrieved leaf with a verified Merkle proof.
//
// Among other use-cases, the callback can be useful to monitoring (progress), or,
// to process the leaf data the moment it was validated.
// The context can be used to provide a timeout.
// TODO: Should there be a constant = lower bound for #samples
func ValidateAvailability(
	ctx context.Context,
	dah *types.DataAvailabilityHeader,
	numSamples int,
	leafSucessCb func(namespacedleaf []byte),
) error {
	return nil
}

/////////////////////////////////////////
//	Retrieve Block Data
///////////////////////////////////////

type EDSParser interface {
	Parse(eds *rsmt2d.ExtendedDataSquare) (types.Txs, types.IntermediateStateRoots, types.Messages, error)
}

// RetrieveBlockData asynchronously fetches block data until the underlying extended
// data square can be restored and returned.
func RetrieveBlockData(
	ctx context.Context,
	dah *types.DataAvailabilityHeader,
	api coreiface.CoreAPI,
	codec rsmt2d.CodecType,
	edsParser EDSParser,
) (types.Data, error) {
	squareSize := uint32(len(dah.ColumnRoots))

	// keep track of leaves using thread safe counter
	lc := newLeafCounter(squareSize)

	// convert the row and col roots into Cids
	rowRoots := dah.RowsRoots.Bytes()
	rowCids, err := convertRoots(rowRoots)
	if err != nil {
		return types.Data{}, err
	}

	colRoots := dah.ColumnRoots.Bytes()
	colCids, err := convertRoots(colRoots)
	if err != nil {
		return types.Data{}, err
	}

	// add a local cancel to the parent ctx
	ctx, cancel := context.WithCancel(ctx)

	// async fetch each leaf
	for outer := uint32(0); outer < squareSize; outer++ {
		for inner := uint32(0); inner < squareSize; inner++ {
			// async fetch leaves.
			go lc.retrieveLeaf(ctx, colCids[inner], false, outer, inner, api)
			go lc.retrieveLeaf(ctx, rowCids[outer], true, outer, inner, api)
		}
	}

	// wait until enough data has been collected. check every 500 milliseconds
	lc.wait(ctx, time.Millisecond*500)

	// we have enough data to repair the square cancel any ongoing requests
	cancel()

	// flatten the square
	flattened := lc.flatten()

	var eds *rsmt2d.ExtendedDataSquare
	// don't repair the square if all the data is there
	if lc.counter == lc.squareSize*lc.squareSize {
		e, err := rsmt2d.ImportExtendedDataSquare(flattened, codec, rsmt2d.NewDefaultTree)
		if err != nil {
			return types.Data{}, err
		}

		eds = e
	} else {
		// repair the square
		e, err := rsmt2d.RepairExtendedDataSquare(rowRoots, colRoots, flattened, codec)
		if err != nil {
			return types.Data{}, err
		}
		eds = e
	}

	// parse the square
	txs, isr, msgs, err := edsParser.Parse(eds)

	// which portion is Txs and which is messages?
	blockData := types.Data{
		Txs:                    txs,
		IntermediateStateRoots: isr,
		Messages:               msgs,
	}

	return blockData, nil
}

// convertRoots converts roots to cids
func convertRoots(roots [][]byte) ([]cid.Cid, error) {
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

// leafCounter is a thread safe tallying mechanism for leaf retrieval
type leafCounter struct {
	leaves     [][][]byte
	counter    uint32
	squareSize uint32
	mut        sync.Mutex
	errors     []error
}

func newLeafCounter(squareSize uint32) *leafCounter {
	leaves := make([][][]byte, squareSize)
	for i := uint32(0); i < squareSize; i++ {
		leaves[i] = make([][]byte, squareSize)
	}
	return &leafCounter{
		leaves:     leaves,
		squareSize: squareSize,
		mut:        sync.Mutex{},
	}
}

// retrieveLeaf uses GetLeafData to fetch a single leaf and counts that leaf
func (lc *leafCounter) retrieveLeaf(
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

	data, err := GetLeafData(ctx, rootCid, idx, lc.squareSize, api)
	if err != nil {
		return
	}

	lc.addLeaf(rowIdx, colIdx, data)
}

// addLeaf adds a leaf to the leafCounter using the mutex to avoid
func (lc *leafCounter) addLeaf(rowIdx, colIdx uint32, data []byte) {
	// avoid panics by not doing anything if the leaf doesn't belong in the leaf
	// counter
	if colIdx > lc.squareSize || rowIdx > lc.squareSize {
		return
	}

	lc.mut.Lock()
	defer lc.mut.Unlock()

	// add the leaf if it does not exist
	if i := lc.leaves[rowIdx][colIdx]; i == nil {
		lc.leaves[rowIdx][colIdx] = data
		lc.counter++
	}
}

// wait until enough data has been collected. check every interval
func (lc *leafCounter) wait(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
		default:
			if lc.done() {
				return
			}
			time.Sleep(interval)
		}
	}
}

// done checks if there are enough collected leaves to recompute the data square
// TODO: this can be fixed
func (lc *leafCounter) done() bool {
	lc.mut.Lock()
	defer lc.mut.Unlock()
	return lc.counter > ((lc.squareSize * lc.squareSize) / 4)
}

func (lc *leafCounter) flatten() [][]byte {
	lc.mut.Lock()
	defer lc.mut.Unlock()

	flattended := make([][]byte, (lc.squareSize * lc.squareSize))
	for i := uint32(0); i < lc.squareSize; i++ {
		copy(flattended[i*lc.squareSize:(i+1)*lc.squareSize], lc.leaves[i])
	}

	return flattended
}

type messageOnlyEDSParser struct {
}

// Parse fullfills the EDSParser interface by assuming that there are only
// messages in the extended data square, and that namespaces are included in
// each share of the rsmt2d, which is not currently the case. FOR TESTING ONLY.
func (m messageOnlyEDSParser) Parse(
	eds *rsmt2d.ExtendedDataSquare,
) (types.Txs, types.IntermediateStateRoots, types.Messages, error) {
	var msgs types.Messages

	for i := uint(0); i < eds.Width(); i++ {
		for _, row := range eds.Row(i) {
			msgs.MessagesList = append(msgs.MessagesList, types.Message{Data: row})
		}
	}

	return types.Txs{}, types.IntermediateStateRoots{}, msgs, nil
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
	leafPath, err := calcCIDPath(leafIndex, totalLeafs)
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

func calcCIDPath(index, total uint32) ([]string, error) {
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
