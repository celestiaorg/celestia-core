package cat

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestRequestSchedulerRerequest(t *testing.T) {
	var (
		requests        = newRequestScheduler(10*time.Millisecond, 1*time.Minute)
		tx              = types.Tx("tx")
		key             = tx.Key()
		signer          = []byte("signer")
		sequence uint64 = 1
		peerA    uint16 = 1 // should be non-zero
		peerB    uint16 = 2
	)
	t.Cleanup(requests.Close)

	// check zero state
	require.Zero(t, requests.ForTx(key))
	require.False(t, requests.Has(peerA, key))
	// marking a tx that was never requested should return false
	require.False(t, requests.MarkReceived(peerA, key, signer, sequence))

	// create a request
	closeCh := make(chan struct{})
	require.True(t, requests.Add(key, signer, sequence, peerA, func(cbKey types.TxKey, timedOutPeer uint16) {
		require.Equal(t, key, cbKey)
		require.Equal(t, peerA, timedOutPeer)
		// the first peer times out to respond so we ask the second peer
		require.True(t, requests.Add(cbKey, signer, sequence, peerB, func(retryKey types.TxKey, retryPeer uint16) {
			require.Equal(t, cbKey, retryKey)
			require.Equal(t, peerB, retryPeer)
			t.Fatal("did not expect to timeout")
		}))
		close(closeCh)
	}))

	// check that the request was added
	require.Equal(t, peerA, requests.ForTx(key))
	require.True(t, requests.Has(peerA, key))

	// should not be able to add the same request again
	require.False(t, requests.Add(key, signer, sequence, peerA, nil))

	// wait for the scheduler to invoke the timeout
	<-closeCh

	// check that the request still exists
	require.True(t, requests.Has(peerA, key))
	// check that peerB was requested
	require.True(t, requests.Has(peerB, key))

	// There should still be a request for the Tx
	require.Equal(t, peerB, requests.ForTx(key))

	// record a response from peerB
	require.True(t, requests.MarkReceived(peerB, key, signer, sequence))

	// peerA comes in later with a response but it's still
	// considered a response from an earlier request
	require.True(t, requests.MarkReceived(peerA, key, signer, sequence))
}

func TestRequestSchedulerNonResponsivePeer(t *testing.T) {
	var (
		requests        = newRequestScheduler(10*time.Millisecond, time.Millisecond)
		tx              = types.Tx("tx")
		key             = tx.Key()
		peerA    uint16 = 1 // should be non-zero
	)

	require.True(t, requests.Add(key, []byte("signer"), 1, peerA, nil))
	require.Eventually(t, func() bool {
		return requests.ForTx(key) == 0
	}, 100*time.Millisecond, 5*time.Millisecond)
}

func TestRequestSchedulerConcurrencyAddsAndReads(t *testing.T) {
	leaktest.CheckTimeout(t, time.Second)()
	requests := newRequestScheduler(10*time.Millisecond, time.Millisecond)
	defer requests.Close()

	N := 5
	keys := make([]types.TxKey, N)
	for i := 0; i < N; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		keys[i] = tx.Key()
	}

	addWg := sync.WaitGroup{}
	receiveWg := sync.WaitGroup{}
	doneCh := make(chan struct{})
	for i := 1; i < N*N; i++ {
		addWg.Add(1)
		go func(peer uint16) {
			defer addWg.Done()
			requests.Add(keys[int(peer)%N], []byte("signer"), uint64(peer), peer, nil)
		}(uint16(i))
	}
	for i := 1; i < N*N; i++ {
		receiveWg.Add(1)
		go func(peer uint16) {
			defer receiveWg.Done()
			markReceived := func() {
				for _, key := range keys {
					if requests.Has(peer, key) {
						requests.MarkReceived(peer, key, []byte("signer"), 0)
					}
				}
			}
			for {
				select {
				case <-doneCh:
					// need to ensure this is run
					// at least once after all adds
					// are done
					markReceived()
					return
				default:
					markReceived()
				}
			}
		}(uint16(i))
	}
	addWg.Wait()
	close(doneCh)

	receiveWg.Wait()

	for _, key := range keys {
		require.Zero(t, requests.ForTx(key))
	}
}

func TestRequestSchedulerCountForPeer(t *testing.T) {
	requests := newRequestScheduler(time.Minute, time.Minute)
	t.Cleanup(requests.Close)

	var peerA uint16 = 1
	var peerB uint16 = 2

	// Initially zero
	require.Equal(t, 0, requests.CountForPeer(peerA))
	require.Equal(t, 0, requests.CountForPeer(peerB))

	// Add requests to peerA
	for i := 0; i < 5; i++ {
		tx := types.Tx(fmt.Sprintf("tx-a-%d", i))
		require.True(t, requests.Add(tx.Key(), []byte("signer-a"), uint64(i), peerA, nil))
	}

	require.Equal(t, 5, requests.CountForPeer(peerA))
	require.Equal(t, 0, requests.CountForPeer(peerB))

	// Add requests to peerB
	for i := 0; i < 3; i++ {
		tx := types.Tx(fmt.Sprintf("tx-b-%d", i))
		require.True(t, requests.Add(tx.Key(), []byte("signer-b"), uint64(i), peerB, nil))
	}

	require.Equal(t, 5, requests.CountForPeer(peerA))
	require.Equal(t, 3, requests.CountForPeer(peerB))

	// Mark some as received
	tx := types.Tx("tx-a-0")
	requests.MarkReceived(peerA, tx.Key(), []byte("signer-a"), 0)

	require.Equal(t, 4, requests.CountForPeer(peerA))

	// Clear all from peerB
	requests.ClearAllRequestsFrom(peerB)

	require.Equal(t, 4, requests.CountForPeer(peerA))
	require.Equal(t, 0, requests.CountForPeer(peerB))
}
