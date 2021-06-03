package ipfs

import (
	"io"

	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/options"
)

// APIProvider allows customizable IPFS core APIs.
type APIProvider func(online bool) (coreiface.APIDagService, io.Closer, error)

type onlineOfflineProvider struct {
	online, offline coreiface.APIDagService
	closer          io.Closer
}

func NewOnlineOfflineProvider(online coreiface.CoreAPI, closer io.Closer) (APIProvider, error) {

	offline, err := online.WithOptions(OfflineOption)
	if err != nil {
		return nil, err
	}

	provider := onlineOfflineProvider{
		online:  online.Dag(),
		offline: offline.Dag(),
	}

	return provider.Provide, nil
}

func (o onlineOfflineProvider) Provide(online bool) (coreiface.APIDagService, io.Closer, error) {
	switch online {
	case true:
		return o.online, o.closer, nil
	case false:
		return o.offline, o.closer, nil
	}
	// the compiler does't know this code is unreachable
	return nil, nil, nil
}

func OfflineOption(settings *options.ApiSettings) error {
	settings.Offline = true
	return nil
}
