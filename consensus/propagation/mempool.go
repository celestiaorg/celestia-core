package propagation

import "github.com/tendermint/tendermint/types"

type Mempool interface {
	GetTxByKey(key types.TxKey) (types.Tx, bool)
}
