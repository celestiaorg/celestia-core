package ipfs

import (
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	ipld "github.com/ipfs/go-ipld-format"
	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/libp2p/go-libp2p-core/routing"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() APIProvider {
	return mockProvider{
		dag: mdutils.Mock(),
	}
}

var _ APIProvider = mockProvider{}

type mockProvider struct {
	dag ipld.DAGService
}

func (m mockProvider) DAG() ipld.DAGService              { return m.dag }
func (m mockProvider) Routing() routing.ContentRouting   { return nil }
func (m mockProvider) Blockstore() blockstore.Blockstore { return nil }
func (m mockProvider) Close() error                      { return nil }
