package cat

import (
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

const defaultPendingSeenPerSigner = 128

// pendingSeen admission limits. These bound the memory a peer (or set of peers)
// can pin in the tracker by sending future-sequence SeenTx for known signers.
// Rather than carrying standalone magic numbers, the global and per-peer caps
// are derived from the configured maximum mempool size (config.Size), so they
// scale with the operator's configuration. See pendingSeenTotalCap and
// pendingSeenPerPeerCap.

// defaultPendingSeenTotal is the fallback global cap used when the configured
// mempool size is non-positive (unlimited / misconfigured). It matches the
// default mempool Size so a misconfigured node still gets a sane, bounded
// budget rather than 0 (which would reject everything) or unbounded growth.
const defaultPendingSeenTotal = 5_000

// pendingSeenPerPeerDivisor derives the per-peer cap as a fraction of the
// global cap (global/divisor), so a single peer cannot pin more than a small
// share of the total budget. Kept strictly greater than 1 so per-peer < total.
const pendingSeenPerPeerDivisor = 10

// pendingSeenTTL is the maximum age of a pending entry before it is pruned.
// Future-sequence SeenTx that never become requestable (e.g. the source peer
// disconnected, or the sequence gap is never filled) are aged out. Kept short
// because legitimate out-of-order gossip resolves quickly; anything still
// pending after a couple of minutes is almost certainly never going to become
// requestable.
const pendingSeenTTL = 2 * time.Minute

// pendingSeenTotalCap returns the global pending-seen cap derived from the
// configured maximum mempool size. A non-positive size (unlimited or
// misconfigured) falls back to defaultPendingSeenTotal so the cap never
// degrades to 0.
func pendingSeenTotalCap(mempoolSize int) int {
	if mempoolSize <= 0 {
		return defaultPendingSeenTotal
	}
	return mempoolSize
}

// pendingSeenPerPeerCap returns the per-peer pending-seen cap as a fraction of
// the global cap, guaranteeing per-peer < total and at least 1.
func pendingSeenPerPeerCap(total int) int {
	perPeer := total / pendingSeenPerPeerDivisor
	if perPeer < 1 {
		perPeer = 1
	}
	return perPeer
}

type pendingSeenTx struct {
	signerKey string
	signer    []byte
	txKey     types.TxKey
	sequence  uint64
	peer      uint16
	requested bool
	lastPeer  uint16
	// addedAt records when the entry was admitted; used for time-based eviction.
	addedAt time.Time
	// addedBy records the peer the entry was admitted on behalf of, so the
	// per-peer count stays accurate even after peer fields are cleared.
	addedBy uint16
}

func (p *pendingSeenTx) peerIDs() []uint16 {
	if p.peer == 0 {
		return nil
	}
	return []uint16{p.peer}
}

type pendingSeenTracker struct {
	mu        sync.Mutex
	perSigner map[string][]*pendingSeenTx
	byTx      map[types.TxKey]*pendingSeenTx
	limit     int
	// countByPeer tracks how many pending entries each peer is responsible for,
	// used to enforce the per-peer admission cap.
	countByPeer map[uint16]int
	// total caps the number of pending entries across all signers.
	total int
	// perPeerLimit caps how many pending entries a single peer can hold.
	perPeerLimit int
	// now returns the current time; overridable in tests for TTL eviction.
	now func() time.Time
}

// newPendingSeenTracker constructs a tracker. perSignerLimit bounds the entries
// retained per signer (0 falls back to defaultPendingSeenPerSigner). mempoolSize
// is the configured maximum mempool size (config.Size); the global and per-peer
// caps are derived from it so they scale with configuration instead of being
// standalone magic numbers.
func newPendingSeenTracker(perSignerLimit, mempoolSize int) *pendingSeenTracker {
	if perSignerLimit <= 0 {
		perSignerLimit = defaultPendingSeenPerSigner
	}
	total := pendingSeenTotalCap(mempoolSize)
	return &pendingSeenTracker{
		perSigner:    make(map[string][]*pendingSeenTx),
		byTx:         make(map[types.TxKey]*pendingSeenTx),
		limit:        perSignerLimit,
		countByPeer:  make(map[uint16]int),
		total:        total,
		perPeerLimit: pendingSeenPerPeerCap(total),
		now:          time.Now,
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
	if _, ok := ps.byTx[txKey]; ok {
		// Already tracking this tx, keep the first peer
		return
	}

	// Enforce the per-peer admission cap: one peer cannot pin more than
	// perPeerLimit entries.
	if ps.perPeerLimit > 0 && ps.countByPeer[peerID] >= ps.perPeerLimit {
		return
	}

	// Enforce the global cap on total pending entries across all signers.
	if ps.total > 0 && len(ps.byTx) >= ps.total {
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
		addedAt:   ps.now(),
		addedBy:   peerID,
	}

	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].sequence >= sequence
	})
	queue = append(queue, nil)
	copy(queue[insertIdx+1:], queue[insertIdx:])
	queue[insertIdx] = entry
	ps.perSigner[signerKey] = queue
	ps.byTx[txKey] = entry
	ps.countByPeer[peerID]++

	for len(queue) > ps.limit {
		lastIdx := len(queue) - 1
		removed := queue[lastIdx]
		queue = queue[:lastIdx]
		delete(ps.byTx, removed.txKey)
		ps.decPeer(removed.addedBy)
	}
	if len(queue) == 0 {
		delete(ps.perSigner, signerKey)
	} else {
		ps.perSigner[signerKey] = queue
	}
}

// decPeer decrements the per-peer pending count, cleaning up the map entry once
// it reaches zero. Must be called with ps.mu held.
func (ps *pendingSeenTracker) decPeer(peerID uint16) {
	if peerID == 0 {
		return
	}
	ps.countByPeer[peerID]--
	if ps.countByPeer[peerID] <= 0 {
		delete(ps.countByPeer, peerID)
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
	ps.decPeer(entry.addedBy)
	return entry
}

// prune removes all pending entries admitted before the given cutoff. It is
// driven by the reactor's periodic maintenance to age out future-sequence
// entries that never become requestable (e.g. the gap is never filled or the
// source peer disconnected).
func (ps *pendingSeenTracker) prune(cutoff time.Time) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for signerKey, queue := range ps.perSigner {
		kept := queue[:0]
		for _, entry := range queue {
			if entry.addedAt.Before(cutoff) {
				delete(ps.byTx, entry.txKey)
				ps.decPeer(entry.addedBy)
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(ps.perSigner, signerKey)
		} else {
			ps.perSigner[signerKey] = kept
		}
	}
}

// len returns the total number of pending entries across all signers.
func (ps *pendingSeenTracker) len() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.byTx)
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
}
