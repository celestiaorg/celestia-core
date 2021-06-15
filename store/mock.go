package store

import (
	"github.com/lazyledger/lazyledger-core/ipfs"
	"github.com/lazyledger/lazyledger-core/libs/db/memdb"
	"github.com/lazyledger/lazyledger-core/libs/log"
)

func MockBlockStore() *BlockStore {
	return NewBlockStore(
		memdb.NewDB(),
		ipfs.MockBlockStore(),
		ipfs.MockRouting(),
		log.NewNopLogger(),
	)
}
