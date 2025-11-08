package blocksync

// RotatingBuffer is a circular buffer that holds at most n elements
// and provides O(1) average calculation
type RotatingBuffer struct {
	buffer   []float64
	capacity int
	size     int     // current number of elements
	head     int     // index where the next element will be written
	sum      float64 // running sum of all elements
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
	if rb.size < rb.capacity {
		// Buffer is not full yet
		rb.buffer[rb.head] = value
		rb.sum += value
		rb.size++
	} else {
		// Buffer is full, replace the oldest element
		oldValue := rb.buffer[rb.head]
		rb.buffer[rb.head] = value
		rb.sum = rb.sum - oldValue + value
	}

	// Move head to next position (circular)
	rb.head = (rb.head + 1) % rb.capacity
}

// GetAverage returns the average of all elements in the buffer
// Time complexity: O(1)
func (rb *RotatingBuffer) GetAverage() float64 {
	if rb.size == 0 {
		return 0
	}
	return rb.sum / float64(rb.size)
}

// Size returns the current number of elements in the buffer
func (rb *RotatingBuffer) Size() int {
	return rb.size
}

// Capacity returns the maximum capacity of the buffer
func (rb *RotatingBuffer) Capacity() int {
	return rb.capacity
}

// GetAll returns all current elements in the buffer (in insertion order)
func (rb *RotatingBuffer) GetAll() []float64 {
	if rb.size == 0 {
		return []float64{}
	}

	result := make([]float64, rb.size)
	if rb.size < rb.capacity {
		// Buffer not full yet, elements are from 0 to size-1
		copy(result, rb.buffer[:rb.size])
	} else {
		// Buffer is full, need to handle circular nature
		// Oldest element is at head position
		for i := 0; i < rb.size; i++ {
			result[i] = rb.buffer[(rb.head+i)%rb.capacity]
		}
	}
	return result
}
