package mockipfs

import (
	"github.com/ipfs/go-ipfs/core/coreapi"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	iface "github.com/ipfs/interface-go-ipfs-core"
)

// MockedIpfsAPI is a testing util function to create a mocked IPFS API
func MockedIpfsAPI() iface.CoreAPI {
	ipfsNode, err := coremock.NewMockNode()
	if err != nil {
		panic(err)
	}

	// issue a new API object
	ipfsAPI, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		panic(err)
	}

	return ipfsAPI
}
