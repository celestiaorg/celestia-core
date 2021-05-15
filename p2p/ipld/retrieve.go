// TODO @renaynay: move this to another package
package ipld

import (
	"context"
	"fmt"
	"github.com/ipfs/go-cid"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/nmt/namespace"
	"math"
)

var (
	ErrOutOfRange = fmt.Errorf("namespaceID not found in range")
)

func RetrieveShares(ctx context.Context, nID namespace.ID, dah *types.DataAvailabilityHeader, api coreiface.CoreAPI) ([][]byte, error) {
	// 1. Find the row root(s) that contains the namespace ID nID
		// loop over row roots and find the root in which the nID exists within the range of root min -> root max
		// return that row
		// if not exist, return empty 2DByteArr, error
	// 2. Traverse the corresponding tree(s) according to the
	//    above informally described algorithm and get the corresponding
	//    leaves (if any) // TODO NOTE: "corresponding leaves" = leaves that correspond to the nID of the tree with the correct row root. Not all leaves correspond to the nID, only find the relevant ones.
		// somehow fetch the tree of the given row root || somehow get leaves of the given row root
	// 3. Return all (raw) shares corresponding to the nID
		// TODO `GetLeafData` to get raw leaf data... is leaf data == `share` ?

	rowRoots, err := rowRootsFromNamespaceID(nID, dah)
	if err != nil {
		return [][]byte{}, err // TODO should we wrap error here?
	}
	return getSharesByNamespace(ctx, nID, dah, rowRoots, api)
}


// rowRootsFromNamespaceID finds the row root(s) that contain(s) the namespace ID `nID` // todo improve docs
func rowRootsFromNamespaceID(nID namespace.ID, dah *types.DataAvailabilityHeader) ([]int, error) {
	roots := make([]int, 0)
	fmt.Println(fmt.Sprintf("NID: %v", []byte(nID)))
	// TODO note: dah.RowRoots ordered lexographically
	for i, row := range dah.RowsRoots {
		// if nID exists within range of min -> max of row, return the row
		fmt.Println(fmt.Sprintf("row min: %v, row max: %v", []byte(row.Min()), []byte(row.Max())))
		if !nID.Less(row.Min()) && nID.LessOrEqual(row.Max()) {
			roots = append(roots, i)
		}
	}
	if len(roots) > 0 {
		return roots, nil
	}
	// TODO what to do if no row contains the nID within given rowRoots?
		// return min or max leaf depending on if nID is Less than row.Min or !LessOrEqual to row.Max
	return roots, ErrOutOfRange
}

func getSharesByNamespace(
	ctx context.Context,
	nID namespace.ID,
	dah *types.DataAvailabilityHeader,
	rowIndices []int,
	api coreiface.CoreAPI) ([][]byte, error) {
	shares := make([][]byte, 0)
	for _, index := range rowIndices {
		// compute the root CID from the DAH
		rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[index].Bytes())
		if err != nil {
			return shares, err
		}
		startingIndex, err := findStartingIndex(ctx, nID, dah, rootCid, api)
		if err != nil {
			return shares, err
		}
		fmt.Println("got starting index: ", startingIndex)
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
	 // TODO it shouldn't hit here:
	return shares, nil
}

// findStartingIndex returns the path to the first leaf that contains the given nID
// in the tree corresponding to the given root CID.
func findStartingIndex( // TODO maybe rename to pathToStartingIndex
	ctx context.Context,
	nID namespace.ID,
	dah *types.DataAvailabilityHeader,
	rootCid cid.Cid,
	api coreiface.CoreAPI) (int, error) {

	left := "0"
	right := "1"

	treeDepth := int(math.Log2(float64(len(dah.RowsRoots)))) // TODO totalLeaves == len(rowRoots in DAH) right?
	currentPath := make([]string, 0)

	for {
		leafLevel := len(currentPath)+1 == treeDepth
		fmt.Println("leaflevel? ", leafLevel)

		leftNode, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), append(currentPath, left)...))
		if err != nil {
			return 0, err
		}
		fmt.Println("got left node")
		leftIntvlDigest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, leftNode.Cid().Hash()[4:])
		if err != nil {
			return 0, err
		}
		fmt.Println("got left node intvl digest: ", leftIntvlDigest.String())

		rightNode, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), append(currentPath, right)...))
		if err != nil {
			return 0, err
		}
		fmt.Println("got right node")
		rightIntvlDigest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, rightNode.Cid().Hash()[4:])
		if err != nil {
			return 0, err
		}
		fmt.Println("got right node intvl digest: ", rightIntvlDigest.String())

		if leafLevel {
			if intervalContains(nID, leftIntvlDigest) {
				// return index from path to left leaf
				return startIndexFromPath(append(currentPath, left)), nil
			} else if intervalContains(nID, rightIntvlDigest) {
				// return path to right leaf
				return startIndexFromPath(append(currentPath, right)), nil
			} else {
				
				// todo proof of absence
				// todo this means returning a proof that the nID is either left or right of the current leaves
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
			indices = indices[0:len(indices)/2]
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
	return !nID.Less(intvlDigest.Min())	&& nID.LessOrEqual(intvlDigest.Max())
}

func walk(
	ctx context.Context,
	nID namespace.ID,
	dah *types.DataAvailabilityHeader,
	rootCid cid.Cid,
	api coreiface.CoreAPI) ([]byte, error) {
	var (
		data []byte
		currentIndex = uint32(len(dah.RowsRoots)/2)  // start in the middle // TODO
	)
	for {
		lPath, err := leafPath(currentIndex, uint32(len(dah.RowsRoots)))
		if err != nil {
			return data, err
		}
		fmt.Println("got leaf path: ", lPath)

		node, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), lPath...))
		if err != nil {
			return data, err
		}
		fmt.Println("resolved api node: ", node.Cid())

		// convert node into interval digest so we can get the min/max ID for that leaf
		nodeHash := node.Cid().Hash()[4:] // IPFS prepends 4 bytes to the data that it stores, so ignore first 4 bytes
		digest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, nodeHash)
		if err != nil {
			return data, err
		}
		fmt.Println("converted to Intvl Digest: ", digest.String())
		fmt.Printf("digest min: %v, digest max: %v\n", []byte(digest.Min()), []byte(digest.Max()))

		fmt.Println(fmt.Sprintf("GIVEN NID: %v", []byte(nID)))

		if !nID.Less(digest.Min()) && nID.LessOrEqual(digest.Max()) {
			fmt.Println("EQUAAAAAALS!!!!!!!")
			fmt.Println(fmt.Sprintf("digest min: %x, digest max: %x", digest.Min(), digest.Max()))
			return node.RawData()[1:], nil
		}
		if nID.Less(digest.Min()) {
			fmt.Println("LEFT")
			// go left
			currentIndex--
			fmt.Println("CURRENT INDEX: ", currentIndex)
		}
		if !nID.LessOrEqual(digest.Max()) {
			fmt.Println("RIGHT")
			// go right
			currentIndex++
			fmt.Println("CURRENT INDEX: ", currentIndex)
		}
	}
}

// 2. Traverse the corresponding tree(s) according to the
//    above informally described algorithm and get the corresponding
//    leaves (if any)
func DRAFT_getSharesByNamespace(ctx context.Context, nID namespace.ID, dah *types.DataAvailabilityHeader, rowIndices []int, api coreiface.CoreAPI) ([][]byte, error) {
	shareData := make([][]byte, 0)
	for _, index := range rowIndices {
		// get the root of the tree as IPFS format node
		rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[index].Bytes())
		if err != nil {
			return [][]byte{}, err
		}
		leafPath, err := leafPath(uint32(index), uint32(len(dah.ColumnRoots)))
		path := path.Join(path.IpldPath(rootCid), leafPath...)
		node, err := api.ResolveNode(ctx, path)
		if err != nil {
			return [][]byte{}, err
		}
		// convert it back to interval digest format
		nodeHash := node.Cid().Hash()[4:] // IPFS prepends 4 bytes to the data that it stores
		intervalDigest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, nodeHash)
		if err != nil {
			return [][]byte{}, err
		}
		if !nID.Less(intervalDigest.Min()) && nID.LessOrEqual(intervalDigest.Max()) {
			// keep walking down this path
		}
		// go 0 or 1 which is L or R, if you find it, return it, if you don't, prove it doesn't exist
		// todo!!!!: 0 == leaf, 1 == inner node: https://github.com/lazyledger/lazyledger-core/blob/37502aac69d755c189df37642b87327772f4ac2a/p2p/ipld/plugin/nodes/nodes.go#L86



		// walk the tree to find the shares with the given namespace

		// TODO walk the tree


		// todo how do you traverse the tree????????
					// 	for _, row := range uniqueRandNumbers(edsWidth/2, edsWidth) {
					//		for _, col := range uniqueRandNumbers(edsWidth/2, edsWidth) {
					//			rootCid, err := nodes.CidFromNamespacedSha256(rowRoots[row])
					//			if err != nil {
					//				return types.Data{}, err
					//			}
					//
					//			go sc.retrieveShare(rootCid, true, row, col, api)
					//		}
					//	}


		rawData := node.RawData()
		id := rawData[:1]
		if nID.Equal(namespace.ID(id)) {
			//shares = append(shares, rawData)
		}

		// TODO create func - you only need 1/2 the shares in the row to be able to get the ????????
		// sample 1/4 of the total extended square by sampling half of the leaves in
		//	// half of the rows
		// this will give you the OG data and the erasured data
		data, err := GetLeafData(ctx, rootCid, uint32(index), uint32(len(dah.ColumnRoots)), api) 	// TODO can use sharecounter here and use goroutine for sc.retrieveShares?
		if err != nil {
			return [][]byte{}, fmt.Errorf("failed to get leaf data: %v", err)
		}
		// decode data
			fmt.Print(data)
			// TODO decode the shares via rsmt2d.Decode(): https://github.com/lazyledger/rsmt2d/blob/master/infectiousRSGF8.go#L53:
		// range over data and store only shares w/ given nID
			// check first __#__ of bytes of []byte ?
	}
	// range over shares to find the shares with the same namespaceID and return those,
	// todo compute the root and create a cid for the root hash???
	return shareData, nil
}

