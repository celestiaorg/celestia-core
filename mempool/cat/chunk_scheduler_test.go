package cat

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestChunkSchedulerAddAndReceive(t *testing.T) {
	c := newChunkRequestScheduler(time.Hour)
	defer c.Close()
	key := types.Tx(randBytes(t, 32)).Key()

	require.True(t, c.Add(key, 0, 1, nil))
	require.True(t, c.Has(key, 0))
	// duplicate request for same chunk is rejected
	require.False(t, c.Add(key, 0, 2, nil))
	// peer 0 is invalid
	require.False(t, c.Add(key, 1, 0, nil))

	peer, sentAt, ok := c.MarkReceived(key, 0)
	require.True(t, ok)
	require.Equal(t, uint16(1), peer)
	require.False(t, sentAt.IsZero())
	require.False(t, c.Has(key, 0))
	_, _, ok = c.MarkReceived(key, 0)
	require.False(t, ok)
}

func TestChunkSchedulerTimeoutRerequest(t *testing.T) {
	c := newChunkRequestScheduler(20 * time.Millisecond)
	defer c.Close()
	key := types.Tx(randBytes(t, 32)).Key()

	var mu sync.Mutex
	var firedIndex uint32
	var firedPeer uint16
	fired := make(chan struct{}, 1)

	require.True(t, c.Add(key, 3, 1, func(tk types.TxKey, idx uint32, peer uint16) {
		mu.Lock()
		firedIndex = idx
		firedPeer = peer
		mu.Unlock()
		fired <- struct{}{}
	}))

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("timeout callback never fired")
	}
	mu.Lock()
	require.Equal(t, uint32(3), firedIndex)
	require.Equal(t, uint16(1), firedPeer)
	mu.Unlock()
	// entry removed before callback, so re-request to a new peer succeeds
	require.True(t, c.Add(key, 3, 2, nil))
}

func TestChunkSchedulerClearTxAndPeer(t *testing.T) {
	c := newChunkRequestScheduler(time.Hour)
	defer c.Close()
	key1 := types.Tx(randBytes(t, 32)).Key()
	key2 := types.Tx(randBytes(t, 33)).Key()

	require.True(t, c.Add(key1, 0, 1, nil))
	require.True(t, c.Add(key1, 1, 2, nil))
	require.True(t, c.Add(key2, 0, 1, nil))

	c.ClearTx(key1)
	require.False(t, c.Has(key1, 0))
	require.False(t, c.Has(key1, 1))
	require.True(t, c.Has(key2, 0))

	affected := c.ClearPeer(1)
	require.Len(t, affected, 1)
	require.Equal(t, chunkRequestKey{txKey: key2, index: 0}, affected[0])
	require.False(t, c.Has(key2, 0))
}
