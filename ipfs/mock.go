package ipfs

import (
	"io"

	coremock "github.com/ipfs/go-ipfs/core/mock"
	ipld "github.com/ipfs/go-ipld-format"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() APIProvider {
	return func() (coreiface.APIDagService, io.Closer, error) {
		node, err := coremock.NewMockNode()
		if err != nil {
			return nil, nil, err
		}
		dom := dagOnlyMock{node.DAG}

		return dom, node, nil
	}
}

type dagOnlyMock struct {
	ipld.DAGService
}

func (dom dagOnlyMock) Dag() coreiface.APIDagService { return dom }
func (dagOnlyMock) Close() error                     { return nil }
func (dom dagOnlyMock) Pinning() ipld.NodeAdder      { return dom }
