package blocksync

import "slices"

// blockStats is a circular blockSizes that holds at most n elements
// and provides O(1) average calculation and O(1) max retrieval
type blockStats struct {
	blockSizes []int
	capacity   int
	size       int // current number of elements
	head       int // index where the next element will be written
	max        int // maximum value in the blockSizes
}

const defaultCapacity = 10

// newBlockStats creates a new rotating blockSizes with given capacity
func newBlockStats(capacity int) *blockStats {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &blockStats{
		blockSizes: make([]int, capacity),
		capacity:   capacity,
		size:       0,
		head:       0,
	}
}

// Add adds a new element to the blockSizes
// If the blockSizes is full, it removes the oldest element
func (rb *blockStats) Add(value int) {
	needRecalculateMax := false

	if rb.size < rb.capacity {
		rb.blockSizes[rb.head] = value
		rb.size++

		if rb.size == 1 || value > rb.max {
			rb.max = value
		}
	} else {
		// Buffer is full, replace the oldest element
		oldValue := rb.blockSizes[rb.head]
		rb.blockSizes[rb.head] = value

		if oldValue == rb.max {
			needRecalculateMax = true
		} else if value > rb.max {
			rb.max = value
		}
	}

	rb.head = (rb.head + 1) % rb.capacity

	if needRecalculateMax {
		rb.recalculateMax()
	}
}

// recalculateMax recalculates the maximum value by scanning the blockSizes
// This is only called when the previous max value is removed
func (rb *blockStats) recalculateMax() {
	if rb.size == 0 {
		rb.max = 0
		return
	}

	rb.max = slices.Max(rb.blockSizes)
}

// GetMax returns the maximum value in the blockSizes
func (rb *blockStats) GetMax() int {
	return rb.max
}

// Size returns the current number of elements in the blockSizes
func (rb *blockStats) Size() int {
	return rb.size
}

// Capacity returns the maximum capacity of the blockSizes
func (rb *blockStats) Capacity() int {
	return rb.capacity
}
