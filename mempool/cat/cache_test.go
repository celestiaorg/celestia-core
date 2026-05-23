package cat

import (
	"fmt"
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
	requireSeenTxSetInvariants(t, seenSet)
}

func TestSeenTxSetPerPeerLimit(t *testing.T) {
	seenSet := NewSeenTxSet()

	const (
		limitedPeer uint16 = 1
		otherPeer   uint16 = 2
	)

	keys := make([]types.TxKey, seenTxPerPeerLimit)
	for i := 0; i < seenTxPerPeerLimit; i++ {
		key := types.Tx(fmt.Sprintf("limited-%d", i)).Key()
		seenSet.Add(key, limitedPeer)
		keys[i] = key
	}
	require.Equal(t, seenTxPerPeerLimit, seenSetPeerCount(t, seenSet, limitedPeer))

	blockedKey := types.Tx("limited-blocked").Key()
	seenSet.Add(blockedKey, limitedPeer)
	require.False(t, seenSet.Has(blockedKey, limitedPeer))
	require.Equal(t, seenTxPerPeerLimit, seenSetPeerCount(t, seenSet, limitedPeer))

	seenSet.Add(keys[0], limitedPeer)
	require.Equal(t, seenTxPerPeerLimit, seenSetPeerCount(t, seenSet, limitedPeer))

	seenSet.Add(blockedKey, otherPeer)
	require.True(t, seenSet.Has(blockedKey, otherPeer))
	require.Equal(t, 1, seenSetPeerCount(t, seenSet, otherPeer))

	seenSet.Remove(keys[1], limitedPeer)
	require.Equal(t, seenTxPerPeerLimit-1, seenSetPeerCount(t, seenSet, limitedPeer))

	refillKey := types.Tx("limited-refill").Key()
	seenSet.Add(refillKey, limitedPeer)
	require.True(t, seenSet.Has(refillKey, limitedPeer))
	require.Equal(t, seenTxPerPeerLimit, seenSetPeerCount(t, seenSet, limitedPeer))
	requireSeenTxSetInvariants(t, seenSet)
}

func TestSeenTxSetCountsRemovalPruneAndReset(t *testing.T) {
	seenSet := NewSeenTxSet()
	oldKey := types.Tx("old").Key()
	freshKey := types.Tx("fresh").Key()

	seenSet.Add(oldKey, 1)
	seenSet.Add(oldKey, 2)
	seenSet.Add(freshKey, 1)
	require.Equal(t, 2, seenSetPeerCount(t, seenSet, 1))
	require.Equal(t, 1, seenSetPeerCount(t, seenSet, 2))

	seenSet.Remove(oldKey, 2)
	require.Equal(t, 2, seenSetPeerCount(t, seenSet, 1))
	require.Zero(t, seenSetPeerCount(t, seenSet, 2))

	seenSet.RemoveKey(freshKey)
	require.Equal(t, 1, seenSetPeerCount(t, seenSet, 1))

	seenSet.mtx.Lock()
	oldSet := seenSet.set[oldKey]
	oldSet.time = time.Now().Add(-2 * time.Hour)
	seenSet.set[oldKey] = oldSet
	seenSet.mtx.Unlock()

	seenSet.Prune(time.Now().Add(-time.Hour))
	require.Zero(t, seenSetPeerCount(t, seenSet, 1))
	require.Zero(t, seenSet.Len())

	seenSet.Add(types.Tx("reset").Key(), 3)
	seenSet.Reset()
	require.Zero(t, seenSet.Len())
	require.Zero(t, seenSetPeerCount(t, seenSet, 3))
	requireSeenTxSetInvariants(t, seenSet)
}

func TestSeenTxSetRemovePeerUpdatesCounts(t *testing.T) {
	seenSet := NewSeenTxSet()
	key1 := types.Tx("tx1").Key()
	key2 := types.Tx("tx2").Key()

	seenSet.Add(key1, 1)
	seenSet.Add(key1, 2)
	seenSet.Add(key2, 1)

	seenSet.RemovePeer(1)
	require.False(t, seenSet.Has(key1, 1))
	require.True(t, seenSet.Has(key1, 2))
	require.Nil(t, seenSet.Get(key2))
	require.Zero(t, seenSetPeerCount(t, seenSet, 1))
	require.Equal(t, 1, seenSetPeerCount(t, seenSet, 2))
	requireSeenTxSetInvariants(t, seenSet)
}

func seenSetPeerCount(t *testing.T, seenSet *SeenTxSet, peer uint16) int {
	t.Helper()

	seenSet.mtx.Lock()
	defer seenSet.mtx.Unlock()
	return seenSet.countByPeer[peer]
}

func requireSeenTxSetInvariants(t *testing.T, seenSet *SeenTxSet) {
	t.Helper()

	seenSet.mtx.Lock()
	defer seenSet.mtx.Unlock()

	countByPeer := make(map[uint16]int)
	for _, seen := range seenSet.set {
		for peer := range seen.peers {
			countByPeer[peer]++
		}
	}
	require.Equal(t, countByPeer, seenSet.countByPeer)
}
