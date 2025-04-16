package propagation

import (
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/p2p"
)

// PeerState keeps track of haves and wants for each peer. This is used for
// block prop and catchup.
type PeerState struct {
	peer p2p.Peer

	mtx *sync.RWMutex
	// state organized the haves and wants for each data is indexed by height
	// and round.
	state map[int64]map[int32]*partState

	// keep track of the peer's latest height and round
	latestHeight int64
	latestRound  int32

	logger log.Logger
}

// newPeerState initializes and returns a new PeerState. This should be
// called for each peer.
func newPeerState(peer p2p.Peer, logger log.Logger) *PeerState {
	return &PeerState{
		mtx:    &sync.RWMutex{},
		state:  make(map[int64]map[int32]*partState),
		peer:   peer,
		logger: logger,
	}
}

// Initialize initializes the state for a given height and round in a
// thread-safe way.
func (d *PeerState) Initialize(height int64, round int32, size int) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, size)
}

// initialize initializes the state for a given height and round. This method is
// not thread-safe.
func (d *PeerState) initialize(height int64, round int32, size int) {
	// Initialize the inner map if it doesn't exist
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		d.state[height][round] = newPartState(size, height, round)
	}
}

// AddSentHaves sets the sent haves for a given height and round.
func (d *PeerState) AddSentHaves(height int64, round int32, haves *bits.BitArray) {
	if haves == nil || haves.Size() == 0 {
		d.logger.Error("peer state sent haves is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, haves.Size())
	d.state[height][round].addSentHaves(haves)
}

// AddReceivedHaves sets the received haves for a given height and round.
func (d *PeerState) AddReceivedHaves(height int64, round int32, haves *bits.BitArray) {
	if haves == nil || haves.Size() == 0 {
		d.logger.Error("peer state received haves is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, haves.Size())
	d.state[height][round].addReceivedHaves(haves)
}

// SetLatestHeight sets the latest height
func (d *PeerState) SetLatestHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.latestHeight = height
}

// GetLatestHeight gets the latest height
func (d *PeerState) GetLatestHeight() int64 {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	return d.latestHeight
}

// SetLatestRound sets the latest height
func (d *PeerState) SetLatestRound(round int32) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.latestRound = round
}

// GetLatestRound gets the latest round
func (d *PeerState) GetLatestRound() int32 {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	return d.latestRound
}

// AddSentWants sets the sent wants for a given height and round.
func (d *PeerState) AddSentWants(height int64, round int32, wants *bits.BitArray) {
	if wants == nil || wants.Size() == 0 {
		d.logger.Error("peer state sent wants is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, wants.Size())
	d.state[height][round].addSentWants(wants)
}

// AddReceivedWants sets the received wants for a given height and round.
func (d *PeerState) AddReceivedWants(height int64, round int32, wants *bits.BitArray) {
	if wants == nil || wants.Size() == 0 {
		d.logger.Error("peer state received wants is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, wants.Size())
	d.state[height][round].addReceivedWants(wants)
}

// SetSentHave sets the sent have bit for a given part. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) SetSentHave(height int64, round int32, part int) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].setSentHave(part, true)
}

// SetReceivedHave sets the received have bit for a given part. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) SetReceivedHave(height int64, round int32, part int) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].setReceivedHave(part, true)
}

// AddReceivedHave adds the received have bits to the existing received haves. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) AddReceivedHave(height int64, round int32, haves *bits.BitArray) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].addReceivedHaves(haves)
}

// SetSentWant sets the sent want bit for a given part. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) SetSentWant(height int64, round int32, part int) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].setSentWant(part, true)
}

// SetReceivedWant sets the received want bit for a given part. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) SetReceivedWant(height int64, round int32, part int, wants bool) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].setReceivedWant(part, wants)
}

// GetSentHaves retrieves the sent haves for a given height and round.
func (d *PeerState) GetSentHaves(height int64, round int32) (empty *bits.BitArray, has bool) {
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
	return rdata.sentHaves, true
}

// WantsPart checks if the peer wants a given part.
func (d *PeerState) WantsPart(height int64, round int32, part uint32) bool {
	w, has := d.GetReceivedWants(height, round)
	if !has {
		return false
	}
	return w.GetIndex(int(part))
}

// GetReceivedHaves retrieves the sent haves for a given height and round.
func (d *PeerState) GetReceivedHaves(height int64, round int32) (empty *bits.BitArray, has bool) {
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
	return rdata.receivedHaves, true
}

// GetSentWants retrieves the sent wants for a given height and round.
func (d *PeerState) GetSentWants(height int64, round int32) (empty *bits.BitArray, has bool) {
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
	return rdata.sentWants, true
}

// GetReceivedWants retrieves the received wants for a given height and round.
func (d *PeerState) GetReceivedWants(height int64, round int32) (empty *bits.BitArray, has bool) {
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
	return rdata.receivedWants, true
}

// DeleteHeight removes all haves and wants for a given height.
func (d *PeerState) DeleteHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	delete(d.state, height)
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
	// todo: prune rounds separately from heights
}

type partState struct {
	// sentHaves the haves sent to a peer
	sentHaves *bits.BitArray
	// receivedHaves the haves received from a peer
	receivedHaves *bits.BitArray
	// requests the parts requested from a peer,
	// i.e., wants sent to a peer.
	sentWants *bits.BitArray
	// wants the wants received from a peer
	receivedWants *bits.BitArray
}

// newPartState initializes and returns a new partState
func newPartState(size int, _ int64, _ int32) *partState {
	return &partState{
		sentHaves:     bits.NewBitArray(size),
		receivedHaves: bits.NewBitArray(size),
		receivedWants: bits.NewBitArray(size),
		sentWants:     bits.NewBitArray(size),
	}
}

func (p *partState) addSentHaves(haves *bits.BitArray) {
	p.sentHaves.AddBitArray(haves)
}

func (p *partState) addReceivedHaves(haves *bits.BitArray) {
	p.receivedHaves.AddBitArray(haves)
}

func (p *partState) addSentWants(wants *bits.BitArray) {
	p.sentWants.AddBitArray(wants)
}

func (p *partState) addReceivedWants(wants *bits.BitArray) {
	p.receivedWants.AddBitArray(wants)
}

// setSentHave sets the sent have bit for a given part.
func (p *partState) setSentHave(index int, has bool) {
	p.sentHaves.SetIndex(index, has)
}

// setReceivedHave sets the received have bit for a given part.
func (p *partState) setReceivedHave(index int, has bool) {
	p.receivedHaves.SetIndex(index, has)
}

// SetSentWant sets the sent want bit for a given part.
func (p *partState) setSentWant(part int, wants bool) {
	p.sentWants.SetIndex(part, wants)
}

// setReceivedWant sets the received want bit for a given part.
func (p *partState) setReceivedWant(part int, wants bool) {
	p.receivedWants.SetIndex(part, wants)
}
