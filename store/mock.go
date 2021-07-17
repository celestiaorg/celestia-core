package store

import (
	"github.com/celestiaorg/celestia-core/ipfs"
	dbm "github.com/celestiaorg/celestia-core/libs/db"
	"github.com/celestiaorg/celestia-core/libs/db/memdb"
	"github.com/celestiaorg/celestia-core/libs/log"
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
