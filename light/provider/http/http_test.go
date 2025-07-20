package http_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/light/provider"
	lighthttp "github.com/cometbft/cometbft/light/provider/http"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctest "github.com/cometbft/cometbft/rpc/test"
	"github.com/cometbft/cometbft/types"
)

func TestNewProvider(t *testing.T) {
	c, err := lighthttp.New("chain-test", "192.168.0.1:26657")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%s", c), "http{http://192.168.0.1:26657}")

	c, err = lighthttp.New("chain-test", "http://153.200.0.1:26657")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%s", c), "http{http://153.200.0.1:26657}")

	c, err = lighthttp.New("chain-test", "153.200.0.1")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%s", c), "http{http://153.200.0.1}")
}

func TestProvider(t *testing.T) {
	app := kvstore.NewInMemoryApplication()
	app.RetainBlocks = 10
	// Force recreation of config to avoid temp directory reuse issues in multi-run tests
	node := rpctest.StartTendermint(app, rpctest.RecreateConfig)

	cfg := rpctest.GetConfig()
	rpcAddr := cfg.RPC.ListenAddress
	genDoc, err := types.GenesisDocFromFile(cfg.GenesisFile())
	require.NoError(t, err)
	chainID := genDoc.ChainID

	c, err := rpchttp.New(rpcAddr, "/websocket")
	require.Nil(t, err)

	p := lighthttp.NewWithClient(chainID, c)
	require.NoError(t, err)
	require.NotNil(t, p)

	// let it produce some blocks
	err = rpcclient.WaitForHeight(c, 10, nil)
	require.NoError(t, err)

	// let's get the highest block
	lb, err := p.LightBlock(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, lb)
	assert.True(t, lb.Height > 0)

	// Save original height before reassigning lb variable
	originalHeight := lb.Height

	// let's check this is valid somehow
	assert.Nil(t, lb.ValidateBasic(chainID))

	// historical queries now work :)
	lower := lb.Height - 3
	lb, err = p.LightBlock(context.Background(), lower)
	require.NoError(t, err)
	assert.Equal(t, lower, lb.Height)

	// fetching missing heights (both future and pruned) should return appropriate errors
	_, err = p.LightBlock(context.Background(), originalHeight+1000)
	require.Error(t, err)
	// Both ErrHeightTooHigh and ErrLightBlockNotFound are valid responses for blocks that are too far in the future
	// depending on the node's internal state and RPC error message format
	assert.True(t, err == provider.ErrHeightTooHigh || err == provider.ErrLightBlockNotFound,
		"expected ErrHeightTooHigh or ErrLightBlockNotFound, got: %v", err)

	_, err = p.LightBlock(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, provider.ErrLightBlockNotFound, err)

	// fetching with the context canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.LightBlock(ctx, lower+3)
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)

	// fetching with the deadline exceeded (a mock RPC client is used to simulate this)
	c2, err := newMockHTTP(rpcAddr)
	require.NoError(t, err)
	p2 := lighthttp.NewWithClient(chainID, c2)
	_, err = p2.LightBlock(context.Background(), 0)
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)

	// stop the full node and check that a no response error is returned
	rpctest.StopTendermint(node)
	time.Sleep(10 * time.Second)
	lb, err = p.LightBlock(context.Background(), lower+2)
	// we should see a connection refused
	require.Error(t, err)
	require.Contains(t, err.Error(), "connection refused")
	require.Nil(t, lb)
}

type mockHTTP struct {
	*rpchttp.HTTP
}

func newMockHTTP(remote string) (*mockHTTP, error) {
	c, err := rpchttp.New(remote, "/websocket")
	if err != nil {
		return nil, err
	}
	return &mockHTTP{c}, nil
}

func (m *mockHTTP) Validators(ctx context.Context, height *int64, page, perPage *int) (*ctypes.ResultValidators, error) {
	return nil, fmt.Errorf("post failed: %w", context.DeadlineExceeded)
}
