package consensus

import (
	"sync"
)

// heightRoundIndex is a data struct that organizes data relevant to specific
// proposals indexed by height and round.
type heightRoundIndex[T any] struct {
	mtx  *sync.RWMutex
	data map[int64]map[int32]T
}

// NewHeightRoundIndex initializes and returns a new heightRoundIndex
func NewHeightRoundIndex[T any]() *heightRoundIndex[T] {
	return &heightRoundIndex[T]{
		mtx:  &sync.RWMutex{},
		data: make(map[int64]map[int32]T),
	}
}

// Add adds a data indexed by height and round.
func (h *heightRoundIndex[T]) Add(height int64, round int32, data T) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	// Initialize the inner map if it doesn't exist
	if h.data[height] == nil {
		h.data[height] = make(map[int32]T)
	}
	h.data[height][round] = data
}

// Get retrieves data indexed by height and round.
func (h *heightRoundIndex[T]) Get(height int64, round int32) (empty T, has bool) {
	h.mtx.RLock()
	defer h.mtx.RUnlock()
	// create the maps if they don't exist
	hdata, has := h.data[height]
	if !has {
		return empty, false
	}
	rdata, has := hdata[round]
	return rdata, has
}

// DeleteHeight removes all data for a given height.
func (h *heightRoundIndex[T]) DeleteHeight(height int64) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	delete(h.data, height)
}

// DeleteRound removes all data for a given round.
func (h *heightRoundIndex[T]) DeleteRound(height int64, round int32) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	if _, exists := h.data[height]; exists {
		delete(h.data[height], round)
	}
}
