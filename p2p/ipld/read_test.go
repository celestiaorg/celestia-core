package ipld

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/coreapi"
	"github.com/ipfs/go-ipfs/core/node/libp2p"
	"github.com/ipfs/go-ipfs/repo/fsrepo"

	// coremock "github.com/ipfs/go-ipfs/core/mock"
	"github.com/ipfs/go-ipfs/plugin/loader"
	format "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"
	"github.com/stretchr/testify/assert"
)

func TestCalcCIDPath(t *testing.T) {
	type test struct {
		name         string
		index, total uint32
		expected     string
	}

	// test cases
	tests := []test{
		{"nil", 0, 0, ""},
		{"0 index 16 total leaves", 0, 16, "0/0/0/0"},
		{"1 index 16 total leaves", 1, 16, "0/0/0/1"},
		{"9 index 16 total leaves", 9, 16, "1/0/0/1"},
		{"15 index 16 total leaves", 15, 16, "1/1/1/1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := calcCIDPath(tt.index, tt.total)
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
		index   uint32
		total   uint32
	}

	// create a mock node
	ipfsNode, err := createTestIPFSNode()
	if err != nil {
		t.Error(err)
	}

	ipfsAPI, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		t.Error(err)
	}

	ctx := context.Background()
	batch := format.NewBatch(ctx, ipfsAPI.Dag())

	// create a random tree
	tree, err := createNmtTree(
		ctx,
		batch,
		generateRandNamespacedRawData(16, types.NamespaceSize, types.ShareSize),
	)
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
		{"basic", time.Second, rootCid, 1, 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()
			data, err := GetLeafData(ctx, tt.rootCid, tt.index, tt.total, ipfsAPI.Dag())
			if err != nil {
				t.Error(err)
			}
			fmt.Println(data)

		},
		)
	}
}

// nmtcommitment generates the nmt root of some namespaced data
func createNmtTree(ctx context.Context, batch *format.Batch, namespacedData [][]byte) (*nmt.NamespacedMerkleTree, error) {
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

// most of this is unexported code from the node package
func createTestIPFSNode() (*core.IpfsNode, error) {
	// config := config.DefaultConfig()
	repoRoot := "/home/evan/.tendermint_app/ipfs"

	if !fsrepo.IsInitialized(repoRoot) {
		// TODO: sentinel err
		return nil, fmt.Errorf("ipfs repo root: %v not intitialized", repoRoot)
	}

	if err := setupPlugins(repoRoot); err != nil {
		return nil, err
	}

	repo, err := fsrepo.Open(repoRoot)
	if err != nil {
		return nil, err
	}

	// Construct the node
	nodeOptions := &core.BuildCfg{
		Online: true,
		// This option sets the node to be a full DHT node (both fetching and storing DHT Records)
		Routing: libp2p.DHTOption,
		// This option sets the node to be a client DHT node (only fetching records)
		// Routing: libp2p.DHTClientOption,
		Repo: repo,
	}

	ctx := context.Background()
	node, err := core.NewNode(ctx, nodeOptions)
	if err != nil {
		return nil, err
	}
	// run as daemon:
	node.IsDaemon = true
	return node, nil
}

func setupPlugins(path string) error {
	// Load plugins. This will skip the repo if not available.
	plugins, err := loader.NewPluginLoader(filepath.Join(path, "plugins"))
	if err != nil {
		return fmt.Errorf("error loading plugins: %s", err)
	}
	if err := plugins.Load(&nodes.LazyLedgerPlugin{}); err != nil {
		return fmt.Errorf("error loading lazyledger plugin: %s", err)
	}
	if err := plugins.Initialize(); err != nil {
		return fmt.Errorf("error initializing plugins: plugins.Initialize(): %s", err)
	}
	if err := plugins.Inject(); err != nil {
		return fmt.Errorf("error initializing plugins: could not Inject() %w", err)
	}

	return nil
}
