package ipld

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-ipfs/core/coreapi"

	coremock "github.com/ipfs/go-ipfs/core/mock"
	format "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"github.com/lazyledger/rsmt2d"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeafPath(t *testing.T) {
	type test struct {
		name         string
		index, total uint32
		expected     []string
	}

	// test cases
	tests := []test{
		{"nil", 0, 0, []string(nil)},
		{"0 index 16 total leaves", 0, 16, strings.Split("0/0/0/0", "/")},
		{"1 index 16 total leaves", 1, 16, strings.Split("0/0/0/1", "/")},
		{"9 index 16 total leaves", 9, 16, strings.Split("1/0/0/1", "/")},
		{"15 index 16 total leaves", 15, 16, strings.Split("1/1/1/1", "/")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result, err := leafPath(tt.index, tt.total)
			if err != nil {
				t.Error(err)
			}
			assert.Equal(t, tt.expected, result)
		},
		)
	}
}

func TestNextPowerOf2(t *testing.T) {
	type test struct {
		input    uint32
		expected uint32
	}
	tests := []test{
		{
			input:    2,
			expected: 2,
		},
		{
			input:    11,
			expected: 8,
		},
		{
			input:    511,
			expected: 256,
		},
		{
			input:    1,
			expected: 1,
		},
		{
			input:    0,
			expected: 0,
		},
	}
	for _, tt := range tests {
		res := nextPowerOf2(tt.input)
		assert.Equal(t, tt.expected, res)
	}
}

func TestGetLeafData(t *testing.T) {
	type test struct {
		name    string
		timeout time.Duration
		rootCid cid.Cid
		leaves  [][]byte
	}

	// create a mock node
	ipfsNode, err := coremock.NewMockNode()
	if err != nil {
		t.Error(err)
	}

	// issue a new API object
	ipfsAPI, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		t.Error(err)
	}

	// create the context and batch needed for node collection from the tree
	ctx := context.Background()
	batch := format.NewBatch(ctx, ipfsAPI.Dag().Pinning())

	// generate random data for the nmt
	data := generateRandNamespacedRawData(16, types.NamespaceSize, types.ShareSize)

	// create a random tree
	tree, err := createNmtTree(ctx, batch, data)
	if err != nil {
		t.Error(err)
	}

	// calculate the root
	root := tree.Root()

	// commit the data to IPFS
	err = batch.Commit()
	if err != nil {
		t.Error(err)
	}

	// compute the root and create a cid for the root hash
	rootCid, err := nodes.CidFromNamespacedSha256(root.Bytes())
	if err != nil {
		t.Error(err)
	}

	// test cases
	tests := []test{
		{"16 leaves", time.Second, rootCid, data},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()
			for i, leaf := range tt.leaves {
				data, err := GetLeafData(ctx, tt.rootCid, uint32(i), uint32(len(tt.leaves)), ipfsAPI)
				if err != nil {
					t.Error(err)
				}
				assert.Equal(t, leaf, data)
			}
		},
		)
	}
}

func TestBlockRecovery(t *testing.T) {
	ipfsNode, err := coremock.NewMockNode()
	if err != nil {
		t.Error(err)
	}

	ipfsAPI, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		t.Error(err)
	}

	testCases := []struct {
		name      string
		blockData types.Data
		expectErr bool
		errString string
	}{
		{"16 leaves", generateRandomData(16), false, ""},
		// {"max square size", generateRandomData(17), false, ""},
	}
	ctx := context.Background()
	for _, tc := range testCases {
		tc := tc

		block := &types.Block{
			Data:       tc.blockData,
			LastCommit: &types.Commit{},
		}

		block.Hash()

		t.Run(tc.name, func(t *testing.T) {
			err = block.PutBlock(ctx, ipfsAPI.Dag().Pinning())
			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errString)
				return
			}

			require.NoError(t, err)

			rowRoots := block.DataAvailabilityHeader.RowsRoots.Bytes()
			colRoots := block.DataAvailabilityHeader.ColumnRoots.Bytes()

			data := tc.blockData.ComputeShares().RawShares()

			fmt.Println("row roots", len(rowRoots))

			tree := NewErasuredNamespacedMerkleTree(uint64(len(rowRoots) / 2))

			_, err := rsmt2d.RepairExtendedDataSquare(
				rowRoots,
				colRoots,
				removeRandShares(data),
				rsmt2d.RSGF8,
				tree.Constructor,
			)

			require.NoError(t, err)
			// perform some check that namespaces are recovered as well

		})
	}
}

// nmtcommitment generates the nmt root of some namespaced data
func createNmtTree(
	ctx context.Context,
	batch *format.Batch,
	namespacedData [][]byte,
) (*nmt.NamespacedMerkleTree, error) {
	na := nodes.NewNmtNodeAdder(ctx, batch)
	tree := nmt.New(sha256.New(), nmt.NamespaceIDSize(types.NamespaceSize), nmt.NodeVisitor(na.Visit))
	for _, leaf := range namespacedData {
		err := tree.Push(leaf[:types.NamespaceSize], leaf[types.NamespaceSize:])
		if err != nil {
			return tree, err
		}
	}

	return tree, nil
}

// this code is copy pasted from the plugin, and should likely be exported in the plugin instead
func generateRandNamespacedRawData(total int, nidSize int, leafSize int) [][]byte {
	data := make([][]byte, total)
	for i := 0; i < total; i++ {
		nid := make([]byte, nidSize)
		_, err := rand.Read(nid)
		if err != nil {
			panic(err)
		}
		data[i] = nid
	}

	sortByteArrays(data)
	for i := 0; i < total; i++ {
		d := make([]byte, leafSize)
		_, err := rand.Read(d)
		if err != nil {
			panic(err)
		}
		data[i] = append(data[i], d...)
	}

	return data
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}

func generateRandomData(msgCount int) types.Data {
	out := make([]types.Message, msgCount)
	for i, msg := range generateRandNamespacedRawData(msgCount, types.NamespaceSize, types.ShareSize) {
		out[i] = types.Message{NamespaceID: msg[:types.NamespaceSize], Data: msg[:types.NamespaceSize]}
	}
	return types.Data{
		Messages: types.Messages{MessagesList: out},
	}
}

func removeRandShares(data [][]byte) [][]byte {
	count := len(data)
	// remove half of the shares randomly
	for i := 0; i < (count / 2); {
		ind := rand.Intn(count)
		if len(data[ind]) == 0 {
			continue
		}
		data[ind] = nil
		i++
	}
	fmt.Println("removal data len", len(data))
	return data
}
