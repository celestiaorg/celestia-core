package ipfs

import (
	"io"

	coreiface "github.com/ipfs/interface-go-ipfs-core"
)

// APIProvider allows customizable IPFS core APIs.
type APIProvider func() (coreiface.APIDagService, io.Closer, error)
