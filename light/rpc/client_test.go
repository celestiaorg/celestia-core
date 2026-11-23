package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/light/rpc/mocks"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
)

// nextClient embeds the rpcclient.Client interface so every method except the
// one we override panics if unexpectedly called. It lets the test control what
// the underlying RPC server returns for ConsensusParams.
type nextClient struct {
	rpcclient.Client
	res *ctypes.ResultConsensusParams
}

func (n nextClient) ConsensusParams(_ context.Context, _ *int64) (*ctypes.ResultConsensusParams, error) {
	return n.res, nil
}

// A malicious primary can answer a ConsensusParams request for height H with
// authentic parameters from a different height X. The response must be rejected
// on the height mismatch, before the params are authenticated against X's light
// block (so VerifyLightBlockAtHeight is never reached).
func TestConsensusParamsRejectsWrongHeightResponse(t *testing.T) {
	const (
		requestedHeight int64 = 100 // what the caller asks for
		responseHeight  int64 = 1   // what the malicious primary answers with
	)

	// Authentic historical params from height X=1.
	paramsX := types.DefaultConsensusParams()
	paramsX.Block.MaxBytes = 1 * 1024 * 1024

	res := &ctypes.ResultConsensusParams{
		BlockHeight:     responseHeight,
		ConsensusParams: *paramsX,
	}

	// No light-client interaction is expected: the request is rejected before
	// hash verification. A bare mock with no expectations would fail the test if
	// any method were called.
	lc := &mocks.LightClient{}

	c := NewClient(nextClient{res: res}, lc)

	h := requestedHeight
	_, err := c.ConsensusParams(context.Background(), &h)
	require.Error(t, err, "wrapper accepted params from height %d for a request at height %d",
		responseHeight, requestedHeight)
	lc.AssertNotCalled(t, "VerifyLightBlockAtHeight")
}

// When the response height matches the requested height, verification succeeds.
func TestConsensusParamsAcceptsMatchingHeightResponse(t *testing.T) {
	const height int64 = 100

	params := types.DefaultConsensusParams()
	res := &ctypes.ResultConsensusParams{
		BlockHeight:     height,
		ConsensusParams: *params,
	}
	lightBlock := &types.LightBlock{
		SignedHeader: &types.SignedHeader{
			Header: &types.Header{ConsensusHash: params.Hash()},
		},
	}
	lc := &mocks.LightClient{}
	lc.On("VerifyLightBlockAtHeight", mock.Anything, height, mock.Anything).
		Return(lightBlock, nil)

	c := NewClient(nextClient{res: res}, lc)

	h := height
	got, err := c.ConsensusParams(context.Background(), &h)
	require.NoError(t, err)
	require.Equal(t, height, got.BlockHeight)
}

// nextTxClient stubs the underlying RPC client's Tx method.
type nextTxClient struct {
	rpcclient.Client
	res *ctypes.ResultTx //nolint:staticcheck // testing the deprecated endpoint's verification
}

//nolint:staticcheck // testing the deprecated endpoint's verification
func (n nextTxClient) Tx(_ context.Context, _ []byte, _ bool) (*ctypes.ResultTx, error) {
	return n.res, nil
}

// A malicious upstream can answer a Tx request with a different, genuine
// transaction. The response must be rejected on the hash mismatch before any
// light-client verification runs.
func TestTxRejectsMismatchedHash(t *testing.T) {
	res := &ctypes.ResultTx{ //nolint:staticcheck // testing the deprecated endpoint's verification
		Height: 5,
		Tx:     types.Tx("returned tx"),
	}
	lc := &mocks.LightClient{}
	c := NewClient(nextTxClient{res: res}, lc)

	requestedHash := types.Tx("requested tx").Hash()
	_, err := c.Tx(context.Background(), requestedHash, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match requested hash")
	lc.AssertNotCalled(t, "VerifyLightBlockAtHeight")
}

// nextBlockClient stubs the underlying RPC client's hash-selected block and
// header methods.
type nextBlockClient struct {
	rpcclient.Client
	block *types.Block
}

func (n nextBlockClient) BlockByHash(_ context.Context, _ []byte) (*ctypes.ResultBlock, error) {
	return &ctypes.ResultBlock{
		BlockID: types.BlockID{Hash: n.block.Hash()},
		Block:   n.block,
	}, nil
}

func (n nextBlockClient) HeaderByHash(_ context.Context, _ cmtbytes.HexBytes) (*ctypes.ResultHeader, error) {
	return &ctypes.ResultHeader{Header: &n.block.Header}, nil
}

// makeValidBlock returns a block that passes ValidateBasic and has a non-nil
// hash, so only the requested-hash check can reject it.
func makeValidBlock(t *testing.T, height int64) *types.Block {
	t.Helper()
	block := types.MakeBlock(height, types.Data{}, &types.Commit{}, nil)
	block.ProposerAddress = tmhash.SumTruncated([]byte("proposer"))
	block.ValidatorsHash = tmhash.Sum([]byte("validators"))
	require.NoError(t, block.ValidateBasic())
	require.NotNil(t, block.Hash())
	return block
}

// A malicious upstream can answer a BlockByHash request with a different,
// genuine block. The response must be rejected on the hash mismatch before any
// light-client verification runs.
func TestBlockByHashRejectsMismatchedHash(t *testing.T) {
	lc := &mocks.LightClient{}
	c := NewClient(nextBlockClient{block: makeValidBlock(t, 5)}, lc)

	requestedHash := tmhash.Sum([]byte("requested block"))
	_, err := c.BlockByHash(context.Background(), requestedHash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match requested hash")
	lc.AssertNotCalled(t, "VerifyLightBlockAtHeight")
}

// A malicious upstream can answer a HeaderByHash request with a different,
// genuine header. The response must be rejected on the hash mismatch before
// any light-client verification runs.
func TestHeaderByHashRejectsMismatchedHash(t *testing.T) {
	lc := &mocks.LightClient{}
	c := NewClient(nextBlockClient{block: makeValidBlock(t, 5)}, lc)

	requestedHash := tmhash.Sum([]byte("requested header"))
	_, err := c.HeaderByHash(context.Background(), requestedHash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match requested hash")
	lc.AssertNotCalled(t, "VerifyLightBlockAtHeight")
}

// nextEventsClient hands out one event channel for every subscription.
type nextEventsClient struct {
	rpcclient.Client
	out chan ctypes.ResultEvent
}

func (n nextEventsClient) Subscribe(context.Context, string, string, ...int) (<-chan ctypes.ResultEvent, error) {
	return n.out, nil
}

func (n nextEventsClient) Unsubscribe(context.Context, string, string) error { return nil }

func (n nextEventsClient) UnsubscribeAll(context.Context, string) error { return nil }

type wsConn struct {
	ctx context.Context
}

func (wsConn) GetRemoteAddr() string                                        { return "remote" }
func (wsConn) WriteRPCResponse(context.Context, rpctypes.RPCResponse) error { return nil }
func (wsConn) TryWriteRPCResponse(rpctypes.RPCResponse) bool                { return true }
func (c wsConn) Context() context.Context                                   { return c.ctx }

// The goroutine that forwards events to a websocket client must stop when the
// client unsubscribes or disconnects.
func TestSubscribeWSStopsForwarding(t *testing.T) {
	// forwarding reports whether something still reads the event channel.
	forwarding := func(out chan ctypes.ResultEvent) bool {
		select {
		case out <- ctypes.ResultEvent{}:
			return true
		case <-time.After(20 * time.Millisecond):
			return false
		}
	}

	testCases := []struct {
		name string
		stop func(c *Client, ctx *rpctypes.Context, disconnect context.CancelFunc)
	}{
		{"unsubscribe", func(c *Client, ctx *rpctypes.Context, _ context.CancelFunc) {
			_, err := c.UnsubscribeWS(ctx, "query")
			require.NoError(t, err)
		}},
		{"unsubscribe all", func(c *Client, ctx *rpctypes.Context, _ context.CancelFunc) {
			_, err := c.UnsubscribeAllWS(ctx)
			require.NoError(t, err)
		}},
		{"disconnect", func(_ *Client, _ *rpctypes.Context, disconnect context.CancelFunc) {
			disconnect()
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := make(chan ctypes.ResultEvent)
			c := NewClient(nextEventsClient{out: out}, &mocks.LightClient{})

			connCtx, disconnect := context.WithCancel(context.Background())
			defer disconnect()
			ctx := &rpctypes.Context{
				JSONReq: &rpctypes.RPCRequest{ID: rpctypes.JSONRPCStringID("1")},
				WSConn:  wsConn{ctx: connCtx},
			}

			_, err := c.SubscribeWS(ctx, "query")
			require.NoError(t, err)
			require.Eventually(t, func() bool { return forwarding(out) }, 5*time.Second, time.Millisecond)

			tc.stop(c, ctx, disconnect)
			require.Eventually(t, func() bool { return !forwarding(out) }, 5*time.Second, time.Millisecond)

			// The stopped subscription is forgotten.
			require.Eventually(t, func() bool {
				c.wsSubsMtx.Lock()
				defer c.wsSubsMtx.Unlock()
				return len(c.wsSubs) == 0
			}, 5*time.Second, time.Millisecond)
		})
	}
}

// Update returns no light block and no error when the primary has nothing
// newer than the latest trusted light block. Requests without a height must
// then be served from the latest trusted light block instead of panicking.
func TestNoHeightRequestsUseLatestTrustedLightBlock(t *testing.T) {
	const latestTrustedHeight int64 = 0

	trustedBlock := makeValidBlock(t, 5)
	validator := types.NewValidator(ed25519.GenPrivKey().PubKey(), 10)
	trusted := &types.LightBlock{
		SignedHeader: &types.SignedHeader{Header: &trustedBlock.Header, Commit: &types.Commit{}},
		ValidatorSet: types.NewValidatorSet([]*types.Validator{validator}),
	}

	lc := &mocks.LightClient{}
	lc.On("Update", mock.Anything, mock.Anything).Return(nil, nil)
	lc.On("TrustedLightBlock", latestTrustedHeight).Return(trusted, nil)
	c := NewClient(nextBlockClient{}, lc)
	ctx := context.Background()

	commit, err := c.Commit(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, trustedBlock.Height, commit.Height)

	header, err := c.Header(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, trustedBlock.Height, header.Header.Height)

	validators, err := c.Validators(ctx, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, trustedBlock.Height, validators.BlockHeight)
	require.Equal(t, 1, validators.Total)
}

// A failed update for a request without a height must return an error
// instead of dereferencing the nil height.
func TestNoHeightRequestsReturnUpdateError(t *testing.T) {
	lc := &mocks.LightClient{}
	lc.On("Update", mock.Anything, mock.Anything).Return(nil, errors.New("primary unavailable"))
	c := NewClient(nextBlockClient{}, lc)

	_, err := c.Commit(context.Background(), nil)
	require.ErrorContains(t, err, "primary unavailable")
}
