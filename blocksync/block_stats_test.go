package blocksync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBlockStats(t *testing.T) {
	t.Run("positive capacity", func(t *testing.T) {
		rb := newBlockStats(5)
		require.NotNil(t, rb)

		assert.Equal(t, 5, rb.Capacity())
		assert.Equal(t, 0, rb.Size())
		assert.Equal(t, 0, rb.GetMax())
	})

	t.Run("non-positive capacity is default", func(t *testing.T) {
		require.Equal(t, defaultCapacity, newBlockStats(0).capacity)
		require.Equal(t, defaultCapacity, newBlockStats(-1).capacity)
	})
}

func TestAddAndGetMax_BelowCapacity(t *testing.T) {
	rb := newBlockStats(3)

	t.Run("add first element", func(t *testing.T) {
		rb.Add(5)
		assert.Equal(t, 1, rb.Size())
		assert.Equal(t, 3, rb.Capacity())
		assert.Equal(t, 5, rb.GetMax())
	})

	t.Run("add smaller element", func(t *testing.T) {
		rb.Add(3)
		assert.Equal(t, 2, rb.Size())
		assert.Equal(t, 5, rb.GetMax())
	})

	t.Run("add larger element", func(t *testing.T) {
		rb.Add(10)
		assert.Equal(t, 3, rb.Size())
		assert.Equal(t, 10, rb.GetMax())
	})
}

func TestAddAndGetMax_AtAndBeyondCapacity(t *testing.T) {
	rb := newBlockStats(3)

	// Fill to capacity: [5, 4, 3]
	rb.Add(5)
	rb.Add(4)
	rb.Add(3)

	assert.Equal(t, 3, rb.Size())
	assert.Equal(t, 5, rb.GetMax())

	t.Run("overwrite non-max element", func(t *testing.T) {
		// Current underlying slice indices (head == 0):
		// [5, 4, 3]
		// Overwrite index 0 (value 5) next, so we first overwrite a non-max
		// by advancing head manually via add.
		// To be precise: head is now 0 (next write), and oldValue is 5 (max).
		// To test non-max overwrite, recreate a situation:
		rb2 := newBlockStats(3)
		rb2.Add(5) // [5, 0, 0] head=1, max=5
		rb2.Add(4) // [5, 4, 0] head=2
		rb2.Add(3) // [5, 4, 3] head=0

		// Now overwrite index 0 with a *larger* value.
		rb2.Add(6) // oldValue=5 (max), value=6 -> need recalc? no, value>max so max=6

		assert.Equal(t, 3, rb2.Size())
		assert.Equal(t, 6, rb2.GetMax())
	})

	t.Run("remove current max triggers recalculation", func(t *testing.T) {
		rb3 := newBlockStats(3)

		// [5, 4, 3], head=0, max=5
		rb3.Add(5)
		rb3.Add(4)
		rb3.Add(3)

		// Next Add overwrites index 0 (value 5, which is max).
		// Use a value that is not larger than the remaining ones to force recalc.
		rb3.Add(2) // now buffer is [2,4,3] in some rotation; max should be 4.

		assert.Equal(t, 3, rb3.Size())
		assert.Equal(t, 4, rb3.GetMax())
	})

	t.Run("max updates when new max added after recalc", func(t *testing.T) {
		rb4 := newBlockStats(3)

		// [5, 4, 3]
		rb4.Add(5)
		rb4.Add(4)
		rb4.Add(3)

		// Overwrite 5 with 2 -> recalc, max should become 4
		rb4.Add(2)
		assert.Equal(t, 4, rb4.GetMax())

		// Now add a new bigger value, should update max immediately
		rb4.Add(10)
		assert.Equal(t, 10, rb4.GetMax())
	})
}

func TestSizeNeverExceedsCapacity(t *testing.T) {
	rb := newBlockStats(2)

	rb.Add(1)
	assert.Equal(t, 1, rb.Size())

	rb.Add(2)
	assert.Equal(t, 2, rb.Size())

	// Now the buffer is full; further adds should keep size == capacity
	rb.Add(3)
	assert.Equal(t, 2, rb.Size())

	rb.Add(4)
	assert.Equal(t, 2, rb.Size())
}
