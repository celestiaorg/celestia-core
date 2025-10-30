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
			name: "same signer and sequence with different txKeys rejected",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("tx-version-1")
				key2 := txKey("tx-version-2")

				// Add same (signer, sequence) with different txKeys
				tracker.add(signer, key1, 10, 5)
				tracker.add(signer, key2, 10, 7) // Should be rejected

				// Should only have one entry in the queue
				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 1)
				require.Equal(t, uint64(10), entries[0].sequence)

				// Should have first peer only
				require.Equal(t, []uint16{5}, entries[0].peerIDs())

				// Only first txKey should be tracked
				require.NotNil(t, tracker.byTx[key1])
				require.Nil(t, tracker.byTx[key2])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPendingSeenTracker(tc.limit)
			tc.run(t, tracker)
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
}
