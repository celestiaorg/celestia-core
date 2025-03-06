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
		d.state[height][round] = newpartState(size, height, round)
	}
}

// AddHaves sets the haves for a given height and round.
func (d *PeerState) AddHaves(height int64, round int32, haves *bits.BitArray) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, haves.Size())
	d.state[height][round].addHaves(haves)
}

// AddWants sets the wants for a given height and round.
func (d *PeerState) AddWants(height int64, round int32, wants *bits.BitArray) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, wants.Size())
	d.state[height][round].addWants(wants)
}

// AddRequests sets the requests for a given height and round.
func (d *PeerState) AddRequests(height int64, round int32, requests *bits.BitArray) {
	if requests == nil || requests.Size() == 0 {
		d.logger.Error("peer state requests is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.initialize(height, round, requests.Size())
	d.state[height][round].addRequests(requests)
}

// SetHave sets the have bit for a given part. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) SetHave(height int64, round int32, part int) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].setHave(part, true)
}

// SetWant sets the want bit for a given part. WARNING: if the state is not
// initialized for a given height and round, the function will panic.
func (d *PeerState) SetWant(height int64, round int32, part int, wants bool) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	d.state[height][round].setWant(part, wants)
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

// prune removes all haves and wants for heights less than the given height,
// while keeping the last keepRecentRounds for the current height.
func (d *PeerState) prune(currentHeight int64, keepRecentHeights, keepRecentRounds int) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	for height := range d.state {
		if height < currentHeight-int64(keepRecentHeights) {
			delete(d.state, height)
		}
		if height == currentHeight {
			continue
		}
	}
	// delete all but the last round for each remaining height except the current.
	// this is because we need to keep the last round for the current height.
	for height := range d.state {
		if height == currentHeight {
			continue
		}
		for round := range d.state[height] {
			if round < int32(currentHeight)-int32(keepRecentRounds) {
				delete(d.state[height], round)
			}
		}
	}
}

type partState struct {
	haves    *bits.BitArray
	wants    *bits.BitArray
	requests *bits.BitArray
}

// newpartState initializes and returns a new partState
func newpartState(size int, height int64, round int32) *partState {
	return &partState{
		haves:    bits.NewBitArray(size),
		wants:    bits.NewBitArray(size),
		requests: bits.NewBitArray(size),
	}
}

func (p *partState) addHaves(haves *bits.BitArray) {
	p.wants.AddBitArray(haves)
}

func (p *partState) addWants(wants *bits.BitArray) {
	p.wants.AddBitArray(wants)
}

func (p *partState) addRequests(requests *bits.BitArray) {
	p.requests.AddBitArray(requests)
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
