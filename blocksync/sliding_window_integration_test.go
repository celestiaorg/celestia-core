package blocksync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
)

// TestSlidingWindowPool_BasicFlow tests the basic flow of the sliding window pool
func TestSlidingWindowPool_BasicFlow(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 10, 5, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	// Add a peer
	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Add blocks in order
	for h := int64(100); h < 110; h++ {
		block := makeBlock(h, h-1)
		err := pool.AddBlock(peer1, block, nil, 1000)
		require.NoError(t, err)
	}

	// Verify we can peek two blocks
	first, second, _ := pool.PeekTwoBlocks()
	assert.NotNil(t, first)
	assert.NotNil(t, second)
	assert.Equal(t, int64(100), first.Height)
	assert.Equal(t, int64(101), second.Height)

	// Pop first block
	pool.PopRequest()
	assert.Equal(t, int64(101), pool.Height())

	// Should still be able to peek
	first, second, _ = pool.PeekTwoBlocks()
	assert.Equal(t, int64(101), first.Height)
	assert.Equal(t, int64(102), second.Height)
}

// TestSlidingWindowPool_RejectOutsideWindow tests that blocks outside the window are rejected
func TestSlidingWindowPool_RejectOutsideWindow(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 10, 5, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Try to add block at window boundary (should succeed)
	block109 := makeBlock(109, 108)
	err = pool.AddBlock(peer1, block109, nil, 1000)
	require.NoError(t, err)

	// Try to add block outside window (should fail)
	block110 := makeBlock(110, 109)
	err = pool.AddBlock(peer1, block110, nil, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside window")

	// Verify peer error was sent
	select {
	case peerErr := <-errorsCh:
		assert.Equal(t, peer1, peerErr.peerID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected peer error to be sent")
	}
}

// TestSlidingWindowPool_WindowAdvancement tests that the window advances correctly
func TestSlidingWindowPool_WindowAdvancement(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	windowSize := int64(10)
	pool := NewBlockPoolWithParams(100, windowSize, 5, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Fill the window
	for h := int64(100); h < 100+windowSize; h++ {
		block := makeBlock(h, h-1)
		err := pool.AddBlock(peer1, block, nil, 1000)
		require.NoError(t, err)
	}

	// Window is full - block 110 should be rejected
	block110 := makeBlock(110, 109)
	err = pool.AddBlock(peer1, block110, nil, 1000)
	require.Error(t, err)

	// Pop first block - window advances
	pool.PopRequest()

	// Now block 110 should be accepted
	err = pool.AddBlock(peer1, block110, nil, 1000)
	require.NoError(t, err)

	// But block 111 should still be rejected
	block111 := makeBlock(111, 110)
	err = pool.AddBlock(peer1, block111, nil, 1000)
	require.Error(t, err)
}

// TestSlidingWindowPool_OutOfOrderDelivery tests handling of out-of-order block delivery
func TestSlidingWindowPool_OutOfOrderDelivery(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Add blocks out of order
	heights := []int64{105, 102, 100, 103, 101, 104}
	for _, h := range heights {
		block := makeBlock(h, h-1)
		err := pool.AddBlock(peer1, block, nil, 1000)
		require.NoError(t, err, "height %d", h)
	}

	// Should be able to peek blocks 100 and 101
	first, second, _ := pool.PeekTwoBlocks()
	assert.Equal(t, int64(100), first.Height)
	assert.Equal(t, int64(101), second.Height)

	// Pop blocks in order (only up to 104, since we need 105 to validate 104)
	for h := int64(100); h < 105; h++ {
		first, second, _ := pool.PeekTwoBlocks()
		require.NotNil(t, first, "height %d", h)
		require.NotNil(t, second, "height %d", h+1)
		assert.Equal(t, h, first.Height)
		assert.Equal(t, h+1, second.Height)
		pool.PopRequest()
	}

	assert.Equal(t, int64(105), pool.Height())
}

// TestSlidingWindowPool_MultiplePeers tests handling blocks from multiple peers
func TestSlidingWindowPool_MultiplePeers(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	peer2 := p2p.ID("peer2")
	peer3 := p2p.ID("peer3")

	pool.SetPeerRange(peer1, 100, 200)
	pool.SetPeerRange(peer2, 100, 200)
	pool.SetPeerRange(peer3, 100, 200)

	// Different peers send different blocks
	pool.AddBlock(peer1, makeBlock(100, 99), nil, 1000)
	pool.AddBlock(peer2, makeBlock(101, 100), nil, 1000)
	pool.AddBlock(peer3, makeBlock(102, 101), nil, 1000)
	pool.AddBlock(peer1, makeBlock(103, 102), nil, 1000)

	// Should be able to process all
	for h := int64(100); h < 103; h++ {
		first, _, _ := pool.PeekTwoBlocks()
		require.NotNil(t, first)
		assert.Equal(t, h, first.Height)
		pool.PopRequest()
	}
}

// TestSlidingWindowPool_DuplicateBlocks tests that duplicate blocks are handled gracefully
func TestSlidingWindowPool_DuplicateBlocks(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	peer2 := p2p.ID("peer2")

	pool.SetPeerRange(peer1, 100, 200)
	pool.SetPeerRange(peer2, 100, 200)

	block100 := makeBlock(100, 99)

	// Add same block from two peers (simulating dual-request)
	err = pool.AddBlock(peer1, block100, nil, 1000)
	require.NoError(t, err)

	err = pool.AddBlock(peer2, block100, nil, 1000)
	require.NoError(t, err) // Should not error

	// Should only have one copy
	stats := pool.buffer.Stats()
	assert.Equal(t, 1, stats.BufferSize)
}

// TestSlidingWindowPool_BlockRemovalOnError tests block removal when validation fails
func TestSlidingWindowPool_BlockRemovalOnError(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Add blocks
	pool.AddBlock(peer1, makeBlock(100, 99), nil, 1000)
	pool.AddBlock(peer1, makeBlock(101, 100), nil, 1000)

	assert.True(t, pool.buffer.HasBlock(100))
	assert.True(t, pool.buffer.HasBlock(101))

	// Simulate validation failure - remove peer and redo requests
	peerID := pool.RemovePeerAndRedoAllPeerRequests(100)
	assert.Equal(t, peer1, peerID)

	// Block should be removed from buffer
	assert.False(t, pool.buffer.HasBlock(100))
}

// TestSlidingWindowPool_MaxRequestersLimit tests that requester count is limited
func TestSlidingWindowPool_MaxRequestersLimit(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	maxRequesters := int32(5)
	pool := NewBlockPoolWithParams(100, 20, int64(maxRequesters), requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Give time for requesters to be created
	time.Sleep(200 * time.Millisecond)

	// Check that we don't exceed max requesters
	_, _, numRequesters := pool.GetStatus()
	assert.LessOrEqual(t, numRequesters, int(maxRequesters))
}

// TestSlidingWindowPool_IsCaughtUp tests the caught-up detection logic
func TestSlidingWindowPool_IsCaughtUp(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	// No peers - not caught up
	assert.False(t, pool.IsCaughtUp())

	// Add peer at height 110
	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 110)

	// Still not caught up (too far behind)
	assert.False(t, pool.IsCaughtUp())

	// Add blocks to get close to peer height
	for h := int64(100); h <= 109; h++ {
		pool.AddBlock(peer1, makeBlock(h, h-1), nil, 1000)
		pool.PopRequest()
	}

	// Now should be caught up (within 1 block)
	assert.True(t, pool.IsCaughtUp())
}

// TestSlidingWindowPool_PeerHeightRegression tests security fix for height regression
func TestSlidingWindowPool_PeerHeightRegression(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")

	// Peer reports height 200
	pool.SetPeerRange(peer1, 100, 200)

	// Verify peer is in pool
	pool.mtx.Lock()
	_, exists := pool.peers[peer1]
	pool.mtx.Unlock()
	assert.True(t, exists)

	// Peer reports lower height (attack!)
	pool.SetPeerRange(peer1, 100, 150)

	// Peer should be removed and banned
	pool.mtx.Lock()
	_, exists = pool.peers[peer1]
	banned := pool.isPeerBanned(peer1)
	pool.mtx.Unlock()

	assert.False(t, exists, "Peer should be removed")
	assert.True(t, banned, "Peer should be banned")
}

// TestSlidingWindowPool_Stats tests statistics reporting
func TestSlidingWindowPool_Stats(t *testing.T) {
	requestsCh := make(chan BlockRequest, 100)
	errorsCh := make(chan peerError, 100)

	pool := NewBlockPoolWithParams(100, 20, 10, requestsCh, errorsCh, trace.NoOpTracer())
	err := pool.Start()
	require.NoError(t, err)
	defer pool.Stop()

	peer1 := p2p.ID("peer1")
	pool.SetPeerRange(peer1, 100, 200)

	// Add some blocks with gaps
	pool.AddBlock(peer1, makeBlock(100, 99), nil, 1000)
	pool.AddBlock(peer1, makeBlock(102, 101), nil, 2000)
	pool.AddBlock(peer1, makeBlock(105, 104), nil, 1500)

	stats := pool.buffer.Stats()
	assert.Equal(t, int64(100), stats.BaseHeight)
	assert.Equal(t, int64(20), stats.WindowSize)
	assert.Equal(t, 3, stats.BufferSize)
	assert.Equal(t, int64(4500), stats.TotalBytes)

	// Check missing heights
	expectedMissing := []int64{101, 103, 104, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115, 116, 117, 118, 119}
	assert.Equal(t, expectedMissing, stats.MissingHeights)
}
