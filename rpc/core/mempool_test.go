package core

import (
	"testing"

	mempoolmocks "github.com/cometbft/cometbft/mempool/mocks"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
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
