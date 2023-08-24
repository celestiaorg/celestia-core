package factory

import "github.com/cometbft/cometbft/types"

// MakeTxs is a helper function to generate mock transactions by given the block height
// and the transaction numbers.
func MakeTxs(height int64, num int) (txs []types.Tx) {
	for i := 0; i < num; i++ {
		txs = append(txs, types.Tx([]byte{byte(height), byte(i)}))
	}
	return txs
}

func MakeTenTxs(height int64) (txs []types.Tx) {
	return MakeTxs(height, 10)
}

func MakeData(txs []types.Tx) types.Data {
	return types.Data{
		Txs: txs,
	}
}
