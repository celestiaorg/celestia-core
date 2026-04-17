package cat

import (
	"encoding/binary"
	"testing"

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

func TestSeenTxSetMaxSize(t *testing.T) {
	seenSet := NewSeenTxSet()

	// Pre-fill the map to exactly maxSeenTxSetSize by inserting dummy entries directly.
	for i := 0; i < maxSeenTxSetSize; i++ {
		var key types.TxKey
		binary.BigEndian.PutUint64(key[:8], uint64(i))
		seenSet.set[key] = timestampedPeerSet{peers: map[uint16]struct{}{1: {}}}
	}
	require.Equal(t, maxSeenTxSetSize, seenSet.Len())

	// New key should evict one entry and be added (size stays at cap).
	var extraKey types.TxKey
	binary.BigEndian.PutUint64(extraKey[:8], uint64(maxSeenTxSetSize+1))
	seenSet.Add(extraKey, 1)
	require.Equal(t, maxSeenTxSetSize, seenSet.Len())
	require.True(t, seenSet.Has(extraKey, 1))

	// Adding a new peer to an existing key should still work at capacity.
	seenSet.Add(extraKey, 2)
	require.True(t, seenSet.Has(extraKey, 2))
	require.Equal(t, maxSeenTxSetSize, seenSet.Len())
}
