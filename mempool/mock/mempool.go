package mock

import (
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/clist"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

// Mempool is an empty implementation of a Mempool, useful for testing.
type Mempool struct {
	txs        map[types.TxKey]types.Tx
	evictedTxs map[types.TxKey]bool
}

var _ mempool.Mempool = &Mempool{}

func (Mempool) Lock()     {}
func (Mempool) Unlock()   {}
func (Mempool) Size() int { return 0 }
func (m *Mempool) CheckTx(tx types.Tx, _ func(*abci.Response), _ mempool.TxInfo) error {
	if m.txs == nil {
		m.txs = make(map[types.TxKey]types.Tx)
	}
	if m.evictedTxs == nil {
		m.evictedTxs = make(map[types.TxKey]bool)
	}
	m.txs[tx.Key()] = tx
	return nil
}
func (m *Mempool) RemoveTxByKey(txKey types.TxKey) error {
	delete(m.txs, txKey)
	m.evictedTxs[txKey] = true
	return nil
}
func (Mempool) ReapMaxBytesMaxGas(_, _ int64) types.Txs { return types.Txs{} }
func (Mempool) ReapMaxTxs(n int) types.Txs              { return types.Txs{} }
func (Mempool) Update(
	_ int64,
	_ types.Txs,
	_ []*abci.ResponseDeliverTx,
	_ mempool.PreCheckFunc,
	_ mempool.PostCheckFunc,
) error {
	return nil
}
func (Mempool) Flush()                        {}
func (Mempool) FlushAppConn() error           { return nil }
func (Mempool) TxsAvailable() <-chan struct{} { return make(chan struct{}) }
func (Mempool) EnableTxsAvailable()           {}
func (Mempool) SizeBytes() int64              { return 0 }

func (m Mempool) GetTxByKey(txKey types.TxKey) (types.Tx, bool) {
	tx, ok := m.txs[txKey]
	return tx, ok
}
func (m Mempool) GetTxEvicted(txKey types.TxKey) bool {
	return m.evictedTxs[txKey]
}

func (Mempool) TxsFront() *clist.CElement    { return nil }
func (Mempool) TxsWaitChan() <-chan struct{} { return nil }

func (Mempool) InitWAL() error { return nil }
func (Mempool) CloseWAL()      {}
