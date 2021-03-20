package txindex_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	abci "github.com/lazyledger/lazyledger-core/abci/types"
	"github.com/lazyledger/lazyledger-core/libs/db/memdb"
	"github.com/lazyledger/lazyledger-core/libs/log"
	"github.com/lazyledger/lazyledger-core/state/txindex"
	"github.com/lazyledger/lazyledger-core/state/txindex/kv"
	"github.com/lazyledger/lazyledger-core/types"
)

func TestIndexerServiceIndexesBlocks(t *testing.T) {
	// event bus
	eventBus := types.NewEventBus()
	eventBus.SetLogger(log.TestingLogger())
	err := eventBus.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := eventBus.Stop(); err != nil {
			t.Error(err)
		}
	})

	// tx indexer
	store := memdb.NewDB()
	txIndexer := kv.NewTxIndex(store)

	service := txindex.NewIndexerService(txIndexer, eventBus)
	service.SetLogger(log.TestingLogger())
	err = service.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
	})

	// publish block with txs
	err = eventBus.PublishEventNewBlockHeader(types.EventDataNewBlockHeader{
		Header: types.Header{Height: 1},
		NumTxs: int64(2),
	})
	require.NoError(t, err)
	txResult1 := &abci.TxResult{
		Height: 1,
		Index:  uint32(0),
		Tx:     types.Tx("foo"),
		Result: abci.ResponseDeliverTx{Code: 0},
	}
	err = eventBus.PublishEventTx(types.EventDataTx{TxResult: *txResult1})
	require.NoError(t, err)
	txResult2 := &abci.TxResult{
		Height: 1,
		Index:  uint32(1),
		Tx:     types.Tx("bar"),
		Result: abci.ResponseDeliverTx{Code: 0},
	}
	err = eventBus.PublishEventTx(types.EventDataTx{TxResult: *txResult2})
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// check the result
	res, err := txIndexer.Get(types.Tx("foo").Hash())
	assert.NoError(t, err)
	assert.Equal(t, txResult1, res)
	res, err = txIndexer.Get(types.Tx("bar").Hash())
	assert.NoError(t, err)
	assert.Equal(t, txResult2, res)
}
