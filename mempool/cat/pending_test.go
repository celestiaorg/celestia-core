package cat

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestPendingSeenTracker(t *testing.T) {
	signer := []byte("signer")
	otherSigner := []byte("other")

	txKey := func(label string) types.TxKey {
		return types.Tx(label).Key()
	}

	cases := []struct {
		name  string
		limit int
		run   func(*testing.T, *pendingSeenTracker)
	}{
		{
			name: "add and retrieve entry",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx1")
				tracker.add(signer, key, 1, 5)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 1)
				require.Equal(t, key, entries[0].txKey)
				require.Equal(t, uint64(1), entries[0].sequence)
				require.Equal(t, []uint16{5}, entries[0].peerIDs())
			},
		},
		{
			name: "re-adding same tx keeps first peer",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx2")
				tracker.add(signer, key, 2, 5)
				tracker.add(signer, key, 2, 5) // duplicate peer
				tracker.add(signer, key, 2, 7) // different peer, should be ignored

				entry := tracker.entriesForSigner(signer)[0]
				require.Equal(t, []uint16{5}, entry.peerIDs())
			},
		},
		{
			name: "adding beyond nominal limit retains entries",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("a")
				key2 := txKey("b")
				key3 := txKey("c")

				tracker.add(signer, key1, 1, 1)
				tracker.add(signer, key2, 2, 2)
				tracker.add(signer, key3, 3, 3)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 3)
				require.Equal(t, []types.TxKey{key1, key2, key3}, []types.TxKey{entries[0].txKey, entries[1].txKey, entries[2].txKey})

				require.Nil(t, tracker.entriesForSigner(otherSigner))
			},
		},
		{
			name:  "enforces per-signer limit",
			limit: 2,
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("a")
				key2 := txKey("b")
				key3 := txKey("c")

				tracker.add(signer, key1, 1, 1)
				tracker.add(signer, key2, 2, 2)
				tracker.add(signer, key3, 3, 3)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, []types.TxKey{key1, key2}, []types.TxKey{entries[0].txKey, entries[1].txKey})
			},
		},
		{
			name: "remove tx clears entry",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx3")
				tracker.add(signer, key, 3, 6)

				tracker.remove(key)
				require.Empty(t, tracker.entriesForSigner(signer))
			},
		},
		{
			name: "remove peer clears peer and pending state",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx4")
				tracker.add(signer, key, 4, 9)
				tracker.markRequested(key, 9, time.Now())

				tracker.removePeer(9)
				entry := tracker.entriesForSigner(signer)[0]
				require.Nil(t, entry.peerIDs())
				require.False(t, entry.requested)
				require.Equal(t, uint16(0), entry.lastPeer)
				require.Equal(t, 1, pendingSeenPeerCount(t, tracker, 9))
			},
		},
		{
			name: "peerIDs returns copy",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx5")
				tracker.add(signer, key, 5, 21)
				peers := tracker.entriesForSigner(signer)[0].peerIDs()

				peers[0] = 99
				require.Equal(t, []uint16{21}, tracker.entriesForSigner(signer)[0].peerIDs())
			},
		},
		{
			name: "entries stored in ascending sequence order",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("seq10")
				key2 := txKey("seq5")
				key3 := txKey("seq7")

				tracker.add(signer, key1, 10, 1)
				tracker.add(signer, key2, 5, 1)
				tracker.add(signer, key3, 7, 1)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 3)
				require.Equal(t, []uint64{5, 7, 10}, []uint64{entries[0].sequence, entries[1].sequence, entries[2].sequence})
			},
		},
		{
			name: "mark requested and failed updates state",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx6")
				tracker.add(signer, key, 7, 12)
				now := time.Now()
				tracker.markRequested(key, 12, now)

				entry := tracker.entriesForSigner(signer)[0]
				require.True(t, entry.requested)
				require.Equal(t, uint16(12), entry.lastPeer)

				tracker.markRequestFailed(key, 12)

				entry = tracker.entriesForSigner(signer)[0]
				require.False(t, entry.requested)
				require.Equal(t, uint16(0), entry.lastPeer)
				require.Nil(t, entry.peerIDs())
				require.Equal(t, 1, pendingSeenPeerCount(t, tracker, 12))
			},
		},
		{
			name: "same signer and sequence with different txKeys",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("tx-version-1")
				key2 := txKey("tx-version-2")

				// Add same (signer, sequence) with different txKeys
				tracker.add(signer, key1, 10, 5)
				tracker.add(signer, key2, 10, 7)

				// Should have both in the queue
				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, uint64(10), entries[0].sequence)

				require.NotNil(t, tracker.get(key1))
				require.NotNil(t, tracker.get(key2))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPendingSeenTracker(tc.limit)
			tc.run(t, tracker)
			requirePendingSeenInvariants(t, tracker)
		})
	}
}

func TestPendingSeenTrackerConcurrentAccess(t *testing.T) {
	tracker := newPendingSeenTracker(0)
	signer := []byte("signer")

	const total = 5000
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			key := types.Tx(fmt.Sprintf("tx-%d", i)).Key()
			tracker.add(signer, key, uint64(i+1), uint16(i%5+1))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			tracker.entriesForSigner(signer)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			tracker.removePeer(uint16(i%5 + 1))
		}
	}()

	wg.Wait()
	require.LessOrEqual(t, len(tracker.entriesForSigner(signer)), defaultPendingSeenPerSigner)
	requirePendingSeenInvariants(t, tracker)
}

// distinctSigner returns a signer byte slice for the i-th signer so per-signer
// limits do not interfere with per-peer limit tests.
func distinctSigner(i int) []byte {
	return []byte(fmt.Sprintf("signer-%d", i))
}

func distinctSignerWithPrefix(prefix string, i int) []byte {
	return []byte(fmt.Sprintf("%s-signer-%d", prefix, i))
}

func addPendingSeenFromPeer(t *testing.T, tracker *pendingSeenTracker, prefix string, total int, peerID uint16) []types.TxKey {
	t.Helper()

	keys := make([]types.TxKey, total)
	for i := 0; i < total; i++ {
		key := types.Tx(fmt.Sprintf("%s-tx-%d", prefix, i)).Key()
		tracker.add(distinctSignerWithPrefix(prefix, i), key, uint64(i+1), peerID)
		keys[i] = key
	}

	return keys
}

func pendingSeenPeerCount(t *testing.T, tracker *pendingSeenTracker, peerID uint16) int {
	t.Helper()

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.countByPeer[peerID]
}

func requirePendingSeenInvariants(t *testing.T, tracker *pendingSeenTracker) {
	t.Helper()

	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	countByPeer := make(map[uint16]int)
	seenTxs := make(map[types.TxKey]struct{}, len(tracker.byTx))
	for signerKey, queue := range tracker.perSigner {
		require.LessOrEqual(t, len(queue), tracker.perSignerLimit)
		for i, entry := range queue {
			require.Equal(t, signerKey, entry.signerKey)
			require.Same(t, entry, tracker.byTx[entry.txKey])
			require.NotContains(t, seenTxs, entry.txKey)
			seenTxs[entry.txKey] = struct{}{}
			countByPeer[entry.addedBy]++
			if i > 0 {
				require.LessOrEqual(t, queue[i-1].sequence, entry.sequence)
			}
		}
	}

	require.Len(t, tracker.byTx, len(seenTxs))
	for txKey := range tracker.byTx {
		require.Contains(t, seenTxs, txKey)
	}
	require.Equal(t, countByPeer, tracker.countByPeer)
}

func TestPendingSeenTrackerPerPeerLimit(t *testing.T) {
	tracker := newPendingSeenTracker(0)

	const limitedPeer = uint16(7)
	const otherPeer = uint16(9)

	keys := addPendingSeenFromPeer(t, tracker, "limited", seenTxPerPeerLimit, limitedPeer)
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, limitedPeer))

	blockedKey := types.Tx("limited-blocked").Key()
	tracker.add([]byte("limited-blocked-signer"), blockedKey, 1, limitedPeer)
	require.Nil(t, tracker.get(blockedKey))
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, limitedPeer))

	otherKey := types.Tx("other-peer").Key()
	tracker.add([]byte("other-peer-signer"), otherKey, 1, otherPeer)
	require.NotNil(t, tracker.get(otherKey))
	require.Equal(t, 1, pendingSeenPeerCount(t, tracker, otherPeer))

	tracker.add([]byte("duplicate-signer"), keys[1], 1, otherPeer)
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, limitedPeer))
	require.Equal(t, 1, pendingSeenPeerCount(t, tracker, otherPeer))

	tracker.remove(keys[0])
	require.Equal(t, seenTxPerPeerLimit-1, pendingSeenPeerCount(t, tracker, limitedPeer))

	newKey := types.Tx("limited-new").Key()
	tracker.add([]byte("limited-new-signer"), newKey, 1, limitedPeer)
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, limitedPeer))
	require.NotNil(t, tracker.get(newKey))

	requirePendingSeenInvariants(t, tracker)
}

func TestPendingSeenTrackerPerPeerLimitAllowsSamePeerReplacement(t *testing.T) {
	tracker := newPendingSeenTracker(2)

	signer := []byte("signer")
	peer := uint16(7)
	key10 := types.Tx("seq-10").Key()
	key20 := types.Tx("seq-20").Key()
	key5 := types.Tx("seq-5").Key()

	tracker.add(signer, key10, 10, peer)
	tracker.add(signer, key20, 20, peer)
	addPendingSeenFromPeer(t, tracker, "same-peer-fill", seenTxPerPeerLimit-2, peer)
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, peer))

	tracker.add(signer, key5, 5, peer)

	entries := tracker.entriesForSigner(signer)
	require.Len(t, entries, 2)
	require.Equal(t, []uint64{5, 10}, []uint64{entries[0].sequence, entries[1].sequence})
	require.NotNil(t, tracker.get(key5))
	require.NotNil(t, tracker.get(key10))
	require.Nil(t, tracker.get(key20))
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, peer))
	requirePendingSeenInvariants(t, tracker)
}

func TestPendingSeenTrackerPerPeerLimitBlocksReplacementThatWouldGrowPeer(t *testing.T) {
	tracker := newPendingSeenTracker(2)

	signer := []byte("signer")
	blockedPeer := uint16(9)
	addPendingSeenFromPeer(t, tracker, "blocked-peer-fill", seenTxPerPeerLimit, blockedPeer)
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, blockedPeer))

	key10 := types.Tx("seq-10").Key()
	key20 := types.Tx("seq-20").Key()
	key5 := types.Tx("seq-5").Key()
	tracker.add(signer, key10, 10, 7)
	tracker.add(signer, key20, 20, 8)

	tracker.add(signer, key5, 5, blockedPeer)

	entries := tracker.entriesForSigner(signer)
	require.Len(t, entries, 2)
	require.Equal(t, []uint64{10, 20}, []uint64{entries[0].sequence, entries[1].sequence})
	require.Nil(t, tracker.get(key5))
	require.Equal(t, seenTxPerPeerLimit, pendingSeenPeerCount(t, tracker, blockedPeer))
	requirePendingSeenInvariants(t, tracker)
}

func TestPendingSeenTrackerDroppedPerSignerTailDoesNotAffectPeerCount(t *testing.T) {
	tracker := newPendingSeenTracker(2)

	signer := []byte("signer")
	key1 := types.Tx("seq-1").Key()
	key2 := types.Tx("seq-2").Key()
	key3 := types.Tx("seq-3").Key()

	tracker.add(signer, key1, 1, 7)
	tracker.add(signer, key2, 2, 7)
	tracker.add(signer, key3, 3, 9)

	require.Nil(t, tracker.get(key3))
	require.Equal(t, 2, pendingSeenPeerCount(t, tracker, 7))
	require.Zero(t, pendingSeenPeerCount(t, tracker, 9))
	requirePendingSeenInvariants(t, tracker)
}

func TestPendingSeenTrackerTimeBasedEviction(t *testing.T) {
	tracker := newPendingSeenTracker(1000)

	now := time.Now()
	tracker.now = func() time.Time { return now }

	signer := []byte("signer")

	// Add an old entry.
	oldKey := types.Tx("old").Key()
	tracker.add(signer, oldKey, 1, 5)

	// An entry exactly at the cutoff should be kept because prune removes
	// entries strictly before the cutoff.
	now = now.Add(pendingSeenTTL)
	cutoffKey := types.Tx("cutoff").Key()
	tracker.add(signer, cutoffKey, 2, 7)

	// Advance the clock past the TTL, then add a fresh entry.
	now = now.Add(pendingSeenTTL)
	freshKey := types.Tx("fresh").Key()
	tracker.add(signer, freshKey, 3, 6)

	require.Equal(t, 3, tracker.len())

	// Prune everything older than (now - TTL): the old entry goes, the fresh one
	// and cutoff entries stay, and the per-peer count for the evicted entry is
	// released.
	tracker.prune(now.Add(-pendingSeenTTL))

	require.Equal(t, 2, tracker.len())
	require.Nil(t, tracker.get(oldKey))
	require.NotNil(t, tracker.get(cutoffKey))
	require.NotNil(t, tracker.get(freshKey))
	require.Zero(t, pendingSeenPeerCount(t, tracker, 5), "evicted entry's peer count must be released")
	require.Equal(t, 1, pendingSeenPeerCount(t, tracker, 6))
	require.Equal(t, 1, pendingSeenPeerCount(t, tracker, 7))
	requirePendingSeenInvariants(t, tracker)
}
