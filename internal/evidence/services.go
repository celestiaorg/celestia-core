package evidence

import (
	"github.com/celestiaorg/celestia-core/types"
)

//go:generate ../../scripts/mockery_generate.sh BlockStore

type BlockStore interface {
	LoadBlockMeta(height int64) *types.BlockMeta
	LoadBlockCommit(height int64) *types.Commit
	Height() int64
}
