package cat

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestSeenTxSet(t *testing.T) {
	var (
		tx1Key        = types.Tx("tx1").Key()
		tx2Key        = types.Tx("tx2").Key()
		tx3Key        = types.Tx("tx3").Key()
		peer1  uint16 = 1
		peer2  uint16 = 2
	)

	seenSet := NewSeenTxSet()
	require.Nil(t, seenSet.Get(tx1Key))

	seenSet.Add(tx1Key, peer1)
	seenSet.Add(tx1Key, peer1)
	require.Equal(t, 1, seenSet.Len())
	seenSet.Add(tx1Key, peer2)
	peers := seenSet.Get(tx1Key)
	require.NotNil(t, peers)
	require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, peers)
	seenSet.Add(tx2Key, peer1)
	seenSet.Add(tx3Key, peer1)
	require.Equal(t, 3, seenSet.Len())
	seenSet.RemoveKey(tx2Key)
	require.Equal(t, 2, seenSet.Len())
	require.Nil(t, seenSet.Get(tx2Key))
	require.True(t, seenSet.Has(tx3Key, peer1))
}

func TestSeenTxSetConcurrency(t *testing.T) {
	seenSet := NewSeenTxSet()

	const (
		concurrency = 10
		numTx       = 100
	)

	wg := sync.WaitGroup{}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(peer uint16) {
			defer wg.Done()
			for i := 0; i < numTx; i++ {
				tx := types.Tx([]byte(fmt.Sprintf("tx%d", i)))
				seenSet.Add(tx.Key(), peer)
			}
		}(uint16(i % 2))
	}
	time.Sleep(time.Millisecond)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(peer uint16) {
			defer wg.Done()
			for i := 0; i < numTx; i++ {
				tx := types.Tx([]byte(fmt.Sprintf("tx%d", i)))
				seenSet.Has(tx.Key(), peer)
			}
		}(uint16(i % 2))
	}
	time.Sleep(time.Millisecond)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(peer uint16) {
			defer wg.Done()
			for i := numTx - 1; i >= 0; i-- {
				tx := types.Tx([]byte(fmt.Sprintf("tx%d", i)))
				seenSet.RemoveKey(tx.Key())
			}
		}(uint16(i % 2))
	}
	wg.Wait()
}
