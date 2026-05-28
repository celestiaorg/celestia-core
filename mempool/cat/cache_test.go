package cat

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestSeenTrackerPeerOnly(t *testing.T) {
	var (
		tx1Key        = types.Tx("tx1").Key()
		tx2Key        = types.Tx("tx2").Key()
		tx3Key        = types.Tx("tx3").Key()
		peer1  uint16 = 1
		peer2  uint16 = 2
	)

	tracker := NewSeenTracker()
	require.Nil(t, tracker.Peers(tx1Key))

	tracker.Add(tx1Key, peer1, nil, 0)
	tracker.Add(tx1Key, peer1, nil, 0) // duplicate; no-op
	require.Equal(t, 1, tracker.Len())

	tracker.Add(tx1Key, peer2, nil, 0)
	require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, tracker.Peers(tx1Key))

	tracker.Add(tx2Key, peer1, nil, 0)
	tracker.Add(tx3Key, peer1, nil, 0)
	require.Equal(t, 3, tracker.Len())

	tracker.RemoveKey(tx2Key)
	require.Equal(t, 2, tracker.Len())
	require.Nil(t, tracker.Peers(tx2Key))
	require.True(t, tracker.Has(tx3Key, peer1))
}

func TestSeenTrackerPendingFlow(t *testing.T) {
	signer := []byte("signer")
	otherSigner := []byte("other")

	txKey := func(label string) types.TxKey {
		return types.Tx(label).Key()
	}

	cases := []struct {
		name           string
		perSignerLimit int
		run            func(*testing.T, *SeenTracker)
	}{
		{
			name: "add and retrieve sequence-indexed entry",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("tx1")
				tracker.Add(key, 5, signer, 1)

				entries := tracker.PendingForSigner(signer)
				require.Len(t, entries, 1)
				require.Equal(t, key, entries[0].txKey)
				require.Equal(t, uint64(1), entries[0].sequence)
				require.Equal(t, []uint16{5}, entries[0].peerIDs())
			},
		},
		{
			name: "re-adding same tx with different peer adds both peers",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("tx2")
				tracker.Add(key, 5, signer, 2)
				tracker.Add(key, 5, signer, 2)
				tracker.Add(key, 7, signer, 2)

				entry := tracker.PendingForSigner(signer)[0]
				peers := entry.peers
				require.Equal(t, map[uint16]struct{}{5: {}, 7: {}}, peers)
			},
		},
		{
			name: "queue beyond nominal capacity retains all entries when under limit",
			run: func(t *testing.T, tracker *SeenTracker) {
				key1 := txKey("a")
				key2 := txKey("b")
				key3 := txKey("c")

				tracker.Add(key1, 1, signer, 1)
				tracker.Add(key2, 2, signer, 2)
				tracker.Add(key3, 3, signer, 3)

				entries := tracker.PendingForSigner(signer)
				require.Len(t, entries, 3)
				require.Equal(t, []types.TxKey{key1, key2, key3}, []types.TxKey{entries[0].txKey, entries[1].txKey, entries[2].txKey})

				require.Nil(t, tracker.PendingForSigner(otherSigner))
			},
		},
		{
			name:           "per-signer limit demotes tail",
			perSignerLimit: 2,
			run: func(t *testing.T, tracker *SeenTracker) {
				key1 := txKey("a")
				key2 := txKey("b")
				key3 := txKey("c")

				tracker.Add(key1, 1, signer, 1)
				tracker.Add(key2, 2, signer, 2)
				tracker.Add(key3, 3, signer, 3)

				entries := tracker.PendingForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, []types.TxKey{key1, key2}, []types.TxKey{entries[0].txKey, entries[1].txKey})

				// Tail demote keeps the byTx entry alive (so peer-has-tx
				// info survives) but removes signer/sequence from it.
				demoted := tracker.Get(key3)
				require.NotNil(t, demoted)
				require.Equal(t, uint64(0), demoted.sequence)
				require.Empty(t, demoted.signer)
				require.True(t, tracker.Has(key3, 3))
			},
		},
		{
			name: "remove tx clears entry",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("tx3")
				tracker.Add(key, 6, signer, 3)

				tracker.RemoveKey(key)
				require.Empty(t, tracker.PendingForSigner(signer))
				require.Nil(t, tracker.Get(key))
			},
		},
		{
			name: "remove peer empties single-peer entry and clears request state",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("tx4")
				tracker.Add(key, 9, signer, 4)
				tracker.MarkRequested(key, 9)

				tracker.RemovePeer(9)
				require.Empty(t, tracker.PendingForSigner(signer))
				require.Nil(t, tracker.Get(key))
				require.Equal(t, 0, peerCount(t, tracker, 9))
			},
		},
		{
			name: "remove peer preserves entries with other peers",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("multi")
				tracker.Add(key, 9, signer, 4)
				tracker.Add(key, 11, signer, 4)
				tracker.MarkRequested(key, 9)

				tracker.RemovePeer(9)
				entry := tracker.PendingForSigner(signer)[0]
				require.Equal(t, map[uint16]struct{}{11: {}}, entry.peers)
				require.False(t, entry.requested)
				require.Equal(t, uint16(0), entry.lastPeer)
				require.Equal(t, 0, peerCount(t, tracker, 9))
				require.Equal(t, 1, peerCount(t, tracker, 11))
			},
		},
		{
			name: "PendingForSigner returns clones",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("tx5")
				tracker.Add(key, 21, signer, 5)
				entry := tracker.PendingForSigner(signer)[0]

				entry.peers[99] = struct{}{}
				require.Equal(t, map[uint16]struct{}{21: {}}, tracker.PendingForSigner(signer)[0].peers)
			},
		},
		{
			name: "entries stored in ascending sequence order",
			run: func(t *testing.T, tracker *SeenTracker) {
				key1 := txKey("seq10")
				key2 := txKey("seq5")
				key3 := txKey("seq7")

				tracker.Add(key1, 1, signer, 10)
				tracker.Add(key2, 1, signer, 5)
				tracker.Add(key3, 1, signer, 7)

				entries := tracker.PendingForSigner(signer)
				require.Len(t, entries, 3)
				require.Equal(t, []uint64{5, 7, 10}, []uint64{entries[0].sequence, entries[1].sequence, entries[2].sequence})
			},
		},
		{
			name: "mark requested and failed updates state and drops peer",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("tx6")
				tracker.Add(key, 12, signer, 7)
				tracker.MarkRequested(key, 12)

				entry := tracker.PendingForSigner(signer)[0]
				require.True(t, entry.requested)
				require.Equal(t, uint16(12), entry.lastPeer)

				tracker.MarkRequestFailed(key, 12)

				// peer 12 was the only peer; the entry is removed.
				require.Empty(t, tracker.PendingForSigner(signer))
				require.Equal(t, 0, peerCount(t, tracker, 12))
			},
		},
		{
			name: "same signer and sequence with different txKeys",
			run: func(t *testing.T, tracker *SeenTracker) {
				key1 := txKey("tx-version-1")
				key2 := txKey("tx-version-2")

				tracker.Add(key1, 5, signer, 10)
				tracker.Add(key2, 7, signer, 10)

				entries := tracker.PendingForSigner(signer)
				require.Len(t, entries, 2)
				require.Equal(t, uint64(10), entries[0].sequence)

				require.NotNil(t, tracker.Get(key1))
				require.NotNil(t, tracker.Get(key2))
			},
		},
		{
			name: "peer-has-tx upgrades to sequence-indexed on later SeenTx",
			run: func(t *testing.T, tracker *SeenTracker) {
				key := txKey("upgrade")
				// Arrives first via PeerHasTx (no signer/seq).
				tracker.Add(key, 3, nil, 0)
				require.Empty(t, tracker.PendingForSigner(signer))

				// Sequence-bearing SeenTx arrives later; upgrades in place.
				tracker.Add(key, 3, signer, 42)
				entries := tracker.PendingForSigner(signer)
				require.Len(t, entries, 1)
				require.Equal(t, uint64(42), entries[0].sequence)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewSeenTracker()
			if tc.perSignerLimit > 0 {
				tracker.perSignerLimit = tc.perSignerLimit
			}
			tc.run(t, tracker)
			requireSeenTrackerInvariants(t, tracker)
		})
	}
}

func TestSeenTrackerConcurrentAccess(t *testing.T) {
	tracker := NewSeenTracker()
	signer := []byte("signer")

	const total = 5000
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			key := types.Tx(fmt.Sprintf("tx-%d", i)).Key()
			tracker.Add(key, uint16(i%5+1), signer, uint64(i+1))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			tracker.PendingForSigner(signer)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			tracker.RemovePeer(uint16(i%5 + 1))
		}
	}()

	wg.Wait()
	require.LessOrEqual(t, len(tracker.PendingForSigner(signer)), tracker.perSignerLimit)
	requireSeenTrackerInvariants(t, tracker)
}

func TestSeenTrackerPerPeerLimit(t *testing.T) {
	tracker := NewSeenTracker()

	const limitedPeer = uint16(7)
	const otherPeer = uint16(9)

	keys := addSeenFromPeer(t, tracker, "limited", tracker.perPeerLimit, limitedPeer)
	require.Equal(t, tracker.perPeerLimit, peerCount(t, tracker, limitedPeer))

	blockedKey := types.Tx("limited-blocked").Key()
	tracker.Add(blockedKey, limitedPeer, []byte("limited-blocked-signer"), 1)
	require.Nil(t, tracker.Get(blockedKey))
	require.Equal(t, tracker.perPeerLimit, peerCount(t, tracker, limitedPeer))

	// A different peer can still admit new entries.
	otherKey := types.Tx("other-peer").Key()
	tracker.Add(otherKey, otherPeer, []byte("other-peer-signer"), 1)
	require.NotNil(t, tracker.Get(otherKey))
	require.Equal(t, 1, peerCount(t, tracker, otherPeer))

	// Adding the limited peer back via a tx that already has another peer
	// is also blocked once the limited peer is at capacity.
	tracker.Add(otherKey, limitedPeer, nil, 0)
	require.False(t, tracker.Has(otherKey, limitedPeer))

	// Freeing one slot lets the limited peer admit one more.
	tracker.RemoveKey(keys[0])
	require.Equal(t, tracker.perPeerLimit-1, peerCount(t, tracker, limitedPeer))
	newKey := types.Tx("limited-new").Key()
	tracker.Add(newKey, limitedPeer, []byte("limited-new-signer"), 1)
	require.Equal(t, tracker.perPeerLimit, peerCount(t, tracker, limitedPeer))
	require.NotNil(t, tracker.Get(newKey))

	requireSeenTrackerInvariants(t, tracker)
}

func TestSeenTrackerTimeBasedEviction(t *testing.T) {
	tracker := NewSeenTracker()
	tracker.perSignerLimit = 1000

	now := time.Now().UTC()
	tracker.now = func() time.Time { return now }

	signer := []byte("signer")

	oldKey := types.Tx("old").Key()
	tracker.Add(oldKey, 5, signer, 1)

	now = now.Add(seenEntryTTL)
	cutoffKey := types.Tx("cutoff").Key()
	tracker.Add(cutoffKey, 7, signer, 2)

	now = now.Add(seenEntryTTL)
	freshKey := types.Tx("fresh").Key()
	tracker.Add(freshKey, 6, signer, 3)

	require.Equal(t, 3, tracker.Len())

	tracker.Prune(now.Add(-seenEntryTTL))

	require.Equal(t, 2, tracker.Len())
	require.Nil(t, tracker.Get(oldKey))
	require.NotNil(t, tracker.Get(cutoffKey))
	require.NotNil(t, tracker.Get(freshKey))
	require.Equal(t, 0, peerCount(t, tracker, 5), "evicted entry's peer count must be released")
	require.Equal(t, 1, peerCount(t, tracker, 6))
	require.Equal(t, 1, peerCount(t, tracker, 7))
	requireSeenTrackerInvariants(t, tracker)
}

func addSeenFromPeer(t *testing.T, tracker *SeenTracker, prefix string, total int, peerID uint16) []types.TxKey {
	t.Helper()

	keys := make([]types.TxKey, total)
	for i := 0; i < total; i++ {
		key := types.Tx(fmt.Sprintf("%s-tx-%d", prefix, i)).Key()
		// Distinct signer per tx so the per-signer cap never interferes.
		signer := []byte(fmt.Sprintf("%s-signer-%d", prefix, i))
		tracker.Add(key, peerID, signer, uint64(i+1))
		keys[i] = key
	}

	return keys
}

func peerCount(t *testing.T, tracker *SeenTracker, peerID uint16) int {
	t.Helper()

	tracker.mtx.Lock()
	defer tracker.mtx.Unlock()
	return tracker.countByPeer[peerID]
}

func requireSeenTrackerInvariants(t *testing.T, tracker *SeenTracker) {
	t.Helper()

	tracker.mtx.Lock()
	defer tracker.mtx.Unlock()

	// Sequence-indexed entries: each lives in exactly one per-signer queue
	// in ascending order, and is the same object as the byTx entry.
	indexed := make(map[types.TxKey]struct{})
	for signerKey, queue := range tracker.perSigner {
		require.LessOrEqual(t, len(queue), tracker.perSignerLimit)
		for i, entry := range queue {
			require.Equal(t, signerKey, entry.signerKey)
			require.NotZero(t, entry.sequence)
			require.Same(t, entry, tracker.byTx[entry.txKey])
			require.NotContains(t, indexed, entry.txKey)
			indexed[entry.txKey] = struct{}{}
			if i > 0 {
				require.LessOrEqual(t, queue[i-1].sequence, entry.sequence)
			}
		}
	}

	// countByPeer reflects union of peers across every byTx entry.
	countByPeer := make(map[uint16]int)
	for txKey, entry := range tracker.byTx {
		require.Equal(t, txKey, entry.txKey)
		require.NotEmpty(t, entry.peers, "entries with no peers must be deleted")
		if entry.signerKey == "" {
			require.Zero(t, entry.sequence)
			require.Empty(t, entry.signer)
		} else {
			require.Contains(t, indexed, entry.txKey)
		}
		for peer := range entry.peers {
			countByPeer[peer]++
		}
	}

	require.Equal(t, countByPeer, tracker.countByPeer)
}
