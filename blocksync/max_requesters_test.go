package blocksync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
)

func TestReactorMaxRequestersOption(t *testing.T) {
	pool := NewBlockPool(1, make(chan BlockRequest), make(chan peerError))
	bcR := &Reactor{pool: pool}
	require.Equal(t, defaultMaxRequesters, pool.maxRequesters)
	ReactorMaxRequesters(0)(bcR)
	require.Equal(t, defaultMaxRequesters, pool.maxRequesters, "0 keeps the default")
	ReactorMaxRequesters(7)(bcR)
	require.Equal(t, 7, pool.maxRequesters)
}

// TestBlockPoolMaxRequesters checks that the pool fills its window and that
// a full window stops further requesters.
func TestBlockPoolMaxRequesters(t *testing.T) {
	const window = 5
	pool := NewBlockPool(1, make(chan BlockRequest, 100), make(chan peerError, 100))
	pool.maxRequesters = window
	pool.SetLogger(log.TestingLogger())
	require.NoError(t, pool.Start())
	t.Cleanup(func() { _ = pool.Stop() })
	pool.SetPeerRange("peer", 1, 1000)

	require.Eventually(t, func() bool {
		_, _, n := pool.GetStatus()
		return n >= window
	}, 10*time.Second, 10*time.Millisecond)

	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	require.Len(t, pool.requesters, window)
	require.True(t, pool.requestersFull())
	delete(pool.requesters, pool.height)
	require.False(t, pool.requestersFull())
}
