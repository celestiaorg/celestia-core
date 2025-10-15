package cat

import (
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

const defaultPendingSeenPerSigner = 64

type pendingSeenTx struct {
	signerKey string
	signer    []byte
	txKey     types.TxKey
	sequence  uint64
	peers     []uint16
	addedAt   time.Time
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
	}

	entry.peers = []uint16{peerID}

	queue := ps.perSigner[signerKey]
	queue = append(queue, entry)
	ps.perSigner[signerKey] = queue
	ps.byTx[txKey] = entry

	if len(queue) > ps.limit {
		removed := queue[0]
		delete(ps.byTx, removed.txKey)
		ps.perSigner[signerKey] = queue[1:]
	}
}

func (ps *pendingSeenTracker) remove(txKey types.TxKey) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	entry, ok := ps.byTx[txKey]
	if !ok {
		return
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

	for signerKey, queue := range ps.perSigner {
		dst := queue[:0]
		for _, entry := range queue {
			entry.peers = removePeerFromSlice(entry.peers, peerID)
			if len(entry.peers) == 0 {
				delete(ps.byTx, entry.txKey)
				continue
			}
			dst = append(dst, entry)
		}
		if len(dst) == 0 {
			delete(ps.perSigner, signerKey)
		} else {
			ps.perSigner[signerKey] = dst
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
