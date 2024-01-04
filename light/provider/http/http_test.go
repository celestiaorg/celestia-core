package http_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/abci/example/kvstore"
	"github.com/tendermint/tendermint/light/provider"
	lighthttp "github.com/tendermint/tendermint/light/provider/http"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctest "github.com/tendermint/tendermint/rpc/test"
	"github.com/tendermint/tendermint/types"
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
	app := kvstore.NewApplication()
	app.RetainBlocks = 10
	node := rpctest.StartTendermint(app)

	cfg := rpctest.GetConfig()
	defer os.RemoveAll(cfg.RootDir)
	rpcAddr := cfg.RPC.ListenAddress
	genDoc, err := types.GenesisDocFromFile(cfg.GenesisFile())
	require.NoError(t, err)
	chainID := genDoc.ChainID

	c, err := rpchttp.New(rpcAddr, "/websocket")
	require.NoError(t, err)

	p := lighthttp.NewWithClient(chainID, c)
	require.NotNil(t, p)

	// let it produce some blocks
	err = rpcclient.WaitForHeight(c, 10, nil)
	require.NoError(t, err)

	// let's get the highest block
	lb, err := p.LightBlock(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, lb)
	assert.True(t, lb.Height < 1000)

	// let's check this is valid somehow
	assert.Nil(t, lb.ValidateBasic(chainID))

	// historical queries now work :)
	lower := lb.Height - 3
	lb, err = p.LightBlock(context.Background(), lower)
	require.NoError(t, err)
	assert.Equal(t, lower, lb.Height)

	// fetching missing heights (both future and pruned) should return appropriate errors
	lb, err = p.LightBlock(context.Background(), 1000)
	require.Error(t, err)
	require.Nil(t, lb)
	assert.Equal(t, provider.ErrHeightTooHigh, err)

	_, err = p.LightBlock(context.Background(), 1)
	require.Error(t, err)
	require.Nil(t, lb)
	assert.Equal(t, provider.ErrLightBlockNotFound, err)

	// fetching with the context cancelled
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
