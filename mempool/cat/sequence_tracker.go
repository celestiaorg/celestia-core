package cat

import (
	"math/rand/v2"
	"slices"
	"sync"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
)

type seqRange struct {
	min uint64
	max uint64
}

// sequenceTracker maintains per-peer signer sequence ranges derived from SeenTx messages.
type sequenceTracker struct {
	mu sync.Mutex
	// ranges tracks signer -> peerID -> seqRange.
	ranges map[string]map[uint16]seqRange
}

// newSequenceTracker constructs an empty sequence tracker.
func newSequenceTracker() *sequenceTracker {
	return &sequenceTracker{
		ranges: make(map[string]map[uint16]seqRange),
	}
}

// recordSeenTx updates range tracking for a peer based on a SeenTx message.
func (st *sequenceTracker) recordSeenTx(msg *protomem.SeenTx, peerID uint16) {
	if peerID == 0 || len(msg.Signer) == 0 {
		return
	}

	if msg.MinSequence == 0 && msg.MaxSequence == 0 {
		return
	}

	if msg.MinSequence > 0 && msg.MaxSequence > 0 && msg.MinSequence > msg.MaxSequence {
		return
	}

	signerKey := string(msg.Signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.ranges[signerKey] == nil {
		st.ranges[signerKey] = make(map[uint16]seqRange)
	}

	cur := st.ranges[signerKey][peerID]
	if msg.MinSequence > 0 {
		if cur.min == 0 || msg.MinSequence > cur.min {
			cur.min = msg.MinSequence
		}
	}
	if msg.MaxSequence > 0 {
		if msg.MaxSequence > cur.max {
			cur.max = msg.MaxSequence
		}
	}
	st.ranges[signerKey][peerID] = cur
}

// removePeer clears range tracking for a disconnected peer.
func (st *sequenceTracker) removePeer(peerID uint16) {
	if peerID == 0 {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	for signerKey, peers := range st.ranges {
		delete(peers, peerID)
		if len(peers) == 0 {
			delete(st.ranges, signerKey)
		}
	}
}

// removeBelowSequence adjusts ranges to drop sequences below expectedSeq.
func (st *sequenceTracker) removeBelowSequence(signer []byte, expectedSeq uint64) {
	if len(signer) == 0 || expectedSeq == 0 {
		return
	}

	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	peers := st.ranges[signerKey]
	for peerID, r := range peers {
		if r.max < expectedSeq {
			delete(peers, peerID)
			continue
		}
		if r.min < expectedSeq {
			r.min = expectedSeq
			peers[peerID] = r
		}
	}
	if len(peers) == 0 {
		delete(st.ranges, signerKey)
	}
}

// signerKeysFromPeerMax returns signer keys that peers are tracking ranges for.
func (st *sequenceTracker) signerKeysFromPeerMax() [][]byte {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.ranges) == 0 {
		return nil
	}

	out := make([][]byte, 0, len(st.ranges))
	for signerKey := range st.ranges {
		out = append(out, []byte(signerKey))
	}
	return out
}

// getPeersForSignerSequence returns peers whose ranges include the sequence.
func (st *sequenceTracker) getPeersForSignerSequence(signer []byte, sequence uint64) []uint16 {
	if len(signer) == 0 || sequence == 0 {
		return nil
	}

	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	peers := st.ranges[signerKey]
	if len(peers) == 0 {
		return nil
	}

	out := make([]uint16, 0, len(peers))
	for peerID, r := range peers {
		if r.max < sequence {
			continue
		}
		if r.min > 0 && sequence < r.min {
			continue
		}
		out = append(out, peerID)
	}
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// getMaxSeenSeqForSigner returns the highest max sequence observed for a signer.
func (st *sequenceTracker) getMaxSeenSeqForSigner(signer []byte) uint64 {
	if len(signer) == 0 {
		return 0
	}

	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	var maxSeq uint64
	for _, r := range st.ranges[signerKey] {
		if r.max > maxSeq {
			maxSeq = r.max
		}
	}
	return maxSeq
}

// rangesForSigner returns a snapshot of ranges for a signer (testing/debug use).
func (st *sequenceTracker) rangesForSigner(signer []byte) map[uint16]seqRange {
	if len(signer) == 0 {
		return nil
	}

	signerKey := string(signer)

	st.mu.Lock()
	defer st.mu.Unlock()

	peers := st.ranges[signerKey]
	if len(peers) == 0 {
		return nil
	}
	out := make(map[uint16]seqRange, len(peers))
	for peerID, r := range peers {
		out[peerID] = r
	}
	return out
}

// signerKeys returns signer keys for which ranges exist (testing helper).
func (st *sequenceTracker) signerKeys() [][]byte {
	keys := st.signerKeysFromPeerMax()
	if len(keys) == 0 {
		return nil
	}
	slices.SortFunc(keys, func(a, b []byte) int {
		return slices.Compare(a, b)
	})
	return keys
}
