package rpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/light/rpc/mocks"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
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
