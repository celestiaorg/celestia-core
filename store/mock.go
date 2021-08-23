package store

import (
	dbm "github.com/celestiaorg/celestia-core/libs/db"
	"github.com/celestiaorg/celestia-core/libs/db/memdb"
	"github.com/celestiaorg/celestia-core/libs/log"
	"github.com/celestiaorg/celestia-core/pkg/da/ipfs"
)

// MockBlockStore returns a mocked blockstore. a nil db will result in a new in memory db
func MockBlockStore(db dbm.DB) *BlockStore {
	if db == nil {
		db = memdb.NewDB()
	}

	return NewBlockStore(
		db,
		ipfs.MockBlockStore(),
		log.NewNopLogger(),
	)
}
