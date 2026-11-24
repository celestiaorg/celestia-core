package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p/mock"
)

// The switch can stop and remove a peer while it is still calling AddPeer
// for it. AddPeer must not then keep state for the removed peer.
func TestAddPeerAfterRemovePeerKeepsNoState(t *testing.T) {
	reactors, switches := testBlockPropReactors(1, defaultTestP2PConf())
	stopSwitchesOnCleanup(t, switches)
	r := reactors[0]

	peer := mock.NewPeer(nil)
	require.NoError(t, peer.Stop())
	r.RemovePeer(peer, "stopped")
	r.AddPeer(peer)
	require.Nil(t, r.getPeer(peer.ID()))

	running := mock.NewPeer(nil)
	r.AddPeer(running)
	ps := r.getPeer(running.ID())
	require.NotNil(t, ps)

	require.NoError(t, running.Stop())
	r.RemovePeer(running, "stopped")
	require.Nil(t, r.getPeer(running.ID()))
	require.Error(t, ps.ctx.Err(), "removing the peer stops its request routine")
}
