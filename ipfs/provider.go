package ipfs

import (
	"io"

	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/ipfs/go-ipfs/core"
	ipld "github.com/ipfs/go-ipld-format"
	"github.com/libp2p/go-libp2p-core/routing"
)

// APIProvider allows customizable IPFS core APIs.
type APIProvider interface {
	DAG() ipld.DAGService
	Blockstore() blockstore.Blockstore
	Routing() routing.ContentRouting

	io.Closer
}

var _ APIProvider = FullNodeProvider{}

// FullNodeProvider wraps a ipfs core node to fullfill the APIProvider interface
type FullNodeProvider struct {
	node *core.IpfsNode
}

func (f FullNodeProvider) DAG() ipld.DAGService {
	return f.node.DAG
}

func (f FullNodeProvider) Routing() routing.ContentRouting {
	return f.node.Routing
}

func (f FullNodeProvider) Blockstore() blockstore.Blockstore {
	return f.node.Blockstore
}

func (f FullNodeProvider) Close() error {
	return f.Close()
}
