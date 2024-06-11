package core

import (
	"testing"

	mock "github.com/cometbft/cometbft/rpc/core/mocks"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	types "github.com/cometbft/cometbft/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

// TestTxStatus tests the TxStatus function in the RPC core
// making sure it fetches the correct status for each transaction.
func TestTxStatus(t *testing.T) {
	// Create a controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a new environment
	env := &Environment{}

	// Create a new mempool and block store
	mempool := mock.NewMockMempool(ctrl)
	env.Mempool = mempool
	blockStore := mockBlockStore{
		height: 0,
		blocks: nil,
	}
	env.BlockStore = blockStore
	SetEnvironment(env)

	tests := []struct {
		name           string
		setup          func(*Environment, []types.Tx)
		expectedStatus string
	}{
		{
			name: "Committed",
			setup: func(env *Environment, txs []types.Tx) {
				height := int64(5)
				blocks := randomBlocks(height)
				blockStore = mockBlockStore{
					height: height,
					blocks: blocks,
				}
				env.BlockStore = blockStore
				for _, tx := range txs {
					// Set GetTxByKey to return nil and false for all transactions
					mempool.EXPECT().GetTxByKey(tx.Key()).Return(nil, false).AnyTimes()
					// Set GetTxEvicted to return false for all transactions
					mempool.EXPECT().GetTxEvicted(tx.Key()).Return(false).AnyTimes()
				}
			},
			expectedStatus: "COMMITTED",
		},
		{
			name: "Unknown",
			setup: func(env *Environment, txs []types.Tx) {
				env.BlockStore = mockBlockStore{
					height: 0,
					blocks: nil,
				}
				for _, tx := range txs {
					// Set GetTxByKey to return nil and false for all transactions
					mempool.EXPECT().GetTxByKey(tx.Key()).Return(nil, false).AnyTimes()
					// Set GetTxEvicted to return false for all transactions
					mempool.EXPECT().GetTxEvicted(tx.Key()).Return(false).AnyTimes()
				}
			},

			expectedStatus: "UNKNOWN",
		},
		{
			name: "Pending",
			setup: func(env *Environment, txs []types.Tx) {
				env.BlockStore = mockBlockStore{
					height: 0,
					blocks: nil,
				}
				// Reset the mempool
				mempool = mock.NewMockMempool(ctrl)
				env.Mempool = mempool

				for _, tx := range txs {
					// Set GetTxByKey to return the transaction and true for all transactions
					mempool.EXPECT().GetTxByKey(tx.Key()).Return(tx, true).AnyTimes()
				}
			},
			expectedStatus: "PENDING",
		},
		{
			name: "Evicted",
			setup: func(env *Environment, txs []types.Tx) {
				env.BlockStore = mockBlockStore{
					height: 0,
					blocks: nil,
				}
				// Reset the mempool
				mempool = mock.NewMockMempool(ctrl)
				env.Mempool = mempool

				for _, tx := range txs {
					// Set GetTxByKey to return nil and false for all transactions
					mempool.EXPECT().GetTxByKey(tx.Key()).Return(nil, false).AnyTimes()
					// Set GetTxEvicted to return true for all transactions
					mempool.EXPECT().GetTxEvicted(tx.Key()).Return(true).AnyTimes()
				}
			},
			expectedStatus: "EVICTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			height := int64(2)
			// Create a set of transactions on the specified height
			txs := makeTxs(height)

			tt.setup(env, txs)

			// Check the status of each transaction
			for i, tx := range txs {
				txStatus, _ := TxStatus(&rpctypes.Context{}, tx.Hash())
				assert.Equal(t, tt.expectedStatus, txStatus.Status)

				// Check the height and index of transactions that are committed
				if blockStore.height > 0 && tt.expectedStatus == "COMMITTED" {
					txStatus, _ := TxStatus(&rpctypes.Context{}, tx.Hash())

					assert.Equal(t, txStatus.Status, tt.expectedStatus)
					assert.Equal(t, height, txStatus.Height)
					assert.Equal(t, int64(i), txStatus.Index)
				}
			}

		})
	}
}
