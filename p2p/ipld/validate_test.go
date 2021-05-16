package ipld

import (
	"context"
	"testing"

	"github.com/lazyledger/nmt/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/types"
)

// TODO(@Wondertan): Add test to simulate ErrValidationFailed

func TestValidateAvailability(t *testing.T) {
	const (
		shares          = 15
		squareSize      = 8
		adjustedMsgSize = types.MsgShareSize - 2
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// issue a new API object
	ipfsAPI := mockedIpfsAPI(t)

	blockData := generateRandomBlockData(squareSize*squareSize, adjustedMsgSize)
	block := &types.Block{
		Data:       blockData,
		LastCommit: &types.Commit{},
	}
	block.Hash()

	err := PutBlock(ctx, ipfsAPI.Dag(), block)
	require.NoError(t, err)

	calls := 0
	err = ValidateAvailability(ctx, ipfsAPI, &block.DataAvailabilityHeader, shares, func(data namespace.PrefixedData8) {
		calls++
	})
	assert.NoError(t, err)
	assert.Equal(t, shares, calls)
}
