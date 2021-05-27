package ipfs

import (
	"io"

	ipld "github.com/ipfs/go-ipld-format"
	mdutils "github.com/ipfs/go-merkledag/test"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
)

// Mock provides simple mock IPFS API useful for testing
func Mock() APIProvider {
	return func() (coreiface.APIDagService, io.Closer, error) {
		dom := dagOnlyMock{mdutils.Mock()}

		return dom, dom, nil
	}
}

type dagOnlyMock struct {
	ipld.DAGService
}

func (dom dagOnlyMock) Dag() coreiface.APIDagService { return dom }
func (dagOnlyMock) Close() error                     { return nil }
func (dom dagOnlyMock) Pinning() ipld.NodeAdder      { return dom }
