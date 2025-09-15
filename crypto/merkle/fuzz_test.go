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
	// Seed with diverse test cases
	f.Add([]byte{1, 2, 3, 4, 5})
	f.Add([]byte{0xFF, 0xAB, 0xCD, 0xEF})
	f.Add(make([]byte, 1000)) // Large zero-filled data

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		// Use data bytes to determine variable parameters for more robust testing
		dataIdx := 0
		nextByte := func() byte {
			if dataIdx >= len(data) {
				dataIdx = 0
			}
			b := data[dataIdx]
			dataIdx++
			return b
		}

		// Vary the number of items (1 to 100)
		numItems := max(1, int(nextByte())%100+1)

		// Vary leaf size ranges based on fuzz input
		leafSizeVariant := nextByte() % 5
		var minLeafSize, maxLeafSize int
		switch leafSizeVariant {
		case 0: // Small leaves (1-32 bytes)
			minLeafSize, maxLeafSize = 1, 32
		case 1: // Medium leaves (32-512 bytes)
			minLeafSize, maxLeafSize = 32, 512
		case 2: // Large leaves (512-8192 bytes)
			minLeafSize, maxLeafSize = 512, 8192
		case 3: // Mixed sizes (1-2048 bytes)
			minLeafSize, maxLeafSize = 1, 2048
		case 4: // Very large leaves (~64KB, like Celestia blocks)
			minLeafSize, maxLeafSize = 65536-1024, 65536+1024 // ~65KB ± 1KB
		}

		// Create items with varying sizes
		items := make([][]byte, numItems)
		for i := 0; i < numItems; i++ {
			// Vary leaf size within the range
			leafSize := minLeafSize + int(nextByte())%(maxLeafSize-minLeafSize+1)

			// Create leaf data by cycling through available data
			leaf := make([]byte, leafSize)
			for j := 0; j < leafSize; j++ {
				leaf[j] = nextByte()
			}
			items[i] = leaf
		}

		// Test with the generated inputs
		testParallelCorrectness(t, items)

		// Additional test with different leaf count but same data pattern
		if numItems > 1 {
			// Test with fewer items (stress different tree shapes)
			reducedCount := max(1, numItems/2)
			reducedItems := make([][]byte, reducedCount)
			for i := 0; i < reducedCount; i++ {
				reducedItems[i] = items[i*2%len(items)] // Sample every other item
			}
			testParallelCorrectness(t, reducedItems)
		}

		// Test with power-of-2 and non-power-of-2 counts (different tree structures)
		if numItems >= 4 {
			// Power of 2 test
			powerOf2Count := 1
			for powerOf2Count < numItems {
				powerOf2Count *= 2
			}
			powerOf2Count /= 2 // Get largest power of 2 <= numItems

			if powerOf2Count >= 2 && powerOf2Count != numItems {
				powerOf2Items := make([][]byte, powerOf2Count)
				for i := 0; i < powerOf2Count; i++ {
					powerOf2Items[i] = items[i%len(items)]
				}
				testParallelCorrectness(t, powerOf2Items)
			}
		}
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
		if err := actual.Verify(actualRoot, items[i]); err != nil {
			t.Errorf("Parallel proof %d failed verification: %v", i, err)
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
			if _, err := rand.Read(items[i]); err != nil {
				t.Fatalf("Failed to read random data: %v", err)
			}
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
	const numItems = 128       // Reduced for testing, but same pattern
	const itemSize = 1024 * 64 // 1KiB for testing

	items := make([][]byte, numItems)
	for i := range items {
		items[i] = make([]byte, itemSize)
		if _, err := rand.Read(items[i]); err != nil {
			t.Fatalf("Failed to read random data: %v", err)
		}
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
