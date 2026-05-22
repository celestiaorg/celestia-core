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

				// Only first txKey should be tracked
				require.NotNil(t, tracker.byTx[key1])
				require.NotNil(t, tracker.byTx[key2])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPendingSeenTracker(tc.limit, 0)
			tc.run(t, tracker)
		})
	}
}

func TestPendingSeenTrackerConcurrentAccess(t *testing.T) {
	tracker := newPendingSeenTracker(0, 0)
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
}

// distinctSigner returns a signer byte slice for the i-th signer so per-signer
// limits do not interfere with global/per-peer cap tests.
func distinctSigner(i int) []byte {
	return []byte(fmt.Sprintf("signer-%d", i))
}

// TestPendingSeenTrackerCapsDerivedFromMempoolSize verifies the global and
// per-peer caps are derived from the configured mempool size, and that a
// non-positive size falls back to a sane default rather than a 0 cap.
func TestPendingSeenTrackerCapsDerivedFromMempoolSize(t *testing.T) {
	cases := []struct {
		name        string
		mempoolSize int
		wantTotal   int
		wantPerPeer int
	}{
		{name: "default mempool size", mempoolSize: 5000, wantTotal: 5000, wantPerPeer: 500},
		{name: "small mempool size", mempoolSize: 100, wantTotal: 100, wantPerPeer: 10},
		{name: "zero falls back to default", mempoolSize: 0, wantTotal: defaultPendingSeenTotal, wantPerPeer: defaultPendingSeenTotal / pendingSeenPerPeerDivisor},
		{name: "negative falls back to default", mempoolSize: -1, wantTotal: defaultPendingSeenTotal, wantPerPeer: defaultPendingSeenTotal / pendingSeenPerPeerDivisor},
		{name: "tiny size keeps per-peer at least 1", mempoolSize: 3, wantTotal: 3, wantPerPeer: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPendingSeenTracker(0, tc.mempoolSize)
			require.Equal(t, tc.wantTotal, tracker.total, "global cap")
			require.Equal(t, tc.wantPerPeer, tracker.perPeerLimit, "per-peer cap")
			require.Less(t, tracker.perPeerLimit, tracker.total+1, "per-peer must not exceed total")
			require.GreaterOrEqual(t, tracker.perPeerLimit, 1, "per-peer must be at least 1")
		})
	}
}

func TestPendingSeenTrackerGlobalCap(t *testing.T) {
	tracker := newPendingSeenTracker(0, 0)
	// Small global cap; large per-signer and per-peer caps so the global cap is
	// the only thing that can reject admissions.
	tracker.total = 5
	tracker.perPeerLimit = 0 // disabled
	tracker.limit = 1000

	// Spread entries across many signers so the per-signer cap never triggers.
	for i := 0; i < 20; i++ {
		key := types.Tx(fmt.Sprintf("tx-%d", i)).Key()
		tracker.add(distinctSigner(i), key, uint64(i+1), uint16(i%3+1))
	}

	require.Equal(t, 5, tracker.len(), "global cap must bound total pending entries")
}

func TestPendingSeenTrackerPerPeerCap(t *testing.T) {
	tracker := newPendingSeenTracker(0, 0)
	tracker.total = 0 // global cap disabled
	tracker.perPeerLimit = 3
	tracker.limit = 1000

	const greedyPeer = uint16(7)
	const otherPeer = uint16(9)

	// The greedy peer tries to add 10 entries across distinct signers but is
	// capped at 3.
	for i := 0; i < 10; i++ {
		key := types.Tx(fmt.Sprintf("greedy-%d", i)).Key()
		tracker.add(distinctSigner(i), key, uint64(i+1), greedyPeer)
	}
	require.Equal(t, 3, tracker.countByPeer[greedyPeer], "per-peer cap must bound a single peer")

	// A different peer is unaffected by the greedy peer's count.
	for i := 0; i < 3; i++ {
		key := types.Tx(fmt.Sprintf("other-%d", i)).Key()
		tracker.add(distinctSigner(100+i), key, uint64(i+1), otherPeer)
	}
	require.Equal(t, 3, tracker.countByPeer[otherPeer])

	// Removing one of the greedy peer's entries frees a slot for it.
	greedy0 := types.Tx("greedy-0").Key()
	tracker.remove(greedy0)
	require.Equal(t, 2, tracker.countByPeer[greedyPeer])

	newKey := types.Tx("greedy-new").Key()
	tracker.add(distinctSigner(200), newKey, 1, greedyPeer)
	require.Equal(t, 3, tracker.countByPeer[greedyPeer])
	require.NotNil(t, tracker.get(newKey))
}

func TestPendingSeenTrackerTimeBasedEviction(t *testing.T) {
	tracker := newPendingSeenTracker(0, 0)
	tracker.limit = 1000

	now := time.Now()
	tracker.now = func() time.Time { return now }

	signer := []byte("signer")

	// Add an old entry.
	oldKey := types.Tx("old").Key()
	tracker.add(signer, oldKey, 1, 5)

	// Advance the clock past the TTL, then add a fresh entry.
	now = now.Add(2 * pendingSeenTTL)
	freshKey := types.Tx("fresh").Key()
	tracker.add(signer, freshKey, 2, 6)

	require.Equal(t, 2, tracker.len())

	// Prune everything older than (now - TTL): the old entry goes, the fresh one
	// stays, and the per-peer count for the evicted entry is released.
	tracker.prune(now.Add(-pendingSeenTTL))

	require.Equal(t, 1, tracker.len())
	require.Nil(t, tracker.get(oldKey))
	require.NotNil(t, tracker.get(freshKey))
	require.Zero(t, tracker.countByPeer[5], "evicted entry's peer count must be released")
	require.Equal(t, 1, tracker.countByPeer[6])
}
