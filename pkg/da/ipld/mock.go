package ipld

import (
	"context"

	nilrouting "github.com/ipfs/go-ipfs-routing/none"
	"github.com/libp2p/go-libp2p-core/routing"
)

func MockRouting() routing.Routing {
	croute, _ := nilrouting.ConstructNilRouting(context.TODO(), nil, nil, nil)
	return croute
}
