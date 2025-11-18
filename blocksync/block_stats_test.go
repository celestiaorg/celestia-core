package blocksync

import (
	"math"
	"testing"
)

func TestNewRotatingBuffer(t *testing.T) {
	rb := NewBlockStats(5)
	if rb.Capacity() != 5 {
		t.Errorf("Expected capacity 5, got %d", rb.Capacity())
	}
	if rb.Size() != 0 {
		t.Errorf("Expected size 0, got %d", rb.Size())
	}
}

func TestAddAndGetAverage(t *testing.T) {
	rb := NewBlockStats(3)

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
	rb := NewBlockStats(3)

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

func TestEmptyBuffer(t *testing.T) {
	rb := NewBlockStats(5)
	if rb.GetAverage() != 0 {
		t.Errorf("Expected average 0 for empty buffer, got %f", rb.GetAverage())
	}
}

func TestSingleElement(t *testing.T) {
	rb := NewBlockStats(1)
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
	rb := NewBlockStats(4)
	rb.Add(1.1)
	rb.Add(2.2)
	rb.Add(3.3)
	rb.Add(4.4)

	expected := (1.1 + 2.2 + 3.3 + 4.4) / 4.0
	if math.Abs(rb.GetAverage()-expected) > 0.0001 {
		t.Errorf("Expected average %f, got %f", expected, rb.GetAverage())
	}
}

func TestGetMax(t *testing.T) {
	rb := NewBlockStats(5)

	// Empty buffer
	if rb.GetMax() != 0 {
		t.Errorf("Expected max 0 for empty buffer, got %f", rb.GetMax())
	}

	// Add first element
	rb.Add(10)
	if rb.GetMax() != 10 {
		t.Errorf("Expected max 10, got %f", rb.GetMax())
	}

	// Add larger element
	rb.Add(30)
	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30, got %f", rb.GetMax())
	}

	// Add smaller element
	rb.Add(20)
	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30, got %f", rb.GetMax())
	}

	// Add even larger element
	rb.Add(50)
	if rb.GetMax() != 50 {
		t.Errorf("Expected max 50, got %f", rb.GetMax())
	}

	// Add another element
	rb.Add(25)
	if rb.GetMax() != 50 {
		t.Errorf("Expected max 50, got %f", rb.GetMax())
	}
}

func TestGetMaxWithRotation(t *testing.T) {
	rb := NewBlockStats(3)

	// Fill buffer with [10, 20, 30]
	rb.Add(10)
	rb.Add(20)
	rb.Add(30)

	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30, got %f", rb.GetMax())
	}

	// Add 15, buffer becomes [20, 30, 15], max still 30
	rb.Add(15)
	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30 after adding 15, got %f", rb.GetMax())
	}

	// Add 25, buffer becomes [30, 15, 25], max still 30
	rb.Add(25)
	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30 after adding 25, got %f", rb.GetMax())
	}

	// Add 12, buffer becomes [15, 25, 12], max should be recalculated to 25
	rb.Add(12)
	if rb.GetMax() != 25 {
		t.Errorf("Expected max 25 after removing 30, got %f", rb.GetMax())
	}

	// Add 100, buffer becomes [25, 12, 100], max should be 100
	rb.Add(100)
	if rb.GetMax() != 100 {
		t.Errorf("Expected max 100, got %f", rb.GetMax())
	}

	// Add 50, buffer becomes [12, 100, 50], max should still be 100
	rb.Add(50)
	if rb.GetMax() != 100 {
		t.Errorf("Expected max 100 after adding 50, got %f", rb.GetMax())
	}

	// Add 60, buffer becomes [100, 50, 60], max should still be 100
	rb.Add(60)
	if rb.GetMax() != 100 {
		t.Errorf("Expected max 100 after adding 60, got %f", rb.GetMax())
	}

	// Add 70, buffer becomes [50, 60, 70], max should be recalculated to 70
	rb.Add(70)
	if rb.GetMax() != 70 {
		t.Errorf("Expected max 70 after removing 100, got %f", rb.GetMax())
	}
}

func TestGetMaxSingleElement(t *testing.T) {
	rb := NewBlockStats(1)

	rb.Add(42)
	if rb.GetMax() != 42 {
		t.Errorf("Expected max 42, got %f", rb.GetMax())
	}

	rb.Add(100)
	if rb.GetMax() != 100 {
		t.Errorf("Expected max 100 after replacement, got %f", rb.GetMax())
	}

	rb.Add(50)
	if rb.GetMax() != 50 {
		t.Errorf("Expected max 50 after replacement, got %f", rb.GetMax())
	}
}

func TestGetMaxMultipleMaxValues(t *testing.T) {
	rb := NewBlockStats(4)

	// Add [30, 30, 20, 10]
	rb.Add(30)
	rb.Add(30)
	rb.Add(20)
	rb.Add(10)

	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30, got %f", rb.GetMax())
	}

	// Add 15, buffer becomes [30, 20, 10, 15], one 30 removed but one remains
	rb.Add(15)
	if rb.GetMax() != 30 {
		t.Errorf("Expected max 30 after removing one 30, got %f", rb.GetMax())
	}

	// Add 25, buffer becomes [20, 10, 15, 25], max should be 25
	rb.Add(25)
	if rb.GetMax() != 25 {
		t.Errorf("Expected max 25 after removing last 30, got %f", rb.GetMax())
	}
}
