package blocksync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

func TestBlockBuffer_AddBlock(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	block := makeBlock(100, 100)
	peerID := p2p.ID("peer1")

	// Add block within window
	err := bb.AddBlock(100, block, nil, peerID, 1000)
	require.NoError(t, err)
	assert.Equal(t, 1, bb.Size())
	assert.True(t, bb.HasBlock(100))
}

func TestBlockBuffer_AddBlockBelowWindow(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	block := makeBlock(99, 99)
	peerID := p2p.ID("peer1")

	// Try to add block below window
	err := bb.AddBlock(99, block, nil, peerID, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below window base")
	assert.Equal(t, 0, bb.Size())
}

func TestBlockBuffer_AddBlockAboveWindow(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	block := makeBlock(150, 150)
	peerID := p2p.ID("peer1")

	// Try to add block above window (100 + 50 = 150, so 150 is outside)
	err := bb.AddBlock(150, block, nil, peerID, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside window")
	assert.Equal(t, 0, bb.Size())
}

func TestBlockBuffer_AddDuplicateBlock(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	block := makeBlock(100, 100)
	peerID1 := p2p.ID("peer1")
	peerID2 := p2p.ID("peer2")

	// Add block from first peer
	err := bb.AddBlock(100, block, nil, peerID1, 1000)
	require.NoError(t, err)

	// Add same block from second peer (should not error, just log)
	err = bb.AddBlock(100, block, nil, peerID2, 1000)
	require.NoError(t, err)
	assert.Equal(t, 1, bb.Size()) // Still only one block
}

func TestBlockBuffer_PeekNext(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	// Peek when empty
	block := bb.PeekNext()
	assert.Nil(t, block)

	// Add block at base height
	testBlock := makeBlock(100, 100)
	peerID := p2p.ID("peer1")
	err := bb.AddBlock(100, testBlock, nil, peerID, 1000)
	require.NoError(t, err)

	// Peek should return the block
	block = bb.PeekNext()
	assert.NotNil(t, block)
	assert.Equal(t, int64(100), block.Block.Height)
	assert.Equal(t, peerID, block.PeerID)

	// Peek should not remove the block
	assert.Equal(t, 1, bb.Size())
}

func TestBlockBuffer_PeekTwoNext(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	// Peek when empty
	first, second := bb.PeekTwoNext()
	assert.Nil(t, first)
	assert.Nil(t, second)

	// Add only first block
	peerID := p2p.ID("peer1")
	err := bb.AddBlock(100, makeBlock(100, 100), nil, peerID, 1000)
	require.NoError(t, err)

	// Should return nil (need both)
	first, second = bb.PeekTwoNext()
	assert.Nil(t, first)
	assert.Nil(t, second)

	// Add second block
	err = bb.AddBlock(101, makeBlock(101, 101), nil, peerID, 1000)
	require.NoError(t, err)

	// Now should return both
	first, second = bb.PeekTwoNext()
	assert.NotNil(t, first)
	assert.NotNil(t, second)
	assert.Equal(t, int64(100), first.Block.Height)
	assert.Equal(t, int64(101), second.Block.Height)
}

func TestBlockBuffer_PopNext(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	// Pop when empty
	block := bb.PopNext()
	assert.Nil(t, block)
	assert.Equal(t, int64(100), bb.BaseHeight()) // Base doesn't change

	// Add blocks
	peerID := p2p.ID("peer1")
	err := bb.AddBlock(100, makeBlock(100, 100), nil, peerID, 1000)
	require.NoError(t, err)
	err = bb.AddBlock(101, makeBlock(101, 101), nil, peerID, 1000)
	require.NoError(t, err)

	// Pop first block
	block = bb.PopNext()
	assert.NotNil(t, block)
	assert.Equal(t, int64(100), block.Block.Height)
	assert.Equal(t, 1, bb.Size())
	assert.Equal(t, int64(101), bb.BaseHeight()) // Base advanced

	// Pop second block
	block = bb.PopNext()
	assert.NotNil(t, block)
	assert.Equal(t, int64(101), block.Block.Height)
	assert.Equal(t, 0, bb.Size())
	assert.Equal(t, int64(102), bb.BaseHeight())
}

func TestBlockBuffer_SlidingWindow(t *testing.T) {
	logger := log.NewNopLogger()
	windowSize := int64(10)
	bb := NewBlockBuffer(100, windowSize, logger)
	peerID := p2p.ID("peer1")

	// Fill window completely
	for h := int64(100); h < 100+windowSize; h++ {
		err := bb.AddBlock(h, makeBlock(h, h), nil, peerID, 1000)
		require.NoError(t, err, "height %d", h)
	}
	assert.Equal(t, int(windowSize), bb.Size())

	// Try to add beyond window
	err := bb.AddBlock(110, makeBlock(110, 110), nil, peerID, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside window")

	// Pop one block
	block := bb.PopNext()
	assert.NotNil(t, block)
	assert.Equal(t, int64(100), block.Block.Height)
	assert.Equal(t, int64(101), bb.BaseHeight())

	// Now we should be able to add block 110
	err = bb.AddBlock(110, makeBlock(110, 110), nil, peerID, 1000)
	require.NoError(t, err)
	assert.True(t, bb.HasBlock(110))

	// But not 111 (would be outside window)
	err = bb.AddBlock(111, makeBlock(111, 111), nil, peerID, 1000)
	require.Error(t, err)
}

func TestBlockBuffer_IsInWindow(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	// Test boundaries
	assert.False(t, bb.IsInWindow(99))  // Below
	assert.True(t, bb.IsInWindow(100))  // Base
	assert.True(t, bb.IsInWindow(149))  // Top of window
	assert.False(t, bb.IsInWindow(150)) // Outside

	// Pop and check window moved
	bb.AddBlock(100, makeBlock(100, 100), nil, p2p.ID("peer1"), 1000)
	bb.PopNext()

	assert.False(t, bb.IsInWindow(100)) // Now below
	assert.True(t, bb.IsInWindow(101))  // New base
	assert.True(t, bb.IsInWindow(150))  // Now in window
	assert.False(t, bb.IsInWindow(151)) // Outside
}

func TestBlockBuffer_Stats(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 10, logger)
	peerID := p2p.ID("peer1")

	// Add some blocks with gaps
	bb.AddBlock(100, makeBlock(100, 100), nil, peerID, 1000)
	bb.AddBlock(102, makeBlock(102, 102), nil, peerID, 2000)
	bb.AddBlock(105, makeBlock(105, 105), nil, peerID, 1500)

	stats := bb.Stats()
	assert.Equal(t, int64(100), stats.BaseHeight)
	assert.Equal(t, int64(10), stats.WindowSize)
	assert.Equal(t, 3, stats.BufferSize)
	assert.Equal(t, int64(105), stats.HighestHeight)
	assert.Equal(t, int64(4500), stats.TotalBytes) // 1000 + 2000 + 1500

	// Check missing heights
	expectedMissing := []int64{101, 103, 104, 106, 107, 108, 109}
	assert.Equal(t, expectedMissing, stats.MissingHeights)
}

func TestBlockBuffer_Clear(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)
	peerID := p2p.ID("peer1")

	// Add blocks
	for h := int64(100); h < 110; h++ {
		bb.AddBlock(h, makeBlock(h, h), nil, peerID, 1000)
	}
	assert.Equal(t, 10, bb.Size())

	// Clear and reset to new height
	bb.Clear(200)

	assert.Equal(t, 0, bb.Size())
	assert.Equal(t, int64(200), bb.BaseHeight())
	assert.False(t, bb.HasBlock(100))

	// Should be able to add at new base
	err := bb.AddBlock(200, makeBlock(200, 200), nil, peerID, 1000)
	require.NoError(t, err)
}

func TestBlockBuffer_RemoveBlock(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)
	peerID := p2p.ID("peer1")

	// Add blocks
	bb.AddBlock(100, makeBlock(100, 100), nil, peerID, 1000)
	bb.AddBlock(101, makeBlock(101, 101), nil, peerID, 1000)
	assert.Equal(t, 2, bb.Size())

	// Remove block 101
	bb.RemoveBlock(101)
	assert.Equal(t, 1, bb.Size())
	assert.True(t, bb.HasBlock(100))
	assert.False(t, bb.HasBlock(101))

	// Base should not change
	assert.Equal(t, int64(100), bb.BaseHeight())

	// Can add block 101 again
	err := bb.AddBlock(101, makeBlock(101, 101), nil, peerID, 1000)
	require.NoError(t, err)
}

func TestBlockBuffer_ConcurrentAccess(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 100, logger)
	peerID := p2p.ID("peer1")

	// Simulate concurrent adds and pops
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for h := int64(100); h < 200; h++ {
			bb.AddBlock(h, makeBlock(h, h), nil, peerID, 1000)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			bb.PeekNext()
			bb.Stats()
		}
		done <- true
	}()

	// Wait for completion
	<-done
	<-done

	// Should have no races (test will fail with -race flag if there are)
}

func TestBlockBuffer_MaxHeight(t *testing.T) {
	logger := log.NewNopLogger()
	bb := NewBlockBuffer(100, 50, logger)

	assert.Equal(t, int64(149), bb.MaxHeight())

	// Need to add a block before we can pop it
	bb.AddBlock(100, makeBlock(100, 99), nil, p2p.ID("peer1"), 1000)
	bb.PopNext() // Advances base to 101
	assert.Equal(t, int64(150), bb.MaxHeight())
}

// Helper function to create a test block
func makeBlock(height int64, lastHeight int64) *types.Block {
	return &types.Block{
		Header: types.Header{
			Height: height,
		},
		Data: types.Data{
			Txs: types.Txs{},
		},
		LastCommit: &types.Commit{
			Height: lastHeight,
		},
	}
}
