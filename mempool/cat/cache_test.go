package cat_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/mempool/cat"
	"github.com/tendermint/tendermint/types"
)

func TestSeenTxSet(t *testing.T) {
	var (
		tx1Key        = types.Tx("tx1").Key()
		tx2Key        = types.Tx("tx2").Key()
		tx3Key        = types.Tx("tx3").Key()
		peer1  uint16 = 1
		peer2  uint16 = 2
	)

	seenSet := cat.NewSeenTxSet(2)
	require.Nil(t, seenSet.Pop(tx1Key))

	seenSet.Add(tx1Key, peer1)
	seenSet.Add(tx1Key, peer2)
	seenSet.Add(tx1Key, peer1)
	peers := seenSet.Pop(tx1Key)
	require.NotNil(t, peers)
	require.Equal(t, map[uint16]bool{peer1: true, peer2: true}, peers)
	seenSet.Add(tx2Key, peer1)
	seenSet.Add(tx3Key, peer1)
	seenSet.Add(tx1Key, peer1)
	require.Equal(t, 2, seenSet.Len())
}

func TestCacheRemove(t *testing.T) {
	cache := cat.NewLRUTxCache(100)
	numTxs := 10

	txs := make([][32]byte, numTxs)
	for i := 0; i < numTxs; i++ {
		// probability of collision is 2**-256
		txBytes := make([]byte, 32)
		_, err := rand.Read(txBytes)
		require.NoError(t, err)

		copy(txs[i][:], txBytes)
		cache.Push(txs[i])

		// make sure its added to both the linked list and the map
		require.Equal(t, i+1, cache.GetList().Len())
	}

	for i := 0; i < numTxs; i++ {
		cache.Remove(txs[i])
		// make sure its removed from both the map and the linked list
		require.Equal(t, numTxs-(i+1), cache.GetList().Len())
	}
}

func TestEvictedTxCache(t *testing.T) {
	var (
		tx1  = types.Tx("tx1")
		tx2  = types.Tx("tx2")
		tx3  = types.Tx("tx3")
		wtx1 = cat.NewWrappedTx(
			tx1, tx1.Key(), 10, 1, 5, "",
		)
		wtx2 = cat.NewWrappedTx(
			tx2, tx2.Key(), 10, 1, 5, "",
		)
		wtx3 = cat.NewWrappedTx(
			tx3, tx3.Key(), 10, 1, 5, "",
		)
	)

	cache := cat.NewEvictedTxCache(2)
	require.False(t, cache.Has(tx1.Key()))
	require.Nil(t, cache.Pop(tx1.Key()))
	cache.Push(wtx1)
	require.True(t, cache.Has(tx1.Key()))
	require.NotNil(t, cache.Pop(tx1.Key()))
	cache.Push(wtx1)
	time.Sleep(1 * time.Millisecond)
	cache.Push(wtx2)
	time.Sleep(1 * time.Millisecond)
	cache.Push(wtx3)
	require.False(t, cache.Has(tx1.Key()))
	cache.Prune(time.Now().UTC().Add(1 * time.Second))
	require.False(t, cache.Has(tx2.Key()))
	require.False(t, cache.Has(tx3.Key()))
}
