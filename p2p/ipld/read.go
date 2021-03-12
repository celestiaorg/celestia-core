package ipld

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"

	"github.com/ipfs/go-cid"
	format "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	mh "github.com/multiformats/go-multihash"
)

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

// RetrieveBlockData can be used to recover the block Data.
// It will carry out a similar protocol as described for ValidateAvailability.
// The key difference is that it will sample enough chunks until it can recover the
// full extended data square, including original data (e.g. by using rsmt2d.RepairExtendedDataSquare).
func RetrieveBlockData(ctx context.Context, dah *types.DataAvailabilityHeader, nodeGetter format.NodeGetter) (types.Data, error) {
	return types.Data{}, nil
}

// GetLeafData takes in a Namespaced Merkle tree root transformed into a Cid
// and the leaf index to retrieve.
// Callers also need to pass in the total number of leaves of that tree.
// Internally, this will be translated to a IPLD path and corresponds to
// an ipfs dag get request, e.g. namespacedCID/0/1/0/0/1.
// The retrieved data should be pinned by default.
func GetLeafData(
	ctx context.Context,
	rootCid cid.Cid,
	leafIndex uint32,
	totalLeafs uint32, // this corresponds to the extended square width
	nodeGetter format.NodeGetter,
) ([]byte, error) {

	labeledCid, err := addCidPath(rootCid, leafIndex, totalLeafs)
	if err != nil {
		return nil, err
	}

	node, err := nodeGetter.Get(ctx, labeledCid)
	if err != nil {
		return nil, err
	}

	return node.RawData(), nil
}

// addCidPath adds the link path to the end of the root cid "0/1/1/1/0"
func addCidPath(id cid.Cid, index, total uint32) (cid.Cid, error) {
	// use the node getter to
	treePath, err := calcCIDPath(index, total)
	if err != nil {
		return cid.Cid{}, err
	}

	raw := bytes.Join([][]byte{id.Bytes(), []byte(treePath)}, []byte{})

	labeled := cid.NewCidV1(nodes.Nmt, mh.Multihash(raw))

	return labeled, nil
}

func calcCIDPath(index, total uint32) (string, error) {
	// ensure that the total is a power of two
	if total != nextPowerOf2(total) {
		return "", errors.New("expected total to be a power of 2")
	}

	if total == 0 {
		return "", nil
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

	return strings.Join(path, "/"), nil
}

func prependString(j []string, i string) []string {
	j = append(j, "")
	copy(j[1:], j)
	j[0] = i
	return j
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
