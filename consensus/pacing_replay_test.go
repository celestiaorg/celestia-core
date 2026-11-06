package consensus

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/abci/types/mocks"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
)

func TestHandshakeStoresDynamicTimeoutCommit(t *testing.T) {
	app := &mocks.Application{}
	app.On("Info", mock.Anything, mock.Anything).Return(&abci.ResponseInfo{}, nil)
	app.On("InitChain", mock.Anything, mock.Anything).Return(&abci.ResponseInitChain{
		TimeoutInfo: abci.TimeoutInfo{TimeoutCommit: time.Nanosecond, DynamicTimeoutCommit: 800 * time.Millisecond},
	}, nil)
	config := ResetConfig("handshake_dynamic_test_")
	t.Cleanup(func() { os.RemoveAll(config.RootDir) })
	stateDB, state, store := stateAndStore(t, config, 0x0)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{})
	genDoc, err := sm.MakeGenesisDocFromFile(config.GenesisFile())
	require.NoError(t, err)
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	t.Cleanup(func() { require.NoError(t, proxyApp.Stop()) })

	_, err = NewHandshaker(stateStore, state, store, genDoc).Handshake(proxyApp)
	require.NoError(t, err)

	state, err = stateStore.Load()
	require.NoError(t, err)
	require.Equal(t, 800*time.Millisecond, state.Timeouts.DynamicTimeoutCommit)
	require.Equal(t, time.Nanosecond, state.Timeouts.TimeoutCommit)
}
