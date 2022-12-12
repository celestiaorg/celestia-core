package cat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/types"
)

func TestRequestScheduler(t *testing.T) {
	var (
		requests        = newRequestScheduler(10*time.Millisecond, 1*time.Minute)
		tx              = types.Tx("tx")
		key             = tx.Key()
		peerA    uint16 = 1 // should be non-zero
		peerB    uint16 = 2
	)

	// check zero state
	require.False(t, requests.ForTx(key))
	require.Equal(t, RequestSet{}, requests.From(peerA))
	// marking a tx that was never requested should return false
	require.False(t, requests.MarkReceived(peerA, key))

	// create a request
	closeCh := make(chan struct{})
	require.True(t, requests.Add(key, peerA, func(key types.TxKey) {
		require.Equal(t, key, key)
		close(closeCh)
		// the first peer times out to respond so we ask the second peer
		require.True(t, requests.Add(key, peerB, func(key types.TxKey) {
			t.Fatal("did not exmpect to timeout")
		}))
	}))

	// check that the request was added
	require.True(t, requests.ForTx(key))
	require.True(t, requests.From(peerA).Includes(key))

	// should not be able to add the same request again
	require.False(t, requests.Add(key, peerA, nil))

	// wait for the scheduler to invoke the timeout
	<-closeCh

	// check that the request stil exists
	require.True(t, requests.From(peerA).Includes(key))
	// check that peerB was requested
	require.True(t, requests.From(peerB).Includes(key))

	// There should still be a request for the Tx
	require.True(t, requests.ForTx(key))

	// record a response from peerB
	require.True(t, requests.MarkReceived(peerB, key))

	// peerA comes in later with a response but it's still
	// considered a response from an earlier request
	require.True(t, requests.MarkReceived(peerA, key))
}

func TestRequestSchedulerNonResponsivePeer(t *testing.T) {
	var (
		requests        = newRequestScheduler(10*time.Millisecond, time.Millisecond)
		tx              = types.Tx("tx")
		key             = tx.Key()
		peerA    uint16 = 1 // should be non-zero
	)

	require.True(t, requests.Add(key, peerA, nil))
	require.Eventually(t, func() bool {
		return len(requests.From(peerA)) == 0
	}, 20*time.Millisecond, 5*time.Millisecond)
}
