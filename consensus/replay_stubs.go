package consensus

import (
	"context"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/clist"
	mempl "github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

//-----------------------------------------------------------------------------

type emptyMempool struct{}

var _ mempl.Mempool = emptyMempool{}

func (emptyMempool) Lock()            {}
func (emptyMempool) Unlock()          {}
func (emptyMempool) Size() int        { return 0 }
func (emptyMempool) SizeBytes() int64 { return 0 }
func (emptyMempool) CheckTx(types.Tx, func(*abci.ResponseCheckTx), mempl.TxInfo) error {
	return nil
}

<<<<<<< HEAD
func (emptyMempool) GetTxByKey(types.TxKey) (*types.CachedTx, bool) { return nil, false }
func (emptyMempool) WasRecentlyEvicted(types.TxKey) bool            { return false }
func (emptyMempool) WasRecentlyRejected(types.TxKey) (bool, uint32) { return false, 0 }
=======
func (emptyMempool) GetTxByKey(types.TxKey) (types.Tx, bool)         { return nil, false }
func (emptyMempool) WasRecentlyEvicted(types.TxKey) bool             { return false }
func (emptyMempool) IsRejectedTx(types.TxKey) (bool, uint32, string) { return false, 0, "" }

>>>>>>> ec6fdcad (feat!: start tracking rejection logs (#2286))
func (txmp emptyMempool) RemoveTxByKey(types.TxKey) error {
	return nil
}

func (emptyMempool) ReapMaxBytesMaxGas(int64, int64) []*types.CachedTx {
	return []*types.CachedTx{}
}
func (emptyMempool) ReapMaxTxs(int) []*types.CachedTx { return []*types.CachedTx{} }
func (emptyMempool) Update(
	int64,
	[]*types.CachedTx,
	[]*abci.ExecTxResult,
	mempl.PreCheckFunc,
	mempl.PostCheckFunc,
) error {
	return nil
}
func (emptyMempool) Flush()                        {}
func (emptyMempool) FlushAppConn() error           { return nil }
func (emptyMempool) TxsAvailable() <-chan struct{} { return make(chan struct{}) }
func (emptyMempool) EnableTxsAvailable()           {}
func (emptyMempool) TxsBytes() int64               { return 0 }

func (emptyMempool) TxsFront() *clist.CElement    { return nil }
func (emptyMempool) TxsWaitChan() <-chan struct{} { return nil }

func (emptyMempool) InitWAL() error { return nil }
func (emptyMempool) CloseWAL()      {}

//-----------------------------------------------------------------------------
// mockProxyApp uses ABCIResponses to give the right results.
//
// Useful because we don't want to call Commit() twice for the same block on
// the real app.

func newMockProxyApp(finalizeBlockResponse *abci.ResponseFinalizeBlock) proxy.AppConnConsensus {
	clientCreator := proxy.NewLocalClientCreator(&mockProxyApp{
		finalizeBlockResponse: finalizeBlockResponse,
	})
	cli, _ := clientCreator.NewABCIClient()
	err := cli.Start()
	if err != nil {
		panic(err)
	}
	return proxy.NewAppConnConsensus(cli, proxy.NopMetrics())
}

type mockProxyApp struct {
	abci.BaseApplication
	finalizeBlockResponse *abci.ResponseFinalizeBlock
}

func (mock *mockProxyApp) FinalizeBlock(context.Context, *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	return mock.finalizeBlockResponse, nil
}
