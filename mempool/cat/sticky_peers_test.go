package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
)

func TestSelectStickyPeersLimitAndEmpty(t *testing.T) {
	t.Run("no peers", func(t *testing.T) {
		out := selectStickyPeers([]byte("signer"), nil, 5, nil)
		require.Nil(t, out)
	})

	t.Run("zero limit", func(t *testing.T) {
		peers := map[uint16]p2p.Peer{
			1: newMockPeer("peer-1"),
		}
		out := selectStickyPeers([]byte("signer"), peers, 0, nil)
		require.Nil(t, out)
	})

	t.Run("limit capped", func(t *testing.T) {
		peers := map[uint16]p2p.Peer{
			1: newMockPeer("peer-1"),
			2: newMockPeer("peer-2"),
		}
		out := selectStickyPeers([]byte("signer"), peers, 5, nil)
		require.Len(t, out, 2)
		require.ElementsMatch(t, []uint16{out[0].id, out[1].id}, []uint16{1, 2})
	})
}

func TestSelectStickyPeersDeterministic(t *testing.T) {
	peers := map[uint16]p2p.Peer{
		1: newMockPeer("peer-1"),
		2: newMockPeer("peer-2"),
		3: newMockPeer("peer-3"),
		4: newMockPeer("peer-4"),
		5: newMockPeer("peer-5"),
		6: newMockPeer("peer-6"),
		7: newMockPeer("peer-7"),
		8: newMockPeer("peer-8"),
	}
	signer := []byte("signer-A")
	salt := []byte("salt")

	first := selectStickyPeers(signer, peers, 3, salt)
	require.Len(t, first, 3)

	second := selectStickyPeers(signer, peers, 3, salt)
	require.Equal(t, stickyPeerIDs(first), stickyPeerIDs(second))

	altPeers := map[uint16]p2p.Peer{
		4: peers[4],
		2: peers[2],
		1: peers[1],
		3: peers[3],
		6: peers[6],
		5: peers[5],
		7: peers[7],
		8: peers[8],
	}
	third := selectStickyPeers(signer, altPeers, 3, salt)
	require.Equal(t, stickyPeerIDs(first), stickyPeerIDs(third))
}

func TestSelectStickyPeersDifferentSalt(t *testing.T) {
	peers := map[uint16]p2p.Peer{
		1: newMockPeer("peer-1"),
		2: newMockPeer("peer-2"),
		3: newMockPeer("peer-3"),
		4: newMockPeer("peer-4"),
	}
	signer := []byte("signer-A")

	first := selectStickyPeers(signer, peers, 3, []byte("salt-a"))
	second := selectStickyPeers(signer, peers, 3, []byte("salt-b"))
	require.Len(t, first, 3)
	require.Len(t, second, 3)
	require.NotEqual(t, stickyPeerIDs(first), stickyPeerIDs(second))
}

func TestStickyScoreDependsOnSignerAndSalt(t *testing.T) {
	peerID := "peer-1"
	scoreA := stickyScore64([]byte("signer-A"), peerID, nil)
	scoreB := stickyScore64([]byte("signer-B"), peerID, nil)
	require.NotEqual(t, scoreA, scoreB)

	scoreSalted := stickyScore64([]byte("signer-A"), peerID, []byte("salt"))
	require.NotEqual(t, scoreA, scoreSalted)
}

func stickyPeerIDs(peers []stickyPeer) []uint16 {
	ids := make([]uint16, len(peers))
	for i, peer := range peers {
		ids[i] = peer.id
	}
	return ids
}

func newMockPeer(id string) *mocks.Peer {
	peer := &mocks.Peer{}
	peer.On("ID").Return(p2p.ID(id))
	return peer
}
