package cat

import (
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

const defaultPendingSeenPerSigner = 128

type pendingSeenTx struct {
	signerKey   string
	signer      []byte
	txKey       types.TxKey
	sequence    uint64
	peers       []uint16
	addedAt     time.Time
	requested   bool
	lastPeer    uint16
	requestedAt time.Time
}

func (p *pendingSeenTx) peerIDs() []uint16 {
	if len(p.peers) == 0 {
		return nil
	}
	out := make([]uint16, len(p.peers))
	copy(out, p.peers)
	return out
}

type pendingSeenTracker struct {
	mu        sync.Mutex
	perSigner map[string][]*pendingSeenTx
	byTx      map[types.TxKey]*pendingSeenTx
	limit     int
}

func newPendingSeenTracker(limit int) *pendingSeenTracker {
	if limit <= 0 {
		limit = defaultPendingSeenPerSigner
	}
	return &pendingSeenTracker{
		perSigner: make(map[string][]*pendingSeenTx),
		byTx:      make(map[types.TxKey]*pendingSeenTx),
		limit:     limit,
	}
}

func (ps *pendingSeenTracker) add(signer []byte, txKey types.TxKey, sequence uint64, peerID uint16) {
	if len(signer) == 0 || sequence == 0 || peerID == 0 {
		return
	}

	signerKey := string(signer)

	ps.mu.Lock()
	defer ps.mu.Unlock()

	if existing, ok := ps.byTx[txKey]; ok {
		if !containsPeer(existing.peers, peerID) {
			existing.peers = append(existing.peers, peerID)
		}
		return
	}

	entry := &pendingSeenTx{
		signerKey: signerKey,
		signer:    append([]byte(nil), signer...),
		txKey:     txKey,
		sequence:  sequence,
		addedAt:   time.Now().UTC(),
		peers:     []uint16{peerID},
	}

	queue := ps.perSigner[signerKey]
	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].sequence >= sequence
	})
	queue = append(queue, nil)
	copy(queue[insertIdx+1:], queue[insertIdx:])
	queue[insertIdx] = entry
	ps.perSigner[signerKey] = queue
	ps.byTx[txKey] = entry
	for len(queue) > ps.limit {
		lastIdx := len(queue) - 1
		removed := queue[lastIdx]
		if removed.requested {
			break
		}
		queue = queue[:lastIdx]
		delete(ps.byTx, removed.txKey)
	}
	if len(queue) == 0 {
		delete(ps.perSigner, signerKey)
	} else {
		ps.perSigner[signerKey] = queue
	}
}

func (ps *pendingSeenTracker) remove(txKey types.TxKey) *pendingSeenTx {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	entry, ok := ps.byTx[txKey]
	if !ok {
		return nil
	}

	signerKey := entry.signerKey
	queue := ps.perSigner[signerKey]
	for i, candidate := range queue {
		if candidate == entry {
			queue = append(queue[:i], queue[i+1:]...)
			break
		}
	}
	if len(queue) == 0 {
		delete(ps.perSigner, signerKey)
	} else {
		ps.perSigner[signerKey] = queue
	}
	delete(ps.byTx, txKey)
	return entry
}

func (ps *pendingSeenTracker) entriesForSigner(signer []byte) []*pendingSeenTx {
	if len(signer) == 0 {
		return nil
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	queue := ps.perSigner[string(signer)]
	if len(queue) == 0 {
		return nil
	}

	out := make([]*pendingSeenTx, len(queue))
	for i, entry := range queue {
		clone := *entry
		if len(entry.signer) > 0 {
			clone.signer = append([]byte(nil), entry.signer...)
		}
		if len(entry.peers) > 0 {
			clone.peers = append([]uint16(nil), entry.peers...)
		}
		clone.requested = entry.requested
		clone.lastPeer = entry.lastPeer
		clone.requestedAt = entry.requestedAt
		out[i] = &clone
	}
	return out
}

func (ps *pendingSeenTracker) removePeer(peerID uint16) {
	if peerID == 0 {
		return
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, queue := range ps.perSigner {
		for _, entry := range queue {
			entry.peers = removePeerFromSlice(entry.peers, peerID)
			if entry.lastPeer == peerID {
				entry.requested = false
				entry.lastPeer = 0
				entry.requestedAt = time.Time{}
			}
		}
	}
}

func containsPeer(peers []uint16, peerID uint16) bool {
	for _, id := range peers {
		if id == peerID {
			return true
		}
	}
	return false
}

func removePeerFromSlice(peers []uint16, peerID uint16) []uint16 {
	if len(peers) == 0 {
		return peers
	}
	out := peers[:0]
	for _, id := range peers {
		if id != peerID {
			out = append(out, id)
		}
	}
	return out
}

func (ps *pendingSeenTracker) signerKeys() [][]byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.perSigner) == 0 {
		return nil
	}

	out := make([][]byte, 0, len(ps.perSigner))
	for signerKey := range ps.perSigner {
		out = append(out, []byte(signerKey))
	}
	return out
}

func (ps *pendingSeenTracker) pruneBelowSequence(signer []byte, minSequence uint64) {
	if len(signer) == 0 {
		return
	}

	signerKey := string(signer)

	ps.mu.Lock()
	defer ps.mu.Unlock()

	queue := ps.perSigner[signerKey]
	dst := queue[:0]
	for _, entry := range queue {
		if entry.sequence < minSequence {
			delete(ps.byTx, entry.txKey)
			continue
		}
		dst = append(dst, entry)
	}

	if len(dst) == 0 {
		delete(ps.perSigner, signerKey)
		return
	}

	ps.perSigner[signerKey] = dst
}

func (ps *pendingSeenTracker) firstEntry(signer []byte) *pendingSeenTx {
	if len(signer) == 0 {
		return nil
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	queue := ps.perSigner[string(signer)]
	if len(queue) == 0 {
		return nil
	}

	candidate := queue[0]
	if candidate.requested {
		return nil
	}
	entry := *candidate
	if len(entry.signer) > 0 {
		entry.signer = append([]byte(nil), entry.signer...)
	}
	if len(entry.peers) > 0 {
		entry.peers = append([]uint16(nil), entry.peers...)
	}
	entry.requested = candidate.requested
	entry.lastPeer = candidate.lastPeer
	entry.requestedAt = candidate.requestedAt
	return &entry
}

func (ps *pendingSeenTracker) markRequested(txKey types.TxKey, peerID uint16, at time.Time) {
	if peerID == 0 {
		return
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	entry, ok := ps.byTx[txKey]
	if !ok {
		return
	}
	entry.requested = true
	entry.lastPeer = peerID
	entry.requestedAt = at
}

func (ps *pendingSeenTracker) markRequestFailed(txKey types.TxKey, peerID uint16) {
	if peerID == 0 {
		return
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	entry, ok := ps.byTx[txKey]
	if !ok {
		return
	}
	if entry.lastPeer == peerID {
		entry.requested = false
		entry.lastPeer = 0
		entry.requestedAt = time.Time{}
	}
	entry.peers = removePeerFromSlice(entry.peers, peerID)
}
