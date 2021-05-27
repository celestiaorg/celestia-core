package ipfs

import (
	"context"
	"io"

	"github.com/ipfs/go-ipfs/core/coreapi"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	format "github.com/ipfs/go-ipld-format"
	mdutils "github.com/ipfs/go-merkledag/test"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/ipfs/interface-go-ipfs-core/path"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() APIProvider {
	return func() (coreiface.CoreAPI, io.Closer, error) {
		plugin.EnableNMT()

		nd, err := coremock.NewMockNode()
		if err != nil {
			return nil, nil, err
		}

		api, err := coreapi.NewCoreAPI(nd)
		if err != nil {
			return nil, nil, err
		}

		return api, nd, nil
	}
}

// DagOnlyMock provides an empty APIProvider that only mocks the DAG portion of
// the ipfs api object. This is much lighter than the full IPFS node and should
// be favored for CI testing
func DagOnlyMock() APIProvider {
	mockAPI := dagOnlyMock{mdutils.Mock()}
	return func() (coreiface.CoreAPI, io.Closer, error) {
		return mockAPI, mockAPI, nil
	}
}

var _ coreiface.CoreAPI = dagOnlyMock{}

type dagOnlyMock struct {
	format.DAGService
}

func (dom dagOnlyMock) Dag() coreiface.APIDagService { return dom }

func (dagOnlyMock) Unixfs() coreiface.UnixfsAPI                                   { return nil }
func (dagOnlyMock) Block() coreiface.BlockAPI                                     { return nil }
func (dagOnlyMock) Name() coreiface.NameAPI                                       { return nil }
func (dagOnlyMock) Key() coreiface.KeyAPI                                         { return nil }
func (dagOnlyMock) Pin() coreiface.PinAPI                                         { return nil }
func (dagOnlyMock) Object() coreiface.ObjectAPI                                   { return nil }
func (dagOnlyMock) Dht() coreiface.DhtAPI                                         { return nil }
func (dagOnlyMock) Swarm() coreiface.SwarmAPI                                     { return nil }
func (dagOnlyMock) PubSub() coreiface.PubSubAPI                                   { return nil }
func (dagOnlyMock) ResolvePath(context.Context, path.Path) (path.Resolved, error) { return nil, nil }
func (dagOnlyMock) ResolveNode(context.Context, path.Path) (format.Node, error)   { return nil, nil }
func (dagOnlyMock) WithOptions(...options.ApiOption) (coreiface.CoreAPI, error)   { return nil, nil }
func (dagOnlyMock) Close() error                                                  { return nil }
func (dagOnlyMock) Pinning() format.NodeAdder                                     { return nil }
