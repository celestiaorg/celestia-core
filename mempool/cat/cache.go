package cat

import (
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/filecoin-project/go-clock"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// seenPerPeerLimit bounds how many txs one peer can keep tracked.
const seenPerPeerLimit = 1000

// seenPerSignerLimit bounds future-sequence entries kept for one signer.
const seenPerSignerLimit = 1000

// seenEntryTTL is short because useful out-of-order gossip should resolve quickly.
const seenEntryTTL = 2 * time.Minute

// SeenTracker tracks peers by tx key and future txs by signer/sequence.
type SeenTracker struct {
	mtx tmsync.Mutex
	// txByKey is the primary lookup for tx keys and the peers that know them.
	txByKey map[types.TxKey]*SeenEntry
	// pendingTxsBySigner queues future-sequence claims, sorted by sequence,
	// waiting for a gap to close.
	pendingTxsBySigner map[string][]*PendingTxInfo
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

	// claims are the distinct signer/sequence pairs that peers announced for
	// the tx while it waits for gap-fill. Each claim is queued under its own
	// signer, so a bogus claim cannot hide an honest one. Each peer backs at
	// most one claim, and claims never outnumber peers.
	claims []*PendingTxInfo

	// pendingTxInfo is set only on copies returned by PendingTxsForSigner: it
	// is the claim under which that copy is queued.
	pendingTxInfo *PendingTxInfo
}

// clone deep-copies the entry so callers cannot mutate tracker state. Fields are
// listed explicitly so a newly added map/slice/pointer can't be silently shared.
func (e *SeenEntry) clone() *SeenEntry {
	cp := &SeenEntry{
		txKey:   e.txKey,
		peers:   maps.Clone(e.peers),
		addedAt: e.addedAt,
	}
	for _, claim := range e.claims {
		cp.claims = append(cp.claims, claim.clone())
	}
	if e.pendingTxInfo != nil {
		cp.pendingTxInfo = e.pendingTxInfo.clone()
	}

	return cp
}

// announcedBy reports whether peer announced the claim this copy is queued
// under or, for an entry that is not such a copy, any of its claims.
func (e *SeenEntry) announcedBy(peer uint16) bool {
	if e.pendingTxInfo != nil {
		return e.pendingTxInfo.announcedBy(peer)
	}
	return e.claimFrom(peer) != nil
}

// claimFrom returns the claim peer announced, if any.
func (e *SeenEntry) claimFrom(peer uint16) *PendingTxInfo {
	for _, claim := range e.claims {
		if claim.announcedBy(peer) {
			return claim
		}
	}
	return nil
}

// peerIDs returns candidate peers for the request path. Peers that announced
// a claim come first, so a tx requested for gap-fill is delivered by a peer
// whose metadata applies to it.
func (e *SeenEntry) peerIDs() []uint16 {
	if len(e.peers) == 0 {
		return nil
	}

	out := make([]uint16, 0, len(e.peers))
	for peer := range e.peers {
		if e.announcedBy(peer) {
			out = append(out, peer)
		}
	}
	for peer := range e.peers {
		if !e.announcedBy(peer) {
			out = append(out, peer)
		}
	}

	return out
}

// PendingTxInfo is one signer/sequence claim for a tx, used to order future txs.
type PendingTxInfo struct {
	// signerKey is string(signer), used as the pendingTxsBySigner map key.
	signerKey string
	// signer is the tx's signer bytes.
	signer   []byte
	sequence uint64
	// announcers are the peers whose latest announcement for the tx is this
	// claim. Only their deliveries are judged by it; the claim is unverified
	// until the application checks the tx.
	announcers map[uint16]struct{}
	// entry is the tracked tx this claim belongs to. It is nil on copies.
	entry *SeenEntry
}

// clone copies the claim without its link to the tracked entry.
func (p *PendingTxInfo) clone() *PendingTxInfo {
	return &PendingTxInfo{
		signerKey:  p.signerKey,
		signer:     append([]byte(nil), p.signer...),
		sequence:   p.sequence,
		announcers: maps.Clone(p.announcers),
	}
}

// announcedBy reports whether peer announced this claim.
func (p *PendingTxInfo) announcedBy(peer uint16) bool {
	_, ok := p.announcers[peer]
	return ok
}

// matches reports whether signer/sequence equal this claim.
func (p *PendingTxInfo) matches(signer []byte, sequence uint64) bool {
	return p.sequence == sequence && p.signerKey == string(signer)
}

// NewSeenTracker creates the shared seen-state tracker used by CAT gossip.
func NewSeenTracker() *SeenTracker {
	return &SeenTracker{
		txByKey:            make(map[types.TxKey]*SeenEntry),
		pendingTxsBySigner: make(map[string][]*PendingTxInfo),
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

// AddPendingTx is AddPeer plus queues the tx under peer's signer/sequence
// claim for gap-fill (seq 0 is valid). Returns false if not stored.
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

	s.recordClaimLocked(entry, peer, signer, sequence, true)
	return true
}

// RecordPendingTxAnnouncement reconciles a peer's announcement with txKey's
// queued claims: peer backs the matching claim, if one is queued, and is
// withdrawn from any other. A claim is dropped once no announcer is left.
// No new claim is queued.
func (s *SeenTracker) RecordPendingTxAnnouncement(txKey types.TxKey, peer uint16, signer []byte, sequence uint64) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}
	// Only a peer known to have the tx can be asked for it.
	if _, has := entry.peers[peer]; !has {
		return
	}
	s.recordClaimLocked(entry, peer, signer, sequence, false)
}

// recordClaimLocked makes signer/sequence peer's only claim for entry. If no
// claim matches, a new one is queued only when create is set.
func (s *SeenTracker) recordClaimLocked(entry *SeenEntry, peer uint16, signer []byte, sequence uint64, create bool) {
	var match *PendingTxInfo
	for _, claim := range slices.Clone(entry.claims) {
		if claim.matches(signer, sequence) {
			match = claim
			continue
		}
		if claim.announcedBy(peer) {
			delete(claim.announcers, peer)
			if len(claim.announcers) == 0 {
				s.dropClaimLocked(claim)
			}
		}
	}
	if match == nil && create {
		match = s.indexClaimLocked(entry, signer, sequence)
	}
	if match != nil {
		match.announcers[peer] = struct{}{}
	}
	s.trimClaimsLocked(entry)
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

// indexClaimLocked queues a new claim for entry by signer/sequence and
// returns it. If the signer queue is full, only the lowest sequences stay
// queued; it returns nil if the new claim is not one of them.
func (s *SeenTracker) indexClaimLocked(entry *SeenEntry, signer []byte, sequence uint64) *PendingTxInfo {
	signerKey := string(signer)
	queue := s.pendingTxsBySigner[signerKey]

	insertIdx := sort.Search(len(queue), func(i int) bool {
		return queue[i].sequence >= sequence
	})

	// very last entry of the queue
	wouldAppendToQueue := insertIdx == len(queue)
	if len(queue) >= s.perSignerLimit && wouldAppendToQueue {
		return nil
	}

	claim := &PendingTxInfo{
		signerKey:  signerKey,
		signer:     append([]byte(nil), signer...),
		sequence:   sequence,
		announcers: make(map[uint16]struct{}, 1),
		entry:      entry,
	}
	entry.claims = append(entry.claims, claim)

	// insert future tx into the queue
	queue = slices.Insert(queue, insertIdx, claim)

	// if queue is over limit, trim from the end (highest sequence)
	for len(queue) > s.perSignerLimit {
		last := queue[len(queue)-1]
		last.entry.removeClaim(last)
		queue = queue[:len(queue)-1]
	}
	s.pendingTxsBySigner[signerKey] = queue
	return claim
}

// removeClaim unlinks claim from the entry, without touching the queue.
func (e *SeenEntry) removeClaim(claim *PendingTxInfo) {
	e.claims = slices.DeleteFunc(e.claims, func(c *PendingTxInfo) bool { return c == claim })
}

// dropClaimLocked removes claim from its signer queue and from its entry.
func (s *SeenTracker) dropClaimLocked(claim *PendingTxInfo) {
	queue := slices.DeleteFunc(s.pendingTxsBySigner[claim.signerKey], func(c *PendingTxInfo) bool { return c == claim })
	if len(queue) == 0 {
		delete(s.pendingTxsBySigner, claim.signerKey)
	} else {
		s.pendingTxsBySigner[claim.signerKey] = queue
	}
	claim.entry.removeClaim(claim)
}

// dropClaimsLocked removes every claim for entry but keeps the peer-has-tx
// state.
func (s *SeenTracker) dropClaimsLocked(entry *SeenEntry) {
	for _, claim := range slices.Clone(entry.claims) {
		s.dropClaimLocked(claim)
	}
}

// ClearPendingTx removes every claim for a tx from the future-sequence queue
// but keeps the peer-has-tx entry around.
func (s *SeenTracker) ClearPendingTx(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if entry, ok := s.txByKey[txKey]; ok {
		s.dropClaimsLocked(entry)
	}
}

// ClearPendingClaim removes the signer/sequence claim for a tx, if queued,
// and keeps the tx's other claims.
func (s *SeenTracker) ClearPendingClaim(txKey types.TxKey, signer []byte, sequence uint64) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}
	for _, claim := range entry.claims {
		if claim.matches(signer, sequence) {
			s.dropClaimLocked(claim)
			return
		}
	}
}

// ClearPendingClaimFrom withdraws peer's announcement for a tx, if any. The
// claim is removed only if no other peer backs it; the tx's other claims are
// kept.
func (s *SeenTracker) ClearPendingClaimFrom(txKey types.TxKey, peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}
	if claim := entry.claimFrom(peer); claim != nil {
		delete(claim.announcers, peer)
		if len(claim.announcers) == 0 {
			s.dropClaimLocked(claim)
		}
	}
}

// PendingClaims returns copies of the signer/sequence claims queued for a tx.
func (s *SeenTracker) PendingClaims(txKey types.TxKey) []*PendingTxInfo {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return nil
	}
	out := make([]*PendingTxInfo, 0, len(entry.claims))
	for _, claim := range entry.claims {
		out = append(out, claim.clone())
	}
	return out
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
	s.dropClaimsLocked(entry)

	for peer := range entry.peers {
		s.decrementPeerTxCountLocked(peer)
	}

	delete(s.txByKey, entry.txKey)
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
		if len(entry.peers) == 0 {
			s.removeEntryLocked(entry)
			continue
		}
		s.removeAnnouncerLocked(entry, peer)
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

// PendingTxInfoFrom reports the signer/sequence claim that peer announced
// for txKey; ok is false if peer announced no queued claim. The returned
// signer is a copy.
func (s *SeenTracker) PendingTxInfoFrom(txKey types.TxKey, peer uint16) (signer []byte, sequence uint64, ok bool) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, found := s.txByKey[txKey]
	if !found {
		return nil, 0, false
	}
	claim := entry.claimFrom(peer)
	if claim == nil {
		return nil, 0, false
	}

	return append([]byte(nil), claim.signer...), claim.sequence, true
}

// PrunePending drops future-sequence metadata for entries added before cutoff
// while keeping the peer-has-tx entry alive.
func (s *SeenTracker) PrunePending(cutoff time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	for _, entry := range s.txByKey {
		if entry.addedAt.Before(cutoff) {
			s.dropClaimsLocked(entry)
		}
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

// RequestCandidates returns the peers known to have txKey, with peers that
// announced a claim for it first.
func (s *SeenTracker) RequestCandidates(txKey types.TxKey) []uint16 {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return nil
	}
	return entry.peerIDs()
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
	s.pendingTxsBySigner = make(map[string][]*PendingTxInfo)
	s.txCountByPeer = make(map[uint16]int)
}

// PendingTxsForSigner returns that signer's future txs in sequence order,
// one copy per claim, each with pendingTxInfo set to that claim.
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
	for i, claim := range queue {
		out[i] = &SeenEntry{
			txKey:         claim.entry.txKey,
			peers:         maps.Clone(claim.entry.peers),
			addedAt:       claim.entry.addedAt,
			pendingTxInfo: claim.clone(),
		}
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

// RemovePeerFromTx forgets that peer has txKey, e.g. after it was asked for
// the tx and never delivered. Drops the entry if no other peer has the tx.
func (s *SeenTracker) RemovePeerFromTx(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()

	entry, ok := s.txByKey[txKey]
	if !ok {
		return
	}

	if _, has := entry.peers[peer]; has {
		delete(entry.peers, peer)
		s.decrementPeerTxCountLocked(peer)
	}
	if len(entry.peers) == 0 {
		s.removeEntryLocked(entry)
		return
	}
	s.removeAnnouncerLocked(entry, peer)
}

// removeAnnouncerLocked withdraws a peer that no longer has the tx from its
// claim. Peer IDs are reused, so a later peer must not inherit the
// announcement. The claim itself stays queued, so a retry with another peer
// can still be requested again when its gap closes. Claims left without
// announcers are dropped only to keep claims from outnumbering peers.
func (s *SeenTracker) removeAnnouncerLocked(entry *SeenEntry, peer uint16) {
	if claim := entry.claimFrom(peer); claim != nil {
		delete(claim.announcers, peer)
	}
	s.trimClaimsLocked(entry)
}

// trimClaimsLocked drops the oldest claims without announcers until claims no
// longer outnumber the entry's peers. Each peer backs at most one claim, so
// this bounds claims by the per-peer limit.
func (s *SeenTracker) trimClaimsLocked(entry *SeenEntry) {
	for _, claim := range slices.Clone(entry.claims) {
		if len(entry.claims) <= len(entry.peers) {
			return
		}
		if len(claim.announcers) == 0 {
			s.dropClaimLocked(claim)
		}
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
