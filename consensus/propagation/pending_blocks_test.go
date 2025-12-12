package propagation

import (
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingBlockCreation(t *testing.T) {
	blockID := test.MakeBlockID()
	parts := proptypes.NewCombinedPartSetFromOriginal(
		types.NewPartSetFromHeader(blockID.PartSetHeader, types.BlockPartSizeBytes),
		false,
	)

	pb := &PendingBlock{
		Height:    10,
		Round:     1,
		Source:    SourceHeaderSync,
		BlockID:   blockID,
		Parts:     parts,
		CreatedAt: time.Now(),
		State:     BlockStateActive,
	}

	require.False(t, pb.CanUseProofCache())
	require.True(t, pb.NeedsInlineProofs())
	require.Equal(t, BlockStateActive, pb.State)

	pb.CompactBlock = &proptypes.CompactBlock{
		Proposal: types.Proposal{
			Height:  pb.Height,
			Round:   pb.Round,
			BlockID: pb.BlockID,
		},
		PartsHashes: [][]byte{{0x1}},
	}

	require.True(t, pb.CanUseProofCache())
	require.False(t, pb.NeedsInlineProofs())
}

func TestPendingBlockStateTransitions(t *testing.T) {
	pb := &PendingBlock{State: BlockStateActive}
	require.Equal(t, BlockStateActive, pb.State)

	pb.State = BlockStateComplete
	require.Equal(t, BlockStateComplete, pb.State)
}

func TestManagerCreation(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	require.Equal(t, defaultMaxConcurrent, manager.config.MaxConcurrent)
	require.Equal(t, defaultMemoryBudget, manager.config.MemoryBudget)
	require.NotNil(t, manager.blocks)
	require.NotNil(t, manager.completedBlocks)
	assert.Empty(t, manager.heights)
}

func TestManagerHeightOrdering(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	for _, h := range []int64{10, 5, 8, 5} {
		manager.mtx.Lock()
		manager.addBlock(&PendingBlock{Height: h})
		manager.mtx.Unlock()
	}

	require.Equal(t, []int64{5, 8, 10}, manager.heights)
	require.Len(t, manager.blocks, 3)
}
