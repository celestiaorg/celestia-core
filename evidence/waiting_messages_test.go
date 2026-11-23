package evidence

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
)

func TestWaitingMessagesLimitsMessageCount(t *testing.T) {
	const smallMessage = 500
	peer := p2p.ID("peer")
	waiting := newWaitingMessages()

	for i := 0; i < maxWaitingMessagesPerPeer; i++ {
		require.True(t, waiting.add(peer, smallMessage))
	}
	require.False(t, waiting.add(peer, smallMessage), "the peer is over its message count")

	// Another peer has its own budget.
	require.True(t, waiting.add(p2p.ID("other peer"), smallMessage))

	// A message leaving the queue frees room for one more.
	waiting.removeOldest(peer)
	require.True(t, waiting.add(peer, smallMessage))
	require.False(t, waiting.add(peer, smallMessage))
}

func TestWaitingMessagesLimitsBytes(t *testing.T) {
	peer := p2p.ID("peer")
	waiting := newWaitingMessages()

	// A peer with nothing waiting can add a message of the maximum size.
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer))
	require.False(t, waiting.add(peer, 1), "the peer is over its byte budget")

	waiting.removeOldest(peer)
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer/2))
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer/2))
	require.False(t, waiting.add(peer, 1))
}

func TestWaitingMessagesRemove(t *testing.T) {
	peer := p2p.ID("peer")

	// removeOldest frees the bytes of the first message added.
	waiting := newWaitingMessages()
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer-10))
	require.True(t, waiting.add(peer, 10))
	waiting.removeOldest(peer)
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer-10))

	// removeNewest frees the bytes of the last message added.
	waiting = newWaitingMessages()
	require.True(t, waiting.add(peer, 10))
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer-10))
	waiting.removeNewest(peer)
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer-10))

	// removePeer frees everything, and removing from an unknown peer is safe.
	waiting.removePeer(peer)
	require.True(t, waiting.add(peer, maxWaitingBytesPerPeer))
	waiting.removeOldest(p2p.ID("unknown"))
	waiting.removeNewest(p2p.ID("unknown"))
	waiting.removePeer(p2p.ID("unknown"))
}
