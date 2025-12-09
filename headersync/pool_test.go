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
	requestsCh := make(chan HeaderBatchRequest, 100)
	errorsCh := make(chan peerError, 100)
	pool := NewHeaderPool(startHeight, batchSize, requestsCh, errorsCh)
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

func TestHeaderPool_AddBatchResponse(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// Simulate a pending batch
	pool.mtx.Lock()
	pool.pendingBatches[1] = &headerBatch{
		startHeight: 1,
		count:       10,
		peerID:      peerID,
		requestTime: time.Now(),
		received:    false,
	}
	pool.mtx.Unlock()

	// Add response
	headers := []*SignedHeader{
		{Header: &types.Header{Height: 1}, Commit: &types.Commit{}},
	}
	err := pool.AddBatchResponse(peerID, headers)
	require.NoError(t, err)

	pool.mtx.Lock()
	batch := pool.pendingBatches[1]
	assert.True(t, batch.received)
	assert.Len(t, batch.headers, 1)
	pool.mtx.Unlock()
}

func TestHeaderPool_PeekAndPopCompletedBatch(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// No completed batch yet
	batch, _ := pool.PeekCompletedBatch()
	assert.Nil(t, batch)

	// Add a completed batch at the current height
	headers := []*SignedHeader{
		{Header: &types.Header{Height: 1}, Commit: &types.Commit{}},
		{Header: &types.Header{Height: 2}, Commit: &types.Commit{}},
	}
	pool.mtx.Lock()
	pool.pendingBatches[1] = &headerBatch{
		startHeight: 1,
		count:       2,
		peerID:      peerID,
		requestTime: time.Now(),
		headers:     headers,
		received:    true,
	}
	pool.mtx.Unlock()

	// Now peek should return the batch
	batch, returnedPeerID := pool.PeekCompletedBatch()
	require.NotNil(t, batch)
	assert.Equal(t, int64(1), batch.startHeight)
	assert.Equal(t, peerID, returnedPeerID)
	assert.Len(t, batch.headers, 2)

	// Pop the batch
	pool.PopBatch(1)

	pool.mtx.Lock()
	assert.Nil(t, pool.pendingBatches[1])
	assert.Equal(t, int64(3), pool.height) // height advances by number of headers
	pool.mtx.Unlock()
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

func TestHeaderPool_TimeoutSelectsDifferentPeer(t *testing.T) {
	pool := newTestPool(1, 50)

	// Add two peers with the same height range
	peer1 := p2p.ID("peer1")
	peer2 := p2p.ID("peer2")
	pool.SetPeerRange(peer1, 1, 100)
	pool.SetPeerRange(peer2, 1, 100)

	// pickPeer should return one of the peers (deterministically the first in sorted order)
	pool.mtx.Lock()
	firstPeer := pool.pickPeer(1)
	require.NotNil(t, firstPeer)
	firstPeerID := firstPeer.id
	pool.mtx.Unlock()

	// Mark the first peer as timed out
	pool.mtx.Lock()
	pool.peers[firstPeerID].didTimeout = true
	pool.mtx.Unlock()

	// Now pickPeer should return the other peer
	pool.mtx.Lock()
	secondPeer := pool.pickPeer(1)
	require.NotNil(t, secondPeer)
	assert.NotEqual(t, firstPeerID, secondPeer.id, "should pick different peer after timeout")
	pool.mtx.Unlock()
}

func TestHeaderPool_TimeoutClearedOnSuccess(t *testing.T) {
	pool := newTestPool(1, 50)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// Simulate a pending batch with timeout flag set
	pool.mtx.Lock()
	pool.peers[peerID].didTimeout = true
	pool.pendingBatches[1] = &headerBatch{
		startHeight: 1,
		count:       10,
		peerID:      peerID,
		requestTime: time.Now(),
		received:    false,
	}
	pool.mtx.Unlock()

	// Peer should be skipped by pickPeer
	pool.mtx.Lock()
	peer := pool.pickPeer(1)
	assert.Nil(t, peer, "should skip timed out peer")
	pool.mtx.Unlock()

	// Add successful response
	headers := []*SignedHeader{
		{Header: &types.Header{Height: 1}, Commit: &types.Commit{}},
	}
	err := pool.AddBatchResponse(peerID, headers)
	require.NoError(t, err)

	// didTimeout should be cleared
	pool.mtx.Lock()
	assert.False(t, pool.peers[peerID].didTimeout, "timeout flag should be cleared on success")
	pool.mtx.Unlock()
}
