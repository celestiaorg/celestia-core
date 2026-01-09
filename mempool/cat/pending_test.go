package cat

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// testAdd is a helper that wraps pendingSeenTracker.add for easier testing.
// It creates a SeenTx message with MinSequence=1 and MaxSequence=sequence.
func (ps *pendingSeenTracker) testAdd(signer []byte, txKey types.TxKey, sequence uint64, peerID uint16) {
	msg := &protomem.SeenTx{
		TxKey:       txKey[:],
		Signer:      signer,
		Sequence:    sequence,
		MinSequence: 1,
		MaxSequence: sequence,
	}
	ps.add(msg, txKey, peerID)
}

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
				tracker.testAdd(signer, key, 1, 5)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 1)
				require.Equal(t, key, entries[0].txKey)
				require.Equal(t, uint64(1), entries[0].sequence)
				require.Equal(t, []uint16{5}, entries[0].peerIDs())
			},
		},
		{
			name: "re-adding same tx adds peer",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx2")
				tracker.testAdd(signer, key, 2, 5)
				tracker.testAdd(signer, key, 2, 5) // duplicate peer - no effect
				tracker.testAdd(signer, key, 2, 7) // different peer - should be added

				entry := tracker.entriesForSigner(signer)[0]
				peerIDs := entry.peerIDs()
				require.Len(t, peerIDs, 2)
				// Check both peers are present (order may vary)
				require.Contains(t, peerIDs, uint16(5))
				require.Contains(t, peerIDs, uint16(7))
			},
		},
		{
			name: "adding beyond nominal limit retains entries",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("a")
				key2 := txKey("b")
				key3 := txKey("c")

				tracker.testAdd(signer, key1, 1, 1)
				tracker.testAdd(signer, key2, 2, 2)
				tracker.testAdd(signer, key3, 3, 3)

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

				tracker.testAdd(signer, key1, 1, 1)
				tracker.testAdd(signer, key2, 2, 2)
				tracker.testAdd(signer, key3, 3, 3)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, []types.TxKey{key1, key2}, []types.TxKey{entries[0].txKey, entries[1].txKey})
			},
		},
		{
			name: "remove tx clears entry",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx3")
				tracker.testAdd(signer, key, 3, 6)

				tracker.remove(key)
				require.Empty(t, tracker.entriesForSigner(signer))
			},
		},
		{
			name: "remove peer clears peer and removes orphaned entries",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx4")
				tracker.testAdd(signer, key, 4, 9)
				tracker.markRequested(key, 9)

				tracker.removePeer(9)
				// Entry should be removed since it has no peers left
				entries := tracker.entriesForSigner(signer)
				require.Empty(t, entries)
			},
		},
		{
			name: "remove peer keeps entries with other peers",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx4b")
				tracker.testAdd(signer, key, 4, 9)
				tracker.testAdd(signer, key, 4, 10) // Add second peer

				tracker.removePeer(9)
				// Entry should remain since peer 10 still has it
				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 1)
				require.Equal(t, []uint16{10}, entries[0].peerIDs())
			},
		},
		{
			name: "peerIDs returns copy",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx5")
				tracker.testAdd(signer, key, 5, 21)
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

				tracker.testAdd(signer, key1, 10, 1)
				tracker.testAdd(signer, key2, 5, 1)
				tracker.testAdd(signer, key3, 7, 1)

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 3)
				require.Equal(t, []uint64{5, 7, 10}, []uint64{entries[0].sequence, entries[1].sequence, entries[2].sequence})
			},
		},
		{
			name: "mark requested and failed updates state",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx6")
				tracker.testAdd(signer, key, 7, 12)
				tracker.markRequested(key, 12)

				entry := tracker.entriesForSigner(signer)[0]
				require.True(t, entry.requested)
				require.Equal(t, uint16(12), entry.lastPeer)

				tracker.markRequestFailed(key, 12)

				entry = tracker.entriesForSigner(signer)[0]
				require.False(t, entry.requested)
				require.Equal(t, uint16(0), entry.lastPeer)
				// Peer should be removed from entry after failed request
				require.Nil(t, entry.peerIDs())
			},
		},
		{
			name: "same signer and sequence with different txKeys",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("tx-version-1")
				key2 := txKey("tx-version-2")

				// Add same (signer, sequence) with different txKeys
				tracker.testAdd(signer, key1, 10, 5)
				tracker.testAdd(signer, key2, 10, 7)

				// Should have both in the queue
				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, uint64(10), entries[0].sequence)

				// Both txKeys should be tracked
				require.NotNil(t, tracker.byTx[key1])
				require.NotNil(t, tracker.byTx[key2])
			},
		},
		{
			name: "invalid MinSequence/MaxSequence rejected",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("invalid")
				// MinSequence > MaxSequence should be rejected
				msg := &protomem.SeenTx{
					TxKey:       key[:],
					Signer:      signer,
					Sequence:    5,
					MinSequence: 10,
					MaxSequence: 5,
				}
				tracker.add(msg, key, 1)

				require.Empty(t, tracker.entriesForSigner(signer))
			},
		},
		{
			name: "sequence outside range rejected",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("outside")
				// Sequence outside [MinSequence, MaxSequence] should be rejected
				msg := &protomem.SeenTx{
					TxKey:       key[:],
					Signer:      signer,
					Sequence:    15, // Outside range
					MinSequence: 1,
					MaxSequence: 10,
				}
				tracker.add(msg, key, 1)

				require.Empty(t, tracker.entriesForSigner(signer))
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
			tracker.testAdd(signer, key, uint64(i+1), uint16(i%5+1))
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
