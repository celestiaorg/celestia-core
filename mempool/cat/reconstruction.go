package cat

import (
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/mempool"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// reconstructionSession tracks the in-progress assembly of a single large tx
// from chunks fetched across multiple peers. It is safe for concurrent use.
type reconstructionSession struct {
	mtx sync.Mutex

	manifest *protomem.TxManifest
	txKey    types.TxKey
	txInfo   mempool.TxInfo

	chunks        [][]byte // indexed 0..chunk_count-1; nil means not yet received
	received      []bool
	receivedCount int
	source        []uint16 // peer that supplied each received chunk (0 if none)

	knownPeers map[uint16]struct{}         // peers that advertise/serve this tx
	inflight   map[uint16]map[int]struct{} // peer -> chunk indexes requested but not yet received

	createdAt time.Time
	deadline  time.Time
	done      bool
}

func newReconstructionSession(m *protomem.TxManifest, txInfo mempool.TxInfo, now time.Time, reconstructionTimeout time.Duration) *reconstructionSession {
	key, _ := types.TxKeyFromBytes(m.TxKey) // validated by caller
	count := int(m.ChunkCount)
	return &reconstructionSession{
		manifest:   m,
		txKey:      key,
		txInfo:     txInfo,
		chunks:     make([][]byte, count),
		received:   make([]bool, count),
		source:     make([]uint16, count),
		knownPeers: make(map[uint16]struct{}),
		inflight:   make(map[uint16]map[int]struct{}),
		createdAt:  now,
		deadline:   now.Add(reconstructionTimeout),
	}
}

// addPeer records that peerID advertises or can serve this tx.
func (s *reconstructionSession) addPeer(peerID uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if peerID != 0 {
		s.knownPeers[peerID] = struct{}{}
	}
}

// peers returns the set of peers known to advertise/serve this tx.
func (s *reconstructionSession) peers() []uint16 {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	out := make([]uint16, 0, len(s.knownPeers))
	for p := range s.knownPeers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// markInflight records that a WantChunk for index was sent to peerID.
func (s *reconstructionSession) markInflight(peerID uint16, index int) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.inflight[peerID] == nil {
		s.inflight[peerID] = make(map[int]struct{})
	}
	s.inflight[peerID][index] = struct{}{}
}

// clearInflight removes a single (peer, index) inflight request.
func (s *reconstructionSession) clearInflight(peerID uint16, index int) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.clearInflightLocked(peerID, index)
}

func (s *reconstructionSession) clearInflightLocked(peerID uint16, index int) {
	if set := s.inflight[peerID]; set != nil {
		delete(set, index)
		if len(set) == 0 {
			delete(s.inflight, peerID)
		}
	}
}

// inflightCountForPeer returns the number of outstanding chunk requests to peerID.
func (s *reconstructionSession) inflightCountForPeer(peerID uint16) int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.inflight[peerID])
}

// isInflight reports whether index is currently requested from any peer.
func (s *reconstructionSession) isInflight(index int) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for _, set := range s.inflight {
		if _, ok := set[index]; ok {
			return true
		}
	}
	return false
}

// addChunk verifies and stores a received chunk. It returns true if the chunk
// was newly added, false if it was a duplicate, and an error if the chunk fails
// verification against the manifest.
func (s *reconstructionSession) addChunk(index int, data []byte, peerID uint16) (bool, error) {
	if err := verifyChunk(s.manifest, index, data); err != nil {
		return false, err
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	// Clear inflight for this index regardless of duplicate status.
	for p := range s.inflight {
		s.clearInflightLocked(p, index)
	}
	if s.received[index] {
		return false, nil
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	s.chunks[index] = cp
	s.received[index] = true
	s.source[index] = peerID
	s.receivedCount++
	return true, nil
}

// missing returns the indexes that have neither been received nor are currently
// inflight, in ascending order.
func (s *reconstructionSession) missing() []int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	inflightIdx := make(map[int]struct{})
	for _, set := range s.inflight {
		for idx := range set {
			inflightIdx[idx] = struct{}{}
		}
	}
	out := make([]int, 0, len(s.received)-s.receivedCount)
	for i, got := range s.received {
		if got {
			continue
		}
		if _, ok := inflightIdx[i]; ok {
			continue
		}
		out = append(out, i)
	}
	return out
}

// reserveChunks atomically selects up to max chunk indexes that are neither
// received nor currently inflight to any peer, marks them inflight to peerID,
// and returns them in ascending order. This makes "pick missing + mark inflight"
// a single atomic step so concurrent schedulers (receive path and timeout
// callbacks) never assign the same chunk twice.
func (s *reconstructionSession) reserveChunks(peerID uint16, max int) []int {
	if max <= 0 {
		return nil
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()

	inflightIdx := make(map[int]struct{})
	for _, set := range s.inflight {
		for idx := range set {
			inflightIdx[idx] = struct{}{}
		}
	}
	out := make([]int, 0, max)
	for i, got := range s.received {
		if len(out) >= max {
			break
		}
		if got {
			continue
		}
		if _, ok := inflightIdx[i]; ok {
			continue
		}
		out = append(out, i)
	}
	if len(out) == 0 {
		return nil
	}
	if s.inflight[peerID] == nil {
		s.inflight[peerID] = make(map[int]struct{})
	}
	for _, idx := range out {
		s.inflight[peerID][idx] = struct{}{}
	}
	return out
}

// sourcePeers returns the unique set of peers that supplied at least one
// received chunk.
func (s *reconstructionSession) sourcePeers() []uint16 {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seen := make(map[uint16]struct{})
	var out []uint16
	for i, got := range s.received {
		if !got {
			continue
		}
		p := s.source[i]
		if p == 0 {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// tryFinish marks the session done, returning true only on the first call. It
// guards against finishing (and re-adding the tx) more than once when chunks
// arrive concurrently or a sweep races the receive path.
func (s *reconstructionSession) tryFinish() bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.done {
		return false
	}
	s.done = true
	return true
}

// isComplete reports whether all chunks have been received.
func (s *reconstructionSession) isComplete() bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.receivedCount == len(s.received)
}

// reconstruct assembles and verifies the full tx. It must only be called once
// isComplete reports true.
func (s *reconstructionSession) reconstruct() ([]byte, error) {
	s.mtx.Lock()
	chunks := make([][]byte, len(s.chunks))
	copy(chunks, s.chunks)
	s.mtx.Unlock()
	return reconstructTx(s.manifest, chunks)
}

// expired reports whether the session has passed its reconstruction deadline.
func (s *reconstructionSession) expired(now time.Time) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return now.After(s.deadline)
}

// removePeer drops a disconnected peer from the session, returning the indexes
// that were inflight to it so the caller can re-request them elsewhere.
func (s *reconstructionSession) removePeer(peerID uint16) []int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.knownPeers, peerID)
	var orphaned []int
	if set := s.inflight[peerID]; set != nil {
		for idx := range set {
			if !s.received[idx] {
				orphaned = append(orphaned, idx)
			}
		}
		delete(s.inflight, peerID)
	}
	sort.Ints(orphaned)
	return orphaned
}

// reconstructionManager owns the set of active reconstruction sessions, keyed by
// tx key. It is safe for concurrent use.
type reconstructionManager struct {
	mtx                   sync.Mutex
	sessions              map[types.TxKey]*reconstructionSession
	reconstructionTimeout time.Duration
}

func newReconstructionManager(reconstructionTimeout time.Duration) *reconstructionManager {
	return &reconstructionManager{
		sessions:              make(map[types.TxKey]*reconstructionSession),
		reconstructionTimeout: reconstructionTimeout,
	}
}

func (rm *reconstructionManager) get(txKey types.TxKey) (*reconstructionSession, bool) {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	s, ok := rm.sessions[txKey]
	return s, ok
}

func (rm *reconstructionManager) has(txKey types.TxKey) bool {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	_, ok := rm.sessions[txKey]
	return ok
}

// create starts a new session for the manifest. It returns the existing session
// (and false) if one is already in progress for the same tx key.
func (rm *reconstructionManager) create(m *protomem.TxManifest, txInfo mempool.TxInfo, now time.Time) (*reconstructionSession, bool) {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	key, _ := types.TxKeyFromBytes(m.TxKey)
	if existing, ok := rm.sessions[key]; ok {
		return existing, false
	}
	s := newReconstructionSession(m, txInfo, now, rm.reconstructionTimeout)
	rm.sessions[key] = s
	return s, true
}

func (rm *reconstructionManager) remove(txKey types.TxKey) {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	delete(rm.sessions, txKey)
}

func (rm *reconstructionManager) len() int {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	return len(rm.sessions)
}

// all returns a snapshot of active sessions.
func (rm *reconstructionManager) all() []*reconstructionSession {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	out := make([]*reconstructionSession, 0, len(rm.sessions))
	for _, s := range rm.sessions {
		out = append(out, s)
	}
	return out
}

// removePeer drops peerID from every active session and returns, per tx key, the
// chunk indexes that were inflight to that peer.
func (rm *reconstructionManager) removePeer(peerID uint16) map[types.TxKey][]int {
	rm.mtx.Lock()
	sessions := make(map[types.TxKey]*reconstructionSession, len(rm.sessions))
	for k, s := range rm.sessions {
		sessions[k] = s
	}
	rm.mtx.Unlock()

	orphaned := make(map[types.TxKey][]int)
	for k, s := range sessions {
		if idxs := s.removePeer(peerID); len(idxs) > 0 {
			orphaned[k] = idxs
		}
	}
	return orphaned
}
