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
	ReactorMaxRequesters(1)(bcR)
	require.Equal(t, defaultMaxRequesters, pool.maxRequesters, "values below 2 are ignored")
	ReactorMaxRequesters(7)(bcR)
	require.Equal(t, 7, pool.maxRequesters)
}

// TestBlockPoolMaxRequesters checks that the pool never creates more
// requesters than its window.
func TestBlockPoolMaxRequesters(t *testing.T) {
	const window = 5
	requestsCh := make(chan BlockRequest, 100)
	pool := NewBlockPool(1, requestsCh, make(chan peerError, 100))
	pool.maxRequesters = window
	pool.SetLogger(log.TestingLogger())
	require.NoError(t, pool.Start())
	t.Cleanup(func() { _ = pool.Stop() })
	pool.SetPeerRange("peer", 1, 1000)

	require.Eventually(t, func() bool {
		_, _, n := pool.GetStatus()
		return n == window
	}, 10*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	_, _, n := pool.GetStatus()
	require.Equal(t, window, n)
}
