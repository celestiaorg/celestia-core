package headersync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// newTestPool creates a HeaderPool for testing with a nop logger.
func newTestPool(startHeight, batchSize int64) *HeaderPool {
	pool := NewHeaderPool(startHeight, batchSize)
	pool.Logger = log.NewNopLogger()
	return pool
}

func TestHeaderPool_SetPeerRange(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")

	// Set initial range
	ok := pool.SetPeerRange(peerID, 1, 100)
	assert.True(t, ok)

	pool.mtx.Lock()
	assert.Len(t, pool.peers, 1)
	assert.Equal(t, int64(1), pool.peers[peerID].base)
	assert.Equal(t, int64(100), pool.peers[peerID].height)
	assert.Equal(t, int64(100), pool.maxPeerHeight)
	pool.mtx.Unlock()

	// Update range with higher height - should succeed
	ok = pool.SetPeerRange(peerID, 1, 200)
	assert.True(t, ok)

	pool.mtx.Lock()
	assert.Equal(t, int64(200), pool.peers[peerID].height)
	assert.Equal(t, int64(200), pool.maxPeerHeight)
	pool.mtx.Unlock()
}

func TestHeaderPool_SetPeerRange_DoSProtection(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")

	// Set initial range
	ok := pool.SetPeerRange(peerID, 1, 100)
	assert.True(t, ok)

	// Sending same height should fail (DoS protection)
	ok = pool.SetPeerRange(peerID, 1, 100)
	assert.False(t, ok, "should reject status update with same height")

	// Peer should be banned
	assert.True(t, pool.IsPeerBanned(peerID))

	// Peer should be removed
	pool.mtx.Lock()
	_, exists := pool.peers[peerID]
	pool.mtx.Unlock()
	assert.False(t, exists)
}

func TestHeaderPool_SetPeerRange_LowerHeight(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")

	// Set initial range
	ok := pool.SetPeerRange(peerID, 1, 100)
	assert.True(t, ok)

	// Sending lower height should fail (DoS protection)
	ok = pool.SetPeerRange(peerID, 1, 50)
	assert.False(t, ok, "should reject status update with lower height")

	// Peer should be banned
	assert.True(t, pool.IsPeerBanned(peerID))
}

func TestHeaderPool_RemovePeer(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	pool.mtx.Lock()
	assert.Len(t, pool.peers, 1)
	pool.mtx.Unlock()

	pool.RemovePeer(peerID)

	pool.mtx.Lock()
	assert.Len(t, pool.peers, 0)
	assert.Equal(t, int64(0), pool.maxPeerHeight)
	pool.mtx.Unlock()
}

// TestHeaderPool_BatchLifecycle tests the complete lifecycle of a batch:
// pending -> response received -> peek -> pop
func TestHeaderPool_BatchLifecycle(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// Phase 1: No completed batch yet
	batch, _ := pool.PeekCompletedBatch()
	assert.Nil(t, batch, "should have no completed batch initially")

	// Phase 2: Simulate a pending batch
	pool.mtx.Lock()
	pool.pendingBatches[1] = &headerBatch{
		startHeight: 1,
		count:       2,
		peerID:      peerID,
		requestTime: time.Now(),
		received:    false,
	}
	pool.mtx.Unlock()

	// Still no completed batch (not received yet)
	batch, _ = pool.PeekCompletedBatch()
	assert.Nil(t, batch, "should have no completed batch before response")

	// Phase 3: Add response
	headers := []*SignedHeader{
		{Header: &types.Header{Height: 1}, Commit: &types.Commit{}},
		{Header: &types.Header{Height: 2}, Commit: &types.Commit{}},
	}
	err := pool.AddBatchResponse(peerID, 1, headers)
	require.NoError(t, err)

	pool.mtx.Lock()
	pendingBatch := pool.pendingBatches[1]
	assert.True(t, pendingBatch.received, "batch should be marked as received")
	assert.Len(t, pendingBatch.headers, 2)
	pool.mtx.Unlock()

	// Phase 4: Peek should now return the batch
	batch, returnedPeerID := pool.PeekCompletedBatch()
	require.NotNil(t, batch)
	assert.Equal(t, int64(1), batch.startHeight)
	assert.Equal(t, peerID, returnedPeerID)
	assert.Len(t, batch.headers, 2)

	// Phase 5: Pop the batch
	pool.PopBatch(1)

	pool.mtx.Lock()
	assert.Nil(t, pool.pendingBatches[1], "batch should be removed after pop")
	assert.Equal(t, int64(3), pool.height, "height should advance by number of headers")
	pool.mtx.Unlock()

	// Phase 6: No more completed batches
	batch, _ = pool.PeekCompletedBatch()
	assert.Nil(t, batch, "should have no completed batch after pop")
}

func TestHeaderPool_IsCaughtUp(t *testing.T) {
	pool := newTestPool(1, 50)
	pool.startTime = time.Now().Add(-10 * time.Second) // Pretend we started 10 seconds ago

	// No peers, not caught up
	assert.False(t, pool.IsCaughtUp())

	// Add a peer
	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// Behind peers, not caught up
	assert.False(t, pool.IsCaughtUp())

	// Set our height to match peers
	pool.mtx.Lock()
	pool.height = 101
	pool.mtx.Unlock()

	// Now caught up
	assert.True(t, pool.IsCaughtUp())
}

func TestHeaderPool_BanPeer(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// Remove peer and ban them
	pool.mtx.Lock()
	pool.removePeer(peerID)
	pool.banPeer(peerID)
	pool.mtx.Unlock()

	// Peer should be banned
	assert.True(t, pool.IsPeerBanned(peerID))

	// New status updates should be ignored for banned peers
	pool.SetPeerRange(peerID, 1, 200)

	pool.mtx.Lock()
	_, exists := pool.peers[peerID]
	assert.False(t, exists)
	pool.mtx.Unlock()
}

// TestHeaderPool_TimeoutLifecycle tests the complete timeout lifecycle:
// peer times out -> skipped for selection -> successful response clears timeout
func TestHeaderPool_TimeoutLifecycle(t *testing.T) {
	pool := newTestPool(1, 50)

	// Add two peers with the same height range
	peer1 := p2p.ID("peer1")
	peer2 := p2p.ID("peer2")
	pool.SetPeerRange(peer1, 1, 100)
	pool.SetPeerRange(peer2, 1, 100)

	// Phase 1: pickPeer returns one of the peers
	pool.mtx.Lock()
	firstPeer := pool.pickPeer(1)
	require.NotNil(t, firstPeer)
	firstPeerID := firstPeer.id
	pool.mtx.Unlock()

	// Phase 2: Mark the first peer as timed out
	pool.mtx.Lock()
	pool.peers[firstPeerID].didTimeout = true
	pool.mtx.Unlock()

	// Phase 3: pickPeer should now return the other peer
	pool.mtx.Lock()
	secondPeer := pool.pickPeer(1)
	require.NotNil(t, secondPeer)
	assert.NotEqual(t, firstPeerID, secondPeer.id, "should pick different peer after timeout")
	pool.mtx.Unlock()

	// Phase 4: Simulate a pending batch for the timed-out peer
	pool.mtx.Lock()
	pool.pendingBatches[1] = &headerBatch{
		startHeight: 1,
		count:       10,
		peerID:      firstPeerID,
		requestTime: time.Now(),
		received:    false,
	}
	pool.mtx.Unlock()

	// Phase 5: Successful response clears timeout flag
	headers := []*SignedHeader{
		{Header: &types.Header{Height: 1}, Commit: &types.Commit{}},
	}
	err := pool.AddBatchResponse(firstPeerID, 1, headers)
	require.NoError(t, err)

	pool.mtx.Lock()
	assert.False(t, pool.peers[firstPeerID].didTimeout, "timeout flag should be cleared on success")
	pool.mtx.Unlock()

	// Phase 6: Peer can be selected again
	pool.mtx.Lock()
	peer := pool.pickPeer(1)
	require.NotNil(t, peer, "peer should be selectable after timeout cleared")
	pool.mtx.Unlock()
}
