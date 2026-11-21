package cat

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

// sameSequenceTestReactor returns a reactor whose peers accept every send.
func sameSequenceTestReactor(t *testing.T, numPeers int) (*Reactor, *sequenceTrackingApp, []*mocks.Peer) {
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
	return reactor, app, peers
}

func receiveSeenTx(reactor *Reactor, src p2p.Peer, key types.TxKey, signer []byte, sequence uint64) {
	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.SeenTx{TxKey: key[:], Signer: signer, Sequence: sequence},
		Src:       src,
	})
}

// TestReactorRequestsEveryKeyQueuedAtSequence checks that when peers
// announce different txs at the same signer/sequence, each of those txs is
// requested. SeenTx metadata is unverified, so the scan cannot tell which
// claim is true, and a claim for one tx must not hold back another.
func TestReactorRequestsEveryKeyQueuedAtSequence(t *testing.T) {
	signer := []byte("sender-000-0")

	testCases := []struct {
		name string
		// otherFirst is set when the conflicting claims are announced
		// before the honest ones.
		otherFirst bool
	}{
		{name: "conflicting claims announced after the honest ones"},
		{name: "conflicting claims announced before the honest ones", otherFirst: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reactor, app, peers := sameSequenceTestReactor(t, 2)
			honest, other := peers[0], peers[1]
			honestID := reactor.ids.GetIDForPeer(honest.ID())
			otherID := reactor.ids.GetIDForPeer(other.ID())
			app.SetSequence(string(signer), 0)

			const numSequences = 3
			keys := make([]types.TxKey, numSequences)
			conflicting := make([]types.TxKey, numSequences)
			for i := range keys {
				keys[i] = newDefaultTx(fmt.Sprintf("same-sequence-%d", i)).Key()
				conflicting[i] = types.Tx(fmt.Sprintf("same-sequence-conflict-%d", i)).Key()
			}

			// Sequence 0 is the expected one, so claims start at 1 and
			// are queued.
			announce := func(peer p2p.Peer, keys []types.TxKey) {
				for i, key := range keys {
					receiveSeenTx(reactor, peer, key, signer, uint64(i+1))
				}
			}
			if tc.otherFirst {
				announce(other, conflicting)
			}
			announce(honest, keys)
			if !tc.otherFirst {
				announce(other, conflicting)
			}

			// The gap closes.
			app.SetSequence(string(signer), 1)
			reactor.processPendingSeenForSigner(signer)

			for i := range keys {
				require.EqualValues(t, honestID, reactor.requests.ForTx(keys[i]), "the tx at sequence %d must be requested", i+1)
				require.EqualValues(t, otherID, reactor.requests.ForTx(conflicting[i]), "the other tx at sequence %d must be requested", i+1)
			}
		})
	}
}

// TestReactorSameSequenceClaimsStayBounded checks that many claims at one
// sequence do not become more requests than the existing limits allow: each
// peer is asked for at most maxRequestsPerPeer txs, and no tx is requested
// maxReceivedBufferSize or more sequences ahead of the expected one.
func TestReactorSameSequenceClaimsStayBounded(t *testing.T) {
	signer := []byte("sender-000-0")
	reactor, app, peers := sameSequenceTestReactor(t, 2)
	honest, other := peers[0], peers[1]
	honestID := reactor.ids.GetIDForPeer(honest.ID())
	otherID := reactor.ids.GetIDForPeer(other.ID())
	app.SetSequence(string(signer), 0)

	// The honest peer announces one tx at each sequence from 1, to past
	// the look-ahead limit.
	keys := make([]types.TxKey, maxReceivedBufferSize+5)
	for i := range keys {
		keys[i] = newDefaultTx(fmt.Sprintf("bounded-%d", i)).Key()
		receiveSeenTx(reactor, honest, keys[i], signer, uint64(i+1))
	}
	// The other peer announces many more txs at sequence 1 than one peer
	// can be asked for.
	for i := 0; i < 3*maxRequestsPerPeer; i++ {
		receiveSeenTx(reactor, other, types.Tx(fmt.Sprintf("bounded-conflict-%d", i)).Key(), signer, 1)
	}

	// The gap closes.
	app.SetSequence(string(signer), 1)
	reactor.processPendingSeenForSigner(signer)

	require.Equal(t, maxRequestsPerPeer, reactor.requests.CountForPeer(otherID), "requests to one peer stay under the per-peer limit")
	for i, key := range keys {
		if i < maxReceivedBufferSize {
			require.EqualValues(t, honestID, reactor.requests.ForTx(key), "the tx at sequence %d must be requested", i+1)
		} else {
			require.Zero(t, reactor.requests.ForTx(key), "the tx at sequence %d is too far ahead to request", i+1)
		}
	}
}
