package cat

import (
	"sort"
	"time"

	"github.com/filecoin-project/go-clock"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// seenPerPeerLimit bounds how many txs one peer can keep tracked.
const seenPerPeerLimit = 10_000

// seenPerSignerLimit bounds future-sequence entries kept for one signer.
const seenPerSignerLimit = 128

// seenEntryTTL is short because useful out-of-order gossip should resolve quickly.
const seenEntryTTL = 2 * time.Minute

// SeenTracker tracks peers by tx key and future txs by signer/sequence.
type SeenTracker struct {
	mtx tmsync.Mutex
	// txByKey is the primary lookup for tx keys and the peers that know them.
	txByKey map[types.TxKey]*SeenEntry
	// pendingTxsBySigner queues future-sequence entries waiting for a gap to close.
	pendingTxsBySigner map[string][]*SeenEntry
	// txCountByPeer tracks per-peer entries so one peer cannot pin unbounded tx keys.
	txCountByPeer map[uint16]int

	perPeerLimit   int
	perSignerLimit int
	clock          clock.Clock
}

// SeenEntry is one gossip tx entry shared by tx-key lookup and the future queue.
type SeenEntry struct {
	txKey   types.TxKey
	peers   map[uint16]struct{}
	addedAt time.Time

	// pendingTxInfo is set only while the tx is queued for future-sequence gap-fill.
	pendingTxInfo *PendingTxInfo

	// requested is true while a WantTx for this tx is in flight, mirroring the
	// requestScheduler. The gap-fill scan checks it alongside requests.ForTx to
	// skip txs already being fetched.
	requested bool
	// lastPeer is the peer that in-flight WantTx went to. It exists so a timeout
	// or disconnect only clears requested when it concerns that exact peer — not
	// some other peer that also advertised the tx.
	lastPeer uint16
}

// clearPendingTxMetadata drops signer/sequence state while keeping the tx keyed by peer.
func (e *SeenEntry) clearPendingTxMetadata() {
	e.pendingTxInfo = nil
	e.requested = false
	e.lastPeer = 0
}

// clone deep-copies the entry so callers cannot mutate tracker state. Fields are
// listed explicitly so a newly added map/slice/pointer can't be silently shared.
func (e *SeenEntry) clone() *SeenEntry {
	cp := &SeenEntry{
		txKey:     e.txKey,
		peers:     make(map[uint16]struct{}, len(e.peers)),
		addedAt:   e.addedAt,
		requested: e.requested,
		lastPeer:  e.lastPeer,
	}

	for peer := range e.peers {
		cp.peers[peer] = struct{}{}
	}

	if e.pendingTxInfo != nil {
		cp.pendingTxInfo = &PendingTxInfo{
			signerKey: e.pendingTxInfo.signerKey,
			signer:    append([]byte(nil), e.pendingTxInfo.signer...),
			sequence:  e.pendingTxInfo.sequence,
		}
	}

	return cp
}

// peerIDs returns candidate peers for the request path.
func (e *SeenEntry) peerIDs() []uint16 {
	if len(e.peers) == 0 {
		return nil
	}

	out := make([]uint16, 0, len(e.peers))
	for peer := range e.peers {
		out = append(out, peer)
	}

	return out
}

// PendingTxInfo is the signer metadata needed to order future txs.
type PendingTxInfo struct {
	// signerKey is string(signer), used as the pendingTxsBySigner map key.
	signerKey string
	// signer is the tx's signer bytes.
	signer   []byte
	sequence uint64
}

// NewSeenTracker creates the shared seen-state tracker used by CAT gossip.
func NewSeenTracker() *SeenTracker {
	return &SeenTracker{
		txByKey:            make(map[types.TxKey]*SeenEntry),
		pendingTxsBySigner: make(map[string][]*SeenEntry),
		txCountByPeer:      make(map[uint16]int),
		perPeerLimit:       seenPerPeerLimit,
		perSignerLimit:     seenPerSignerLimit,
		clock:              clock.New(),
	}
}

// AddPeer notes a peer has this tx. Returns false if not stored (e.g. peer at its limit).
func (s *SeenTracker) AddPeer(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	_, ok := s.addPeerLocked(txKey, peer)
	return ok
}

// AddPendingTx is AddPeer plus queues the tx by signer/sequence for gap-fill (seq 0 is valid). Returns false if not stored.
func (s *SeenTracker) AddPendingTx(txKey types.TxKey, peer uint16, signer []byte, sequence uint64) bool {
	if len(signer) == 0 {
		return false
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.addPeerLocked(txKey, peer)
	if !ok {
		return false
	}

	// if not yet added to the future sequence queue, index
	if entry.pendingTxInfo == nil {
		s.indexPendingTxLocked(entry, signer, sequence)
	}
	return true
}

// addPeerLocked adds the peer to txByKey (creating the entry if new); returns the entry and whether stored.
func (s *SeenTracker) addPeerLocked(txKey types.TxKey, peer uint16) (*SeenEntry, bool) {
	if peer == 0 {
		return nil, false
	}

	if entry, exists := s.txByKey[txKey]; exists {
		if !s.addPeerToEntryLocked(entry, peer) {
			return nil, false
		}
		return entry, true
	}

	if s.txCountByPeer[peer] >= s.perPeerLimit {
		return nil, false
	}

	entry := &SeenEntry{
		txKey: txKey,
		peers: map[uint16]struct{}{
			peer: {},
		},
		addedAt: s.clock.Now(),
	}
	s.txByKey[txKey] = entry
	s.txCountByPeer[peer]++
	return entry, true
}

// addPeerToEntryLocked attaches peer to an already-tracked tx. It returns false
// if peer is at perPeerLimit. Re-adding a peer already on the entry always
// succeeds and does not count against the limit.
func (s *SeenTracker) addPeerToEntryLocked(entry *SeenEntry, peer uint16) bool {
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

// indexPendingTxLocked queues an entry by signer/sequence. If the signer
// queue is full, only the lowest sequences stay queued; overflow remains peer-only.
func (s *SeenTracker) indexPendingTxLocked(entry *SeenEntry, signer []byte, sequence uint64) {
	signerKey := string(signer)
	queue := s.pendingTxsBySigner[signerKey]

	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].pendingTxInfo.sequence >= sequence
	})

	// very last entry of the queue
	wouldAppendToQueue := insertIdx == len(queue)
	if len(queue) >= s.perSignerLimit && wouldAppendToQueue {
		return
	}

	entry.pendingTxInfo = &PendingTxInfo{
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
		queue[len(queue)-1].clearPendingTxMetadata()
		queue = queue[:len(queue)-1]
	}
	s.pendingTxsBySigner[signerKey] = queue
}

// ClearPendingTx removes a tx from the future-sequence queue but keeps the
// peer-has-tx entry around.
func (s *SeenTracker) ClearPendingTx(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok || entry.pendingTxInfo == nil {
		return
	}

	s.removeFromPendingQueueLocked(entry)
	entry.clearPendingTxMetadata()
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
func (s *SeenTracker) removeEntryLocked(entry *SeenEntry) {
	if entry.pendingTxInfo != nil {
		s.removeFromPendingQueueLocked(entry)
	}

	for peer := range entry.peers {
		s.decrementPeerTxCountLocked(peer)
	}

	delete(s.txByKey, entry.txKey)
}

// removeFromPendingQueueLocked removes entry from its signer's future tx queue.
// The tx can still remain in txByKey as peer-has-tx state.
func (s *SeenTracker) removeFromPendingQueueLocked(entry *SeenEntry) {
	signerKey := entry.pendingTxInfo.signerKey
	queue := s.pendingTxsBySigner[signerKey]

	for i, candidate := range queue {
		if candidate == entry {
			queue = append(queue[:i], queue[i+1:]...)
			break
		}
	}

	if len(queue) == 0 {
		delete(s.pendingTxsBySigner, signerKey)
		return
	}
	s.pendingTxsBySigner[signerKey] = queue
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

// PruneExpired removes entries added more than seenEntryTTL ago.
func (s *SeenTracker) PruneExpired() {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	cutoff := s.clock.Now().Add(-seenEntryTTL)
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
func (s *SeenTracker) Get(txKey types.TxKey) *SeenEntry {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return nil
	}

	return entry.clone()
}

// PendingTxInfo reports the signer/sequence under which txKey is queued for
// future gap-fill; ok is false if it is unknown or tracked only by peer. The
// returned signer is a copy.
func (s *SeenTracker) PendingTxInfo(txKey types.TxKey) (signer []byte, sequence uint64, ok bool) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, found := s.txByKey[txKey]
	if !found || entry.pendingTxInfo == nil {
		return nil, 0, false
	}

	return append([]byte(nil), entry.pendingTxInfo.signer...), entry.pendingTxInfo.sequence, true
}

// PrunePending drops future-sequence metadata for entries added before cutoff
// while keeping the peer-has-tx entry alive.
func (s *SeenTracker) PrunePending(cutoff time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	for _, entry := range s.txByKey {
		if entry.pendingTxInfo == nil || !entry.addedAt.Before(cutoff) {
			continue
		}
		s.removeFromPendingQueueLocked(entry)
		entry.clearPendingTxMetadata()
	}
}

// PeersForTx returns a copy of the peers known to have txKey.
func (s *SeenTracker) PeersForTx(txKey types.TxKey) map[uint16]struct{} {
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

	s.txByKey = make(map[types.TxKey]*SeenEntry)
	s.pendingTxsBySigner = make(map[string][]*SeenEntry)
	s.txCountByPeer = make(map[uint16]int)
}

// PendingTxsForSigner returns that signer's future txs in sequence order.
func (s *SeenTracker) PendingTxsForSigner(signer []byte) []*SeenEntry {
	if len(signer) == 0 {
		return nil
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()

	queue := s.pendingTxsBySigner[string(signer)]
	if len(queue) == 0 {
		return nil
	}

	out := make([]*SeenEntry, len(queue))
	for i, entry := range queue {
		out[i] = entry.clone()
	}

	return out
}

// SignersWithPendingTxs lists signers that still have future txs queued.
func (s *SeenTracker) SignersWithPendingTxs() [][]byte {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if len(s.pendingTxsBySigner) == 0 {
		return nil
	}

	out := make([][]byte, 0, len(s.pendingTxsBySigner))
	for signerKey := range s.pendingTxsBySigner {
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
