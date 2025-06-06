package propagation

import "github.com/cometbft/cometbft/types"

type Mempool interface {
	GetTxByKey(key types.TxKey) (*types.CachedTx, bool)
}
