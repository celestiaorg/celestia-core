package core

import (
	"testing"

	mempoolmocks "github.com/cometbft/cometbft/mempool/mocks"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnconfirmedTxs(t *testing.T) {
	mempool := &mempoolmocks.Mempool{}
	mempool.On("ReapMaxTxs", 30).Return([]*types.CachedTx{})
	mempool.On("Size").Return(0)
	mempool.On("SizeBytes").Return(int64(0))
	env := &Environment{
		Mempool: mempool,
	}

	t.Run("should not panic with nil limit", func(t *testing.T) {
		_, err := env.UnconfirmedTxs(&rpctypes.Context{}, nil)
		require.NoError(t, err)
	})
}

func TestTxStatus_Preconfirmation(t *testing.T) {
	// Create a test transaction
	testTx := types.Tx("test transaction")
	txHash := testTx.Hash()
	txKey := testTx.Key()

	tests := []struct {
		name                       string
		setupMocks                 func(*mempoolmocks.Mempool)
		expectedStatus             string
		expectedPreconfVotingPower int64
		expectedPreconfProportion  float64
	}{
		{
			name: "pending transaction with partial preconfirmation",
			setupMocks: func(m *mempoolmocks.Mempool) {
				cachedTx := &types.CachedTx{Tx: testTx}
				m.On("GetTxByKey", txKey).Return(cachedTx, true)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(300))
				m.On("GetValidatorSetTotalPower").Return(int64(1000))
			},
			expectedStatus:             TxStatusPending,
			expectedPreconfVotingPower: 300,
			expectedPreconfProportion:  0.3,
		},
		{
			name: "pending transaction with full preconfirmation",
			setupMocks: func(m *mempoolmocks.Mempool) {
				cachedTx := &types.CachedTx{Tx: testTx}
				m.On("GetTxByKey", txKey).Return(cachedTx, true)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(1000))
				m.On("GetValidatorSetTotalPower").Return(int64(1000))
			},
			expectedStatus:             TxStatusPending,
			expectedPreconfVotingPower: 1000,
			expectedPreconfProportion:  1.0,
		},
		{
			name: "pending transaction with no preconfirmation",
			setupMocks: func(m *mempoolmocks.Mempool) {
				cachedTx := &types.CachedTx{Tx: testTx}
				m.On("GetTxByKey", txKey).Return(cachedTx, true)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(0))
				m.On("GetValidatorSetTotalPower").Return(int64(1000))
			},
			expectedStatus:             TxStatusPending,
			expectedPreconfVotingPower: 0,
			expectedPreconfProportion:  0.0,
		},
		{
			name: "evicted transaction with preconfirmation data",
			setupMocks: func(m *mempoolmocks.Mempool) {
				m.On("GetTxByKey", txKey).Return(&types.CachedTx{}, false)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(200))
				m.On("GetValidatorSetTotalPower").Return(int64(1000))
				m.On("WasRecentlyEvicted", txKey).Return(true)
			},
			expectedStatus:             TxStatusEvicted,
			expectedPreconfVotingPower: 200,
			expectedPreconfProportion:  0.2,
		},
		{
			name: "rejected transaction with preconfirmation data",
			setupMocks: func(m *mempoolmocks.Mempool) {
				m.On("GetTxByKey", txKey).Return(&types.CachedTx{}, false)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(500))
				m.On("GetValidatorSetTotalPower").Return(int64(1000))
				m.On("WasRecentlyEvicted", txKey).Return(false)
				m.On("WasRecentlyRejected", txKey).Return(true, uint32(1), "invalid transaction")
			},
			expectedStatus:             TxStatusRejected,
			expectedPreconfVotingPower: 500,
			expectedPreconfProportion:  0.5,
		},
		{
			name: "unknown transaction with no preconfirmation",
			setupMocks: func(m *mempoolmocks.Mempool) {
				m.On("GetTxByKey", txKey).Return(&types.CachedTx{}, false)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(0))
				m.On("GetValidatorSetTotalPower").Return(int64(1000))
				m.On("WasRecentlyEvicted", txKey).Return(false)
				m.On("WasRecentlyRejected", txKey).Return(false, uint32(0), "")
			},
			expectedStatus:             TxStatusUnknown,
			expectedPreconfVotingPower: 0,
			expectedPreconfProportion:  0.0,
		},
		{
			name: "pending transaction with no validator set",
			setupMocks: func(m *mempoolmocks.Mempool) {
				cachedTx := &types.CachedTx{Tx: testTx}
				m.On("GetTxByKey", txKey).Return(cachedTx, true)
				m.On("GetPreconfirmationVotingPower", txKey).Return(int64(0))
				m.On("GetValidatorSetTotalPower").Return(int64(0))
			},
			expectedStatus:             TxStatusPending,
			expectedPreconfVotingPower: 0,
			expectedPreconfProportion:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			mempool := &mempoolmocks.Mempool{}
			tt.setupMocks(mempool)

			env := &Environment{
				Mempool: mempool,
				BlockStore: &mockBlockStore{
					height: 0,
					blocks: []*types.Block{},
				},
			}

			// Execute
			result, err := env.TxStatus(&rpctypes.Context{}, txHash)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, result.Status)
			assert.Equal(t, tt.expectedPreconfVotingPower, result.PreconfirmationVotingPower)
			assert.InDelta(t, tt.expectedPreconfProportion, result.PreconfirmedProportion, 0.0001)

			// Verify all expectations were met
			mempool.AssertExpectations(t)
		})
	}
}
