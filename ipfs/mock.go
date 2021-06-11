package ipfs

import (
	"github.com/ipfs/go-ipfs/core"
	coremock "github.com/ipfs/go-ipfs/core/mock"
)

// Mock wraps the
func Mock() NodeProvider {
	return func() (*core.IpfsNode, error) {
		return coremock.NewMockNode()
	}
}
