package ipfs

import (
	"io"

	"github.com/ipfs/go-ipfs/core/coreapi"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	coreiface "github.com/ipfs/interface-go-ipfs-core"

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
