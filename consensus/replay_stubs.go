package consensus

import (
	"context"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/clist"
	mempl "github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

//-----------------------------------------------------------------------------
// emptyMempool Mock Implementation
//-----------------------------------------------------------------------------

// emptyMempool implements the mempl.Mempool interface but performs no actions,
// returning zero values or empty results. This is useful for testing consensus
// components that require the interface but should not rely on live mempool behavior.
type emptyMempool struct{}

var _ mempl.Mempool = emptyMempool{}

// Lock/Unlock are necessary for the interface but do nothing in this mock.
func (m emptyMempool) Lock()    {}
func (m emptyMempool) Unlock()  {}

// Size and SizeBytes always return zero, indicating an empty mempool.
func (m emptyMempool) Size() int      { return 0 }
func (m emptyMempool) SizeBytes() int64 { return 0 }

// CheckTx always succeeds but performs no validation.
func (m emptyMempool) CheckTx(types.Tx, func(*abci.ResponseCheckTx), mempl.TxInfo) error {
	return nil
}

// GetTxByKey always returns nil, false as no transactions are stored.
func (m emptyMempool) GetTxByKey(types.TxKey) (*types.CachedTx, bool)       { return nil, false }
func (m emptyMempool) WasRecentlyEvicted(types.TxKey) bool                   { return false }
func (m emptyMempool) WasRecentlyRejected(types.TxKey) (bool, uint32, string) { return false, 0, "" }

func (m emptyMempool) RemoveTxByKey(types.TxKey) error {
	return nil
}

// ReapMaxBytesMaxGas returns an empty slice as there are no transactions to reap.
func (m emptyMempool) ReapMaxBytesMaxGas(int64, int64) []*types.CachedTx {
	return []*types.CachedTx{}
}

// ReapMaxTxs returns an empty slice.
func (m emptyMempool) ReapMaxTxs(int) []*types.CachedTx { return []*types.CachedTx{} }

// Update succeeds but performs no actual state update.
func (m emptyMempool) Update(
	int64,
	[]*types.CachedTx,
	[]*abci.ExecTxResult,
	mempl.PreCheckFunc,
	mempl.PostCheckFunc,
) error {
	return nil
}

// Flush operations do nothing.
func (m emptyMempool) Flush()           {}
func (m emptyMempool) FlushAppConn() error { return nil }
func (m emptyMempool) TxsBytes() int64 { return 0 }

// TxsAvailable returns a new, unwritten channel, blocking if read from,
// simulating an environment where no transactions ever become available.
func (m emptyMempool) TxsAvailable() <-chan struct{} { return make(chan struct{}) }
func (m emptyMempool) EnableTxsAvailable()          {}

func (m emptyMempool) TxsFront() *clist.CElement    { return nil }
func (m emptyMempool) TxsWaitChan() <-chan struct{} { return nil }

func (m emptyMempool) InitWAL() error { return nil }
func (m emptyMempool) CloseWAL()      {}

//-----------------------------------------------------------------------------
// mockProxyApp Implementation
//-----------------------------------------------------------------------------

// newMockProxyApp creates a proxy.AppConnConsensus connection that delegates to a
// mock application, allowing us to control the ABCI responses (e.g., FinalizeBlock).
func newMockProxyApp(finalizeBlockResponse *abci.ResponseFinalizeBlock) proxy.AppConnConsensus {
	clientCreator := proxy.NewLocalClientCreator(&mockProxyApp{
		finalizeBlockResponse: finalizeBlockResponse,
	})
	cli, err := clientCreator.NewABCIClient()
	if err != nil {
		panic(fmt.Errorf("failed to create mock ABCI client: %w", err))
	}
	err = cli.Start()
	if err != nil {
		panic(fmt.Errorf("failed to start mock ABCI client: %w", err))
	}
	return proxy.NewAppConnConsensus(cli, proxy.NopMetrics())
}

// mockProxyApp is an implementation of abci.BaseApplication that only overrides
// FinalizeBlock to return a predefined response.
type mockProxyApp struct {
	abci.BaseApplication
	finalizeBlockResponse *abci.ResponseFinalizeBlock
}

// FinalizeBlock returns the predetermined mock response, simulating the application's
// execution logic without running the actual app.
func (mock *mockProxyApp) FinalizeBlock(context.Context, *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	return mock.finalizeBlockResponse, nil
}
