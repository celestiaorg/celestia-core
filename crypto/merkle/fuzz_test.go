package merkle

import (
	"bytes"
	"crypto/rand"
	"testing"
	"testing/quick"
)

// FuzzParallelImplementations tests that all parallel implementations
// produce identical results to the original sequential implementation
func FuzzParallelImplementations(f *testing.F) {
	// Seed with some test cases
	f.Add([]byte{1, 2, 3, 4, 5})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		// Split data into chunks to create multiple items
		var items [][]byte
		chunkSize := 1 + (len(data) / 10) // Variable chunk size
		if chunkSize > 256 {
			chunkSize = 256
		}

		for i := 0; i < len(data); i += chunkSize {
			end := i + chunkSize
			if end > len(data) {
				end = len(data)
			}
			items = append(items, data[i:end])
		}

		if len(items) == 0 {
			return
		}

		// Test with the original inputs
		testParallelCorrectness(t, items)

		// Test with larger versions (simulate 64KiB leaves) but smaller for testing
		largeItems := make([][]byte, len(items))
		for i, item := range items {
			// Create larger version by repeating data
			large := make([]byte, 0, 1024) // 1KB for testing
			for len(large) < 512 {
				large = append(large, item...)
			}
			largeItems[i] = large[:min(len(large), 1024)]
		}
		testParallelCorrectness(t, largeItems)
	})
}

func testParallelCorrectness(t *testing.T, items [][]byte) {
	// Get reference result from original implementation
	expected := HashFromByteSlices(items)

	// Test the optimized parallel implementation
	implementations := map[string]func([][]byte) []byte{
		"ParallelHashFromByteSlices": ParallelHashFromByteSlices,
	}

	for name, impl := range implementations {
		result := impl(items)
		if !bytes.Equal(expected, result) {
			t.Errorf("%s produced different result than HashFromByteSlices", name)
			t.Errorf("Expected: %x", expected)
			t.Errorf("Got:      %x", result)
			t.Errorf("Items count: %d", len(items))
		}
	}

	// Also test proof generation correctness
	testParallelProofCorrectness(t, items)
}

// testParallelProofCorrectness tests that parallel proof generation
// produces identical results to the original implementation
func testParallelProofCorrectness(t *testing.T, items [][]byte) {
	// Get reference results from original implementation
	expectedRoot, expectedProofs := ProofsFromByteSlices(items)

	// Test parallel proof generation
	actualRoot, actualProofs := ParallelProofsFromByteSlices(items)

	// Root hashes must match
	if !bytes.Equal(expectedRoot, actualRoot) {
		t.Errorf("ParallelProofsFromByteSlices root hash differs from ProofsFromByteSlices")
		t.Errorf("Expected root: %x", expectedRoot)
		t.Errorf("Got root:      %x", actualRoot)
		t.Errorf("Items count: %d", len(items))
		return
	}

	// Number of proofs must match
	if len(expectedProofs) != len(actualProofs) {
		t.Errorf("ParallelProofsFromByteSlices proof count differs from ProofsFromByteSlices")
		t.Errorf("Expected: %d proofs", len(expectedProofs))
		t.Errorf("Got:      %d proofs", len(actualProofs))
		return
	}

	// Each proof must be identical
	for i := range expectedProofs {
		expected := expectedProofs[i]
		actual := actualProofs[i]

		if expected.Total != actual.Total {
			t.Errorf("Proof %d Total differs: expected %d, got %d", i, expected.Total, actual.Total)
		}
		if expected.Index != actual.Index {
			t.Errorf("Proof %d Index differs: expected %d, got %d", i, expected.Index, actual.Index)
		}
		if !bytes.Equal(expected.LeafHash, actual.LeafHash) {
			t.Errorf("Proof %d LeafHash differs", i)
		}
		if len(expected.Aunts) != len(actual.Aunts) {
			t.Errorf("Proof %d Aunts count differs: expected %d, got %d", i, len(expected.Aunts), len(actual.Aunts))
			continue
		}
		for j := range expected.Aunts {
			if !bytes.Equal(expected.Aunts[j], actual.Aunts[j]) {
				t.Errorf("Proof %d Aunt %d differs", i, j)
			}
		}

		// Verify the proof can verify against the root with the original item
		if len(items) > i {
			if err := actual.Verify(actualRoot, items[i]); err != nil {
				t.Errorf("Parallel proof %d failed verification: %v", i, err)
			}
		}
	}
}

// TestParallelImplementationsProperty uses property-based testing
func TestParallelImplementationsProperty(t *testing.T) {
	property := func(numItems uint8, itemSize uint16) bool {
		if numItems == 0 || numItems > 100 || itemSize == 0 || itemSize > 1000 {
			return true // Skip invalid inputs
		}

		items := make([][]byte, numItems)
		for i := range items {
			items[i] = make([]byte, itemSize)
			rand.Read(items[i])
		}

		expected := HashFromByteSlices(items)

		// Test the optimized implementation
		implementations := []func([][]byte) []byte{
			ParallelHashFromByteSlices,
		}

		for _, impl := range implementations {
			result := impl(items)
			if !bytes.Equal(expected, result) {
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, nil); err != nil {
		t.Error(err)
	}
}

// TestParallelImplementationsLargeDataset tests with realistic dataset sizes
func TestParallelImplementationsLargeDataset(t *testing.T) {
	// Test with dataset similar to actual use case: 4000 items of 64KiB each
	const numItems = 100  // Reduced for testing, but same pattern
	const itemSize = 1024 // 1KiB for testing

	items := make([][]byte, numItems)
	for i := range items {
		items[i] = make([]byte, itemSize)
		rand.Read(items[i])
	}

	expected := HashFromByteSlices(items)

	// Test the optimized implementation
	implementations := map[string]func([][]byte) []byte{
		"ParallelHashFromByteSlices": ParallelHashFromByteSlices,
	}

	for name, impl := range implementations {
		t.Run(name, func(t *testing.T) {
			result := impl(items)
			if !bytes.Equal(expected, result) {
				t.Errorf("%s produced incorrect result", name)
			}
		})
	}
}
