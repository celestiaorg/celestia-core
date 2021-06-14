// TODO @renaynay: move this to another package
package ipld

import (
	"context"
	"fmt"
	"math"

	"github.com/ipfs/go-cid"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/nmt/namespace"
)

var (
	ErrNotFoundInRange = fmt.Errorf("namespaceID not found in range")
	ErrBelowRange      = fmt.Errorf("namespaceID is below minimum namespace ID in range")
	ErrExceedsRange    = fmt.Errorf("namespaceID exceeds maximum namespace ID in range")
)

// RetrieveShares retrieves share data for the given namespace ID using the given DataAvailabilityHeader.
func RetrieveShares(ctx context.Context, nID namespace.ID, dah *types.DataAvailabilityHeader, api coreiface.CoreAPI) ([][]byte, error) {
	// 1. Find the row root(s) that contains the namespace ID nID
	// loop over row roots and find the root in which the nID exists within the range of root min -> root max
	// return that row
	// if not exist, return empty 2DByteArr, error
	// 2. Traverse the corresponding tree(s) according to the
	//    above informally described algorithm and get the corresponding
	//    leaves (if any).
	// somehow fetch the tree of the given row root || somehow get leaves of the given row root
	// 3. Return all (raw) shares corresponding to the nID

	rowRoots, err := rowRootsFromNamespaceID(nID, dah)
	if err != nil {
		return [][]byte{}, err // TODO should we wrap error here?
	}
	return getSharesByNamespace(ctx, nID, dah, rowRoots, api)
}

// rowRootsFromNamespaceID finds the row root(s) that contain the given namespace ID.
func rowRootsFromNamespaceID(nID namespace.ID, dah *types.DataAvailabilityHeader) ([]int, error) {
	roots := make([]int, 0)
	for i, row := range dah.RowsRoots {
		// if nID exists within range of min -> max of row, return the row
		if !nID.Less(row.Min()) && nID.LessOrEqual(row.Max()) {
			roots = append(roots, i)
		}
	}
	if len(roots) > 0 {
		return roots, nil
	}
	// return min or max index depending on if nID is below the minimum namespace ID or exceeds the maximum
	// namespace ID of the given DataAvailabilityHeader
	if nID.Less(dah.RowsRoots[0].Min()) {
		return []int{0}, ErrBelowRange
	} else if !nID.LessOrEqual(dah.RowsRoots[len(dah.RowsRoots)-1].Max()) {
		max := len(dah.RowsRoots) - 1
		return []int{max}, ErrExceedsRange // TODO still need to figure out what kind of info to return as a part of the err
	}
	return roots, ErrNotFoundInRange
}

func getSharesByNamespace(
	ctx context.Context,
	nID namespace.ID,
	dah *types.DataAvailabilityHeader,
	rowIndices []int,
	api coreiface.CoreAPI) ([][]byte, error) {
	shares := make([][]byte, 0)

	for i, index := range rowIndices {
		isLastRow := i == len(rowIndices)-1
		// compute the root CID from the DAH
		rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[index].Bytes())
		if err != nil {
			return shares, err
		}

		startingIndex, err := findStartingIndex(ctx, nID, dah, rootCid, api)
		if err != nil {
			if isLastRow {
				if len(shares) > 0 {
					return shares, nil
				}
				return shares, err
			}
			continue
		}
		for {
			leaf, err := GetLeafData(ctx, rootCid, uint32(startingIndex), uint32(len(dah.RowsRoots)), api)
			if err != nil {
				return shares, err
			}
			if nID.Equal(leaf[:8]) {
				shares = append(shares, leaf)
				startingIndex++
			} else {
				break
			}
		}
	}
	return shares, nil
}

// findStartingIndex returns the path to the first leaf that contains the given nID
// in the tree corresponding to the given root CID.
// TODO wrap errors for this function
// TODO maybe rename to pathToStartingIndex
func findStartingIndex(
	ctx context.Context,
	nID namespace.ID,
	dah *types.DataAvailabilityHeader,
	rootCid cid.Cid,
	api coreiface.CoreAPI) (int, error) {

	left := "0"
	right := "1"

	treeDepth := int(math.Log2(float64(len(dah.RowsRoots))))
	currentPath := make([]string, 0)

	for {
		leafLevel := len(currentPath)+1 == treeDepth

		leftNode, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), append(currentPath, left)...))
		if err != nil {
			return 0, err
		}
		leftIntvlDigest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, leftNode.Cid().Hash()[4:])
		if err != nil {
			return 0, err
		}

		rightNode, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), append(currentPath, right)...))
		if err != nil {
			return 0, err
		}
		rightIntvlDigest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, rightNode.Cid().Hash()[4:])
		if err != nil {
			return 0, err
		}

		if leafLevel {
			if intervalContains(nID, leftIntvlDigest) {
				// return index from path to left leaf
				return startIndexFromPath(append(currentPath, left)), nil
			} else if intervalContains(nID, rightIntvlDigest) {
				// return path to right leaf
				return startIndexFromPath(append(currentPath, right)), nil
			} else {
				return 0, fmt.Errorf("namespace ID %x within range of namespace IDs in tree, "+
					"but does not exist", nID.String())
			}
		}

		if intervalContains(nID, leftIntvlDigest) {
			currentPath = append(currentPath, left)
		} else {
			currentPath = append(currentPath, right)
		}
	}
}

func startIndexFromPath(path []string) int {
	start := 0
	indices := make([]int, 0)
	totalLeaves := math.Pow(2, float64(len(path)))
	for i := 0; i < int(totalLeaves); i++ {
		indices = append(indices, i)
	}

	for _, pos := range path {
		if pos == "0" {
			if len(indices) == 2 {
				return indices[0]
			}
			indices = indices[0 : len(indices)/2]
		} else {
			if len(indices) == 2 {
				return indices[1]
			}
			indices = indices[len(indices)/2:]
		}
	}
	return start
}

func intervalContains(nID namespace.ID, intvlDigest namespace.IntervalDigest) bool {
	return !nID.Less(intvlDigest.Min()) && nID.LessOrEqual(intvlDigest.Max())
}

func walk(
	ctx context.Context,
	nID namespace.ID,
	dah *types.DataAvailabilityHeader,
	rootCid cid.Cid,
	api coreiface.CoreAPI) ([]byte, error) {
	var (
		data         []byte
		currentIndex = uint32(len(dah.RowsRoots) / 2) // start in the middle // TODO
	)
	for {
		lPath, err := leafPath(currentIndex, uint32(len(dah.RowsRoots)))
		if err != nil {
			return data, err
		}

		node, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), lPath...))
		if err != nil {
			return data, err
		}

		// convert node into interval digest so we can get the min/max ID for that leaf
		nodeHash := node.Cid().Hash()[4:] // IPFS prepends 4 bytes to the data that it stores, so ignore first 4 bytes
		digest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, nodeHash)
		if err != nil {
			return data, err
		}

		if !nID.Less(digest.Min()) && nID.LessOrEqual(digest.Max()) {
			return node.RawData()[1:], nil
		}
		if nID.Less(digest.Min()) {
			// go left
			currentIndex--
		}
		if !nID.LessOrEqual(digest.Max()) {
			// go right
			currentIndex++
		}
	}
}
