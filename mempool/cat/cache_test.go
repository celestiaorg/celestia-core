package cat

import (
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-clock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestNewSeenTracker(t *testing.T) {
	tracker := NewSeenTracker()
	require.Equal(t, seenPerPeerLimit, tracker.perPeerLimit)
	require.Equal(t, seenPerSignerLimit, tracker.perSignerLimit)
	require.NotNil(t, tracker.clock)
	require.Equal(t, 0, tracker.Len())
}

func TestSeenTrackerAdd(t *testing.T) {
	var (
		txKey        = types.Tx("tx").Key()
		peer1 uint16 = 1
		peer2 uint16 = 2
	)

	t.Run("peer 0 rejected", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.False(t, tracker.AddPeer(txKey, 0))
		require.Equal(t, 0, tracker.Len())
	})

	t.Run("new tx is indexed", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.Has(txKey, peer1))
		require.Equal(t, 1, tracker.Len())
		require.Equal(t, 1, tracker.txCountByPeer[peer1])
	})

	t.Run("duplicate entry, should be idempotent", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.Equal(t, 1, tracker.Len())
		require.Equal(t, 1, tracker.txCountByPeer[peer1])
	})

	t.Run("two peers", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.AddPeer(txKey, peer2))
		require.Equal(t, 1, tracker.Len())
		require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, tracker.PeersForTx(txKey))
	})

	t.Run("per-peer limit on new tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.perPeerLimit = 1
		require.True(t, tracker.AddPeer(types.Tx("a").Key(), peer1))
		require.False(t, tracker.AddPeer(types.Tx("b").Key(), peer1))
		require.Equal(t, 1, tracker.Len())
	})

	t.Run("per-peer limit joining existing tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.perPeerLimit = 1
		shared := types.Tx("shared").Key()
		require.True(t, tracker.AddPeer(types.Tx("a").Key(), peer1)) // peer1 now at limit
		require.True(t, tracker.AddPeer(shared, peer2))
		require.False(t, tracker.AddPeer(shared, peer1)) // peer1 cannot join
		require.False(t, tracker.Has(shared, peer1))
	})
}

func TestSeenTrackerHas(t *testing.T) {
	var (
		txKey        = types.Tx("tx").Key()
		peer1 uint16 = 1
		peer2 uint16 = 2
	)

	t.Run("unknown tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.False(t, tracker.Has(txKey, peer1))
	})

	t.Run("known peer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.Has(txKey, peer1))
	})

	t.Run("unknown peer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.False(t, tracker.Has(txKey, peer2))
	})
}

func TestSeenTrackerGet(t *testing.T) {
	var (
		txKey         = types.Tx("tx").Key()
		signer        = []byte("signer")
		peer1  uint16 = 1
	)

	t.Run("unknown tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.Nil(t, tracker.Get(txKey))
	})

	t.Run("returns a copy", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, 3))

		entry := tracker.Get(txKey)
		require.NotNil(t, entry)
		require.Equal(t, txKey, entry.txKey)

		// Mutating the returned entry must not affect tracker state.
		delete(entry.peers, peer1)
		entry.pendingTxInfo.sequence = 99
		require.True(t, tracker.Has(txKey, peer1))
		_, seq, ok := tracker.PendingTxInfo(txKey)
		require.True(t, ok)
		require.Equal(t, uint64(3), seq)
	})
}

func TestSeenTrackerPeers(t *testing.T) {
	var (
		txKey        = types.Tx("tx").Key()
		peer1 uint16 = 1
	)

	t.Run("unknown tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.Nil(t, tracker.PeersForTx(txKey))
	})

	t.Run("returns a copy", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))

		peers := tracker.PeersForTx(txKey)
		delete(peers, peer1)
		require.True(t, tracker.Has(txKey, peer1))
	})
}

func TestSeenTrackerLen(t *testing.T) {
	var peer1 uint16 = 1

	tracker := NewSeenTracker()
	require.Equal(t, 0, tracker.Len())
	require.True(t, tracker.AddPeer(types.Tx("a").Key(), peer1))
	require.True(t, tracker.AddPeer(types.Tx("b").Key(), peer1))
	require.Equal(t, 2, tracker.Len())
}

func TestSeenTrackerReset(t *testing.T) {
	var peer1 uint16 = 1

	tracker := NewSeenTracker()
	require.True(t, tracker.AddPendingTx(types.Tx("a").Key(), peer1, []byte("signer"), 1))

	tracker.Reset()
	require.Equal(t, 0, tracker.Len())
	require.Empty(t, tracker.txCountByPeer)
	require.Nil(t, tracker.SignersWithPendingTxs())
}

func TestSeenTrackerRemoveKey(t *testing.T) {
	var (
		txKey         = types.Tx("tx").Key()
		signer        = []byte("signer")
		peer1  uint16 = 1
	)

	t.Run("unknown tx is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.RemoveKey(txKey) // must not panic
		require.Equal(t, 0, tracker.Len())
	})

	t.Run("clears every index", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, 1))

		tracker.RemoveKey(txKey)
		require.Equal(t, 0, tracker.Len())
		require.Nil(t, tracker.PeersForTx(txKey))
		require.Empty(t, tracker.txCountByPeer)
		require.Nil(t, tracker.PendingTxsForSigner(signer))
	})
}

func TestSeenTrackerRemovePeer(t *testing.T) {
	var (
		txKey        = types.Tx("tx").Key()
		peer1 uint16 = 1
		peer2 uint16 = 2
	)

	t.Run("peer 0 is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		tracker.RemovePeer(0)
		require.True(t, tracker.Has(txKey, peer1))
	})

	t.Run("entry survives while another peer remains", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.AddPeer(txKey, peer2))

		tracker.RemovePeer(peer1)
		require.False(t, tracker.Has(txKey, peer1))
		require.True(t, tracker.Has(txKey, peer2))
		require.Equal(t, 1, tracker.Len())
		require.Zero(t, tracker.txCountByPeer[peer1])
	})

	t.Run("entry removed with last peer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))

		tracker.RemovePeer(peer1)
		require.Equal(t, 0, tracker.Len())
		require.Empty(t, tracker.txCountByPeer)
	})

	t.Run("clears in-flight request from that peer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.AddPeer(txKey, peer2))
		tracker.MarkRequested(txKey, peer1)

		tracker.RemovePeer(peer1)
		entry := tracker.Get(txKey)
		require.NotNil(t, entry)
		require.False(t, entry.requested)
		require.Equal(t, uint16(0), entry.lastPeer)
	})
}

func TestSeenTrackerPrune(t *testing.T) {
	var (
		baseTime        = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		oldKey          = types.Tx("old").Key()
		freshKey        = types.Tx("fresh").Key()
		peer1    uint16 = 1
	)

	tracker, mockClock := newClockTracker(baseTime)
	require.True(t, tracker.AddPeer(oldKey, peer1))

	mockClock.Set(baseTime.Add(3 * time.Minute))
	require.True(t, tracker.AddPeer(freshKey, peer1))

	tracker.PruneExpired()

	require.Nil(t, tracker.Get(oldKey))
	require.NotNil(t, tracker.Get(freshKey))
	require.Equal(t, 1, tracker.txCountByPeer[peer1])
}

func TestSeenTrackerMarkRequested(t *testing.T) {
	var (
		txKey        = types.Tx("tx").Key()
		peer1 uint16 = 1
	)

	t.Run("peer 0 is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		tracker.MarkRequested(txKey, 0)
		require.False(t, tracker.Get(txKey).requested)
	})

	t.Run("unknown tx is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.MarkRequested(txKey, peer1) // must not panic
		require.Nil(t, tracker.Get(txKey))
	})

	t.Run("records the peer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		tracker.MarkRequested(txKey, peer1)

		entry := tracker.Get(txKey)
		require.True(t, entry.requested)
		require.Equal(t, peer1, entry.lastPeer)
	})
}

func TestSeenTrackerMarkRequestFailed(t *testing.T) {
	var (
		txKey        = types.Tx("tx").Key()
		peer1 uint16 = 1
		peer2 uint16 = 2
	)

	t.Run("peer 0 is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		tracker.MarkRequestFailed(txKey, 0)
		require.True(t, tracker.Has(txKey, peer1))
	})

	t.Run("unknown tx is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.MarkRequestFailed(txKey, peer1) // must not panic
		require.Equal(t, 0, tracker.Len())
	})

	t.Run("drops the peer and clears its request", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		require.True(t, tracker.AddPeer(txKey, peer2))
		tracker.MarkRequested(txKey, peer1)

		tracker.MarkRequestFailed(txKey, peer1)
		entry := tracker.Get(txKey)
		require.NotNil(t, entry)
		require.False(t, entry.requested)
		require.Equal(t, uint16(0), entry.lastPeer)
		require.False(t, tracker.Has(txKey, peer1))
		require.True(t, tracker.Has(txKey, peer2))
	})

	t.Run("removes entry when last peer fails", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))

		tracker.MarkRequestFailed(txKey, peer1)
		require.Equal(t, 0, tracker.Len())
		require.Empty(t, tracker.txCountByPeer)
	})
}

func TestSeenTrackerAddIndexesPendingTx(t *testing.T) {
	var (
		signer        = []byte("signer")
		peer1  uint16 = 1
		peer2  uint16 = 2
	)

	t.Run("indexes by signer", func(t *testing.T) {
		tracker := NewSeenTracker()
		txKey := types.Tx("tx").Key()
		seq := uint64(7)
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, seq))

		gotSigner, gotSeq, ok := tracker.PendingTxInfo(txKey)
		require.True(t, ok)
		require.Equal(t, signer, gotSigner)
		require.Equal(t, seq, gotSeq)
	})

	t.Run("sequence 0 is valid", func(t *testing.T) {
		tracker := NewSeenTracker()
		txKey := types.Tx("tx").Key()
		zeroSeq := uint64(0)
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, zeroSeq))
		_, gotSeq, ok := tracker.PendingTxInfo(txKey)
		require.True(t, ok)
		require.Equal(t, zeroSeq, gotSeq)
	})

	t.Run("empty signer not indexed", func(t *testing.T) {
		tracker := NewSeenTracker()
		txKey := types.Tx("tx").Key()
		require.True(t, tracker.AddPeer(txKey, peer1))
		_, _, ok := tracker.PendingTxInfo(txKey)
		require.False(t, ok)
		require.Nil(t, tracker.SignersWithPendingTxs())
	})

	t.Run("ordered by sequence", func(t *testing.T) {
		tracker := NewSeenTracker()
		keySeq2 := types.Tx("seq2").Key()
		keySeq0 := types.Tx("seq0").Key()
		keySeq1 := types.Tx("seq1").Key()
		require.True(t, tracker.AddPendingTx(keySeq2, peer1, signer, 2))
		require.True(t, tracker.AddPendingTx(keySeq0, peer1, signer, 0))
		require.True(t, tracker.AddPendingTx(keySeq1, peer2, signer, 1))

		entries := tracker.PendingTxsForSigner(signer)
		require.Equal(t, []uint64{0, 1, 2}, seenTrackerSequences(entries))
		require.Equal(t, []types.TxKey{keySeq0, keySeq1, keySeq2}, seenTrackerKeys(entries))
	})

	t.Run("add signer and seq to an indexed tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		txKey := types.Tx("tx").Key()

		// First seen without a signer: tracked by peer only, not queued by sequence.
		require.True(t, tracker.AddPeer(txKey, peer1))
		_, _, ok := tracker.PendingTxInfo(txKey)
		require.False(t, ok)

		// Seen again with a signer: the same tx is now also queued by sequence,
		// while keeping the peer that was already recorded.
		require.True(t, tracker.AddPendingTx(txKey, peer2, signer, 4))
		_, seq, ok := tracker.PendingTxInfo(txKey)
		require.True(t, ok)
		require.Equal(t, uint64(4), seq)
		require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, tracker.PeersForTx(txKey))
	})

	t.Run("per-signer limit keeps lowest sequences", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.perSignerLimit = 2
		keySeq5 := types.Tx("seq5").Key()
		keySeq7 := types.Tx("seq7").Key()
		keySeq6 := types.Tx("seq6").Key()
		require.True(t, tracker.AddPendingTx(keySeq5, peer1, signer, 5))
		require.True(t, tracker.AddPendingTx(keySeq7, peer1, signer, 7))
		require.True(t, tracker.AddPendingTx(keySeq6, peer1, signer, 6))

		entries := tracker.PendingTxsForSigner(signer)
		require.Equal(t, []uint64{5, 6}, seenTrackerSequences(entries))

		// The demoted tx survives as peer-only state.
		demoted := tracker.Get(keySeq7)
		require.NotNil(t, demoted)
		require.Nil(t, demoted.pendingTxInfo)
		require.True(t, tracker.Has(keySeq7, peer1))
	})

	t.Run("per-signer limit rejects higher sequence when full", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.perSignerLimit = 2
		keySeq5 := types.Tx("seq5").Key()
		keySeq6 := types.Tx("seq6").Key()
		keySeq9 := types.Tx("seq9").Key()
		require.True(t, tracker.AddPendingTx(keySeq5, peer1, signer, 5))
		require.True(t, tracker.AddPendingTx(keySeq6, peer1, signer, 6))
		// Queue is full with [5,6]; a higher sequence must stay peer-only rather
		// than displace a lower one.
		require.True(t, tracker.AddPendingTx(keySeq9, peer1, signer, 9))

		require.Equal(t, []uint64{5, 6}, seenTrackerSequences(tracker.PendingTxsForSigner(signer)))
		require.Nil(t, tracker.Get(keySeq9).pendingTxInfo)
		require.True(t, tracker.Has(keySeq9, peer1))
	})

	t.Run("does not re-index an already-indexed key", func(t *testing.T) {
		tracker := NewSeenTracker()
		txKey := types.Tx("tx").Key()
		signerA := []byte("signer-a")
		signerB := []byte("signer-b")
		require.True(t, tracker.AddPendingTx(txKey, peer1, signerA, 1))

		// Re-adding the same key with a different signer keeps the original
		// signer/sequence; only the peer is added.
		require.True(t, tracker.AddPendingTx(txKey, peer2, signerB, 2))

		gotSigner, seq, ok := tracker.PendingTxInfo(txKey)
		require.True(t, ok)
		require.Equal(t, signerA, gotSigner)
		require.Equal(t, uint64(1), seq)
		require.NotNil(t, tracker.PendingTxsForSigner(signerA))
		require.Nil(t, tracker.PendingTxsForSigner(signerB))
		require.Equal(t, map[uint16]struct{}{peer1: {}, peer2: {}}, tracker.PeersForTx(txKey))
	})
}

func TestSeenTrackerPendingTxInfo(t *testing.T) {
	var (
		txKey         = types.Tx("tx").Key()
		signer        = []byte("signer")
		peer1  uint16 = 1
	)

	t.Run("unknown tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		_, _, ok := tracker.PendingTxInfo(txKey)
		require.False(t, ok)
	})

	t.Run("peer-only tx", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		_, _, ok := tracker.PendingTxInfo(txKey)
		require.False(t, ok)
	})

	t.Run("returns a copy of the signer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, 2))

		gotSigner, _, ok := tracker.PendingTxInfo(txKey)
		require.True(t, ok)
		// Mutating the returned signer must not affect tracker state.
		gotSigner[0] = 'x'
		stillSigner, seq, _ := tracker.PendingTxInfo(txKey)
		require.Equal(t, signer, stillSigner)
		require.Equal(t, uint64(2), seq)
	})
}

func TestSeenTrackerPendingTxsForSigner(t *testing.T) {
	var (
		signer        = []byte("signer")
		peer1  uint16 = 1
	)

	t.Run("empty signer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.Nil(t, tracker.PendingTxsForSigner(nil))
	})

	t.Run("unknown signer", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.Nil(t, tracker.PendingTxsForSigner(signer))
	})

	t.Run("returns deep copies", func(t *testing.T) {
		tracker := NewSeenTracker()
		txKey := types.Tx("tx").Key()
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, 1))

		entries := tracker.PendingTxsForSigner(signer)
		require.Len(t, entries, 1)
		delete(entries[0].peers, peer1)
		require.True(t, tracker.Has(txKey, peer1))
	})
}

func TestSeenTrackerSignersWithPendingTxs(t *testing.T) {
	var peer1 uint16 = 1

	t.Run("none", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.Nil(t, tracker.SignersWithPendingTxs())
	})

	t.Run("lists signers", func(t *testing.T) {
		tracker := NewSeenTracker()
		signerA := []byte("signer-a")
		signerB := []byte("signer-b")
		require.True(t, tracker.AddPendingTx(types.Tx("a").Key(), peer1, signerA, 0))
		require.True(t, tracker.AddPendingTx(types.Tx("b").Key(), peer1, signerB, 0))

		require.ElementsMatch(t, [][]byte{signerA, signerB}, tracker.SignersWithPendingTxs())
	})
}

func TestSeenTrackerClearSequence(t *testing.T) {
	var (
		txKey         = types.Tx("tx").Key()
		signer        = []byte("signer")
		peer1  uint16 = 1
	)

	t.Run("unknown tx is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		tracker.ClearPendingTx(txKey) // must not panic
	})

	t.Run("peer-only tx is a no-op", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPeer(txKey, peer1))
		tracker.ClearPendingTx(txKey)
		require.True(t, tracker.Has(txKey, peer1))
	})

	t.Run("drops future tx state, keeps peer cache", func(t *testing.T) {
		tracker := NewSeenTracker()
		require.True(t, tracker.AddPendingTx(txKey, peer1, signer, 5))

		tracker.ClearPendingTx(txKey)
		_, _, ok := tracker.PendingTxInfo(txKey)
		require.False(t, ok)
		require.True(t, tracker.Has(txKey, peer1))
		require.Nil(t, tracker.PendingTxsForSigner(signer))
	})
}

func TestSeenTrackerPrunePending(t *testing.T) {
	var (
		baseTime        = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		oldKey          = types.Tx("old").Key()
		freshKey        = types.Tx("fresh").Key()
		signer          = []byte("signer")
		peer1    uint16 = 1
	)

	tracker, mockClock := newClockTracker(baseTime)
	require.True(t, tracker.AddPendingTx(oldKey, peer1, signer, 1))

	mockClock.Set(baseTime.Add(3 * time.Minute))
	require.True(t, tracker.AddPendingTx(freshKey, peer1, signer, 2))

	tracker.PrunePending(baseTime.Add(time.Minute))

	// Old future metadata is dropped, but the peer cache survives.
	_, _, oldOK := tracker.PendingTxInfo(oldKey)
	require.False(t, oldOK)
	require.True(t, tracker.Has(oldKey, peer1))
	// Fresh future metadata is kept.
	_, _, freshOK := tracker.PendingTxInfo(freshKey)
	require.True(t, freshOK)
	require.Equal(t, []types.TxKey{freshKey}, seenTrackerKeys(tracker.PendingTxsForSigner(signer)))
}

func TestSeenTrackerConcurrent(t *testing.T) {
	const peers = 8
	tracker := NewSeenTracker()
	keys := []types.TxKey{types.Tx("a").Key(), types.Tx("b").Key()}

	// Phase 1: concurrent Adds on shared keys. Adds are additive, so regardless
	// of interleaving every key ends up known by every peer.
	var wg sync.WaitGroup
	for g := range peers {
		wg.Add(1)
		go func(peer uint16) {
			defer wg.Done()
			for range 100 {
				for _, k := range keys {
					tracker.AddPendingTx(k, peer, []byte{byte(peer)}, uint64(peer))
					tracker.Get(k) // clone must not alias the entry others mutate
				}
			}
		}(uint16(g + 1))
	}
	wg.Wait()

	require.Equal(t, len(keys), tracker.Len())
	for _, k := range keys {
		require.Len(t, tracker.PeersForTx(k), peers) // exact: all peers present
	}
	for p := uint16(1); p <= peers; p++ {
		require.Equal(t, len(keys), tracker.txCountByPeer[p]) // exact count
	}

	// Phase 2: each peer concurrently removes itself; tracker must end empty.
	for g := range peers {
		wg.Add(1)
		go func(peer uint16) { defer wg.Done(); tracker.RemovePeer(peer) }(uint16(g + 1))
	}
	wg.Wait()

	require.Equal(t, 0, tracker.Len())
	require.Empty(t, tracker.txCountByPeer)
}

// newClockTracker returns a tracker driven by a controllable mock clock set to now.
func newClockTracker(now time.Time) (*SeenTracker, *clock.Mock) {
	mockClock := clock.NewMock()
	mockClock.Set(now)
	tracker := NewSeenTracker()
	tracker.clock = mockClock
	return tracker, mockClock
}

func seenTrackerSequences(entries []*SeenEntry) []uint64 {
	out := make([]uint64, len(entries))
	for i, entry := range entries {
		out[i] = entry.pendingTxInfo.sequence
	}
	return out
}

func seenTrackerKeys(entries []*SeenEntry) []types.TxKey {
	out := make([]types.TxKey, len(entries))
	for i, entry := range entries {
		out[i] = entry.txKey
	}
	return out
}
