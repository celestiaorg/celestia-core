package cat

import (
	"sort"
	"time"

	"github.com/filecoin-project/go-clock"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// SeenTxSet records transactions that have been
// seen by other peers but not yet by us
type SeenTxSet struct {
	mtx tmsync.Mutex
	set map[types.TxKey]timestampedPeerSet
}

type timestampedPeerSet struct {
	peers map[uint16]struct{}
	time  time.Time
}

func NewSeenTxSet() *SeenTxSet {
	return &SeenTxSet{
		set: make(map[types.TxKey]timestampedPeerSet),
	}
}

// maxSeenTxSetSize limits the number of unique tx keys tracked in the SeenTxSet
// to prevent unbounded memory growth from malicious peers flooding SeenTx messages.
// Each entry costs ~500 bytes (including Go map overhead and GC pressure),
// so 10M entries ≈ 5 GB worst-case.
const maxSeenTxSetSize = 10_000_000

func (s *SeenTxSet) Add(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists && len(s.set) >= maxSeenTxSetSize {
		// Evict one random entry to make room.
		for k := range s.set {
			delete(s.set, k)
			break
		}
	}
	if !exists {
		s.set[txKey] = timestampedPeerSet{
			peers: map[uint16]struct{}{peer: {}},
			time:  time.Now().UTC(),
		}
	} else {
		seenSet.peers[peer] = struct{}{}
	}
}

func (s *SeenTxSet) RemoveKey(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.set, txKey)
}

func (s *SeenTxSet) Remove(txKey types.TxKey, peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	set, exists := s.set[txKey]
	if exists {
		if len(set.peers) == 1 {
			delete(s.set, txKey)
		} else {
			delete(set.peers, peer)
		}
	}
}

func (s *SeenTxSet) RemovePeer(peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		delete(seenSet.peers, peer)
		if len(seenSet.peers) == 0 {
			delete(s.set, key)
		}
	}
}

func (s *SeenTxSet) Prune(limit time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		if seenSet.time.Before(limit) {
			delete(s.set, key)
		}
	}
}

func (s *SeenTxSet) Has(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return false
	}
	_, has := seenSet.peers[peer]
	return has
}

func (s *SeenTxSet) Get(txKey types.TxKey) map[uint16]struct{} {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return nil
	}
	// make a copy of the struct to avoid concurrency issues
	peers := make(map[uint16]struct{}, len(seenSet.peers))
	for peer := range seenSet.peers {
		peers[peer] = struct{}{}
	}
	return peers
}

// Len returns the amount of cached items. Mostly used for testing.
func (s *SeenTxSet) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.set)
}

func (s *SeenTxSet) Reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.set = make(map[types.TxKey]timestampedPeerSet)
}

// seenPerPeerLimit bounds how many txs one peer can keep tracked.
const seenPerPeerLimit = 10_000

// seenPerSignerLimit bounds future-sequence entries kept for one signer.
const seenPerSignerLimit = 128

// seenEntryTTL is short because useful out-of-order gossip should resolve quickly.
//
//nolint:unused // consumed when SeenTracker is wired into the reactor in a later part.
const seenEntryTTL = 2 * time.Minute

// SeenTracker tracks peers by tx key and future txs by signer/sequence.
type SeenTracker struct {
	mtx tmsync.Mutex
	// txByKey is the primary lookup for tx keys and the peers that know them.
	txByKey map[types.TxKey]*seenEntry
	// futureTxsBySigner queues future-sequence entries waiting for a gap to close.
	futureTxsBySigner map[string][]*seenEntry
	// txCountByPeer tracks per-peer entries so one peer cannot pin unbounded tx keys.
	txCountByPeer map[uint16]int

	perPeerLimit   int
	perSignerLimit int
	clock          clock.Clock
}

// seenEntry is one gossip tx entry shared by tx-key lookup and the future queue.
type seenEntry struct {
	txKey   types.TxKey
	peers   map[uint16]struct{}
	addedAt time.Time

	// futureTxInfo is set only while the tx is queued for future-sequence gap-fill.
	futureTxInfo *futureTxInfo

	requested bool
	lastPeer  uint16
}

// clearFutureMetadata drops signer/sequence state while keeping the tx keyed by peer.
func (e *seenEntry) clearFutureMetadata() {
	e.futureTxInfo = nil
	e.requested = false
	e.lastPeer = 0
}

// clone copies mutable fields so callers cannot mutate tracker state.
func (e *seenEntry) clone() *seenEntry {
	cp := *e
	cp.peers = make(map[uint16]struct{}, len(e.peers))
	for peer := range e.peers {
		cp.peers[peer] = struct{}{}
	}
	if e.futureTxInfo != nil {
		cp.futureTxInfo = &futureTxInfo{
			signerKey: e.futureTxInfo.signerKey,
			signer:    append([]byte(nil), e.futureTxInfo.signer...),
			sequence:  e.futureTxInfo.sequence,
		}
	}
	return &cp
}

// peerIDs returns candidate peers for the request path.
//
//nolint:unused // consumed when SeenTracker is wired into the reactor in a later part.
func (e *seenEntry) peerIDs() []uint16 {
	if len(e.peers) == 0 {
		return nil
	}
	out := make([]uint16, 0, len(e.peers))
	for peer := range e.peers {
		out = append(out, peer)
	}
	return out
}

// futureTxInfo is the signer metadata needed to order future txs.
type futureTxInfo struct {
	signerKey string
	signer    []byte
	sequence  uint64
}

// NewSeenTracker creates the shared seen-state tracker used by CAT gossip.
func NewSeenTracker() *SeenTracker {
	return &SeenTracker{
		txByKey:           make(map[types.TxKey]*seenEntry),
		futureTxsBySigner: make(map[string][]*seenEntry),
		txCountByPeer:     make(map[uint16]int),
		perPeerLimit:      seenPerPeerLimit,
		perSignerLimit:    seenPerSignerLimit,
		clock:             clock.New(),
	}
}

// Add records that a peer claims to have txKey.
// If signer is present, the same entry is also queued by signer/sequence for
// future gap-fill. Sequence 0 is valid. It returns false when the peer claim is
// rejected by admission limits.
func (s *SeenTracker) Add(txKey types.TxKey, peer uint16, signer []byte, sequence uint64) bool {
	if peer == 0 {
		return false
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, exists := s.txByKey[txKey]
	if exists {
		if !s.addPeerToEntryLocked(entry, peer) {
			return false
		}
		// if not yet added to the future sequence queue, index
		if entry.futureTxInfo == nil && len(signer) > 0 {
			s.indexFutureTxLocked(entry, signer, sequence)
		}
		return true
	}

	if s.txCountByPeer[peer] >= s.perPeerLimit {
		return false
	}

	entry = &seenEntry{
		txKey: txKey,
		peers: map[uint16]struct{}{
			peer: {},
		},
		addedAt: s.clock.Now(),
	}
	s.txByKey[txKey] = entry
	s.txCountByPeer[peer]++

	if len(signer) > 0 {
		s.indexFutureTxLocked(entry, signer, sequence)
	}
	return true
}

// addPeerToEntryLocked adds another peer to an existing tx without letting that
// peer exceed its own admission limit.
func (s *SeenTracker) addPeerToEntryLocked(entry *seenEntry, peer uint16) bool {
	if _, has := entry.peers[peer]; has {
		return true
	}
	if s.txCountByPeer[peer] >= s.perPeerLimit {
		return false
	}
	entry.peers[peer] = struct{}{}
	s.txCountByPeer[peer]++
	return true
}

// indexFutureTxLocked queues an entry by signer/sequence. If the signer
// queue is full, only the lowest sequences stay queued; overflow remains peer-only.
func (s *SeenTracker) indexFutureTxLocked(entry *seenEntry, signer []byte, sequence uint64) {
	signerKey := string(signer)
	queue := s.futureTxsBySigner[signerKey]

	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].futureTxInfo.sequence >= sequence
	})

	// very last entry of the queue
	wouldAppendToQueue := insertIdx == len(queue)
	if len(queue) >= s.perSignerLimit && wouldAppendToQueue {
		return
	}

	entry.futureTxInfo = &futureTxInfo{
		signerKey: signerKey,
		signer:    append([]byte(nil), signer...),
		sequence:  sequence,
	}

	// insert future tx into the queue
	queue = append(queue, nil)
	copy(queue[insertIdx+1:], queue[insertIdx:])
	queue[insertIdx] = entry

	// if queue is over limit, trim from the end (highest sequence)
	for len(queue) > s.perSignerLimit {
		queue[len(queue)-1].clearFutureMetadata()
		queue = queue[:len(queue)-1]
	}
	s.futureTxsBySigner[signerKey] = queue
}

// ClearSequence removes a tx from the future-sequence queue but keeps the
// peer-has-tx entry around.
func (s *SeenTracker) ClearSequence(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok || entry.futureTxInfo == nil {
		return
	}
	s.removeFromFutureQueueLocked(entry)
	entry.clearFutureMetadata()
}

// RemoveKey drops a tx from every tracker index.
func (s *SeenTracker) RemoveKey(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}
	s.removeEntryLocked(entry)
}

// removeEntryLocked removes the whole entry and fixes peer accounting.
func (s *SeenTracker) removeEntryLocked(entry *seenEntry) {
	if entry.futureTxInfo != nil {
		s.removeFromFutureQueueLocked(entry)
	}
	for peer := range entry.peers {
		s.decrementPeerTxCountLocked(peer)
	}
	delete(s.txByKey, entry.txKey)
}

// removeFromFutureQueueLocked removes entry from its signer's future tx queue.
// The tx can still remain in txByKey as peer-has-tx state.
func (s *SeenTracker) removeFromFutureQueueLocked(entry *seenEntry) {
	signerKey := entry.futureTxInfo.signerKey
	queue := s.futureTxsBySigner[signerKey]
	for i, candidate := range queue {
		if candidate == entry {
			queue = append(queue[:i], queue[i+1:]...)
			break
		}
	}
	if len(queue) == 0 {
		delete(s.futureTxsBySigner, signerKey)
		return
	}
	s.futureTxsBySigner[signerKey] = queue
}

// RemovePeer removes a peer from all seen entries. Entries stay alive if some
// other peer still claims to have the tx.
func (s *SeenTracker) RemovePeer(peer uint16) {
	if peer == 0 {
		return
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()
	for _, entry := range s.txByKey {
		if _, has := entry.peers[peer]; has {
			delete(entry.peers, peer)
			s.decrementPeerTxCountLocked(peer)
		}
		if entry.lastPeer == peer {
			entry.requested = false
			entry.lastPeer = 0
		}
		if len(entry.peers) == 0 {
			s.removeEntryLocked(entry)
		}
	}
	delete(s.txCountByPeer, peer)
}

// Prune expires old gossip state from all indexes.
func (s *SeenTracker) Prune(cutoff time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for _, entry := range s.txByKey {
		if entry.addedAt.Before(cutoff) {
			s.removeEntryLocked(entry)
		}
	}
}

// Has reports whether peer is known to have txKey.
func (s *SeenTracker) Has(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok {
		return false
	}
	_, has := entry.peers[peer]
	return has
}

// Get returns a copy of the tracked tx entry, if any.
func (s *SeenTracker) Get(txKey types.TxKey) *seenEntry {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok {
		return nil
	}
	return entry.clone()
}

// Pending returns a copy of the future-sequence metadata for txKey, or nil if
// the tx is not currently queued for future gap-fill.
func (s *SeenTracker) Pending(txKey types.TxKey) *futureTxInfo {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok || entry.futureTxInfo == nil {
		return nil
	}
	return &futureTxInfo{
		signerKey: entry.futureTxInfo.signerKey,
		signer:    append([]byte(nil), entry.futureTxInfo.signer...),
		sequence:  entry.futureTxInfo.sequence,
	}
}

// PrunePending drops future-sequence metadata for entries added before cutoff
// while keeping the peer-has-tx entry alive.
func (s *SeenTracker) PrunePending(cutoff time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for _, entry := range s.txByKey {
		if entry.futureTxInfo == nil || !entry.addedAt.Before(cutoff) {
			continue
		}
		s.removeFromFutureQueueLocked(entry)
		entry.clearFutureMetadata()
	}
}

// Peers returns a copy of the peers known to have txKey.
func (s *SeenTracker) Peers(txKey types.TxKey) map[uint16]struct{} {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok {
		return nil
	}
	out := make(map[uint16]struct{}, len(entry.peers))
	for peer := range entry.peers {
		out[peer] = struct{}{}
	}
	return out
}

// Len returns the number of unique tx keys being tracked.
func (s *SeenTracker) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.txByKey)
}

// Reset clears all seen state. It is mostly used by tests.
func (s *SeenTracker) Reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.txByKey = make(map[types.TxKey]*seenEntry)
	s.futureTxsBySigner = make(map[string][]*seenEntry)
	s.txCountByPeer = make(map[uint16]int)
}

// PendingForSigner returns that signer's future txs in sequence order.
func (s *SeenTracker) PendingForSigner(signer []byte) []*seenEntry {
	if len(signer) == 0 {
		return nil
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	queue := s.futureTxsBySigner[string(signer)]
	if len(queue) == 0 {
		return nil
	}
	out := make([]*seenEntry, len(queue))
	for i, entry := range queue {
		out[i] = entry.clone()
	}
	return out
}

// SignersWithPending lists signers that still have future txs queued.
func (s *SeenTracker) SignersWithPending() [][]byte {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if len(s.futureTxsBySigner) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(s.futureTxsBySigner))
	for signerKey := range s.futureTxsBySigner {
		out = append(out, []byte(signerKey))
	}
	return out
}

// MarkRequested remembers which peer we last asked for this tx.
func (s *SeenTracker) MarkRequested(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}
	entry.requested = true
	entry.lastPeer = peer
}

// MarkRequestFailed clears the in-flight request and stops using that peer for
// this tx unless another peer also announced it.
func (s *SeenTracker) MarkRequestFailed(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}
	if entry.lastPeer == peer {
		entry.requested = false
		entry.lastPeer = 0
	}
	if _, has := entry.peers[peer]; has {
		delete(entry.peers, peer)
		s.decrementPeerTxCountLocked(peer)
	}
	if len(entry.peers) == 0 {
		s.removeEntryLocked(entry)
	}
}

// decrementPeerTxCountLocked records that peer is tracking one fewer tx.
func (s *SeenTracker) decrementPeerTxCountLocked(peer uint16) {
	if peer == 0 {
		return
	}
	s.txCountByPeer[peer]--
	if s.txCountByPeer[peer] <= 0 {
		delete(s.txCountByPeer, peer)
	}
}
