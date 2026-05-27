package chunked

import (
	"errors"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// Lifecycle states for a chunked transaction.
type Lifecycle uint8

const (
	StateCollecting Lifecycle = iota
	StateReconstructed
)

// Defaults for memory accounting and TTL. Exposed so the reactor can override
// from config.MempoolConfig without coupling this package to config.
const (
	DefaultPartialTTL         = 30 * time.Second
	DefaultGlobalMaxBytes     = 256 << 20 // 256 MiB
	DefaultPerPeerMaxBytes    = 64 << 20  // 64 MiB
	DefaultBootstrapPushPeers = 4
	DefaultAnnounceTarget     = 60
	// DefaultChunksPerPushPeer is the number of random chunks the originator
	// pushes to each peer that received SeenLargeTx. Higher values reduce the
	// likelihood that any receiver needs to pull chunks from the origin and
	// spread the chunk inventory across the network faster.
	DefaultChunksPerPushPeer = 10
)

var (
	ErrAlreadyExists       = errors.New("chunked: partsState already exists for tx_key")
	ErrGlobalCapExceeded   = errors.New("chunked: global partial memory cap exceeded")
	ErrPerPeerCapExceeded  = errors.New("chunked: per-peer partial memory cap exceeded")
	ErrNotCollecting       = errors.New("chunked: partsState is not in collecting state")
	ErrUnknownTx           = errors.New("chunked: unknown tx_key")
	ErrChunkOutOfRange     = errors.New("chunked: chunk index out of range")
	ErrChunkAlreadyPresent = errors.New("chunked: chunk already present")
	ErrLeafHashesMismatch  = errors.New("chunked: leaf_hashes do not commit to parts_root")
)

// PartsState holds all per-tx state for chunked propagation.
type PartsState struct {
	mtx sync.Mutex

	TxKey      types.TxKey
	PartsRoot  []byte
	NumParts   uint32 // 2K, or 1 for the no-parity case
	K          uint32 // number of originals; K == 1 for the no-parity case
	LastLength uint32
	LeafHashes [][]byte

	// Signer and Sequence mirror the equivalent SeenTx fields so the
	// reactor's per-signer sequence-ordering machinery (pendingSeen,
	// receivedBuffer) can be applied uniformly to chunked txs.
	Signer   []byte
	Sequence uint64

	// chunks[i] is non-nil once chunk i has been received and verified.
	chunks   [][]byte
	received *bits.BitArray

	// proofs[i] is the Merkle inclusion proof against PartsRoot for chunk i.
	// Set by InsertReconstructed (sender side) and by ReconstructAndVerify
	// (receiver side, by re-computing from the reconstructed parts) so that
	// serving WantTxChunks does not require re-encoding the body per chunk
	// request. nil for NumParts == 1 (no proof needed; partsRoot == hash).
	proofs []*merkle.Proof

	// Per-peer maps. Keys are p2p IDs assigned by the reactor's mempoolIDs.
	haves    map[uint16]*bits.BitArray // chunks the peer advertises
	inflight map[uint16]*bits.BitArray // chunks we've requested from the peer
	notified map[uint16]*bits.BitArray // chunks we've told the peer we hold

	originPeer   uint16
	firstChunkAt time.Time
	state        Lifecycle
	bytesHeld    int64 // bytes currently retained for accounting

	// Reconstructed-only fields, set during Reconstruct/Promote.
	Body []byte
}

// State returns the current lifecycle state.
func (p *PartsState) State() Lifecycle {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.state
}

// ReceivedCount returns the number of chunks we currently hold.
func (p *PartsState) ReceivedCount() int {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.receivedCountLocked()
}

func (p *PartsState) receivedCountLocked() int {
	if p.received == nil {
		return 0
	}
	c := 0
	for i := uint32(0); i < p.NumParts; i++ {
		if p.received.GetIndex(int(i)) {
			c++
		}
	}
	return c
}

// CanDecode reports whether we hold enough chunks to reconstruct the body.
func (p *PartsState) CanDecode() bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.receivedCountLocked() >= int(p.K)
}

// Missing returns a BitArray of chunks we still need.
func (p *PartsState) Missing() *bits.BitArray {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	ba := bits.NewBitArray(int(p.NumParts))
	for i := uint32(0); i < p.NumParts; i++ {
		if !p.received.GetIndex(int(i)) {
			ba.SetIndex(int(i), true)
		}
	}
	return ba
}

// MissingFromPeer returns the intersection of (chunks the peer advertises) with
// (chunks we still need and haven't requested from any peer recently).
// Returns nil if the peer has nothing useful to us.
func (p *PartsState) MissingFromPeer(peerID uint16) *bits.BitArray {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	have, ok := p.haves[peerID]
	if !ok || have == nil {
		return nil
	}
	out := bits.NewBitArray(int(p.NumParts))
	any := false
	for i := uint32(0); i < p.NumParts; i++ {
		if !have.GetIndex(int(i)) {
			continue
		}
		if p.received.GetIndex(int(i)) {
			continue
		}
		if p.anyInflightLocked(int(i)) {
			continue
		}
		out.SetIndex(int(i), true)
		any = true
	}
	if !any {
		return nil
	}
	return out
}

func (p *PartsState) anyInflightLocked(index int) bool {
	for _, ba := range p.inflight {
		if ba != nil && ba.GetIndex(index) {
			return true
		}
	}
	return false
}

// RecordHaves records that a peer advertises the given chunks.
func (p *PartsState) RecordHaves(peerID uint16, parts *bits.BitArray) {
	if parts == nil {
		return
	}
	p.mtx.Lock()
	defer p.mtx.Unlock()
	cur, ok := p.haves[peerID]
	if !ok || cur == nil {
		cur = bits.NewBitArray(int(p.NumParts))
		p.haves[peerID] = cur
	}
	cur.AddBitArray(parts)
}

// MarkInflight records that we've sent a WantTxChunks for the given parts.
func (p *PartsState) MarkInflight(peerID uint16, parts *bits.BitArray) {
	if parts == nil {
		return
	}
	p.mtx.Lock()
	defer p.mtx.Unlock()
	cur, ok := p.inflight[peerID]
	if !ok || cur == nil {
		cur = bits.NewBitArray(int(p.NumParts))
		p.inflight[peerID] = cur
	}
	cur.AddBitArray(parts)
}

// ClearInflightIndex marks that a previously-inflight request from peer has
// either been satisfied or cancelled.
func (p *PartsState) ClearInflightIndex(peerID uint16, index uint32) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if ba, ok := p.inflight[peerID]; ok && ba != nil {
		ba.SetIndex(int(index), false)
	}
}

// ClearPeer drops all per-peer state for the given peer. Used on peer
// disconnect so backup announcers can be promoted (any in-flight chunks that
// were Want-ed from this peer become re-requestable).
func (p *PartsState) ClearPeer(peerID uint16) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	delete(p.haves, peerID)
	delete(p.inflight, peerID)
	delete(p.notified, peerID)
}

// PeerHasChunk reports whether peer P has told us it holds chunk index.
func (p *PartsState) PeerHasChunk(peerID uint16, index uint32) bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	have, ok := p.haves[peerID]
	if !ok || have == nil {
		return false
	}
	return have.GetIndex(int(index))
}

// PeerKnowsChunk reports whether peer P either holds chunk index (haves) or
// has been told by us that we hold it (notified). Used to skip redundant
// HaveTxChunks gossip.
func (p *PartsState) PeerKnowsChunk(peerID uint16, index uint32) bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if have, ok := p.haves[peerID]; ok && have != nil && have.GetIndex(int(index)) {
		return true
	}
	if n, ok := p.notified[peerID]; ok && n != nil && n.GetIndex(int(index)) {
		return true
	}
	return false
}

// PeerHasAll reports whether peer P holds every chunk.
func (p *PartsState) PeerHasAll(peerID uint16) bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	have, ok := p.haves[peerID]
	if !ok || have == nil {
		return false
	}
	for i := uint32(0); i < p.NumParts; i++ {
		if !have.GetIndex(int(i)) {
			return false
		}
	}
	return true
}

// RecordNotified marks that we have told peer P about the given chunks
// (i.e. we sent them HaveTxChunks containing these bits).
func (p *PartsState) RecordNotified(peerID uint16, parts *bits.BitArray) {
	if parts == nil {
		return
	}
	p.mtx.Lock()
	defer p.mtx.Unlock()
	cur, ok := p.notified[peerID]
	if !ok || cur == nil {
		cur = bits.NewBitArray(int(p.NumParts))
		p.notified[peerID] = cur
	}
	cur.AddBitArray(parts)
}

// RequestableFromPeer returns up to batchSize chunks the peer advertises that
// we still need AND that are not currently in-flight to any peer. Honors a
// per-peer in-flight cap (no more outstanding chunks than perPeerCap from
// any one peer). Returns nil when nothing is requestable.
func (p *PartsState) RequestableFromPeer(peerID uint16, perPeerCap, batchSize int) *bits.BitArray {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	have, ok := p.haves[peerID]
	if !ok || have == nil {
		return nil
	}
	curInflight := 0
	if ba, ok := p.inflight[peerID]; ok && ba != nil {
		for i := uint32(0); i < p.NumParts; i++ {
			if ba.GetIndex(int(i)) {
				curInflight++
			}
		}
	}
	remaining := perPeerCap - curInflight
	if remaining <= 0 {
		return nil
	}
	if remaining > batchSize {
		remaining = batchSize
	}
	out := bits.NewBitArray(int(p.NumParts))
	taken := 0
	for i := uint32(0); i < p.NumParts && taken < remaining; i++ {
		if !have.GetIndex(int(i)) {
			continue
		}
		if p.received.GetIndex(int(i)) {
			continue
		}
		if p.anyInflightLocked(int(i)) {
			continue
		}
		out.SetIndex(int(i), true)
		taken++
	}
	if taken == 0 {
		return nil
	}
	return out
}

// Install installs a verified chunk. The caller must have already validated
// the chunk against PartsRoot via VerifyChunk. Returns (justCompleted true)
// when this insertion is the one that satisfies K-of-2K for the first time.
func (p *PartsState) Install(index uint32, data []byte) (justCompleted bool, err error) {
	if index >= p.NumParts {
		return false, ErrChunkOutOfRange
	}
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.state != StateCollecting {
		return false, ErrNotCollecting
	}
	if p.chunks[index] != nil {
		return false, ErrChunkAlreadyPresent
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	p.chunks[index] = stored
	p.received.SetIndex(int(index), true)
	p.bytesHeld += int64(len(stored))

	// Clear any inflight bit for this chunk across all peers; we have it now.
	for _, ba := range p.inflight {
		if ba != nil {
			ba.SetIndex(int(index), false)
		}
	}

	if p.firstChunkAt.IsZero() {
		p.firstChunkAt = time.Now()
	}

	if p.receivedCountLocked() >= int(p.K) {
		return true, nil
	}
	return false, nil
}

// chunksForDecode returns a snapshot of the chunks slice suitable for Decode.
// Caller takes ownership of the returned slice.
func (p *PartsState) chunksForDecode() [][]byte {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	out := make([][]byte, p.NumParts)
	for i, c := range p.chunks {
		if c != nil {
			out[i] = c
		}
	}
	return out
}

// BytesHeld returns bytes accounted to this state.
func (p *PartsState) BytesHeld() int64 {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.bytesHeld
}

// OriginPeer is the peer whose SeenLargeTx caused us to create this state.
func (p *PartsState) OriginPeer() uint16 {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.originPeer
}

// FirstChunkAt returns the time of the first chunk arrival, or zero.
func (p *PartsState) FirstChunkAt() time.Time {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.firstChunkAt
}

// markReconstructed transitions to StateReconstructed and stores the body.
// Caller must already have verified SHA256(body) == tx_key.
func (p *PartsState) markReconstructed(body []byte) {
	p.markReconstructedWithProofs(body, nil)
}

// markReconstructedWithProofs is the proof-aware variant. If proofs is
// non-nil and has length NumParts it is retained for serving WantTxChunks
// without re-encoding the body.
func (p *PartsState) markReconstructedWithProofs(body []byte, proofs []*merkle.Proof) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	p.state = StateReconstructed
	p.Body = body
	if len(proofs) == int(p.NumParts) {
		p.proofs = proofs
	}
}

// ChunkPayload returns the on-wire payload for a single chunk: the chunk
// bytes and the Merkle proof (if NumParts > 1). hasProof is false when the
// state is single-chunk (NumParts == 1) — in that case the caller should
// transmit an empty proof and the receiver verifies sha256(data) == PartsRoot.
// ok is false when the state does not currently hold the chunk or the proof.
func (p *PartsState) ChunkPayload(index uint32) (data []byte, proof *merkle.Proof, hasProof bool, ok bool) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if index >= p.NumParts || p.chunks[index] == nil {
		return nil, nil, false, false
	}
	if p.NumParts == 1 {
		return p.chunks[index], nil, false, true
	}
	if int(index) >= len(p.proofs) || p.proofs[index] == nil {
		return nil, nil, true, false
	}
	return p.chunks[index], p.proofs[index], true, true
}

// Store keeps PartsStates for in-flight and reconstructed chunked txs.
type Store struct {
	mtx sync.RWMutex

	states map[types.TxKey]*PartsState

	// Memory accounting. Bytes for states in StateCollecting only.
	globalBytes int64
	peerBytes   map[uint16]int64

	globalMax  int64
	perPeerMax int64
	ttl        time.Duration
}

// NewStore constructs a chunked store with default caps and TTL.
func NewStore() *Store {
	return &Store{
		states:     make(map[types.TxKey]*PartsState),
		peerBytes:  make(map[uint16]int64),
		globalMax:  DefaultGlobalMaxBytes,
		perPeerMax: DefaultPerPeerMaxBytes,
		ttl:        DefaultPartialTTL,
	}
}

// WithLimits overrides the default memory caps and TTL.
func (s *Store) WithLimits(globalMax, perPeerMax int64, ttl time.Duration) *Store {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if globalMax > 0 {
		s.globalMax = globalMax
	}
	if perPeerMax > 0 {
		s.perPeerMax = perPeerMax
	}
	if ttl > 0 {
		s.ttl = ttl
	}
	return s
}

// Get returns the PartsState for the given tx_key, or nil.
func (s *Store) Get(txKey types.TxKey) *PartsState {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.states[txKey]
}

// Has reports whether we are tracking the given tx_key in any state.
func (s *Store) Has(txKey types.TxKey) bool {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	_, ok := s.states[txKey]
	return ok
}

// InsertParams captures the fields needed to create a new collecting state.
type InsertParams struct {
	TxKey      types.TxKey
	PartsRoot  []byte
	NumParts   uint32
	LastLength uint32
	LeafHashes [][]byte
	OriginPeer uint16
	Signer     []byte
	Sequence   uint64
}

// Insert creates a new PartsState in StateCollecting. Returns ErrAlreadyExists
// if a state for the key already exists.
func (s *Store) Insert(p InsertParams) (*PartsState, error) {
	if p.NumParts == 0 {
		return nil, ErrChunkOutOfRange
	}
	k := p.NumParts
	if p.NumParts > 1 {
		if p.NumParts%2 != 0 {
			return nil, ErrChunkOutOfRange
		}
		k = p.NumParts / 2
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()
	if _, exists := s.states[p.TxKey]; exists {
		return nil, ErrAlreadyExists
	}

	// Compute proofs from leaf hashes up-front. This both:
	//   1. Validates that leaf_hashes commits to parts_root (mismatch
	//      indicates a malformed SeenLargeTx — reject).
	//   2. Lets the receiver serve chunks from collecting state (before
	//      reconstruction) since proofs are ready.
	var proofs []*merkle.Proof
	if p.NumParts > 1 {
		computedRoot, p2 := proofsFromLeafHashes(p.LeafHashes)
		if !bytesEqual(computedRoot, p.PartsRoot) {
			return nil, ErrLeafHashesMismatch
		}
		proofs = p2
	}

	state := &PartsState{
		TxKey:      p.TxKey,
		PartsRoot:  append([]byte(nil), p.PartsRoot...),
		NumParts:   p.NumParts,
		K:          k,
		LastLength: p.LastLength,
		LeafHashes: p.LeafHashes,
		Signer:     append([]byte(nil), p.Signer...),
		Sequence:   p.Sequence,
		chunks:     make([][]byte, p.NumParts),
		received:   bits.NewBitArray(int(p.NumParts)),
		haves:      make(map[uint16]*bits.BitArray),
		inflight:   make(map[uint16]*bits.BitArray),
		notified:   make(map[uint16]*bits.BitArray),
		originPeer: p.OriginPeer,
		state:      StateCollecting,
		proofs:     proofs,
	}
	s.states[p.TxKey] = state
	return state, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// InsertReconstructed seeds the store with an already-reconstructed state
// (used on the originating node after sender-side encoding). Retains the
// per-chunk Merkle proofs from the encoder so subsequent WantTxChunks can be
// served without re-encoding the body.
func (s *Store) InsertReconstructed(p InsertParams, enc *EncodedTx) (*PartsState, error) {
	state, err := s.Insert(p)
	if err != nil {
		return nil, err
	}
	state.mtx.Lock()
	for i := uint32(0); i < enc.NumParts(); i++ {
		state.chunks[i] = enc.Chunk(i)
		state.received.SetIndex(int(i), true)
	}
	if enc.NumParts() > 1 && len(enc.Proofs) == int(enc.NumParts()) {
		state.proofs = enc.Proofs
	}
	state.Body = enc.Body
	state.state = StateReconstructed
	state.mtx.Unlock()
	return state, nil
}

// Reserve accounts bytes for a partial state before installing a chunk. If
// either the global or per-peer cap would be exceeded, returns the matching
// error. Charges are released by Release / RemoveCollecting.
func (s *Store) Reserve(originPeer uint16, n int64) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.globalBytes+n > s.globalMax {
		return ErrGlobalCapExceeded
	}
	if s.peerBytes[originPeer]+n > s.perPeerMax {
		return ErrPerPeerCapExceeded
	}
	s.globalBytes += n
	s.peerBytes[originPeer] += n
	return nil
}

// Release returns bytes previously charged via Reserve.
func (s *Store) Release(originPeer uint16, n int64) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.globalBytes -= n
	if s.globalBytes < 0 {
		s.globalBytes = 0
	}
	s.peerBytes[originPeer] -= n
	if s.peerBytes[originPeer] <= 0 {
		delete(s.peerBytes, originPeer)
	}
}

// Remove drops the state. Releases any reserved bytes if it was collecting.
func (s *Store) Remove(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	state, ok := s.states[txKey]
	if !ok {
		return
	}
	delete(s.states, txKey)
	state.mtx.Lock()
	if state.state == StateCollecting && state.bytesHeld > 0 {
		s.globalBytes -= state.bytesHeld
		if s.globalBytes < 0 {
			s.globalBytes = 0
		}
		s.peerBytes[state.originPeer] -= state.bytesHeld
		if s.peerBytes[state.originPeer] <= 0 {
			delete(s.peerBytes, state.originPeer)
		}
	}
	state.mtx.Unlock()
}

// SweepExpired drops collecting states whose first chunk arrived before
// now - ttl. Returns the set of evicted tx_keys (callers use this for
// metrics and peer downscoring).
func (s *Store) SweepExpired(now time.Time) []types.TxKey {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	ttl := s.ttl
	if ttl <= 0 {
		return nil
	}
	cutoff := now.Add(-ttl)
	var evicted []types.TxKey
	for k, state := range s.states {
		state.mtx.Lock()
		expired := state.state == StateCollecting &&
			!state.firstChunkAt.IsZero() &&
			state.firstChunkAt.Before(cutoff)
		held := state.bytesHeld
		origin := state.originPeer
		state.mtx.Unlock()
		if !expired {
			continue
		}
		delete(s.states, k)
		s.globalBytes -= held
		if s.globalBytes < 0 {
			s.globalBytes = 0
		}
		s.peerBytes[origin] -= held
		if s.peerBytes[origin] <= 0 {
			delete(s.peerBytes, origin)
		}
		evicted = append(evicted, k)
	}
	return evicted
}

// Stats returns counters for metrics emission.
type Stats struct {
	NumStates        int
	NumCollecting    int
	NumReconstructed int
	GlobalBytes      int64
}

func (s *Store) Stats() Stats {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	st := Stats{NumStates: len(s.states), GlobalBytes: s.globalBytes}
	for _, state := range s.states {
		state.mtx.Lock()
		switch state.state {
		case StateCollecting:
			st.NumCollecting++
		case StateReconstructed:
			st.NumReconstructed++
		}
		state.mtx.Unlock()
	}
	return st
}

// ReconstructAndVerify attempts to reconstruct the body from the chunks
// currently held in the state, then checks SHA256(body) == tx_key. On
// success the body is stored in the state and the lifecycle advances to
// StateReconstructed. Re-encodes from the body to populate per-chunk
// Merkle proofs so subsequent WantTxChunks can be served without redoing
// the Reed-Solomon encode per request. The caller is responsible for the
// subsequent CheckTx admission step.
func (s *Store) ReconstructAndVerify(state *PartsState) ([]byte, error) {
	chunks := state.chunksForDecode()
	body, err := Decode(chunks, state.LastLength)
	if err != nil {
		return nil, err
	}
	// Authentic body must hash to the announced tx_key.
	if !verifyBodyHash(body, state.TxKey) {
		return nil, errors.New("chunked: reconstructed body hash != tx_key")
	}
	// One-shot encode to recover proofs for serving. For NumParts == 1
	// proofs are not needed.
	var proofs []*merkle.Proof
	if state.NumParts > 1 {
		enc, encErr := Encode(body)
		if encErr == nil && len(enc.Proofs) == int(state.NumParts) {
			proofs = enc.Proofs
		}
	}
	state.markReconstructedWithProofs(body, proofs)
	s.mtx.Lock()
	if state.bytesHeld > 0 {
		s.globalBytes -= state.bytesHeld
		if s.globalBytes < 0 {
			s.globalBytes = 0
		}
		s.peerBytes[state.originPeer] -= state.bytesHeld
		if s.peerBytes[state.originPeer] <= 0 {
			delete(s.peerBytes, state.originPeer)
		}
	}
	s.mtx.Unlock()
	return body, nil
}

func verifyBodyHash(body []byte, expected types.TxKey) bool {
	got := types.Tx(body).Key()
	for i := range got {
		if got[i] != expected[i] {
			return false
		}
	}
	return true
}

// ChargeChunk debits bytes for an incoming chunk against the originating
// peer's quota. Returns an error if a cap would be breached. On success
// the caller should proceed to Install.
func (s *Store) ChargeChunk(state *PartsState, n int) error {
	return s.Reserve(state.OriginPeer(), int64(n))
}

// Verify that PartsState satisfies a minimal interface used by tests.
var _ interface{ State() Lifecycle } = (*PartsState)(nil)

// Just so the import doesn't get pruned by goimports in environments where
// the file doesn't reference p2p directly yet.
var _ p2p.ID = ""
