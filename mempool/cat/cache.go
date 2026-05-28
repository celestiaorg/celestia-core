package cat

import (
	"sort"
	"time"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// seenPerPeerLimit caps how many tracked txs a single peer can pin via SeenTx
// gossip. A malicious peer flooding distinct SeenTx messages cannot grow the
// tracker beyond this many entries on its own.
const seenPerPeerLimit = 10_000

// seenPerSignerLimit caps the future-sequence (pending) entries retained per
// signer. The per-signer queue is what drives the gap-fill flow; older entries
// at the tail are demoted once the queue is full, leaving them as plain
// peer-has-tx records.
const seenPerSignerLimit = 128

// seenEntryTTL is the maximum age of a tracked entry. Kept short because
// legitimate out-of-order gossip resolves quickly; anything still around after
// a couple of minutes is almost certainly never going to become actionable.
const seenEntryTTL = 2 * time.Minute

// SeenTracker records, for each transaction, the peers that have advertised it
// and (optionally) the signer/sequence pair when one was supplied via SeenTx.
// Sequence-bearing entries are also indexed in a per-signer queue sorted by
// ascending sequence so the reactor can drive the gap-fill flow.
//
// SeenTracker subsumes the responsibilities of the old SeenTxSet
// (peer-has-tx bookkeeping) and pendingSeenTracker (future-sequence
// hold-queue) so caps, TTL, and peer-removal live in one place.
type SeenTracker struct {
	mtx            tmsync.Mutex
	byTx           map[types.TxKey]*seenEntry
	perSigner      map[string][]*seenEntry
	countByPeer    map[uint16]int
	perPeerLimit   int
	perSignerLimit int
	now            func() time.Time
}

type seenEntry struct {
	txKey   types.TxKey
	peers   map[uint16]struct{}
	addedAt time.Time
	// Sequence-indexed fields are populated when the entry sits in the
	// per-signer queue. They are cleared when the entry is demoted out of
	// the queue (e.g. because the queue overflowed its per-signer cap).
	signerKey string
	signer    []byte
	sequence  uint64
	// Gap-scan request bookkeeping; only meaningful while sequence-indexed.
	requested bool
	lastPeer  uint16
}

func NewSeenTracker() *SeenTracker {
	return &SeenTracker{
		byTx:           make(map[types.TxKey]*seenEntry),
		perSigner:      make(map[string][]*seenEntry),
		countByPeer:    make(map[uint16]int),
		perPeerLimit:   seenPerPeerLimit,
		perSignerLimit: seenPerSignerLimit,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// Add records that `peer` has seen `txKey`. If both `signer` and `sequence`
// are set, the entry is also placed in the per-signer queue so the gap-fill
// flow can later request it in order. An existing entry without sequence
// info is upgraded in-place when a sequence-bearing call arrives.
func (s *SeenTracker) Add(txKey types.TxKey, peer uint16, signer []byte, sequence uint64) {
	if peer == 0 {
		return
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, exists := s.byTx[txKey]
	if exists {
		s.addPeerToEntryLocked(entry, peer)
		if entry.sequence == 0 && sequence > 0 && len(signer) > 0 {
			s.indexBySignerLocked(entry, signer, sequence)
		}
		return
	}

	if s.countByPeer[peer] >= s.perPeerLimit {
		return
	}

	entry = &seenEntry{
		txKey:   txKey,
		peers:   map[uint16]struct{}{peer: {}},
		addedAt: s.now(),
	}
	s.byTx[txKey] = entry
	s.countByPeer[peer]++

	if sequence > 0 && len(signer) > 0 {
		s.indexBySignerLocked(entry, signer, sequence)
	}
}

// addPeerToEntryLocked records that `peer` knows about `entry`, respecting the
// per-peer cap. Caller must hold s.mtx.
func (s *SeenTracker) addPeerToEntryLocked(entry *seenEntry, peer uint16) {
	if _, has := entry.peers[peer]; has {
		return
	}
	if s.countByPeer[peer] >= s.perPeerLimit {
		return
	}
	entry.peers[peer] = struct{}{}
	s.countByPeer[peer]++
}

// indexBySignerLocked inserts `entry` into the per-signer queue at the right
// sorted position. If the queue is already full and the entry would land at
// the tail, the upgrade is skipped (entry stays as a plain peer-has-tx
// record). Otherwise tail entries beyond perSignerLimit are demoted out of
// the queue without disturbing their peer counts or byTx residency.
// Caller must hold s.mtx.
func (s *SeenTracker) indexBySignerLocked(entry *seenEntry, signer []byte, sequence uint64) {
	signerKey := string(signer)
	queue := s.perSigner[signerKey]

	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].sequence >= sequence
	})
	if len(queue) >= s.perSignerLimit && insertIdx == len(queue) {
		return
	}

	entry.signerKey = signerKey
	entry.signer = append([]byte(nil), signer...)
	entry.sequence = sequence

	queue = append(queue, nil)
	copy(queue[insertIdx+1:], queue[insertIdx:])
	queue[insertIdx] = entry

	for len(queue) > s.perSignerLimit {
		demoted := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		demoted.signerKey = ""
		demoted.signer = nil
		demoted.sequence = 0
		demoted.requested = false
		demoted.lastPeer = 0
	}

	s.perSigner[signerKey] = queue
}

// ClearSequence removes `txKey` from the per-signer queue but keeps the
// peer-has-tx record intact. Used when a tx is accepted into the mempool:
// the gap-fill queue no longer needs it, but the broadcast path still wants
// to skip peers already known to have the tx.
func (s *SeenTracker) ClearSequence(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return
	}
	if entry.signerKey == "" {
		return
	}
	s.removeFromSignerQueueLocked(entry)
	entry.signerKey = ""
	entry.signer = nil
	entry.sequence = 0
	entry.requested = false
	entry.lastPeer = 0
}

// RemoveKey deletes the entry entirely.
func (s *SeenTracker) RemoveKey(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return
	}
	s.removeEntryLocked(entry)
}

func (s *SeenTracker) removeEntryLocked(entry *seenEntry) {
	if entry.signerKey != "" {
		s.removeFromSignerQueueLocked(entry)
	}
	for peer := range entry.peers {
		s.decPeerLocked(peer)
	}
	delete(s.byTx, entry.txKey)
}

func (s *SeenTracker) removeFromSignerQueueLocked(entry *seenEntry) {
	queue := s.perSigner[entry.signerKey]
	for i, candidate := range queue {
		if candidate == entry {
			queue = append(queue[:i], queue[i+1:]...)
			break
		}
	}
	if len(queue) == 0 {
		delete(s.perSigner, entry.signerKey)
	} else {
		s.perSigner[entry.signerKey] = queue
	}
}

// RemovePeer drops `peer` from every entry. Entries left with no peers are
// deleted; entries whose pending request was outstanding to `peer` have their
// request bookkeeping cleared so the gap-scan can pick another peer.
func (s *SeenTracker) RemovePeer(peer uint16) {
	if peer == 0 {
		return
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()

	for _, entry := range s.byTx {
		if _, has := entry.peers[peer]; has {
			delete(entry.peers, peer)
			s.decPeerLocked(peer)
		}
		if entry.lastPeer == peer {
			entry.requested = false
			entry.lastPeer = 0
		}
		if len(entry.peers) == 0 {
			if entry.signerKey != "" {
				s.removeFromSignerQueueLocked(entry)
			}
			delete(s.byTx, entry.txKey)
		}
	}

	// Safety: ensure the per-peer counter is fully cleared even if drift
	// somehow accumulated.
	delete(s.countByPeer, peer)
}

// Prune evicts every entry with addedAt before cutoff, regardless of whether
// the entry is sequence-indexed.
func (s *SeenTracker) Prune(cutoff time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	for _, entry := range s.byTx {
		if entry.addedAt.Before(cutoff) {
			s.removeEntryLocked(entry)
		}
	}
}

// Has returns whether `peer` is recorded as having seen `txKey`.
func (s *SeenTracker) Has(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return false
	}
	_, has := entry.peers[peer]
	return has
}

// Get returns a clone of the entry for `txKey`, or nil if untracked. The
// clone is safe to inspect without holding the tracker mutex.
func (s *SeenTracker) Get(txKey types.TxKey) *seenEntry {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return nil
	}
	return entry.clone()
}

// Peers returns a defensive copy of the peers known to have `txKey`.
func (s *SeenTracker) Peers(txKey types.TxKey) map[uint16]struct{} {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return nil
	}
	out := make(map[uint16]struct{}, len(entry.peers))
	for p := range entry.peers {
		out[p] = struct{}{}
	}
	return out
}

// Len returns the number of tracked txs.
func (s *SeenTracker) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.byTx)
}

// Reset clears all state.
func (s *SeenTracker) Reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.byTx = make(map[types.TxKey]*seenEntry)
	s.perSigner = make(map[string][]*seenEntry)
	s.countByPeer = make(map[uint16]int)
}

// PendingForSigner returns clones of the sequence-indexed entries for
// `signer`, sorted by ascending sequence. The reactor uses this to drive
// gap-fill request scheduling.
func (s *SeenTracker) PendingForSigner(signer []byte) []*seenEntry {
	if len(signer) == 0 {
		return nil
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	queue := s.perSigner[string(signer)]
	if len(queue) == 0 {
		return nil
	}
	out := make([]*seenEntry, len(queue))
	for i, e := range queue {
		out[i] = e.clone()
	}
	return out
}

// SignersWithPending returns signer keys that currently hold at least one
// sequence-indexed entry.
func (s *SeenTracker) SignersWithPending() [][]byte {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if len(s.perSigner) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(s.perSigner))
	for k := range s.perSigner {
		out = append(out, []byte(k))
	}
	return out
}

// MarkRequested records that a WantTx was sent to `peer` for `txKey`.
func (s *SeenTracker) MarkRequested(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return
	}
	entry.requested = true
	entry.lastPeer = peer
}

// MarkRequestFailed clears the pending-request flag if it pointed at `peer`
// and drops `peer` from the entry so the gap-scan reaches for a different
// source. An entry left empty (no peers) is removed.
func (s *SeenTracker) MarkRequestFailed(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.byTx[txKey]
	if !ok {
		return
	}
	if entry.lastPeer == peer {
		entry.requested = false
		entry.lastPeer = 0
	}
	if _, has := entry.peers[peer]; has {
		delete(entry.peers, peer)
		s.decPeerLocked(peer)
	}
	if len(entry.peers) == 0 {
		if entry.signerKey != "" {
			s.removeFromSignerQueueLocked(entry)
		}
		delete(s.byTx, entry.txKey)
	}
}

func (s *SeenTracker) decPeerLocked(peer uint16) {
	if peer == 0 {
		return
	}
	s.countByPeer[peer]--
	if s.countByPeer[peer] <= 0 {
		delete(s.countByPeer, peer)
	}
}

func (e *seenEntry) clone() *seenEntry {
	cp := *e
	cp.peers = make(map[uint16]struct{}, len(e.peers))
	for p := range e.peers {
		cp.peers[p] = struct{}{}
	}
	if len(e.signer) > 0 {
		cp.signer = append([]byte(nil), e.signer...)
	}
	return &cp
}

// peerIDs returns the peers known to have `e` as a deterministic slice. Used
// by gap-scan logic that wants to iterate request candidates without taking
// the tracker's mutex (callers already hold a cloned copy).
func (e *seenEntry) peerIDs() []uint16 {
	if len(e.peers) == 0 {
		return nil
	}
	out := make([]uint16, 0, len(e.peers))
	for p := range e.peers {
		out = append(out, p)
	}
	return out
}
