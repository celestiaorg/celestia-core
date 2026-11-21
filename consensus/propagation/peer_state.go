package propagation

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

const (
	// MaxUnverifiedProposals is the maximum number of compact blocks
	// that can be cached per peer. Keeps the lowest heights when full.
	MaxUnverifiedProposals = 24

	// MaxPeerStateHeights caps the heights a PeerState tracks part state and
	// request counters for. The reactor only tracks heights it holds a
	// proposal for, from the last committed height up, and pruning on commit
	// drops lower ones, so honest use needs a few heights. A full PeerState
	// keeps its lowest heights, which are the next ones to commit.
	MaxPeerStateHeights = 16

	// MaxPeerStateRoundsPerHeight caps the rounds a PeerState tracks per
	// height. Consensus reaches round 32 at one height only after a stall of
	// several minutes. The reactor only tracks rounds it accepted a proposal
	// for, and it accepts proposals only for the current consensus round, so
	// a peer cannot pick the rounds. A full height keeps its highest rounds,
	// because the current round is the newest one and parts for it must stay
	// requestable after a long stall.
	MaxPeerStateRoundsPerHeight = 32
)

type request struct {
	height int64
	round  int32
	index  uint32
}

// PeerState keeps track of haves and wants for each peer. This is used for
// block prop and catchup.
type PeerState struct {
	ctx    context.Context
	cancel context.CancelFunc
	peer   p2p.Peer

	mtx *sync.RWMutex
	// state organized the haves and wants for each data is indexed by height
	// and round.
	state map[int64]map[int32]*partState

	concurrentReqs    atomic.Int64
	receivedParts     chan partData
	receivedHaves     chan request
	canRequest        chan struct{}
	remainingRequests map[int64]map[int32]int

	logger log.Logger

	// consensusPeerState allows the propagation reactor to update peer state
	// in the consensus reactor. This enables both reactors to gossip data
	// while minimizing redundant bandwidth.
	consensusPeerState PeerStateEditor

	// unverifiedProposals stores compact blocks received from this peer
	// for heights we weren't ready to process. Indexed by height.
	// These have NOT been verified via the consensus reactor's verification
	// function. Limited to MaxUnverifiedProposals entries, keeping lowest heights.
	unverifiedProposals map[int64]*proptypes.CompactBlock
}

type partData struct {
	height int64
	round  int32
}

// newPeerState initializes and returns a new PeerState. This should be
// called for each peer.
func newPeerState(ctx context.Context, peer p2p.Peer, logger log.Logger) *PeerState {
	ctx, cancel := context.WithCancel(ctx)
	return &PeerState{
		ctx:                 ctx,
		cancel:              cancel,
		mtx:                 &sync.RWMutex{},
		state:               make(map[int64]map[int32]*partState),
		peer:                peer,
		logger:              logger,
		receivedHaves:       make(chan request, 20_000),
		receivedParts:       make(chan partData, 20_000),
		canRequest:          make(chan struct{}, 1),
		remainingRequests:   make(map[int64]map[int32]int),
		consensusPeerState:  noOpPSE{},
		unverifiedProposals: make(map[int64]*proptypes.CompactBlock),
	}
}

// SetConsensusPeerState sets the consensus peer state editor for this peer
func (ps *PeerState) SetConsensusPeerState(editor PeerStateEditor) {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	ps.consensusPeerState = editor
}

// GetConsensusPeerState returns the consensus peer state editor if available
func (ps *PeerState) GetConsensusPeerState() PeerStateEditor {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	return ps.consensusPeerState
}

// MaxUnverifiedProposalHeight returns the highest cached unverified proposal height.
// Returns 0 if none exist.
func (ps *PeerState) MaxUnverifiedProposalHeight() int64 {
	ps.mtx.RLock()
	defer ps.mtx.RUnlock()
	var max int64
	for h := range ps.unverifiedProposals {
		if h > max {
			max = h
		}
	}
	return max
}

// Initialize initializes the state for a given height and round in a
// thread-safe way.
func (d *PeerState) Initialize(height int64, round int32, size int) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, size)
}

// initialize initializes the state for a given height and round and returns
// it, or returns nil if the caps on tracked heights and rounds refuse it. This
// method is not thread-safe.
func (d *PeerState) initialize(height int64, round int32, size int) *partState {
	if !admit(d.state, height, MaxPeerStateHeights, keepLowest) {
		return nil
	}
	rounds := d.state[height]
	if rounds == nil {
		rounds = make(map[int32]*partState)
		d.state[height] = rounds
	}
	if !admit(rounds, round, MaxPeerStateRoundsPerHeight, keepHighest) {
		return nil
	}
	if rounds[round] == nil {
		rounds[round] = newpartState(size, height, round)
	}
	return rounds[round]
}

// keepLowest and keepHighest select which keys a full map keeps in admit.
const (
	keepLowest  = false
	keepHighest = true
)

// admit reports whether key may be stored in m, which holds at most limit
// keys. A full map keeps its lowest keys, or its highest keys if highest is
// set: admit evicts the key at the other end to make room for the new one,
// and refuses a key that would itself be that key.
func admit[K int64 | int32, V any](m map[K]V, key K, limit int, highest bool) bool {
	if _, ok := m[key]; ok || len(m) < limit {
		return true
	}
	evict := key
	for k := range m {
		if highest && k < evict || !highest && k > evict {
			evict = k
		}
	}
	if evict == key {
		return false
	}
	delete(m, evict)
	return true
}

func (d *PeerState) IncreaseConcurrentReqs(add int64) {
	d.concurrentReqs.Add(add)
}

func (d *PeerState) SetConcurrentReqs(count int64) {
	d.concurrentReqs.Store(count)
}

func (d *PeerState) DecreaseRemainingRequests(height int64, round int32, sub int) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.remainingRequests[height] == nil {
		return
	}
	remainingRequests := d.remainingRequests[height][round]
	if remainingRequests == 0 {
		return
	}
	if remainingRequests < sub {
		d.remainingRequests[height][round] = 0
		return
	}
	d.remainingRequests[height][round] -= sub
}

// SetRemainingRequests sets the request counter for a given height and round.
// It reports false, storing nothing, if the caps on tracked heights and rounds
// refuse it.
func (d *PeerState) SetRemainingRequests(height int64, round int32, count int) bool {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if !admit(d.remainingRequests, height, MaxPeerStateHeights, keepLowest) {
		return false
	}
	rounds := d.remainingRequests[height]
	if rounds == nil {
		rounds = make(map[int32]int)
		d.remainingRequests[height] = rounds
	}
	if !admit(rounds, round, MaxPeerStateRoundsPerHeight, keepHighest) {
		return false
	}
	rounds[round] = count
	return true
}

func (d *PeerState) GetRemainingRequests(height int64, round int32) int {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.remainingRequests[height] == nil {
		return 0
	}
	return d.remainingRequests[height][round]
}

func (d *PeerState) DecreaseConcurrentReqs(sub int64) {
	concurrentReqs := d.concurrentReqs.Load()
	if concurrentReqs == 0 {
		return
	}
	if concurrentReqs < sub {
		d.concurrentReqs.Store(0)
		return
	}
	d.concurrentReqs.Store(d.concurrentReqs.Load() - sub)
}

// AddHaves sets the haves for a given height and round.
func (d *PeerState) AddHaves(height int64, round int32, haves *bits.BitArray) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if ps := d.initialize(height, round, haves.Size()); ps != nil {
		ps.addHaves(haves)
	}
}

// AddWants sets the wants for a given height and round.
func (d *PeerState) AddWants(height int64, round int32, wants *bits.BitArray) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if ps := d.initialize(height, round, wants.Size()); ps != nil {
		ps.addWants(wants)
	}
}

// AddRequests sets the requests for a given height and round.
func (d *PeerState) AddRequests(height int64, round int32, requests *bits.BitArray) {
	if requests == nil || requests.Size() == 0 {
		d.logger.Error("peer state requests is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if ps := d.initialize(height, round, requests.Size()); ps != nil {
		ps.addRequests(requests)
	}
}

// RequestParts records the parts as requested from the peer and sends the
// want. Recording first means a fast reply is never treated as unrequested; a
// failed send rolls back only the parts this call added. It sends nothing and
// returns false if the caps on tracked heights and rounds refuse the state.
func (d *PeerState) RequestParts(height int64, round int32, partsToRequest *bits.BitArray, wantMessage p2p.Envelope) bool {
	if partsToRequest == nil || partsToRequest.Size() == 0 {
		d.logger.Error("peer state requests is nil or empty")
		return false
	}

	d.mtx.Lock()
	defer d.mtx.Unlock()

	// a part that arrives without a recorded request is dropped, so a want
	// the caps refuse to record is not sent.
	partState := d.initialize(height, round, partsToRequest.Size())
	if partState == nil {
		return false
	}

	// remember what was already marked, so a failed send can undo only its
	// own marks
	addedToRequests := partsToRequest.Sub(partState.requests)
	addedToEverRequested := partsToRequest.Sub(partState.everRequested)
	previousRequestedAt := partState.requestedAt

	// mark the parts as requested before sending, so a fast reply isn't
	// treated as unrequested
	partState.addRequests(partsToRequest)

	sent := d.peer.TrySend(wantMessage)
	if sent {
		return true
	}

	// the peer never got the request, so undo this call's marks. Earlier
	// marks stay: those requests did go out.
	for _, partIndex := range addedToRequests.GetTrueIndices() {
		partState.requests.SetIndex(partIndex, false)
	}
	for _, partIndex := range addedToEverRequested.GetTrueIndices() {
		partState.everRequested.SetIndex(partIndex, false)
	}
	partState.requestedAt = previousRequestedAt
	return false
}

// SetHave sets the have bit for a given part.
// Returns an error if the state is not initialized.
func (d *PeerState) SetHave(height int64, round int32, part int) error {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	if d.state == nil {
		return errors.New("peer state: nil state")
	}
	if d.state[height] == nil {
		return errors.New("peer state: height not found")
	}
	if d.state[height][round] == nil {
		return errors.New("peer state: round not found")
	}
	d.state[height][round].setHave(part, true)
	return nil
}

// SetWant sets the want bit for a given part.
// Returns an error if the state is not initialized.
func (d *PeerState) SetWant(height int64, round int32, part int, wants bool) error {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	if d.state == nil {
		return errors.New("peer state: nil state")
	}
	if d.state[height] == nil {
		return errors.New("peer state: height not found")
	}
	if d.state[height][round] == nil {
		return errors.New("peer state: round not found")
	}
	d.state[height][round].setWant(part, wants)
	return nil
}

// GetHaves retrieves the haves for a given height and round.
func (d *PeerState) GetHaves(height int64, round int32) (empty *bits.BitArray, has bool) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// create the maps if they don't exist
	hdata, has := d.state[height]
	if !has {
		return empty, false
	}
	rdata, has := hdata[round]
	if !has {
		return empty, false
	}
	return rdata.haves, true
}

// GetWants retrieves the wants for a given height and round.
func (d *PeerState) GetWants(height int64, round int32) (empty *bits.BitArray, has bool) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	// create the maps if they don't exist
	hdata, has := d.state[height]
	if !has {
		return empty, false
	}
	rdata, has := hdata[round]
	if !has {
		return empty, false
	}
	return rdata.wants, true
}

// GetRequests retrieves the requests for a given height and round.
func (d *PeerState) GetRequests(height int64, round int32) (empty *bits.BitArray, has bool) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	// create the maps if they don't exist
	hdata, has := d.state[height]
	if !has {
		return empty, false
	}
	rdata, has := hdata[round]
	if !has {
		return empty, false
	}
	return rdata.requests, true
}

// WantsPart checks if the peer wants a given part.
func (d *PeerState) WantsPart(height int64, round int32, part uint32) bool {
	w, has := d.GetWants(height, round)
	if !has {
		return false
	}
	return w.GetIndex(int(part))
}

// DeleteHeight removes all haves and wants for a given height.
func (d *PeerState) DeleteHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	delete(d.state, height)
}

// DeleteRound removes all part state for a given height and round.
func (d *PeerState) DeleteRound(height int64, round int32) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.state[height] != nil {
		delete(d.state[height], round)
	}
	if d.remainingRequests[height] != nil {
		delete(d.remainingRequests[height], round)
	}
}

func (d *PeerState) RequestsReady() {
	select {
	case d.canRequest <- struct{}{}:
	default:
	}
}

func (d *PeerState) CanRequest() chan struct{} {
	return d.canRequest
}

// prune removes all haves and wants for heights less than the given height,
// while keeping the last keepRecentRounds for the current height.
func (d *PeerState) prune(prunePastHeight int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	for height := range d.state {
		if height < prunePastHeight {
			delete(d.state, height)
		}
	}
	// A want that needs no new wants recorded sets a request counter without a
	// part state entry, so prune the counters on their own.
	for height := range d.remainingRequests {
		if height < prunePastHeight {
			delete(d.remainingRequests, height)
		}
	}
	// Prune unverified proposals for heights <= prunePastHeight
	for height := range d.unverifiedProposals {
		if height <= prunePastHeight {
			delete(d.unverifiedProposals, height)
		}
	}
	// todo: prune rounds separately from heights. Until then the caps in
	// initialize and SetRemainingRequests bound the rounds kept per height.
}

// StoreUnverifiedProposal caches a compact block for a future height.
// Returns true if stored, false if rejected (cache full with lower heights).
func (d *PeerState) StoreUnverifiedProposal(cb *proptypes.CompactBlock) bool {
	d.mtx.Lock()
	defer d.mtx.Unlock()

	height := cb.Proposal.Height

	// If we already have a proposal for this height, replace it (last write wins)
	if _, exists := d.unverifiedProposals[height]; exists {
		d.unverifiedProposals[height] = cb
		return true
	}

	// If cache has room, store directly
	if len(d.unverifiedProposals) < MaxUnverifiedProposals {
		d.unverifiedProposals[height] = cb
		return true
	}

	// Cache is full - find the highest height
	var maxHeight int64 = -1
	for h := range d.unverifiedProposals {
		if h > maxHeight {
			maxHeight = h
		}
	}

	// Only store if this height is lower than the highest cached
	// (we prioritize catching up on nearest heights first)
	if height < maxHeight {
		delete(d.unverifiedProposals, maxHeight)
		d.unverifiedProposals[height] = cb
		return true
	}

	// Reject - cache is full with lower heights
	return false
}

// GetUnverifiedProposal returns cached compact block for height, or nil.
func (d *PeerState) GetUnverifiedProposal(height int64) *proptypes.CompactBlock {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	return d.unverifiedProposals[height]
}

// DeleteUnverifiedProposal removes a cached compact block for a specific height.
func (d *PeerState) DeleteUnverifiedProposal(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	delete(d.unverifiedProposals, height)
}

// GrantPushAllowance lets the peer that sent cb push the parts no mempool can
// rebuild, unrequested. Only the first grant counts, so resending the compact
// block does not refill it.
func (d *PeerState) GrantPushAllowance(cb *proptypes.CompactBlock) {
	height, round := cb.Proposal.Height, cb.Proposal.Round
	totalParts := cb.Proposal.BlockID.PartSetHeader.Total

	d.mtx.Lock()
	defer d.mtx.Unlock()

	ps := d.initialize(height, round, int(totalParts)*2)
	if ps == nil {
		return
	}
	if !ps.pushGranted {
		ps.pushGranted = true
		ps.pushAllowance = uncoveredParts(cb.Blobs, totalParts, types.BlockPartSizeBytes, cb.LastLen)
	}
}

// ConsumePushAllowance reports whether the peer may push the part with this
// index unrequested. Each part can be pushed once.
func (d *PeerState) ConsumePushAllowance(height int64, round int32, index uint32) bool {
	d.mtx.Lock()
	defer d.mtx.Unlock()

	ps := d.state[height][round]
	if ps == nil {
		return false
	}
	for i, allowed := range ps.pushAllowance {
		if allowed == index {
			ps.pushAllowance = append(ps.pushAllowance[:i], ps.pushAllowance[i+1:]...)
			return true
		}
	}
	return false
}

// Requested reports whether this node has ever requested the part from the
// peer, including requests that have since expired.
func (d *PeerState) Requested(height int64, round int32, index uint32) bool {
	d.mtx.RLock()
	defer d.mtx.RUnlock()

	partState := d.state[height][round]
	if partState == nil {
		return false
	}
	return partState.everRequested.GetIndex(int(index))
}

type partState struct {
	haves    *bits.BitArray
	wants    *bits.BitArray
	requests *bits.BitArray
	// requestedAt is when requests were last added.
	requestedAt time.Time
	// everRequested is every part we have asked this peer for. Unlike
	// requests it is never cleared, so a reply that arrives after its request
	// expired is still recognized as requested.
	everRequested *bits.BitArray
	// pushAllowance is the parts the peer may still send unrequested. It is
	// granted once, when the peer sends the compact block.
	pushAllowance []uint32
	pushGranted   bool
}

// newpartState initializes and returns a new partState
func newpartState(size int, _ int64, _ int32) *partState {
	return &partState{
		haves:    bits.NewBitArray(size),
		wants:    bits.NewBitArray(size),
		requests: bits.NewBitArray(size),

		everRequested: bits.NewBitArray(size),
	}
}

func (p *partState) addHaves(haves *bits.BitArray) {
	p.haves.AddBitArray(haves)
}

func (p *partState) addWants(wants *bits.BitArray) {
	p.wants.AddBitArray(wants)
}

func (p *partState) addRequests(requests *bits.BitArray) {
	p.requests.AddBitArray(requests)
	p.requestedAt = time.Now()
	p.everRequested.AddBitArray(requests)
}

// SetHave sets the have bit for a given part.
// TODO support setting the hash and the proof
func (p *partState) setHave(index int, has bool) {
	p.haves.SetIndex(index, has)
}

// SetWant sets the want bit for a given part.
func (p *partState) setWant(part int, wants bool) {
	p.wants.SetIndex(part, wants)
}

// ExpireRequests clears the requests for a given height and round if none
// were added within timeout.
func (d *PeerState) ExpireRequests(height int64, round int32, timeout time.Duration) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	hdata, has := d.state[height]
	if !has {
		return
	}
	rdata, has := hdata[round]
	if !has || rdata.requests.IsEmpty() {
		return
	}
	if time.Since(rdata.requestedAt) >= timeout {
		rdata.requests = bits.NewBitArray(rdata.requests.Size())
	}
}
