package ipld

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	format "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/nmt/namespace"
)

func Test_rowRootsFromNamespaceID(t *testing.T) {
	data := generateRandNamespacedRawData(16, 8, 8)
	nID := data[len(data)/2][:8]
	dah, err := makeDAHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	indices, err := rowRootsFromNamespaceID(nID, dah)
	if err != nil {
		t.Fatal(err)
	}

	expected := make([]int, 0)
	for i, row := range dah.RowsRoots {
		if !namespace.ID(nID).Less(row.Min()) && namespace.ID(nID).LessOrEqual(row.Max()) {
			expected = append(expected, i)
		}
	}

	assert.Equal(t, expected, indices)
}

func makeDAHeader(data [][]byte) (*types.DataAvailabilityHeader, error) {
	rows, err := types.NmtRootsFromBytes(data)
	if err != nil {
		return nil, err
	}
	clns, err := types.NmtRootsFromBytes(data)
	if err != nil {
		return nil, err
	}

	return &types.DataAvailabilityHeader{
		RowsRoots:   rows,
		ColumnRoots: clns,
	}, nil
}

func TestRetrieveShares(t *testing.T) {
	// set nID
	api := mockedIpfsAPI(t)
	treeRoots := make(types.NmtRoots, 0)

	ctx := context.Background()

	var (
		nIDData []byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		fmt.Printf("%+v\n", data)
		if len(nID) == 0 {
			nIDData = data[rand.Intn(len(data)-1)]
			nID = nIDData[:8]
		}

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}

		treeRoots = append(treeRoots, treeRoot)
		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	fmt.Println("NID DATA: ", nIDData, "nID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	shares, err := RetrieveShares(ctx, nID, dah, api)
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) > 1 {
		t.Fatal("unexpected share(s) returned: ", shares)
	}
	assert.Equal(t, nIDData, shares[0])
}

func Test_walk(t *testing.T) {
	// set nID
	api := mockedIpfsAPI(t)
	treeRoots := make(types.NmtRoots, 0)

	ctx := context.Background()

	var (
		nIDData []byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		fmt.Printf("%+v\n", data)
		if len(nID) == 0 {
			nIDData = data[rand.Intn(len(data)-1)] // todo maybe make this nicer later
			nID = nIDData[:8]
		}

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}

		treeRoots = append(treeRoots, treeRoot)
		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	fmt.Println("NID DATA: ", nIDData, "nID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[0].Bytes())
	if err != nil {
		t.Error(err)
	}

	shares, err := walk(ctx, nID, dah, rootCid, api)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("GOT: ", shares)
	if !reflect.DeepEqual(nIDData, shares) {
		t.Fatalf("expected %v, got %v", nIDData, shares)
	}
}


// todo fix later
func commitTreeDataToDAG(ctx context.Context, data [][]byte, batchAdder *nodes.NmtNodeAdder) (namespace.IntervalDigest, error) {

	tree := nmt.New(sha256.New(), nmt.NodeVisitor(batchAdder.Visit)) // TODO consider changing this to default size
	// add some fake data
	for _, d := range data {
		if err := tree.Push(d); err != nil {
			panic(fmt.Sprintf("unexpected error: %v", err))
		}
	}
	return tree.Root(), nil
}

func TestWalkTree(t *testing.T) { // TODO DELETE
	// set nID
	api := mockedIpfsAPI(t)
	treeRoots := make(types.NmtRoots, 0)

	ctx := context.Background()

	var (
		nIDData []byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		fmt.Printf("%+v\n", data)
		if len(nID) == 0 {
			nIDData = data[rand.Intn(len(data)-1)] // todo maybe make this nicer later
			nID = nIDData[:8]
		}

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}

		treeRoots = append(treeRoots, treeRoot)
		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	fmt.Println("NID DATA: ", nIDData, "\nnID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[0].Bytes())
	if err != nil {
		t.Error(err)
	}
	fmt.Println("rootCID: ", rootCid)

	i :=  0
	for i < len(dah.RowsRoots) {
		node, err := api.ResolveNode(ctx, path.Join(path.IpldPath(rootCid), "1"))
		if err != nil {
			t.Fatal(err)
		}
		nodeHash := node.Cid().Hash()[4:] // IPFS prepends 4 bytes to the data that it stores
		intervalDigest, err := namespace.IntervalDigestFromBytes(nmt.DefaultNamespaceIDLen, nodeHash)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("min %v, max %v\n", []byte(intervalDigest.Min()), []byte(intervalDigest.Max()))
		if namespace.ID(nID).Equal(intervalDigest.Min()) && namespace.ID(nID).Equal(intervalDigest.Max()) {
			fmt.Println("SUCCESS!!!!!!", i)
			return
		}
		i++
	}
}

func Test_multipleLeaves_findStartingIndex(t *testing.T) {
	// set nID
	api := mockedIpfsAPI(t)
	ctx := context.Background()
	treeRoots := make(types.NmtRoots, 0)

	var (
		leaves [][]byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		if len(nID) == 0 {
			index := rand.Intn(len(data)-2)
			leaves = make([][]byte, 2)
			for i, _ := range leaves {
				leaves[i] = make([]byte, 24)
			}
			nID = make([]byte, 8)
			// TODO make this nicer later
			copy(nID, data[index][:8])
			// make 2 byte slices in data have same nID so that nID associated with multiple leaves
			if index == len(data)-2 {
				copy(data[index-1], append(nID, data[index-1][8:]...))

				copy(leaves[0], data[index-1])
				copy(leaves[1], data[index])
				fmt.Println("data index: ", data[index], "\n data index-1: ", data[index-1])
				fmt.Println("leaf 0: ", leaves[0], "\nleaf 1: ", leaves[1])
			} else {
				copy(data[index+1], append(nID, data[index+1][8:]...))
				copy(leaves[0], data[index])
				copy(leaves[1], data[index+1])
				fmt.Println("data index: ", data[index], "\n data index+1: ", data[index+1])
			}
		}
		fmt.Printf("%+v\n", data)

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}
		treeRoots = append(treeRoots, treeRoot)

		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("NID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[0].Bytes())
	if err != nil {
		t.Error(err)
	}
	fmt.Println("rootCID: ", rootCid)
	shares, err := getSharesByNamespace(ctx, nID, dah, []int{0}, api)
	if err != nil {
		t.Fatal(err)
	}
	for i, share := range shares {
		assert.Equal(t, leaves[i], share)
	}
}

func Test_successful_findStartingIndex(t *testing.T) {
	// set nID
	api := mockedIpfsAPI(t)
	treeRoots := make(types.NmtRoots, 0)

	ctx := context.Background()

	var (
		nIDData []byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		fmt.Printf("%+v\n", data)
		if len(nID) == 0 {
			nIDData = data[rand.Intn(len(data)-1)] // todo maybe make this nicer later
			nID = nIDData[:8]
		}

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}
		treeRoots = append(treeRoots, treeRoot)

		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	fmt.Println("NID DATA: ", nIDData, "\nnID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[0].Bytes())
	if err != nil {
		t.Error(err)
	}
	fmt.Println("rootCID: ", rootCid)

	startingIndex, err := findStartingIndex(ctx, nID, dah, rootCid, api)
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println("starting index: ", startingIndex)

	leaf, err := GetLeafData(ctx, rootCid, uint32(startingIndex), uint32(len(dah.RowsRoots)), api)
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println("LEAF FROM PATH: ", leaf)
	assert.Equal(t, nIDData, leaf)
}

func Test_unsuccessful_findStartingIndex(t *testing.T) {
	// set nID
	api := mockedIpfsAPI(t)
	treeRoots := make(types.NmtRoots, 0)

	ctx := context.Background()

	var (
		nIDData []byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		fmt.Printf("%+v\n", data)
		if len(nID) == 0 {
			nIDData = data[rand.Intn(len(data)-1)] // todo maybe make this nicer later
			nID = nIDData[:8]
		}

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}
		treeRoots = append(treeRoots, treeRoot)

		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	fmt.Println("NID DATA: ", nIDData, "\nnID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	// get rootCID of a row in which nID does NOT exist
	rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[2].Bytes())
	if err != nil {
		t.Error(err)
	}
	fmt.Println("rootCID: ", rootCid)

	_, err = findStartingIndex(ctx, nID, dah, rootCid, api)
	if err == nil {
		t.Fatal(err)
	}
	assert.True(t, strings.Contains(err.Error(), "within range of tree, but does not exist"))
}

func Test_unsuccessfulLessThan_findStartingIndex(t *testing.T) { // TODO
	// set nID
	api := mockedIpfsAPI(t)
	treeRoots := make(types.NmtRoots, 0)

	ctx := context.Background()

	var (
		nIDData []byte
		nID     []byte
	)

	// create nmt adder wrapping batch adder
	batchAdder := nodes.NewNmtNodeAdder(ctx, format.NewBatch(ctx, api.Dag()))

	for i := 0; i < 4; i++ {
		data := generateRandNamespacedRawData(4, nmt.DefaultNamespaceIDLen, 16)
		fmt.Printf("%+v\n", data)
		if len(nID) == 0 {
			nIDData = data[rand.Intn(len(data)-1)] // todo maybe make this nicer later
			nID = nIDData[:8]
		}

		treeRoot, err := commitTreeDataToDAG(ctx, data, batchAdder)
		if err != nil {
			t.Fatal(err)
		}
		treeRoots = append(treeRoots, treeRoot)

		if err := batchAdder.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	fmt.Println("NID DATA: ", nIDData, "\nnID: ", nID)

	dah := &types.DataAvailabilityHeader{
		RowsRoots: treeRoots,
	}

	// get rootCID of a row in which nID does NOT exist
	rootCid, err := nodes.CidFromNamespacedSha256(dah.RowsRoots[2].Bytes())
	if err != nil {
		t.Error(err)
	}
	fmt.Println("rootCID: ", rootCid)

	_, err = findStartingIndex(ctx, nID, dah, rootCid, api)
	if err == nil {
		t.Fatal(err)
	}
	assert.True(t, strings.Contains(err.Error(), "within range of tree, but does not exist"))
}

func Test_startIndexFromPath(t *testing.T) {
	var tests = []struct {
		path []string
		expected int
	}{
		{
			path: []string{"0", "0", "1"},
			expected: 1,
		},
		{
			path: []string{"0", "1", "1", "1"},
			expected: 7,
		},
		{
			path: []string{"0", "0"},
			expected: 0,
		},
		{
			path: []string{"1", "1", "0"},
			expected: 6,
		},
		{
			path: []string{"0", "1", "0", "1"},
			expected: 5,
		},
		{
			path: []string{"0", "0", "0", "0"},
			expected: 0,
		},
		{
			path: []string{"1", "1", "1", "1"},
			expected: 15,
		},
	}
	for i, tt := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			got := startIndexFromPath(tt.path)
			if got != tt.expected {
				t.Fatalf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}
