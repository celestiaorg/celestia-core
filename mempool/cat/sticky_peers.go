package cat

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"github.com/cometbft/cometbft/p2p"
)

const stickyHashNamespace = "cat/sticky/v1"

type stickyPeer struct {
	id   uint16
	peer p2p.Peer
}

type stickyPeerScore struct {
	stickyPeer
	score uint64
}

// selectStickyPeers returns up to limit peers deterministically ranked for the signer.
// Uses rendezvous (highest random weight) hashing to compute the ranking.
func selectStickyPeers(signer []byte, peers map[uint16]p2p.Peer, limit int, salt []byte) []stickyPeer {
	if limit <= 0 || len(peers) == 0 {
		return nil
	}

	scored := make([]stickyPeerScore, 0, len(peers))
	for id, peer := range peers {
		if peer == nil {
			continue
		}
		scored = append(scored, stickyPeerScore{
			stickyPeer: stickyPeer{
				id:   id,
				peer: peer,
			},
			score: stickyScore64(signer, string(peer.ID()), salt),
		})
	}

	if len(scored) == 0 {
		return nil
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			peerIDI := string(scored[i].peer.ID())
			peerIDJ := string(scored[j].peer.ID())
			if peerIDI == peerIDJ {
				return scored[i].id < scored[j].id
			}
			return peerIDI < peerIDJ
		}
		return scored[i].score > scored[j].score
	})

	if limit > len(scored) {
		limit = len(scored)
	}

	out := make([]stickyPeer, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, scored[i].stickyPeer)
	}

	return out
}

// stickyScore64 hashes (signer, peerID, salt) -> uint64.
func stickyScore64(signer []byte, peerID string, salt []byte) uint64 {
	h := sha256.New()
	h.Write([]byte(stickyHashNamespace))
	if len(salt) > 0 {
		h.Write(salt)
	}
	if len(signer) > 0 {
		h.Write(signer)
	}
	h.Write([]byte{0})
	h.Write([]byte(peerID))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}
