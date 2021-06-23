package ipfs

import (
	"context"

	ds "github.com/ipfs/go-datastore"
	ds_sync "github.com/ipfs/go-datastore/sync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	nilrouting "github.com/ipfs/go-ipfs-routing/none"
	"github.com/ipfs/go-ipfs/core"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	"github.com/libp2p/go-libp2p-core/routing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() NodeProvider {
	return func() (*core.IpfsNode, error) {
		plugin.EnableNMT()

		nd, err := MockNode()
		if err != nil {
			return nil, err
		}

		return nd, nil
	}
}

func MockNode() (*core.IpfsNode, error) {
	ctx := context.TODO()
	nd, err := core.NewNode(ctx, &core.BuildCfg{
		Online: true,
		Host:   coremock.MockHostOption(mocknet.New(ctx)),
	})
	if err != nil {
		return nil, err
	}
	nd.Routing = MockRouting()
	return nd, err
}

func MockRouting() routing.Routing {
	croute, _ := nilrouting.ConstructNilRouting(context.TODO(), nil, nil, nil)
	return croute
}

func MockBlockStore() blockstore.Blockstore {
	return blockstore.NewBlockstore(ds_sync.MutexWrap(ds.NewMapDatastore()))
}
