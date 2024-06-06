package core

import (
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	types "github.com/cometbft/cometbft/types"
)

// TxStatus retrieves the status of a transaction given its hash. It returns a ResultTxStatus
// containing the height and index of the transaction within the block.
func TxStatus(ctx *rpctypes.Context, hash []byte) (*ctypes.ResultTxStatus, error) {
	env := GetEnvironment()
	// not committed
	txKey, err := types.TxKeyFromBytes(hash)
	if err != nil {
		return nil, err
	}
	txInMempool, ok := env.Mempool.GetTxByKey(txKey)
	if txInMempool != nil && ok{
		return &ctypes.ResultTxStatus{Status: "PENDING"}, nil
	}
	isEvicted := env.Mempool.GetTxEvicted(txKey)
	if isEvicted {
		return &ctypes.ResultTxStatus{Status: "EVICTED"}, nil
	}
	// committed
	txInfo := env.BlockStore.LoadTxInfo(hash)
    if txInfo != nil {
		return &ctypes.ResultTxStatus{Height: txInfo.Height, Index: txInfo.Index, Status: "COMMITTED"}, nil
	}
	
	return &ctypes.ResultTxStatus{Status: "UNKNOWN"}, nil
}
