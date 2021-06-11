package ipfs

import (
	"github.com/ipfs/go-ipfs/core"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
)

// APIProvider allows customizable IPFS core APIs.
type APIProvider func() (coreiface.APIDagService, *core.IpfsNode, error)
