package cat

import (
	"testing"

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
			name: "deduplicate and append peer ids",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx2")
				tracker.add(signer, key, 2, 5)
				tracker.add(signer, key, 2, 5) // duplicate peer
				tracker.add(signer, key, 2, 7) // new peer

				entry := tracker.entriesForSigner(signer)[0]
				require.ElementsMatch(t, []uint16{5, 7}, entry.peerIDs())
			},
		},
		{
			name:  "enforces per-signer limit and evicts oldest",
			limit: 2,
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key1 := txKey("a")
				key2 := txKey("b")
				key3 := txKey("c")

				tracker.add(signer, key1, 1, 1)
				tracker.add(signer, key2, 2, 2)
				tracker.add(signer, key3, 3, 3) // should evict key1

				entries := tracker.entriesForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, []types.TxKey{key2, key3}, []types.TxKey{entries[0].txKey, entries[1].txKey})

				require.Nil(t, tracker.entriesForSigner(otherSigner))
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
			name: "remove peer updates peer list",
			run: func(t *testing.T, tracker *pendingSeenTracker) {
				key := txKey("tx4")
				tracker.add(signer, key, 4, 9)
				tracker.add(signer, key, 4, 11)

				tracker.removePeer(9)

				entry := tracker.entriesForSigner(signer)[0]
				require.Equal(t, []uint16{11}, entry.peerIDs())

				tracker.removePeer(11)
				require.Empty(t, tracker.entriesForSigner(signer))
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPendingSeenTracker(tc.limit)
			tc.run(t, tracker)
		})
	}
}
