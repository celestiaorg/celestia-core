package ipfs

import (
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	ipld "github.com/ipfs/go-ipld-format"
	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/libp2p/go-libp2p-core/routing"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() APIProvider {
	return dagOnlyMockProvider{
		dag: mdutils.Mock(),
	}
}

var _ APIProvider = dagOnlyMockProvider{}

type dagOnlyMockProvider struct {
	dag ipld.DAGService
}

func (m dagOnlyMockProvider) DAG() ipld.DAGService              { return m.dag }
func (m dagOnlyMockProvider) Routing() routing.ContentRouting   { return nil }
func (m dagOnlyMockProvider) Blockstore() blockstore.Blockstore { return nil }
func (m dagOnlyMockProvider) Close() error                      { return nil }
