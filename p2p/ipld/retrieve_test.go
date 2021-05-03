package ipld

import (
	"context"
	"crypto/sha256"
	"fmt"
	format "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestReturnContainingRow(t *testing.T) {
	data := generateRandNamespacedRawData(16, 8, 8)
	nID := data[len(data)/2][:8]
	dah, err := makeDAHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println("num rows: ", len(dah.RowsRoots))
	fmt.Println("num cols: ", len(dah.ColumnRoots))

	indices, err := rowRootsFromNamespaceID(nID, dah)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("got %d rows", len(indices))
	for _, index := range indices {
		fmt.Println("row num: ", index, "data: ", dah.RowsRoots[index].Bytes())
	}
	// TODO implement check
}

func makeFakeNMT(nIDSize int, data [][]byte) *nmt.NamespacedMerkleTree {
	tree := nmt.New(sha256.New(), nmt.NamespaceIDSize(nIDSize)) // TODO consider changing this to default size
	// add some fake data
	for _, d := range data {
		if err := tree.Push(d); err != nil {
			panic(fmt.Sprintf("unexpected error: %v", err))
		}
	}
	return tree
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
}

func Test_getSharesByNamespace(t *testing.T) {
	// create a DAH with only 1 row containing the nID
	rawData := generateRandNamespacedRawData(16, nmt.DefaultNamespaceIDLen, 40)
	for i, data := range rawData {
		fmt.Println("row: ", i, "data: ", data)
	}

	nIDRawData := rawData[4]
	nID := nIDRawData[:8]

	dah, err := makeDAHeader(rawData)
	if err != nil {
		t.Fatal(err)
	}

	rowIndices, err := rowRootsFromNamespaceID(nID, dah)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("row indices: ", rowIndices)

	ctx := context.Background()

	// create api
	api := mockedIpfsAPI(t)

	shares, err := getSharesByNamespace(ctx, nID, dah, rowIndices, api)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("SUCCESS!", shares)
}

func Test_walk(t *testing.T) {
	// set nID
	data := generateRandNamespacedRawData(16, nmt.DefaultNamespaceIDLen, 8)
	nIDData := data[rand.Intn(len(data)-1)]
	nID := nIDData[:8]
	fmt.Println("NID DATA: ", nIDData, "nID: ", nID)
	// marshal that into a dah
	dah, err := makeDAHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	api := mockedIpfsAPI(t)
	ctx := context.Background()
	batch := format.NewBatch(ctx, api.Dag())
	root, err := getNmtRoot(ctx, batch, dah.RowsRoots.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("root max: %x, root min: %x", root.Max(), root.Min())
	rootCid, err := nodes.CidFromNamespacedSha256(root.Bytes())
	if err != nil {
		t.Error(err)
	}

	shares, err := walk(context.Background(), nID, dah, rootCid, api)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("GOT: ", shares)
	if !reflect.DeepEqual(nIDData, shares) {
		t.Fatalf("expected %v, got %v", nIDData, shares)
	}
}
