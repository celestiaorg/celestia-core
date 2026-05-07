package coregrpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/rpc/core"
)

// newTestBlockAPI returns a BlockAPI suitable for unit-testing the
// broadcast / listener bookkeeping paths. It avoids spinning up a full
// node — only fields read by these paths are populated.
func newTestBlockAPI(t *testing.T) *BlockAPI {
	t.Helper()
	env := &core.Environment{Logger: log.NewNopLogger()}
	return NewBlockAPI(env)
}

// fillChannel writes n placeholder events to ch without blocking the test
// if ch's capacity is smaller than n.
func fillChannel(t *testing.T, ch chan SubscribeNewHeightsResponse, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case ch <- SubscribeNewHeightsResponse{Height: int64(i)}:
		default:
			t.Fatalf("channel full after %d writes (cap=%d)", i, cap(ch))
		}
	}
}

// TestBroadcastToListeners_DoesNotBlockOnSlowSubscriber verifies that a
// listener whose channel buffer is full does not block the broadcaster.
// Regression for https://github.com/celestiaorg/celestia-core/issues/2967.
func TestBroadcastToListeners_DoesNotBlockOnSlowSubscriber(t *testing.T) {
	api := newTestBlockAPI(t)

	slow := api.addHeightListener()
	fast := api.addHeightListener()

	// Saturate the slow listener's channel (cap=50, see addHeightListener).
	fillChannel(t, slow, cap(slow))

	done := make(chan struct{})
	go func() {
		api.broadcastToListeners(context.Background(), 100, []byte("hash"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcastToListeners blocked on slow subscriber")
	}

	// Fast listener still receives the broadcast event.
	select {
	case ev := <-fast:
		assert.Equal(t, int64(100), ev.Height)
		assert.Equal(t, []byte("hash"), ev.Hash)
	case <-time.After(1 * time.Second):
		t.Fatal("fast subscriber did not receive the broadcast")
	}
}

// TestBroadcastToListeners_DoesNotHoldLockDuringSend verifies that
// addHeightListener / removeHeightListener can make progress while a
// broadcast is in flight to a saturated subscriber. Regression for
// https://github.com/celestiaorg/celestia-core/issues/2967.
func TestBroadcastToListeners_DoesNotHoldLockDuringSend(t *testing.T) {
	api := newTestBlockAPI(t)

	slow := api.addHeightListener()
	fillChannel(t, slow, cap(slow))

	// Run the broadcast concurrently. With the bug present, this acquires
	// the lock and then blocks indefinitely on the saturated channel.
	// We synchronize on `started` and yield briefly so the broadcast
	// goroutine has actually entered broadcastToListeners and acquired
	// the lock before the test contends for it.
	started := make(chan struct{})
	go func() {
		close(started)
		api.broadcastToListeners(context.Background(), 100, []byte("hash"))
	}()
	<-started
	time.Sleep(5 * time.Millisecond)

	// addHeightListener must not block on the broadcaster's lock.
	addDone := make(chan chan SubscribeNewHeightsResponse, 1)
	go func() {
		addDone <- api.addHeightListener()
	}()

	var newCh chan SubscribeNewHeightsResponse
	select {
	case newCh = <-addDone:
	case <-time.After(2 * time.Second):
		t.Fatal("addHeightListener blocked while broadcast was in flight")
	}

	// removeHeightListener must also not block.
	removeDone := make(chan struct{})
	go func() {
		api.removeHeightListener(newCh)
		close(removeDone)
	}()

	select {
	case <-removeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("removeHeightListener blocked while broadcast was in flight")
	}
}

// TestBroadcastToListeners_DeliversToAllSubscribersWhenAllDrain verifies
// that with all subscribers actively draining their channels, every
// broadcast reaches every subscriber.
func TestBroadcastToListeners_DeliversToAllSubscribersWhenAllDrain(t *testing.T) {
	api := newTestBlockAPI(t)

	const numSubs = 4
	const numEvents = 25

	chans := make([]chan SubscribeNewHeightsResponse, numSubs)
	for i := range chans {
		chans[i] = api.addHeightListener()
	}

	var wg sync.WaitGroup
	received := make([][]int64, numSubs)
	for i, ch := range chans {
		i, ch := i, ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			for len(received[i]) < numEvents {
				select {
				case ev := <-ch:
					received[i] = append(received[i], ev.Height)
				case <-time.After(2 * time.Second):
					return
				}
			}
		}()
	}

	for h := int64(1); h <= numEvents; h++ {
		api.broadcastToListeners(context.Background(), h, []byte("hash"))
	}

	wg.Wait()

	for i, got := range received {
		require.Len(t, got, numEvents, "subscriber %d missed events", i)
		for j, h := range got {
			assert.Equal(t, int64(j+1), h, "subscriber %d out-of-order", i)
		}
	}
}

// TestStop_DoesNotDeadlock is a regression test for
// https://github.com/celestiaorg/celestia-core/issues/3003.
// Stop acquires blockAPI.Lock() and then calls closeAllListeners which
// also tries to acquire the same non-reentrant mutex, causing the
// gRPC server shutdown path to hang indefinitely.
func TestStop_DoesNotDeadlock(t *testing.T) {
	env := &core.Environment{Logger: log.NewNopLogger()}
	api := NewBlockAPI(env)

	// Register a couple of listeners so closeAllListeners has work to do.
	_ = api.addHeightListener()
	_ = api.addHeightListener()

	done := make(chan error, 1)
	go func() {
		done <- api.Stop(context.Background())
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked: did not return within 2s")
	}
}

// TestBroadcastToListeners_DoesNotDeadlockOnPanic guards against the same
// reentrant-mutex pattern as TestStop_DoesNotDeadlock, but in
// broadcastToListeners. broadcastToListeners holds blockAPI.Lock() while
// sending to each listener; if a send panics (e.g. channel closed by
// SubscribeNewHeights returning) the recover handler calls
// removeHeightListener, which tries to acquire the same non-reentrant
// mutex and deadlocks the goroutine forever.
func TestBroadcastToListeners_DoesNotDeadlockOnPanic(t *testing.T) {
	env := &core.Environment{Logger: log.NewNopLogger()}
	api := NewBlockAPI(env)

	// Close the listener's channel so the next send panics with
	// "send on closed channel", triggering the recover path.
	ch := api.addHeightListener()
	close(ch)

	done := make(chan struct{})
	go func() {
		api.broadcastToListeners(context.Background(), 1, []byte("hash"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcastToListeners() deadlocked: did not return within 2s")
	}

	api.Lock()
	_, exists := api.heightListeners[ch]
	api.Unlock()
	require.False(t, exists, "listener should be removed after the recover path runs")
}
