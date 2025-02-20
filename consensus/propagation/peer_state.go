package propagation

import (
	"github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/crypto/merkle"
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

// SetHaves sets the haves for a given height and round.
func (d *PeerState) SetHaves(height int64, round int32, haves *types.HaveParts) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		d.state[height][round] = newpartState(len(haves.Parts), height, round)
	}
	d.state[height][round].setHaves(haves)
}

// SetWants sets the wants for a given height and round.
func (d *PeerState) SetWants(wants *types.WantParts) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if d.state[wants.Height] == nil {
		d.state[wants.Height] = make(map[int32]*partState)
	}
	if d.state[wants.Height][wants.Round] == nil {
		d.state[wants.Height][wants.Round] = newpartState(wants.Parts.Size(), wants.Height, wants.Round)
	}
	d.state[wants.Height][wants.Round].setWants(wants)
}

// SetRequests sets the requests for a given height and round.
func (d *PeerState) SetRequests(height int64, round int32, requests *bits.BitArray) {
	if requests == nil || requests.Size() == 0 {
		d.logger.Error("peer state requests is nil or empty")
		return
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		d.state[height][round] = newpartState(requests.Size(), height, round)
	}
	d.state[height][round].setRequests(requests)
}

// SetRequest sets the request bit for a given part.
func (d *PeerState) SetRequest(height int64, round int32, part int) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.state[height] == nil {
		return
	}
	if d.state[height][round] == nil {
		return
	}
	d.state[height][round].setRequest(part)
}

// SetHave sets the have bit for a given part.
func (d *PeerState) SetHave(height int64, round int32, part int) {
	// this is only a read mtx hold because each bitarrary holds the write mtx
	// so this function only needs to ensure that reading is safe.
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	// TODO refactor these initialisations to a single function
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		// d.state[height][round] = newpartState(wants.Size())
		// todo(evan): actually do something here
		return
	}
	d.state[height][round].setHave(part)
}

// SetWant sets the want bit for a given part.
func (d *PeerState) SetWant(height int64, round int32, part int, wants bool) {
	// this is only a read mtx hold because each bitarrary holds the write mtx
	// so this function only needs to ensure that reading is safe.
	d.mtx.Lock()
	defer d.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if d.state[height] == nil {
		d.state[height] = make(map[int32]*partState)
	}
	if d.state[height][round] == nil {
		// d.state[height][round] = newpartState(wants.Size())
		// todo(evan): actually do something here
		return
	}
	d.state[height][round].setWant(part, wants)
}

// GetHaves retrieves the haves for a given height and round.
func (d *PeerState) GetHaves(height int64, round int32) (empty *types.HaveParts, has bool) {
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
func (d *PeerState) GetWants(height int64, round int32) (empty *types.WantParts, has bool) {
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
	d.mtx.Lock()
	defer d.mtx.Unlock()
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
	haves    *types.HaveParts
	wants    *types.WantParts
	requests *bits.BitArray
}

// newpartState initializes and returns a new partState
func newpartState(size int, height int64, round int32) *partState {
	return &partState{
		haves: &types.HaveParts{
			Height: height,
			Round:  round,
			Parts:  make([]types.PartMetaData, size),
		},
		wants: &types.WantParts{
			Parts:  bits.NewBitArray(size),
			Height: height,
			Round:  round,
		},
		requests: bits.NewBitArray(size),
	}
}

func (p *partState) setHaves(haves *types.HaveParts) {
	p.haves = haves
	// p.wants.Sub(haves) // todo(evan): revert. we're only commenting this out atm so that we can simulate optimistically sending wants
}

func (p *partState) setWants(wants *types.WantParts) {
	p.wants = wants
}

func (p *partState) setRequests(requests *bits.BitArray) {
	// TODO delete the request state after we download the data
	p.requests.AddBitArray(requests)
}

// SetHave sets the have bit for a given part.
// TODO support setting the hash and the proof
func (p *partState) setHave(part int) {
	p.haves.SetIndex(uint32(part), nil, &merkle.Proof{})
}

// SetWant sets the want bit for a given part.
func (p *partState) setWant(part int, wants bool) {
	p.wants.Parts.SetIndex(part, wants)
}

func (p *partState) setRequest(part int) {
	p.requests.SetIndex(part, true)
}

func (p *partState) getWant(part int) bool {
	return p.wants.Parts.GetIndex(part)
}

func (p *partState) getHave(part int) bool {
	return p.haves.GetIndex(uint32(part))
}

func (p *partState) getRequest(part int) bool {
	return p.requests.GetIndex(part)
}
