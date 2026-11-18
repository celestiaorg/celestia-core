package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

// TestSeenTrackerPendingTxInfoFrom checks which peers the queued metadata is
// attributed to as peers announce, contradict and disconnect.
func TestSeenTrackerPendingTxInfoFrom(t *testing.T) {
	tracker := NewSeenTracker()
	key := types.Tx("pending-info-from").Key()
	signer := []byte("signer")

	require.True(t, tracker.AddPendingTx(key, 1, signer, 3))
	require.True(t, tracker.AddPendingTx(key, 2, signer, 3))
	require.True(t, tracker.AddPendingTx(key, 3, []byte("other"), 7))

	for _, peer := range []uint16{1, 2} {
		gotSigner, gotSeq, ok := tracker.PendingTxInfoFrom(key, peer)
		require.True(t, ok)
		require.Equal(t, signer, gotSigner)
		require.EqualValues(t, 3, gotSeq)
	}
	_, _, ok := tracker.PendingTxInfoFrom(key, 3)
	require.False(t, ok, "a peer that announced different metadata must not be attributed the queued one")

	// A disconnected peer's ID may be reused, so its announcement is dropped.
	tracker.RemovePeer(2)
	_, _, ok = tracker.PendingTxInfoFrom(key, 2)
	require.False(t, ok)

	// Agreeing metadata is not withdrawn; conflicting metadata is, and the
	// queued metadata goes once no announcer is left.
	tracker.RecordPendingTxAnnouncement(key, 1, signer, 3)
	_, _, ok = tracker.PendingTxInfoFrom(key, 1)
	require.True(t, ok)
	tracker.RecordPendingTxAnnouncement(key, 1, signer, 4)
	_, _, ok = tracker.PendingTxInfoFrom(key, 1)
	require.False(t, ok)
	require.Empty(t, tracker.PendingTxsForSigner(signer))
	require.True(t, tracker.Has(key, 1), "the peer is still known to have the tx")
}

// TestSeenTrackerKeepsClaimWithoutAnnouncers checks that a claim stays queued
// when its last announcer stops being a source, so a retry with another peer
// can still be requested when the gap closes.
func TestSeenTrackerKeepsClaimWithoutAnnouncers(t *testing.T) {
	signer := []byte("signer")
	removals := map[string]func(*SeenTracker, types.TxKey, uint16){
		"peer removed":        func(s *SeenTracker, _ types.TxKey, peer uint16) { s.RemovePeer(peer) },
		"peer removed for tx": func(s *SeenTracker, key types.TxKey, peer uint16) { s.RemovePeerFromTx(key, peer) },
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			tracker := NewSeenTracker()
			key := types.Tx("no-announcers").Key()
			require.True(t, tracker.AddPendingTx(key, 1, signer, 3))
			require.True(t, tracker.AddPeer(key, 2))

			remove(tracker, key, 1)

			entries := tracker.PendingTxsForSigner(signer)
			require.Len(t, entries, 1, "the claim stays queued")
			require.Empty(t, entries[0].pendingTxInfo.announcers)
			_, _, ok := tracker.PendingTxInfoFrom(key, 1)
			require.False(t, ok, "a reused peer ID must not inherit the announcement")
			require.True(t, tracker.Has(key, 2), "the remaining peer is still known to have the tx")
		})
	}
}
