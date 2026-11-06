package cat

import (
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func servedKey(i int) types.TxKey {
	return types.Tx([]byte{byte(i), byte(i >> 8)}).Key()
}

func sendOK() bool { return true }

func queueSize(n int) func() int {
	return func() int { return n }
}

func TestServedTxsIsQueued(t *testing.T) {
	s := newServedTxs()
	key := servedKey(1)

	// Untracked peers are never deduplicated.
	require.True(t, s.Serve(1, key, sendOK))
	require.False(t, s.IsQueued(1, key, queueSize(1)))

	s.InitPeer(1)
	require.False(t, s.IsQueued(1, key, queueSize(0)))

	s.Send(1, sendOK)       // message 1: a SeenTx
	s.Serve(1, key, sendOK) // message 2: the reply
	s.Send(1, sendOK)       // message 3: a SeenTx

	require.True(t, s.IsQueued(1, key, queueSize(3)))
	require.True(t, s.IsQueued(1, key, queueSize(2)), "reply not yet fully sent")
	require.False(t, s.IsQueued(1, key, queueSize(1)), "reply has left the queue")
	require.False(t, s.IsQueued(1, key, queueSize(0)))
	require.False(t, s.IsQueued(2, key, queueSize(3)), "tracking is per peer")

	// Re-serving moves the reply to the back of the queue.
	s.Serve(1, key, sendOK) // message 4
	require.True(t, s.IsQueued(1, key, queueSize(1)))
	require.False(t, s.IsQueued(1, key, queueSize(0)))

	s.RemovePeer(1)
	require.False(t, s.IsQueued(1, key, queueSize(4)))
}

func TestServedTxsFailedSendNotRecorded(t *testing.T) {
	s := newServedTxs()
	s.InitPeer(1)
	key := servedKey(1)

	require.False(t, s.Serve(1, key, func() bool { return false }))
	require.False(t, s.Send(1, func() bool { return false }))
	require.Zero(t, s.byPeer[1].queued)
	require.False(t, s.IsQueued(1, key, queueSize(0)))
}

// TestServedTxsConcurrentSendsKeepFIFOOrder enqueues SeenTx-like messages and
// replies concurrently into a FIFO queue, then drains it one message at a
// time. A reply still in the queue must always be reported as queued.
func TestServedTxsConcurrentSendsKeepFIFOOrder(t *testing.T) {
	const (
		senders = 8
		perSend = 200
	)
	s := newServedTxs()
	s.limit = senders * perSend
	s.InitPeer(1)

	var (
		qMtx  sync.Mutex
		queue []*types.TxKey // nil for a non-reply message
	)
	enqueue := func(key *types.TxKey) func() bool {
		return func() bool {
			// Yield between the caller's checks and the enqueue to widen
			// any window in which bookkeeping could be reordered.
			runtime.Gosched()
			qMtx.Lock()
			queue = append(queue, key)
			qMtx.Unlock()
			return true
		}
	}

	var wg sync.WaitGroup
	for g := 0; g < senders; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perSend; i++ {
				if i%2 == 0 {
					s.Send(1, enqueue(nil))
					continue
				}
				key := servedKey(g*perSend + i)
				s.Serve(1, key, enqueue(&key))
			}
		}(g)
	}
	wg.Wait()

	for len(queue) > 0 {
		for _, key := range queue {
			if key != nil {
				require.True(t, s.IsQueued(1, *key, queueSize(len(queue))))
			}
		}
		queue = queue[1:]
	}
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
		s.Serve(1, servedKey(i), sendOK)
	}

	require.False(t, s.IsQueued(1, servedKey(0), queueSize(4)), "oldest entry evicted")
	for i := 1; i < 4; i++ {
		require.True(t, s.IsQueued(1, servedKey(i), queueSize(4)))
	}
	require.Len(t, s.byPeer[1].keys, 3)
	require.Equal(t, 3, s.byPeer[1].order.Len())

	// Re-serving a key does not grow the set.
	s.Serve(1, servedKey(3), sendOK)
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
