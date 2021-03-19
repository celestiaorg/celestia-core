package ipld

import (
	"context"
	"errors"
	"math"

	"github.com/ipfs/go-cid"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"
)

// /////////////////////////////////////
//	Get Leaf Data
// /////////////////////////////////////

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
