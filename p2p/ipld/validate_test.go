package ipld

import (
	"context"
	"testing"
	"time"

	"github.com/celestiaorg/nmt/namespace"
	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/celestia-core/ipfs"
	"github.com/celestiaorg/celestia-core/libs/log"
	"github.com/celestiaorg/celestia-core/types"
	"github.com/celestiaorg/celestia-core/types/consts"
)

// TODO(@Wondertan): Add test to simulate ErrValidationFailed

func TestValidateAvailability(t *testing.T) {
	const (
		shares          = 15
		squareSize      = 8
		adjustedMsgSize = consts.MsgShareSize - 2
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	blockData := generateRandomBlockData(squareSize*squareSize, adjustedMsgSize)
	block := &types.Block{
		Data:       blockData,
		LastCommit: &types.Commit{},
	}
	block.Hash()

	dag := mdutils.Mock()
	err := PutBlock(ctx, dag, block, ipfs.MockRouting(), log.TestingLogger())
	require.NoError(t, err)

	calls := 0
	err = ValidateAvailability(ctx, dag, &block.DataAvailabilityHeader, shares, func(data namespace.PrefixedData8) {
		calls++
	})
	assert.NoError(t, err)
	assert.Equal(t, shares, calls)
}
