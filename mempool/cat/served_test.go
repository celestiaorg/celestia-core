package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func servedKey(i int) types.TxKey {
	return types.Tx([]byte{byte(i), byte(i >> 8)}).Key()
}

func queueSize(n int) func() int {
	return func() int { return n }
}

func TestServedTxsIsQueued(t *testing.T) {
	s := newServedTxs()
	key := servedKey(1)

	// Untracked peers are never deduplicated.
	s.MarkServed(1, key)
	require.False(t, s.IsQueued(1, key, queueSize(1)))

	s.InitPeer(1)
	require.False(t, s.IsQueued(1, key, queueSize(0)))

	s.MarkQueued(1)      // message 1: a SeenTx
	s.MarkServed(1, key) // message 2: the reply
	s.MarkQueued(1)      // message 3: a SeenTx

	require.True(t, s.IsQueued(1, key, queueSize(3)))
	require.True(t, s.IsQueued(1, key, queueSize(2)), "reply not yet fully sent")
	require.False(t, s.IsQueued(1, key, queueSize(1)), "reply has left the queue")
	require.False(t, s.IsQueued(1, key, queueSize(0)))
	require.False(t, s.IsQueued(2, key, queueSize(3)), "tracking is per peer")

	// Re-serving moves the reply to the back of the queue.
	s.MarkServed(1, key) // message 4
	require.True(t, s.IsQueued(1, key, queueSize(1)))
	require.False(t, s.IsQueued(1, key, queueSize(0)))

	s.RemovePeer(1)
	require.False(t, s.IsQueued(1, key, queueSize(4)))
}

func TestServedTxsQueueSizeOnlyReadWhenServed(t *testing.T) {
	s := newServedTxs()
	s.InitPeer(1)
	called := false
	require.False(t, s.IsQueued(1, servedKey(1), func() int { called = true; return 1 }))
	require.False(t, called)
}

func TestServedTxsPerPeerLimit(t *testing.T) {
	s := newServedTxs()
	s.limit = 3
	s.InitPeer(1)

	for i := 0; i < 4; i++ {
		s.MarkServed(1, servedKey(i))
	}

	require.False(t, s.IsQueued(1, servedKey(0), queueSize(4)), "oldest entry evicted")
	for i := 1; i < 4; i++ {
		require.True(t, s.IsQueued(1, servedKey(i), queueSize(4)))
	}
	require.Len(t, s.byPeer[1].keys, 3)
	require.Equal(t, 3, s.byPeer[1].order.Len())

	// Re-serving a key does not grow the set.
	s.MarkServed(1, servedKey(3))
	require.Len(t, s.byPeer[1].keys, 3)
	require.Equal(t, 3, s.byPeer[1].order.Len())
}

func TestServedPerPeerLimitCoversDataQueue(t *testing.T) {
	reactor, _ := setupReactor(t)
	for _, ch := range reactor.GetChannels() {
		if ch.ID == MempoolDataChannel {
			require.GreaterOrEqual(t, servedPerPeerLimit, ch.SendQueueCapacity)
			return
		}
	}
	t.Fatal("data channel not found")
}
