package cat

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

// seenMetadataTestReactor returns a reactor whose peers accept every send.
func seenMetadataTestReactor(t *testing.T, numPeers int) (*Reactor, *TxPool, *sequenceTrackingApp, []*mocks.Peer) {
	t.Helper()
	app := newSequenceTrackingApp()
	pool, cleanup := newMempoolWithApp(proxy.NewLocalClientCreator(app))
	t.Cleanup(cleanup)

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)
	t.Cleanup(reactor.requests.Close)

	peers := genPeers(numPeers)
	for _, peer := range peers {
		peer.On("TrySend", mock.Anything).Return(true).Maybe()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)
	}
	return reactor, pool, app, peers
}

func receiveSeenTx(reactor *Reactor, src p2p.Peer, key types.TxKey, signer []byte, sequence uint64) {
	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.SeenTx{TxKey: key[:], Signer: signer, Sequence: sequence},
		Src:       src,
	})
}

func receiveTx(reactor *Reactor, src p2p.Peer, tx types.Tx) {
	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.Txs{Txs: [][]byte{tx}},
		Src:       src,
	})
}

// TestReactorDeliveryIgnoresOtherPeersSeenTxMetadata checks that signer and
// sequence metadata announced by one peer is not used to decide what happens
// to the tx when a different peer delivers it.
func TestReactorDeliveryIgnoresOtherPeersSeenTxMetadata(t *testing.T) {
	realSigner := []byte("sender-000-0")
	otherSigner := []byte("some-other-signer")

	testCases := []struct {
		name     string
		sequence uint64
	}{
		{name: "beyond the buffer window", sequence: 1_000_000},
		{name: "within the buffer window", sequence: 5},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reactor, pool, app, peers := seenMetadataTestReactor(t, 2)
			first, second := peers[0], peers[1]
			app.SetSequence(string(realSigner), 0)

			tx := newDefaultTx("seen-metadata")
			key := tx.Key()

			receiveSeenTx(reactor, first, key, otherSigner, tc.sequence)
			require.Zero(t, reactor.requests.ForTx(key), "a future-sequence announcement must not be requested")

			receiveSeenTx(reactor, second, key, realSigner, 0)
			secondID := reactor.ids.GetIDForPeer(second.ID())
			require.EqualValues(t, secondID, reactor.requests.ForTx(key), "an in-sequence announcement must be requested")

			receiveTx(reactor, second, tx)

			require.Equal(t, 1, app.CheckTxCalls(), "the delivered tx must be checked by the application")
			require.True(t, pool.Has(key), "the delivered tx must enter the mempool")
			require.Empty(t, reactor.receivedBuffer.signerKeys(), "the delivered tx must not be buffered")
		})
	}
}

// TestReactorDeliveryIgnoresPeersOwnContradictedMetadata checks that a peer
// whose later announcement for the same tx contradicts its earlier one cannot
// have the earlier metadata applied to its delivery.
func TestReactorDeliveryIgnoresPeersOwnContradictedMetadata(t *testing.T) {
	reactor, pool, app, peers := seenMetadataTestReactor(t, 1)
	peer := peers[0]
	peerID := reactor.ids.GetIDForPeer(peer.ID())

	tx := newDefaultTx("contradicted-metadata")
	key := tx.Key()

	receiveSeenTx(reactor, peer, key, []byte("signer-a"), 1_000_000)
	require.Zero(t, reactor.requests.ForTx(key))

	receiveSeenTx(reactor, peer, key, []byte("signer-b"), 0)
	require.EqualValues(t, peerID, reactor.requests.ForTx(key))

	receiveTx(reactor, peer, tx)

	require.Equal(t, 1, app.CheckTxCalls(), "the delivered tx must be checked by the application")
	require.True(t, pool.Has(key), "the delivered tx must enter the mempool")
}

// TestReactorDeliveryFromAgreeingPeerIsBuffered checks that every peer that
// announced the same future sequence can deliver the tx into the buffer, so
// pipelined gap-fill keeps working when the request goes to any of them.
func TestReactorDeliveryFromAgreeingPeerIsBuffered(t *testing.T) {
	reactor, pool, app, peers := seenMetadataTestReactor(t, 2)
	first, second := peers[0], peers[1]
	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	tx := newDefaultTx("agreeing-metadata")
	key := tx.Key()

	receiveSeenTx(reactor, first, key, signer, 3)
	receiveSeenTx(reactor, second, key, signer, 3)
	require.Zero(t, reactor.requests.ForTx(key))

	requestTxFromPeer(t, reactor, second, key)
	receiveTx(reactor, second, tx)

	require.Zero(t, app.CheckTxCalls(), "a tx ahead of the expected sequence must wait in the buffer")
	require.False(t, pool.Has(key))
	require.NotNil(t, reactor.receivedBuffer.get(signer, 3), "the tx must be buffered at its announced sequence")
}

// TestReactorRetryToPeerAnnouncedDuringRequestIsBuffered checks that a peer
// announcing the queued metadata while the tx is already being requested is
// recorded as an announcer, so a retry to it still lands in the buffer.
func TestReactorRetryToPeerAnnouncedDuringRequestIsBuffered(t *testing.T) {
	reactor, pool, app, peers := seenMetadataTestReactor(t, 2)
	first, second := peers[0], peers[1]
	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	tx := newDefaultTx("retry-metadata")
	key := tx.Key()

	receiveSeenTx(reactor, first, key, signer, 3)
	requestTxFromPeer(t, reactor, first, key)
	receiveSeenTx(reactor, second, key, signer, 3)

	// The first peer goes away, so the request is retried with the second.
	reactor.RemovePeer(first, "test")
	secondID := reactor.ids.GetIDForPeer(second.ID())
	require.EqualValues(t, secondID, reactor.requests.ForTx(key))

	receiveTx(reactor, second, tx)

	require.Zero(t, app.CheckTxCalls(), "a tx ahead of the expected sequence must wait in the buffer")
	require.False(t, pool.Has(key))
	require.NotNil(t, reactor.receivedBuffer.get(signer, 3), "the tx must be buffered at its announced sequence")
}

// TestTryRequestQueuedTxPrefersAnnouncingPeers checks that gap-fill asks a
// peer that announced the queued sequence before a peer only known to have
// the tx.
func TestTryRequestQueuedTxPrefersAnnouncingPeers(t *testing.T) {
	reactor, _, _, peers := seenMetadataTestReactor(t, 2)
	signer := []byte("sender-000-0")
	announcerID := reactor.ids.GetIDForPeer(peers[0].ID())
	otherID := reactor.ids.GetIDForPeer(peers[1].ID())

	for i := 0; i < 20; i++ {
		key := types.Tx([]byte{byte(i), 'k'}).Key()
		reactor.mempool.PeerHasTx(otherID, key)
		require.True(t, reactor.mempool.seenTracker.AddPendingTx(key, announcerID, signer, 3))

		entry := reactor.mempool.seenTracker.Get(key)
		require.True(t, reactor.tryRequestQueuedTx(entry))
		require.EqualValues(t, announcerID, reactor.requests.ForTx(key))
	}
}

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

// TestSeenTrackerClearsMetadataWithoutAnnouncers checks that queued metadata
// is dropped once its last announcer goes away, even while other peers are
// still known to have the tx.
func TestSeenTrackerClearsMetadataWithoutAnnouncers(t *testing.T) {
	signer := []byte("signer")
	removals := map[string]func(*SeenTracker, types.TxKey){
		"peer removed":        func(s *SeenTracker, _ types.TxKey) { s.RemovePeer(1) },
		"peer removed for tx": func(s *SeenTracker, key types.TxKey) { s.RemovePeerFromTx(key, 1) },
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			tracker := NewSeenTracker()
			key := types.Tx("no-announcers").Key()
			require.True(t, tracker.AddPendingTx(key, 1, signer, 3))
			require.True(t, tracker.AddPeer(key, 2))

			remove(tracker, key)

			require.Empty(t, tracker.PendingTxsForSigner(signer))
			_, _, ok := tracker.PendingTxInfo(key)
			require.False(t, ok)
			require.True(t, tracker.Has(key, 2), "the remaining peer is still known to have the tx")
		})
	}
}

// TestReactorContradictionDuringRequestIsApplied checks that a peer whose
// announcement changes while its tx is being requested cannot have its
// earlier metadata applied to its delivery.
func TestReactorContradictionDuringRequestIsApplied(t *testing.T) {
	reactor, pool, app, peers := seenMetadataTestReactor(t, 1)
	peer := peers[0]
	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	tx := newDefaultTx("contradiction-during-request")
	key := tx.Key()

	receiveSeenTx(reactor, peer, key, signer, 3)
	requestTxFromPeer(t, reactor, peer, key)
	receiveSeenTx(reactor, peer, key, []byte("other-signer"), 0)

	receiveTx(reactor, peer, tx)

	require.Equal(t, 1, app.CheckTxCalls(), "the delivered tx must be checked by the application")
	require.True(t, pool.Has(key))
	require.Empty(t, reactor.receivedBuffer.signerKeys())
}
