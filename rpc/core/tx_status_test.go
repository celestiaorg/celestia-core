package core

import (
	"testing"

	mempl "github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/mock"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	types "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxStatus(t *testing.T) {
	env := &Environment{}
	mempool := mock.Mempool{}
	env.Mempool = &mempool
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
				height := int64(50)
				blocks := randomBlocks(height)
				blockStore = mockBlockStore{
					height: height,
					blocks: blocks,
				}
				env.BlockStore = blockStore
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
				for _, tx := range txs {
					err := mempool.CheckTx(tx, nil, mempl.TxInfo{})
					require.NoError(t, err)
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
				for _, tx := range txs {
					err := mempool.CheckTx(tx, nil, mempl.TxInfo{})
					require.NoError(t, err)
					err = mempool.RemoveTxByKey(tx.Key())
					require.NoError(t, err)
				}
			},
			expectedStatus: "EVICTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			txs := makeTxs(2)

			tt.setup(env, txs)

			// Check the status of each transaction
			for _, tx := range txs {
				txStatus, _ := TxStatus(&rpctypes.Context{}, tx.Hash())
				assert.Equal(t, tt.expectedStatus, txStatus.Status)
			}

			// Check the height and index of each transaction if they are committed
			if blockStore.height > 0 && tt.expectedStatus == "COMMITTED" {
				for _, block := range blockStore.blocks {
					for i, tx := range block.Txs {
						txStatus, _ := TxStatus(&rpctypes.Context{}, tx.Hash())
						assert.Equal(t, block.Height, txStatus.Height)
						assert.Equal(t, int64(i), txStatus.Index)
					}
				}
			}
		})
	}
}
