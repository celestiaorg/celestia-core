package cat

import (
	"math/rand/v2"
	"sort"
	"sync"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

const defaultPendingSeenPerSigner = 128

type pendingSeenTx struct {
	signerKey string
	signer    []byte
	txKey     types.TxKey
	sequence  uint64
	peer      uint16
	requested bool
	lastPeer  uint16
}

func (p *pendingSeenTx) peerIDs() []uint16 {
	if p.peer == 0 {
		return nil
	}
	return []uint16{p.peer}
}

type sequenceRange struct {
	minSeq uint64 // minimum sequence seen
	maxSeq uint64 // maximum sequence seen
}

type pendingSeenTracker struct {
	mu          sync.Mutex
	perSigner   map[string][]*pendingSeenTx
	seenTxRange map[string]map[uint16]*sequenceRange // signer -> peer -> sequence range
	maxSeenSeq  map[string]uint64                    // signer -> max sequence seen in SeenTx messages
	byTx        map[types.TxKey]*pendingSeenTx
	limit       int
}

func newPendingSeenTracker(limit int) *pendingSeenTracker {
	if limit <= 0 {
		limit = defaultPendingSeenPerSigner
	}
	return &pendingSeenTracker{
		perSigner:   make(map[string][]*pendingSeenTx),
		seenTxRange: make(map[string]map[uint16]*sequenceRange),
		maxSeenSeq:  make(map[string]uint64),
		byTx:        make(map[types.TxKey]*pendingSeenTx),
		limit:       limit,
	}
}

func (ps *pendingSeenTracker) add(msg *protomem.SeenTx, txKey types.TxKey, peerID uint16) {
	if len(msg.Signer) == 0 || msg.Sequence == 0 || peerID == 0 {
		return
	}

	signerKey := string(msg.Signer)

	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Track max sequence seen for this signer
	if msg.Sequence > ps.maxSeenSeq[signerKey] {
		ps.maxSeenSeq[signerKey] = msg.Sequence
	}

	// First check if we already have this exact txKey
	if _, ok := ps.byTx[txKey]; ok {
		// Already tracking this tx, keep the first peer
		return
	}

	queue := ps.perSigner[signerKey]

	// No existing entry for this (signer, sequence), so create a new one
	entry := &pendingSeenTx{
		signerKey: signerKey,
		signer:    append([]byte(nil), msg.Signer...),
		txKey:     txKey,
		sequence:  msg.Sequence,
		peer:      peerID,
	}

	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].sequence >= msg.Sequence
	})
	queue = append(queue, nil)
	copy(queue[insertIdx+1:], queue[insertIdx:])
	queue[insertIdx] = entry
	ps.perSigner[signerKey] = queue
	ps.byTx[txKey] = entry

	// Track sequence range per signer per peer
	ps.updateSeenTxRange(signerKey, peerID, msg.MinSequence, msg.MaxSequence)

	// Enforce limit by removing highest sequence entries
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

// updateSeenTxRange updates the sequence range for a peer. Must be called with lock held.
func (ps *pendingSeenTracker) updateSeenTxRange(signerKey string, peerID uint16, minSeq, maxSeq uint64) {
	if ps.seenTxRange[signerKey] == nil {
		ps.seenTxRange[signerKey] = make(map[uint16]*sequenceRange)
	}
	ps.seenTxRange[signerKey][peerID] = &sequenceRange{
		minSeq: minSeq,
		maxSeq: maxSeq,
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

	// Note: We intentionally do NOT clean up seenTxRange here.
	// The peer still claims to have the sequence range even after
	// we process this specific tx. seenTxRange is only cleaned up
	// when the peer disconnects (removePeer).

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

	for signerKey, queue := range ps.perSigner {
		for _, entry := range queue {
			if entry.peer == peerID {
				entry.peer = 0
			}
			if entry.lastPeer == peerID {
				entry.requested = false
				entry.lastPeer = 0
			}
		}
		// Remove peer from seenTxRange
		if ps.seenTxRange[signerKey] != nil {
			delete(ps.seenTxRange[signerKey], peerID)
			if len(ps.seenTxRange[signerKey]) == 0 {
				delete(ps.seenTxRange, signerKey)
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

// maxSeenSeqSignerKeys returns all signers for which we've tracked max seen sequence.
func (ps *pendingSeenTracker) maxSeenSeqSignerKeys() [][]byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.maxSeenSeq) == 0 {
		return nil
	}

	out := make([][]byte, 0, len(ps.maxSeenSeq))
	for signerKey := range ps.maxSeenSeq {
		out = append(out, []byte(signerKey))
	}
	return out
}

func (ps *pendingSeenTracker) markRequested(txKey types.TxKey, peerID uint16) {
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

// getSequenceRangeForSigner returns peers that have sequences we need.
// Returns peers whose range has maxSeq >= nextSequence (they have useful data).
func (ps *pendingSeenTracker) getSequenceRangeForSigner(signer []byte, nextSequence uint64) (maxSeq uint64, peersWithRanges []uint16) {
	if len(signer) == 0 || nextSequence == 0 {
		return 0, nil
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	signerKey := string(signer)
	peerRanges := ps.seenTxRange[signerKey]
	if len(peerRanges) == 0 {
		return 0, nil
	}

	maxSeq = uint64(0)
	// Include peers that have sequences >= nextSequence
	// A peer is useful if their maxSeq >= nextSequence (they have something we might need)
	for peerID, peerRange := range peerRanges {
		if peerRange.maxSeq >= nextSequence {
			peersWithRanges = append(peersWithRanges, peerID)
			if peerRange.maxSeq > maxSeq {
				maxSeq = peerRange.maxSeq
			}
		}
	}
	// Shuffle the peers so we don't always request from the same peers
	rand.Shuffle(len(peersWithRanges), func(i, j int) {
		peersWithRanges[i], peersWithRanges[j] = peersWithRanges[j], peersWithRanges[i]
	})
	return maxSeq, peersWithRanges
}

// getMaxSeenSeqForSigner returns the maximum sequence this node has seen
// in SeenTx messages for the given signer.
func (ps *pendingSeenTracker) getMaxSeenSeqForSigner(signer []byte) uint64 {
	if len(signer) == 0 {
		return 0
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	return ps.maxSeenSeq[string(signer)]
}

// getMaxSeenRangeSeqForSigner returns the maximum sequence any peer claims to have
// for the given signer based on SeenTx ranges.
func (ps *pendingSeenTracker) getMaxSeenRangeSeqForSigner(signer []byte) uint64 {
	if len(signer) == 0 {
		return 0
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	peerRanges := ps.seenTxRange[string(signer)]
	if len(peerRanges) == 0 {
		return 0
	}

	var maxSeq uint64
	for _, rng := range peerRanges {
		if rng.maxSeq > maxSeq {
			maxSeq = rng.maxSeq
		}
	}
	return maxSeq
}

// doesPeerHaveSequence checks if a peer claims to have a specific sequence.
// Note: This assumes contiguous ranges - the peer may have gaps in reality.
func (ps *pendingSeenTracker) doesPeerHaveSequence(signer []byte, sequence uint64, peerID uint16) bool {
	if len(signer) == 0 || sequence == 0 || peerID == 0 {
		return false
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	signerKey := string(signer)
	peerRanges := ps.seenTxRange[signerKey]
	if peerRanges == nil {
		return false
	}

	peerRange, ok := peerRanges[peerID]
	if !ok {
		return false
	}
	return sequence >= peerRange.minSeq && sequence <= peerRange.maxSeq
}
