package blocksync

// RotatingBuffer is a circular buffer that holds at most n elements
// and provides O(1) average calculation and O(1) max retrieval
type RotatingBuffer struct {
	buffer   []float64
	capacity int
	size     int     // current number of elements
	head     int     // index where the next element will be written
	sum      float64 // running sum of all elements
	max      float64 // maximum value in the buffer
}

// NewRotatingBuffer creates a new rotating buffer with given capacity
func NewRotatingBuffer(capacity int) *RotatingBuffer {
	if capacity <= 0 {
		panic("capacity must be positive")
	}
	return &RotatingBuffer{
		buffer:   make([]float64, capacity),
		capacity: capacity,
		size:     0,
		head:     0,
		sum:      0,
	}
}

// Add adds a new element to the buffer
// If the buffer is full, it removes the oldest element
func (rb *RotatingBuffer) Add(value float64) {
	needRecalculateMax := false

	if rb.size < rb.capacity {
		rb.buffer[rb.head] = value
		rb.sum += value
		rb.size++

		if rb.size == 1 || value > rb.max {
			rb.max = value
		}
	} else {
		// Buffer is full, replace the oldest element
		oldValue := rb.buffer[rb.head]
		rb.buffer[rb.head] = value
		rb.sum = rb.sum - oldValue + value

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

// recalculateMax recalculates the maximum value by scanning the buffer
// This is only called when the previous max value is removed
func (rb *RotatingBuffer) recalculateMax() {
	if rb.size == 0 {
		rb.max = 0
		return
	}

	rb.max = rb.buffer[0]
	for i := 1; i < rb.size; i++ {
		if rb.buffer[i] > rb.max {
			rb.max = rb.buffer[i]
		}
	}
}

// GetAverage returns the average of all elements in the buffer
func (rb *RotatingBuffer) GetAverage() float64 {
	if rb.size == 0 {
		return 0
	}
	return rb.sum / float64(rb.size)
}

// GetMax returns the maximum value in the buffer
func (rb *RotatingBuffer) GetMax() float64 {
	return rb.max
}

// Size returns the current number of elements in the buffer
func (rb *RotatingBuffer) Size() int {
	return rb.size
}

// Capacity returns the maximum capacity of the buffer
func (rb *RotatingBuffer) Capacity() int {
	return rb.capacity
}
