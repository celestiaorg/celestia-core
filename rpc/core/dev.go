package core

import (
	ctypes "github.com/celestiaorg/celestia-core/rpc/core/types"
	rpctypes "github.com/celestiaorg/celestia-core/rpc/jsonrpc/types"
)

// UnsafeFlushMempool removes all transactions from the mempool.
func (env *Environment) UnsafeFlushMempool(ctx *rpctypes.Context) (*ctypes.ResultUnsafeFlushMempool, error) {
	env.Mempool.Flush()
	return &ctypes.ResultUnsafeFlushMempool{}, nil
}
