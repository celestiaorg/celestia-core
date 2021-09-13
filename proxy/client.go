package proxy

import (
	"fmt"
	"io"

	abcicli "github.com/celestiaorg/celestia-core/abci/client"
	"github.com/celestiaorg/celestia-core/abci/example/kvstore"
	"github.com/celestiaorg/celestia-core/abci/types"
	tmsync "github.com/celestiaorg/celestia-core/internal/libs/sync"
)

//go:generate ../scripts/mockery_generate.sh ClientCreator

// ClientCreator creates new ABCI clients.
type ClientCreator interface {
	// NewABCIClient returns a new ABCI client.
	NewABCIClient() (abcicli.Client, error)
}

//----------------------------------------------------
// local proxy uses a mutex on an in-proc app

type localClientCreator struct {
	mtx *tmsync.RWMutex
	app types.Application
}

// NewLocalClientCreator returns a ClientCreator for the given app,
// which will be running locally.
func NewLocalClientCreator(app types.Application) ClientCreator {
	return &localClientCreator{
		mtx: new(tmsync.RWMutex),
		app: app,
	}
}

func (l *localClientCreator) NewABCIClient() (abcicli.Client, error) {
	return abcicli.NewLocalClient(l.mtx, l.app), nil
}

// DefaultClientCreator returns a default ClientCreator, which will create a
// local client if addr is one of: 'kvstore',
// 'persistent_kvstore' or 'noop', otherwise - a remote client.
//
// The Closer is a noop except for persistent_kvstore applications,
// which will clean up the store.
func DefaultClientCreator(addr, transport, dbDir string) (ClientCreator, io.Closer) {
	switch addr {
	case "kvstore":
		return NewLocalClientCreator(kvstore.NewApplication()), noopCloser{}
	case "persistent_kvstore":
		app := kvstore.NewPersistentKVStoreApplication(dbDir)
		return NewLocalClientCreator(app), app
	case "noop":
		return NewLocalClientCreator(types.NewBaseApplication()), noopCloser{}
	default:
		mustConnect := false // loop retrying
		return NewRemoteClientCreator(addr, transport, mustConnect), noopCloser{}
	}
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }
