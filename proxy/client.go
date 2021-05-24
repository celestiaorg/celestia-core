package proxy

import (
	abcicli "github.com/lazyledger/lazyledger-core/abci/client"
	"github.com/lazyledger/lazyledger-core/abci/example/counter"
	"github.com/lazyledger/lazyledger-core/abci/example/kvstore"
	"github.com/lazyledger/lazyledger-core/abci/types"
	tmsync "github.com/lazyledger/lazyledger-core/libs/sync"
)

// ClientCreator creates new ABCI clients.
type ClientCreator interface {
	// NewABCIClient returns a new ABCI client.
	NewABCIClient() (abcicli.Client, error)
}

//----------------------------------------------------
// local proxy uses a mutex on an in-proc app

type localClientCreator struct {
	mtx *tmsync.Mutex
	app types.Application
}

// NewLocalClientCreator returns a ClientCreator for the given app,
// which will be running locally.
func NewLocalClientCreator(app types.Application) ClientCreator {
	return &localClientCreator{
		mtx: new(tmsync.Mutex),
		app: app,
	}
}

func (l *localClientCreator) NewABCIClient() (abcicli.Client, error) {
	return abcicli.NewLocalClient(l.mtx, l.app), nil
}

// DefaultClientCreator returns a default ClientCreator, which will create a
// local client if app is one of: 'counter', 'counter_serial', 'kvstore',
// 'persistent_kvstore' or 'noop', otherwise - a remote client.
func DefaultClientCreator(app, dbDir string) ClientCreator {
	switch app {
	case "counter":
		return NewLocalClientCreator(counter.NewApplication(false))
	case "counter_serial":
		return NewLocalClientCreator(counter.NewApplication(true))
	case "kvstore":
		return NewLocalClientCreator(kvstore.NewApplication())
	case "persistent_kvstore":
		return NewLocalClientCreator(kvstore.NewPersistentKVStoreApplication(dbDir))
	case "noop":
		return NewLocalClientCreator(types.NewBaseApplication())
	default:
		panic("RemoteClientCreator not implemented in lazyledger-core")
	}
}
