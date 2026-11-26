package propagation

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
)

// envelopeRecordingPeer is a mock peer that keeps every envelope sent to it.
type envelopeRecordingPeer struct {
	*mock.Peer
	mtx       sync.Mutex
	envelopes []p2p.Envelope
}

func (peer *envelopeRecordingPeer) TrySend(e p2p.Envelope) bool {
	peer.mtx.Lock()
	defer peer.mtx.Unlock()
	peer.envelopes = append(peer.envelopes, e)
	return true
}

func (peer *envelopeRecordingPeer) sentEnvelopes() []p2p.Envelope {
	peer.mtx.Lock()
	defer peer.mtx.Unlock()
	return append([]p2p.Envelope(nil), peer.envelopes...)
}

func addEnvelopeRecordingPeer(r *Reactor) *envelopeRecordingPeer {
	peer := &envelopeRecordingPeer{Peer: mock.NewPeer(net.IP{127, 0, 0, 1})}
	r.setPeer(peer.ID(), newPeerState(context.Background(), peer, log.NewNopLogger()))
	return peer
}

// requireOnlyBulkParts checks that the peer was sent at least one message and
// that all of them are block parts marked as bulk.
func requireOnlyBulkParts(t *testing.T, peer *envelopeRecordingPeer) {
	t.Helper()
	envelopes := peer.sentEnvelopes()
	require.NotEmpty(t, envelopes)
	for _, envelope := range envelopes {
		require.IsType(t, &propproto.RecoveryPart{}, envelope.Message)
		require.Equal(t, DataChannel, envelope.ChannelID)
		require.True(t, envelope.Bulk, "a part sent in answer to a want must be bulk")
	}
}

// Parts sent in answer to a want are bulk, so they can't use the room that the
// data channel send queue reserves for compact blocks and haves.
func TestPartsServedFromCacheAreBulk(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	peer := addEnvelopeRecordingPeer(r)
	height := cacheTestProposal(t, r, 0, ps)

	r.handleWants(peer.ID(), wantAllParts(height, 0, ps.Total(), false))

	requireOnlyBulkParts(t, peer)
}

func TestPartsServedFromStoreAreBulk(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	peer := addEnvelopeRecordingPeer(r)

	r.handleWants(peer.ID(), storedWant(ps.Total(), 2, 0, 1))

	requireOnlyBulkParts(t, peer)
}

// A compact block is not sent again if the send queue refuses it, so it must
// be able to use the reserved room.
func TestCompactBlockIsNotBulk(t *testing.T) {
	r, _ := newStoredBlockReactor(t)
	peer := addEnvelopeRecordingPeer(r)

	r.broadcastCompactBlock(&proptypes.CompactBlock{}, "")

	envelopes := peer.sentEnvelopes()
	require.Len(t, envelopes, 1)
	require.False(t, envelopes[0].Bulk)
}
