package store

import (
	"github.com/lazyledger/lazyledger-core/ipfs"
	dbm "github.com/lazyledger/lazyledger-core/libs/db"
	"github.com/lazyledger/lazyledger-core/libs/db/memdb"
	"github.com/lazyledger/lazyledger-core/libs/log"
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
