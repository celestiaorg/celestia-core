package cat

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/filecoin-project/go-clock"
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

func TestSeenTrackerTracksPeersByTx(t *testing.T) {
	var (
		txKey        = types.Tx("tx1").Key()
		peer1 uint16 = 1
		peer2 uint16 = 2
	)

	tracker := NewSeenTracker()
	require.Nil(t, tracker.Peers(txKey))

	require.False(t, tracker.Add(txKey, 0, nil, 0))
	require.True(t, tracker.Add(txKey, peer1, nil, 0))
	require.True(t, tracker.Add(txKey, peer1, nil, 0))
	require.True(t, tracker.Add(txKey, peer2, nil, 0))

	require.Equal(t, 1, tracker.Len())
	require.True(t, tracker.Has(txKey, peer1))
	require.True(t, tracker.Has(txKey, peer2))
	require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, tracker.Peers(txKey))

	entry := tracker.Get(txKey)
	require.NotNil(t, entry)
	require.Nil(t, entry.futureTxInfo)

	delete(entry.peers, peer1)
	require.True(t, tracker.Has(txKey, peer1), "Get should return a copy")

	peers := tracker.Peers(txKey)
	delete(peers, peer2)
	require.True(t, tracker.Has(txKey, peer2), "Peers should return a copy")

	tracker.RemoveKey(txKey)
	require.Equal(t, 0, tracker.Len())
	require.Nil(t, tracker.Peers(txKey))
	require.Empty(t, tracker.txCountByPeer)
}

func TestSeenTrackerIndexesFutureTxsBySigner(t *testing.T) {
	var (
		signer        = []byte("signer")
		peer1  uint16 = 1
		peer2  uint16 = 2
	)

	tracker := NewSeenTracker()
	keySeq2 := types.Tx("seq2").Key()
	keySeq0 := types.Tx("seq0").Key()
	keySeq1 := types.Tx("seq1").Key()

	tracker.Add(keySeq2, peer1, signer, 2)
	tracker.Add(keySeq0, peer1, signer, 0)
	tracker.Add(keySeq1, peer2, signer, 1)

	entries := tracker.PendingForSigner(signer)
	require.Len(t, entries, 3)
	require.Equal(t, []uint64{0, 1, 2}, seenTrackerSequences(entries))
	require.Equal(t, []types.TxKey{keySeq0, keySeq1, keySeq2}, seenTrackerKeys(entries))

	firstEntry := tracker.Get(keySeq0)
	require.NotNil(t, firstEntry)
	require.NotNil(t, firstEntry.futureTxInfo)
	require.Equal(t, signer, firstEntry.futureTxInfo.signer)
	require.Equal(t, uint64(0), firstEntry.futureTxInfo.sequence)

	signers := tracker.SignersWithPending()
	require.Len(t, signers, 1)
	require.Equal(t, signer, signers[0])
}

func TestSeenTrackerUpgradesPeerOnlyEntryWithSignerSequence(t *testing.T) {
	var (
		txKey         = types.Tx("upgrade").Key()
		signer        = []byte("signer")
		peer1  uint16 = 1
		peer2  uint16 = 2
	)

	tracker := NewSeenTracker()
	tracker.Add(txKey, peer1, nil, 0)
	require.Nil(t, tracker.PendingForSigner(signer))

	tracker.Add(txKey, peer2, signer, 0)

	entry := tracker.Get(txKey)
	require.NotNil(t, entry)
	require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, tracker.Peers(txKey))

	require.NotNil(t, entry.futureTxInfo)
	require.Equal(t, signer, entry.futureTxInfo.signer)
	require.Equal(t, uint64(0), entry.futureTxInfo.sequence)

	entries := tracker.PendingForSigner(signer)
	require.Len(t, entries, 1)
	require.Equal(t, txKey, entries[0].txKey)
}

func TestSeenTrackerClearSequenceKeepsPeerCache(t *testing.T) {
	var (
		txKey         = types.Tx("future").Key()
		signer        = []byte("signer")
		peer   uint16 = 1
	)

	tracker := NewSeenTracker()
	tracker.Add(txKey, peer, signer, 5)

	tracker.ClearSequence(txKey)

	entry := tracker.Get(txKey)
	require.NotNil(t, entry)
	require.Nil(t, entry.futureTxInfo)
	require.True(t, tracker.Has(txKey, peer))
	require.Nil(t, tracker.PendingForSigner(signer))
}

func TestSeenTrackerLimits(t *testing.T) {
	var (
		signer        = []byte("signer")
		peer   uint16 = 1
	)

	tracker := NewSeenTracker()
	tracker.perPeerLimit = 2

	key1 := types.Tx("peer-limit-1").Key()
	key2 := types.Tx("peer-limit-2").Key()
	key3 := types.Tx("peer-limit-3").Key()
	require.True(t, tracker.Add(key1, peer, nil, 0))
	require.True(t, tracker.Add(key2, peer, nil, 0))
	require.False(t, tracker.Add(key3, peer, nil, 0))

	require.Equal(t, 2, tracker.Len())
	require.True(t, tracker.Has(key1, peer))
	require.True(t, tracker.Has(key2, peer))
	require.False(t, tracker.Has(key3, peer))
	require.Equal(t, 2, tracker.txCountByPeer[peer])

	tracker = NewSeenTracker()
	tracker.perSignerLimit = 2

	keySeq5 := types.Tx("signer-limit-5").Key()
	keySeq7 := types.Tx("signer-limit-7").Key()
	keySeq6 := types.Tx("signer-limit-6").Key()
	tracker.Add(keySeq5, peer, signer, 5)
	tracker.Add(keySeq7, peer, signer, 7)
	tracker.Add(keySeq6, peer, signer, 6)

	entries := tracker.PendingForSigner(signer)
	require.Len(t, entries, 2)
	require.Equal(t, []uint64{5, 6}, seenTrackerSequences(entries))

	demoted := tracker.Get(keySeq7)
	require.NotNil(t, demoted)
	require.Nil(t, demoted.futureTxInfo)
	require.True(t, tracker.Has(keySeq7, peer))
}

func TestSeenTrackerRemovePeerAndPrune(t *testing.T) {
	var (
		baseTime        = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		signer          = []byte("signer")
		oldKey          = types.Tx("old").Key()
		freshKey        = types.Tx("fresh").Key()
		peer1    uint16 = 1
		peer2    uint16 = 2
	)

	mockClock := clock.NewMock()
	mockClock.Set(baseTime)
	tracker := NewSeenTracker()
	tracker.clock = mockClock

	tracker.Add(oldKey, peer1, signer, 1)
	tracker.Add(oldKey, peer2, nil, 0)
	tracker.MarkRequested(oldKey, peer1)

	tracker.RemovePeer(peer1)

	entry := tracker.Get(oldKey)
	require.NotNil(t, entry)
	require.False(t, entry.requested)
	require.Equal(t, uint16(0), entry.lastPeer)
	require.False(t, tracker.Has(oldKey, peer1))
	require.True(t, tracker.Has(oldKey, peer2))
	require.Equal(t, 1, tracker.txCountByPeer[peer2])
	require.NotNil(t, tracker.PendingForSigner(signer))

	mockClock.Set(baseTime.Add(3 * time.Minute))
	tracker.Add(freshKey, peer2, nil, 0)
	tracker.Prune(baseTime.Add(time.Minute))

	require.Nil(t, tracker.Get(oldKey))
	require.NotNil(t, tracker.Get(freshKey))
	require.Nil(t, tracker.PendingForSigner(signer))
	require.Equal(t, 1, tracker.txCountByPeer[peer2])

	tracker.MarkRequested(freshKey, peer2)
	tracker.MarkRequestFailed(freshKey, peer2)
	require.Equal(t, 0, tracker.Len())
	require.Empty(t, tracker.txCountByPeer)
}

func TestSeenTrackerPrunesPendingWithoutDroppingPeerCache(t *testing.T) {
	var (
		baseTime        = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		txKey           = types.Tx("pending-prune").Key()
		signer          = []byte("signer")
		peer     uint16 = 1
	)

	mockClock := clock.NewMock()
	mockClock.Set(baseTime)
	tracker := NewSeenTracker()
	tracker.clock = mockClock

	require.True(t, tracker.Add(txKey, peer, signer, 1))
	require.NotNil(t, tracker.Pending(txKey))

	mockClock.Set(baseTime.Add(3 * time.Minute))
	tracker.PrunePending(baseTime.Add(time.Minute))

	require.Nil(t, tracker.Pending(txKey))
	require.True(t, tracker.Has(txKey, peer))
	entry := tracker.Get(txKey)
	require.NotNil(t, entry)
	require.Nil(t, entry.futureTxInfo)
	require.Nil(t, tracker.PendingForSigner(signer))
}

func seenTrackerSequences(entries []*seenEntry) []uint64 {
	out := make([]uint64, len(entries))
	for i, entry := range entries {
		out[i] = entry.futureTxInfo.sequence
	}
	return out
}

func seenTrackerKeys(entries []*seenEntry) []types.TxKey {
	out := make([]types.TxKey, len(entries))
	for i, entry := range entries {
		out[i] = entry.txKey
	}
	return out
}
