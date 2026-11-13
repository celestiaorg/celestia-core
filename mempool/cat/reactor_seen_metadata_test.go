package cat

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"
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
	gotSigner, gotSeq, ok := tracker.PendingTxInfoFrom(key, 3)
	require.True(t, ok)
	require.Equal(t, []byte("other"), gotSigner, "a peer is attributed only the claim it announced")
	require.EqualValues(t, 7, gotSeq)

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
// can still be requested when the gap closes, but that such claims never
// outnumber the peers that have the tx.
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

			// Peer 2 now backs its own claim, so the claim without
			// announcers would outnumber the peers and is dropped.
			require.True(t, tracker.AddPendingTx(key, 2, signer, 4))
			_, seq, ok := tracker.PendingTxInfoFrom(key, 2)
			require.True(t, ok)
			require.EqualValues(t, 4, seq)
			require.Len(t, tracker.PendingTxsForSigner(signer), 1)
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

// sequenceEnforcingApp rejects a registered tx unless its sequence is the
// signer's expected one, and advances the sequence when it accepts it, like
// the real application does.
type sequenceEnforcingApp struct {
	*sequenceTrackingApp
	txSequences map[string]uint64
}

const errCodeWrongSequence = 32

func (app *sequenceEnforcingApp) CheckTx(ctx context.Context, req *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	rsp, err := app.sequenceTrackingApp.CheckTx(ctx, req)
	if err != nil || rsp.Code != abcitypes.CodeTypeOK {
		return rsp, err
	}
	app.mtx.Lock()
	defer app.mtx.Unlock()
	sequence, ok := app.txSequences[string(req.Tx)]
	if !ok {
		return rsp, nil
	}
	signer := string(rsp.Address)
	if sequence != app.sequences[signer] {
		return &abcitypes.ResponseCheckTx{Code: errCodeWrongSequence, Address: rsp.Address, Sequence: sequence}, nil
	}
	app.sequences[signer]++
	rsp.Sequence = sequence
	return rsp, nil
}

// sequenceEnforcingReactor is seenMetadataTestReactor with an application
// that checks each tx's sequence.
func sequenceEnforcingReactor(t *testing.T, numPeers int) (*Reactor, *TxPool, *sequenceEnforcingApp, []*mocks.Peer) {
	t.Helper()
	app := &sequenceEnforcingApp{sequenceTrackingApp: newSequenceTrackingApp(), txSequences: map[string]uint64{}}
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

// queuedSequences returns the sequences under which key is queued for signer,
// and the peers that announced each one.
func queuedSequences(reactor *Reactor, signer []byte, key types.TxKey) map[uint64][]uint16 {
	out := map[uint64][]uint16{}
	for _, entry := range reactor.mempool.seenTracker.PendingTxsForSigner(signer) {
		if entry.txKey != key {
			continue
		}
		announcers := make([]uint16, 0)
		for peer := range entry.pendingTxInfo.announcers {
			announcers = append(announcers, peer)
		}
		out[entry.pendingTxInfo.sequence] = announcers
	}
	return out
}

// requireRequestedAndAccepted closes the signer's gap at sequence, checks the
// tx is requested from want, and checks it enters the mempool once delivered.
func requireRequestedAndAccepted(t *testing.T, reactor *Reactor, pool *TxPool, app *sequenceEnforcingApp, want *mocks.Peer, signer []byte, sequence uint64, tx types.Tx) {
	t.Helper()
	key := tx.Key()
	app.SetSequence(string(signer), sequence)
	reactor.processPendingSeenForSigner(signer)
	require.EqualValues(t, reactor.ids.GetIDForPeer(want.ID()), reactor.requests.ForTx(key), "the tx must be requested again once its gap closes")

	receiveTx(reactor, want, tx)
	require.True(t, pool.Has(key), "the tx must enter the mempool at its real sequence")
}

// TestReactorRejectedDeliveryKeepsOtherPeersMetadata checks that when a
// delivery is rejected, signer/sequence metadata that other peers announced
// stays queued, so the tx is requested again once its gap closes.
func TestReactorRejectedDeliveryKeepsOtherPeersMetadata(t *testing.T) {
	signer := []byte("sender-000-0")
	const sequence = 3

	testCases := []struct {
		name string
		// deliver makes the attacker deliver the tx while the honest peer's
		// metadata says it is ahead of the expected sequence.
		deliver func(t *testing.T, reactor *Reactor, honest, attacker *mocks.Peer, key types.TxKey)
		// honestTimedOut is set when the honest peer is no longer a source,
		// so the gap-fill request goes to the remaining peer.
		honestTimedOut bool
	}{
		{
			name: "attacker announces at the current sequence",
			deliver: func(t *testing.T, reactor *Reactor, honest, attacker *mocks.Peer, key types.TxKey) {
				receiveSeenTx(reactor, honest, key, signer, sequence)
				receiveSeenTx(reactor, attacker, key, []byte("fresh-signer"), 0)
				require.EqualValues(t, reactor.ids.GetIDForPeer(attacker.ID()), reactor.requests.ForTx(key))
			},
		},
		{
			name: "attacker contradicts itself while its request is in flight",
			deliver: func(t *testing.T, reactor *Reactor, honest, attacker *mocks.Peer, key types.TxKey) {
				receiveSeenTx(reactor, attacker, key, signer, sequence)
				receiveSeenTx(reactor, honest, key, signer, sequence)
				requestTxFromPeer(t, reactor, attacker, key)
				receiveSeenTx(reactor, attacker, key, signer, 0)
			},
		},
		{
			name: "request is retried with a peer that did not announce the metadata",
			deliver: func(t *testing.T, reactor *Reactor, honest, attacker *mocks.Peer, key types.TxKey) {
				receiveSeenTx(reactor, honest, key, signer, sequence)
				reactor.mempool.PeerHasTx(reactor.ids.GetIDForPeer(attacker.ID()), key)
				requestTxFromPeer(t, reactor, honest, key)

				honestID := reactor.ids.GetIDForPeer(honest.ID())
				require.True(t, reactor.requests.Remove(key, honestID))
				reactor.onRequestTimeout(key, honestID)
				require.EqualValues(t, reactor.ids.GetIDForPeer(attacker.ID()), reactor.requests.ForTx(key))
			},
			honestTimedOut: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reactor, pool, app, peers := sequenceEnforcingReactor(t, 2)
			honest, attacker := peers[0], peers[1]
			app.SetSequence(string(signer), 1)

			tx := newDefaultTx("rejected-delivery")
			key := tx.Key()
			app.txSequences[string(tx)] = sequence

			tc.deliver(t, reactor, honest, attacker, key)
			receiveTx(reactor, attacker, tx)

			require.Equal(t, 1, app.CheckTxCalls(), "the delivery is checked by the application")
			require.False(t, pool.Has(key), "the tx is ahead of the expected sequence")
			rejected, _, _ := pool.WasRecentlyRejected(key)
			require.False(t, rejected, "the tx must not be recorded as rejected while other peers say it is ahead")
			require.Contains(t, queuedSequences(reactor, signer, key), uint64(sequence), "the honest peer's metadata must stay queued")

			source := honest
			if tc.honestTimedOut {
				source = attacker
			}
			requireRequestedAndAccepted(t, reactor, pool, app, source, signer, sequence, tx)
		})
	}
}

// TestReactorFirstAnnouncementDoesNotHideLaterOnes checks that each distinct
// signer/sequence announced for a tx is queued, so a bogus first announcement
// cannot hide the real one.
func TestReactorFirstAnnouncementDoesNotHideLaterOnes(t *testing.T) {
	reactor, pool, app, peers := sequenceEnforcingReactor(t, 2)
	honest, attacker := peers[0], peers[1]
	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	tx := newDefaultTx("first-announcement")
	key := tx.Key()
	app.txSequences[string(tx)] = 3

	receiveSeenTx(reactor, attacker, key, signer, 9)
	receiveSeenTx(reactor, honest, key, signer, 3)

	queued := queuedSequences(reactor, signer, key)
	require.ElementsMatch(t, []uint16{reactor.ids.GetIDForPeer(honest.ID())}, queued[3], "the honest announcement must be queued")
	require.ElementsMatch(t, []uint16{reactor.ids.GetIDForPeer(attacker.ID())}, queued[9])

	requireRequestedAndAccepted(t, reactor, pool, app, honest, signer, 3, tx)
}

// TestReactorStaleDeliveryKeepsOtherSources checks that a delivery whose own
// announced sequence is already used only removes that peer as a source: the
// tx is requested from another peer that has it.
func TestReactorStaleDeliveryKeepsOtherSources(t *testing.T) {
	reactor, _, app, peers := sequenceEnforcingReactor(t, 2)
	honest, attacker := peers[0], peers[1]
	signer := []byte("sender-000-0")
	attackerSigner := []byte("attacker-signer")
	app.SetSequence(string(signer), 1)
	app.SetSequence(string(attackerSigner), 5)

	tx := newDefaultTx("stale-delivery")
	key := tx.Key()
	app.txSequences[string(tx)] = 3

	receiveSeenTx(reactor, honest, key, signer, 3)
	receiveSeenTx(reactor, attacker, key, attackerSigner, 6)
	requestTxFromPeer(t, reactor, attacker, key)
	app.SetSequence(string(attackerSigner), 7)

	receiveTx(reactor, attacker, tx)

	require.Zero(t, app.CheckTxCalls(), "a delivery at a used sequence is dropped without a check")
	require.False(t, reactor.mempool.seenTracker.Has(key, reactor.ids.GetIDForPeer(attacker.ID())), "the stale peer is no longer a source")
	require.Contains(t, queuedSequences(reactor, signer, key), uint64(3), "the honest peer's metadata must stay queued")
	require.EqualValues(t, reactor.ids.GetIDForPeer(honest.ID()), reactor.requests.ForTx(key), "the tx must be requested from the remaining source")

	receiveTx(reactor, honest, tx)
	require.NotNil(t, reactor.receivedBuffer.get(signer, 3), "the honest delivery is buffered at its announced sequence")
}

// queuedAt returns the sequences under which key is queued for signer.
func queuedAt(reactor *Reactor, signer []byte, key types.TxKey) []uint64 {
	var out []uint64
	for _, entry := range reactor.mempool.seenTracker.PendingTxsForSigner(signer) {
		if entry.txKey == key {
			out = append(out, entry.pendingTxInfo.sequence)
		}
	}
	return out
}

// expireRequest does what the request timer does when peer does not answer
// in time: the request moves on, but peer keeps its slot for a late reply.
func expireRequest(t *testing.T, reactor *Reactor, key types.TxKey, peer uint16) {
	t.Helper()
	reactor.requests.mtx.Lock()
	require.Equal(t, peer, reactor.requests.requestsByTx[key])
	delete(reactor.requests.requestsByTx, key)
	reactor.requests.mtx.Unlock()
	reactor.onRequestTimeout(key, peer)
}

// TestReactorLateReplyKeepsQueuedMetadata checks that a reply that arrives
// after its request timed out, while the tx is still ahead of the expected
// sequence, does not erase the queued metadata or mark the tx as rejected.
// This can happen between honest peers alone.
func TestReactorLateReplyKeepsQueuedMetadata(t *testing.T) {
	reactor, pool, app, peers := sequenceEnforcingReactor(t, 2)
	first, second := peers[0], peers[1]
	firstID, secondID := reactor.ids.GetIDForPeer(first.ID()), reactor.ids.GetIDForPeer(second.ID())
	signer := []byte("sender-000-0")
	const sequence = 3
	app.SetSequence(string(signer), 1)

	tx := newDefaultTx("late-reply")
	key := tx.Key()
	app.txSequences[string(tx)] = sequence

	receiveSeenTx(reactor, first, key, signer, sequence)
	receiveSeenTx(reactor, second, key, signer, sequence)
	requestTxFromPeer(t, reactor, first, key)
	expireRequest(t, reactor, key, firstID)
	require.EqualValues(t, secondID, reactor.requests.ForTx(key), "the request moves to the other announcer")

	receiveTx(reactor, first, tx)

	require.False(t, pool.Has(key), "the tx is ahead of the expected sequence")
	rejected, _, _ := pool.WasRecentlyRejected(key)
	require.False(t, rejected, "a late reply must not mark the tx as rejected")
	require.Contains(t, queuedAt(reactor, signer, key), uint64(sequence), "the other peer's metadata must stay queued")
	require.EqualValues(t, secondID, reactor.requests.ForTx(key), "the request to the other peer stays in flight")

	receiveTx(reactor, second, tx)
	require.NotNil(t, reactor.receivedBuffer.get(signer, sequence), "the other peer's delivery is buffered at its announced sequence")
}

// TestReactorCapFallbackDeliveryKeepsQueuedMetadata checks that when the
// announcer has no free request slot and gap-fill asks a peer that did not
// announce the tx, an early delivery from that peer does not erase the
// queued metadata, and the tx is requested again once its gap closes.
func TestReactorCapFallbackDeliveryKeepsQueuedMetadata(t *testing.T) {
	reactor, pool, app, peers := sequenceEnforcingReactor(t, 2)
	announcer, other := peers[0], peers[1]
	announcerID, otherID := reactor.ids.GetIDForPeer(announcer.ID()), reactor.ids.GetIDForPeer(other.ID())
	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	next, ahead := newDefaultTx("cap-fallback-2"), newDefaultTx("cap-fallback-3")
	app.txSequences[string(next)] = 2
	app.txSequences[string(ahead)] = 3
	for seq, tx := range map[uint64]types.Tx{2: next, 3: ahead} {
		receiveSeenTx(reactor, announcer, tx.Key(), signer, seq)
		reactor.mempool.PeerHasTx(otherID, tx.Key())
	}

	for i := 0; i < maxRequestsPerPeer; i++ {
		require.True(t, reactor.requests.Add(newDefaultTx(fmt.Sprintf("filler-%d", i)).Key(), announcerID, nil))
	}
	app.SetSequence(string(signer), 2)
	reactor.processPendingSeenForSigner(signer)
	require.EqualValues(t, otherID, reactor.requests.ForTx(next.Key()))
	require.EqualValues(t, otherID, reactor.requests.ForTx(ahead.Key()))

	receiveTx(reactor, other, ahead)

	require.False(t, pool.Has(ahead.Key()), "the tx is ahead of the expected sequence")
	rejected, _, _ := pool.WasRecentlyRejected(ahead.Key())
	require.False(t, rejected, "an early delivery must not mark the tx as rejected")
	require.Contains(t, queuedAt(reactor, signer, ahead.Key()), uint64(3), "the announcer's metadata must stay queued")

	receiveTx(reactor, other, next)
	require.True(t, pool.Has(next.Key()))
	require.EqualValues(t, otherID, reactor.requests.ForTx(ahead.Key()), "the tx must be requested again once its gap closes")

	receiveTx(reactor, other, ahead)
	require.True(t, pool.Has(ahead.Key()), "the tx must enter the mempool at its real sequence")
}
