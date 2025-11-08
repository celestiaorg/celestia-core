package blocksync

import (
	"math"
	"testing"
)

func TestNewRotatingBuffer(t *testing.T) {
	rb := NewRotatingBuffer(5)
	if rb.Capacity() != 5 {
		t.Errorf("Expected capacity 5, got %d", rb.Capacity())
	}
	if rb.Size() != 0 {
		t.Errorf("Expected size 0, got %d", rb.Size())
	}
}

func TestAddAndGetAverage(t *testing.T) {
	rb := NewRotatingBuffer(3)

	// Add first element
	rb.Add(10)
	if rb.GetAverage() != 10 {
		t.Errorf("Expected average 10, got %f", rb.GetAverage())
	}

	// Add second element
	rb.Add(20)
	if rb.GetAverage() != 15 {
		t.Errorf("Expected average 15, got %f", rb.GetAverage())
	}

	// Add third element (buffer full)
	rb.Add(30)
	if rb.GetAverage() != 20 {
		t.Errorf("Expected average 20, got %f", rb.GetAverage())
	}
}

func TestRotatingBehavior(t *testing.T) {
	rb := NewRotatingBuffer(3)

	// Fill the buffer
	rb.Add(10)
	rb.Add(20)
	rb.Add(30)

	// Add fourth element (should remove 10)
	rb.Add(40)
	expected := (20.0 + 30.0 + 40.0) / 3.0
	if math.Abs(rb.GetAverage()-expected) > 0.0001 {
		t.Errorf("Expected average %f, got %f", expected, rb.GetAverage())
	}

	// Verify size stays at capacity
	if rb.Size() != 3 {
		t.Errorf("Expected size 3, got %d", rb.Size())
	}
}

func TestGetAll(t *testing.T) {
	rb := NewRotatingBuffer(3)

	rb.Add(10)
	rb.Add(20)
	rb.Add(30)

	all := rb.GetAll()
	if len(all) != 3 {
		t.Errorf("Expected 3 elements, got %d", len(all))
	}

	// Add one more to trigger rotation
	rb.Add(40)
	all = rb.GetAll()

	// Should have [20, 30, 40]
	expected := []float64{20, 30, 40}
	for i, v := range expected {
		if all[i] != v {
			t.Errorf("Expected element %d to be %f, got %f", i, v, all[i])
		}
	}
}

func TestEmptyBuffer(t *testing.T) {
	rb := NewRotatingBuffer(5)
	if rb.GetAverage() != 0 {
		t.Errorf("Expected average 0 for empty buffer, got %f", rb.GetAverage())
	}
}

func TestSingleElement(t *testing.T) {
	rb := NewRotatingBuffer(1)
	rb.Add(42)

	if rb.GetAverage() != 42 {
		t.Errorf("Expected average 42, got %f", rb.GetAverage())
	}

	// Add another element (should replace the first)
	rb.Add(100)
	if rb.GetAverage() != 100 {
		t.Errorf("Expected average 100, got %f", rb.GetAverage())
	}
}

func TestFloatingPointPrecision(t *testing.T) {
	rb := NewRotatingBuffer(4)
	rb.Add(1.1)
	rb.Add(2.2)
	rb.Add(3.3)
	rb.Add(4.4)

	expected := (1.1 + 2.2 + 3.3 + 4.4) / 4.0
	if math.Abs(rb.GetAverage()-expected) > 0.0001 {
		t.Errorf("Expected average %f, got %f", expected, rb.GetAverage())
	}
}
