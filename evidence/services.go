package evidence

import (
	"github.com/lazyledger/lazyledger-core/types"
)

//go:generate mockery --case underscore --name BlockStore

type BlockStore interface {
	LoadBlockMeta(height int64) *types.BlockMeta
	LoadBlockCommit(height int64) *types.Commit
}
