package consensus

import (
	"sync"

	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/p2p"
)

// dataPeerState keeps track of haves and wants for each peers. This is used for
// block prop and catchup.
type dataPeerState struct {
	peer p2p.Peer

	mtx *sync.RWMutex
	// state organized the haves and wants for each data is indexed by height
	// and round.
	state map[int64]map[int32]*partState
}

// newDataPeerState initializes and returns a new dataPeerState. This should be
// called for each peer.
func newDataPeerState(peer p2p.Peer) *dataPeerState {
	return &dataPeerState{
		mtx:   &sync.RWMutex{},
		state: make(map[int64]map[int32]*partState),
		peer:  peer,
	}
}

// SetHaves sets the haves for a given height and round.
func (d *dataPeerState) SetHaves(height int64, round int32, haves *bits.BitArray) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		d.state[height][round] = newpartState(haves.Size())
	}
	d.state[height][round].setHaves(haves)
}

// SetWants sets the wants for a given height and round.
func (d *dataPeerState) SetWants(height int64, round int32, wants *bits.BitArray) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		d.state[height][round] = newpartState(wants.Size())
	}
	d.state[height][round].setWants(wants)
}

// GetHaves retrieves the haves for a given height and round.
func (d *dataPeerState) GetHaves(height int64, round int32) (empty *bits.BitArray, has bool) {
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
	return rdata.haves, true
}

// GetWants retrieves the wants for a given height and round.
func (d *dataPeerState) GetWants(height int64, round int32) (empty *bits.BitArray, has bool) {
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

// WantsPart checks if the peer wants a given part.
func (d *dataPeerState) WantsPart(height int64, round int32, part uint32) bool {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	hdata, has := d.state[height]
	if !has {
		return false
	}
	rdata, has := hdata[round]
	if !has {
		return false
	}
	return rdata.getWant(int(part))
}

// DeleteHeight removes all haves and wants for a given height.
func (d *dataPeerState) DeleteHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	delete(d.state, height)
}

type partState struct {
	haves *bits.BitArray
	wants *bits.BitArray
}

// newpartState initializes and returns a new partState
func newpartState(size int) *partState {
	return &partState{
		haves: bits.NewBitArray(size),
		wants: bits.NewBitArray(size),
	}
}

func (p *partState) setHaves(haves *bits.BitArray) {
	p.haves.AddBitArray(haves)
}

func (p *partState) setWants(haves *bits.BitArray) {
	p.wants.AddBitArray(haves)
}

// SetHave sets the have bit for a given part.
func (p *partState) setHave(part int) {
	p.haves.SetIndex(part, true)
}

// SetWant sets the want bit for a given part.
func (p *partState) setWant(part int) {
	p.wants.SetIndex(part, true)
}

func (p *partState) getWant(part int) bool {
	return p.wants.GetIndex(part)
}

type requestTracker struct {
	mtx  *sync.RWMutex
	reqs map[int64]map[int32]*bits.BitArray
}

// newRequestTracker initializes and returns a new requestTracker
func newRequestTracker() *requestTracker {
	return &requestTracker{
		mtx:  &sync.RWMutex{},
		reqs: make(map[int64]map[int32]*bits.BitArray),
	}
}

// Add adds a request for a given height and round.
func (r *requestTracker) Add(height int64, round int32, req *bits.BitArray) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if r.reqs[height] == nil {
		r.reqs[height] = make(map[int32]*bits.BitArray)
	}
	if r.reqs[height][round] == nil {
		r.reqs[height][round] = bits.NewBitArray(req.Size())
	}
	r.reqs[height][round].AddBitArray(req)
}
