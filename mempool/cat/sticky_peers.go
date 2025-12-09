package cat

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand"

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

// selectStickyPeers returns up to limit peers in random order.
// Sticky peer selection is disabled - peers are shuffled randomly.
func selectStickyPeers(signer []byte, peers map[uint16]p2p.Peer, limit int, salt []byte) []stickyPeer {
	if limit <= 0 || len(peers) == 0 {
		return nil
	}

	// Collect all peers
	allPeers := make([]stickyPeer, 0, len(peers))
	for id, peer := range peers {
		if peer == nil {
			continue
		}
		allPeers = append(allPeers, stickyPeer{
			id:   id,
			peer: peer,
		})
	}

	if len(allPeers) == 0 {
		return nil
	}

	// Shuffle randomly instead of deterministic ordering
	rand.Shuffle(len(allPeers), func(i, j int) {
		allPeers[i], allPeers[j] = allPeers[j], allPeers[i]
	})

	if limit > len(allPeers) {
		limit = len(allPeers)
	}

	return allPeers[:limit]
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
