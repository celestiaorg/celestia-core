package evidence

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
)

func TestWaitingMessagesBudget(t *testing.T) {
	peer := p2p.ID("peer")
	waiting := newWaitingMessages()

	// The message count is limited per peer.
	for i := 0; i < maxWaitingMessagesPerPeer; i++ {
		require.True(t, waiting.add(peer, 1))
	}
	require.False(t, waiting.add(peer, 1))
	require.True(t, waiting.add(p2p.ID("other peer"), 1))

	// A message leaving the queue frees room for one more.
	waiting.removeOldest(peer)
	require.True(t, waiting.add(peer, 1))

	// The bytes are limited per peer too.
	for i := 0; i < maxWaitingMessagesPerPeer; i++ {
		waiting.removeOldest(peer)
	}
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer-10))
	require.True(t, waiting.add(peer, 10))
	require.False(t, waiting.add(peer, 1))

	// removeNewest frees the last message's bytes, removeOldest the first's.
	waiting.removeNewest(peer)
	require.True(t, waiting.add(peer, 10))
	waiting.removeOldest(peer)
	require.False(t, waiting.add(peer, maxWaitingBytesPerPeer))
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer-10))
}
