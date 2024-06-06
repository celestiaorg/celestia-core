package core

import (
	"fmt"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	types "github.com/cometbft/cometbft/types"
)

// TxStatus retrieves the status of a transaction given its hash. It returns a ResultTxStatus
// containing the height and index of the transaction within the block(if committed).
func TxStatus(ctx *rpctypes.Context, hash []byte) (*ctypes.ResultTxStatus, error) {
	env := GetEnvironment()

	// Get the tx key from the hash
	txKey, err := types.TxKeyFromBytes(hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get tx key from hash: %v", err)
	}

	// Check if the tx is in the mempool
	txInMempool, ok := env.Mempool.GetTxByKey(txKey)
	if txInMempool != nil && ok {
		return &ctypes.ResultTxStatus{Status: "PENDING"}, nil
	}

	// Check if the tx is evicted
	isEvicted := env.Mempool.GetTxEvicted(txKey)
	if isEvicted {
		return &ctypes.ResultTxStatus{Status: "EVICTED"}, nil
	}

	// Check if the tx has been committed
	txInfo := env.BlockStore.LoadTxInfo(hash)
	if txInfo != nil {
		return &ctypes.ResultTxStatus{Height: txInfo.Height, Index: txInfo.Index, Status: "COMMITTED"}, nil
	}

	// If the tx is not in the mempool, evicted, or committed, return unknown
	return &ctypes.ResultTxStatus{Status: "UNKNOWN"}, nil
}
