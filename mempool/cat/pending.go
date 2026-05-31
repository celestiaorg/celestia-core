package cat

import (
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

const defaultPendingSeenPerSigner = 128

type pendingSeenTx struct {
	signerKey string
	signer    []byte
	txKey     types.TxKey
	sequence  uint64
	peer      uint16
	peers     map[uint16]struct{}
	requested bool
	lastPeer  uint16
}

func (p *pendingSeenTx) peerIDs() []uint16 {
	if p.peer == 0 && len(p.peers) == 0 {
		return nil
	}
	ids := make([]uint16, 0, len(p.peers)+1)
	seen := make(map[uint16]struct{}, len(p.peers)+1)
	if p.peer != 0 {
		ids = append(ids, p.peer)
		seen[p.peer] = struct{}{}
	}
	for peerID := range p.peers {
		if peerID == 0 {
			continue
		}
		if _, ok := seen[peerID]; ok {
			continue
		}
		ids = append(ids, peerID)
		seen[peerID] = struct{}{}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
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

	// First check if we already have this exact txKey
	if entry, ok := ps.byTx[txKey]; ok {
		if entry.peers == nil {
			entry.peers = make(map[uint16]struct{})
		}
		entry.peers[peerID] = struct{}{}
		return
	}

	queue := ps.perSigner[signerKey]

	// No existing entry for this (signer, sequence), so create a new one
	entry := &pendingSeenTx{
		signerKey: signerKey,
		signer:    append([]byte(nil), signer...),
		txKey:     txKey,
		sequence:  sequence,
		peer:      peerID,
		peers:     map[uint16]struct{}{peerID: {}},
	}

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

// get returns the pending entry for a txKey without removing it.
// Returns nil if not found.
func (ps *pendingSeenTracker) get(txKey types.TxKey) *pendingSeenTx {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	entry, ok := ps.byTx[txKey]
	if !ok {
		return nil
	}

	clone := *entry
	if len(entry.signer) > 0 {
		clone.signer = append([]byte(nil), entry.signer...)
	}
	if len(entry.peers) > 0 {
		clone.peers = make(map[uint16]struct{}, len(entry.peers))
		for peerID := range entry.peers {
			clone.peers[peerID] = struct{}{}
		}
	}
	return &clone
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
			clone.peers = make(map[uint16]struct{}, len(entry.peers))
			for peerID := range entry.peers {
				clone.peers[peerID] = struct{}{}
			}
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

	for _, queue := range ps.perSigner {
		for _, entry := range queue {
			if entry.peer == peerID {
				entry.peer = 0
			}
			delete(entry.peers, peerID)
			if entry.lastPeer == peerID {
				entry.requested = false
				entry.lastPeer = 0
			}
		}
	}
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
	}
	if entry.peer == peerID {
		entry.peer = 0
	}
	delete(entry.peers, peerID)
}
