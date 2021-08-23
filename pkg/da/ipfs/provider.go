package ipfs

import (
	"github.com/ipfs/go-ipfs/core"
)

// NodeProvider initializes and returns an IPFS node
type NodeProvider func() (*core.IpfsNode, error)
