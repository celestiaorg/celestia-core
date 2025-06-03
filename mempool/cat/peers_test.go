package cat

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
)

func TestPeerLifecycle(t *testing.T) {
	ids := newMempoolIDs()
	peer1 := &mocks.Peer{}
	peerID := p2p.ID("peer1")
	peer1.On("ID").Return(peerID)

	require.Nil(t, ids.GetPeer(1))
	require.Zero(t, ids.GetIDForPeer(peerID))
	require.Len(t, ids.GetAll(), 0)
	ids.ReserveForPeer(peer1)

	id := ids.GetIDForPeer(peerID)
	require.Equal(t, uint16(1), id)
	require.Equal(t, peer1, ids.GetPeer(id))
	require.Len(t, ids.GetAll(), 1)

	// duplicate peer should be handled gracefully (no panic)
	require.NotPanics(t, func() {
		ids.ReserveForPeer(peer1)
	})
	
	// peer should still have the same ID and be present
	require.Equal(t, id, ids.GetIDForPeer(peerID))
	require.Equal(t, peer1, ids.GetPeer(id))
	require.Len(t, ids.GetAll(), 1)

	require.Equal(t, ids.Reclaim(peerID), id)
	require.Nil(t, ids.GetPeer(id))
	require.Zero(t, ids.GetIDForPeer(peerID))
	require.Len(t, ids.GetAll(), 0)
}

// TestPeerConcurrentReservation tests the fix for the duplicate peer panic
// by attempting to reserve the same peer concurrently, which could happen
// in a high load scenario with rapid peer additions/removals
func TestPeerConcurrentReservation(t *testing.T) {
	ids := newMempoolIDs()
	peer1 := &mocks.Peer{}
	peerID := p2p.ID("peer1")
	peer1.On("ID").Return(peerID)

	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Try to reserve the same peer multiple times concurrently
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			// This should not panic and should be handled gracefully
			require.NotPanics(t, func() {
				ids.ReserveForPeer(peer1)
			})
		}()
	}

	wg.Wait()

	// Verify that the peer was added exactly once
	require.Len(t, ids.GetAll(), 1)
	id := ids.GetIDForPeer(peerID)
	require.NotZero(t, id)
	require.Equal(t, peer1, ids.GetPeer(id))
}
