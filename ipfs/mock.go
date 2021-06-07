package ipfs

import (
	"context"

	nilrouting "github.com/ipfs/go-ipfs-routing/none"
	"github.com/ipfs/go-ipfs/core"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	ipld "github.com/ipfs/go-ipld-format"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/libp2p/go-libp2p-core/routing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() APIProvider {
	return func() (coreiface.APIDagService, *core.IpfsNode, error) {
		plugin.EnableNMT()

		nd, err := MockNode()
		if err != nil {
			return nil, nil, err
		}

		return dagOnlyMock{nd.DAG}, nd, nil
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

type dagOnlyMock struct {
	ipld.DAGService
}

func (dom dagOnlyMock) Dag() coreiface.APIDagService { return dom }
func (dom dagOnlyMock) Pinning() ipld.NodeAdder      { return dom }
