package merkle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
)

// TestParallelHashFromByteSlicesCorrectness tests that the optimized
// implementation produces identical results to the original for all test cases
func TestParallelHashFromByteSlicesCorrectness(t *testing.T) {
	testcases := map[string]struct {
		slices     [][]byte
		expectHash string // in hex format
	}{
		"nil":          {nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		"empty":        {[][]byte{}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		"single":       {[][]byte{{1, 2, 3}}, "054edec1d0211f624fed0cbca9d4f9400b0e491c43742af2c5b0abebf0c990d8"},
		"single blank": {[][]byte{{}}, "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d"},
		"two":          {[][]byte{{1, 2, 3}, {4, 5, 6}}, "82e6cfce00453804379b53962939eaa7906b39904be0813fcadd31b100773c4b"},
		"many": {
			[][]byte{{1, 2}, {3, 4}, {5, 6}, {7, 8}, {9, 10}},
			"f326493eceab4f2d9ffbc78c59432a0a005d6ea98392045c74df5d14a113be18",
		},
	}

	for name, tc := range testcases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			hash := ParallelHashFromByteSlices(tc.slices)
			assert.Equal(t, tc.expectHash, hex.EncodeToString(hash))

			// Also verify it matches the original implementation
			originalHash := HashFromByteSlices(tc.slices)
			assert.Equal(t, originalHash, hash, "Should match original implementation")
		})
	}
}

// TestParallelVsOriginal tests the optimized implementation against
// the original with various dataset sizes and characteristics
func TestParallelVsOriginal(t *testing.T) {
	testCases := []struct {
		name        string
		numItems    int
		itemSize    int
		description string
	}{
		{"small_items_few", 5, 32, "Small items, few count - should use sequential"},
		{"small_items_many", 100, 32, "Small items, many count - should use parallel"},
		{"medium_items", 50, 1024, "1KiB items - should use parallel"},
		{"large_items_few", 10, 64 * 1024, "64KiB items, few count - should use parallel"},
		{"target_4000x64KiB", 100, 64 * 1024, "Scaled version of 4000×64KiB target"},
		{"target_2KiB_many", 1000, 2 * 1024, "2KiB items, many count"},
		{"mixed_sizes", 0, 0, "Mixed sizes - special case"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var items [][]byte

			if tc.name == "mixed_sizes" {
				// Create items with varying sizes
				items = [][]byte{
					make([]byte, 100),
					make([]byte, 1024),
					make([]byte, 64*1024),
					make([]byte, 2*1024),
					make([]byte, 50),
				}
				for i := range items {
					rand.Read(items[i])
				}
			} else {
				items = make([][]byte, tc.numItems)
				for i := 0; i < tc.numItems; i++ {
					items[i] = make([]byte, tc.itemSize)
					rand.Read(items[i])
				}
			}

			// Test correctness
			expected := HashFromByteSlices(items)
			result := ParallelHashFromByteSlices(items)

			require.Equal(t, expected, result,
				" implementation failed for %s: expected %x, got %x",
				tc.description, expected, result)
		})
	}
}

// TestParallelSizeVariations tests with comprehensive size variations
// to ensure the adaptive thresholds work correctly
func TestParallelSizeVariations(t *testing.T) {
	sizes := []int{0, 1, 2, 3, 4, 7, 8, 15, 16, 31, 32, 63, 64, 100, 500, 1000}
	itemSizes := []int{32, 1024, 2 * 1024, 64 * 1024} // 32B, 1KiB, 2KiB, 64KiB

	for _, numItems := range sizes {
		for _, itemSize := range itemSizes {
			testName := fmt.Sprintf("%d_items_%d_bytes", numItems, itemSize)
			t.Run(testName, func(t *testing.T) {
				// Create test data
				var items [][]byte
				if numItems > 0 {
					items = make([][]byte, numItems)
					for i := 0; i < numItems; i++ {
						items[i] = cmtrand.Bytes(itemSize)
					}
				}

				// Test correctness
				expected := HashFromByteSlices(items)
				result := ParallelHashFromByteSlices(items)

				require.Equal(t, expected, result,
					"Failed for %d items of %d bytes each", numItems, itemSize)
			})
		}
	}
}

// TestParallelConsistency ensures deterministic results
func TestParallelConsistency(t *testing.T) {
	// Test both target use cases
	testCases := []struct {
		name     string
		numItems int
		itemSize int
	}{
		{"4000x64KiB_scaled", 50, 64 * 1024}, // Scaled down version
		{"2KiB_many", 500, 2 * 1024},         // 2KiB leaves
		{"mixed_workload", 100, 8 * 1024},    // 8KiB leaves
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			items := make([][]byte, tc.numItems)
			for i := 0; i < tc.numItems; i++ {
				items[i] = cmtrand.Bytes(tc.itemSize)
			}

			// Run multiple times to check for race conditions
			const numRuns = 10
			firstResult := ParallelHashFromByteSlices(items)

			for i := 1; i < numRuns; i++ {
				result := ParallelHashFromByteSlices(items)
				require.Equal(t, firstResult, result,
					"Inconsistent results on run %d for %s", i, tc.name)
			}
		})
	}
}

// TestParallelEdgeCases tests edge cases that could cause issues
func TestParallelEdgeCases(t *testing.T) {
	testCases := map[string][][]byte{
		"nil_input":      nil,
		"empty_slice":    [][]byte{},
		"single_empty":   [][]byte{{}},
		"multiple_empty": [][]byte{{}, {}, {}},
		"mixed_empty":    [][]byte{{1}, {}, {2, 3}},
		"single_large":   [][]byte{make([]byte, 100*1024)}, // 100KiB
		"all_same": {
			[]byte{1, 2, 3},
			[]byte{1, 2, 3},
			[]byte{1, 2, 3},
			[]byte{1, 2, 3},
		},
	}

	// Initialize data for test cases that need it
	rand.Read(testCases["single_large"][0])

	for testName, items := range testCases {
		t.Run(testName, func(t *testing.T) {
			expected := HashFromByteSlices(items)
			result := ParallelHashFromByteSlices(items)

			require.Equal(t, expected, result,
				"Edge case %s failed: expected %x, got %x",
				testName, expected, result)
		})
	}
}

// TestParallelLargeDatasets tests performance with large datasets
// that represent the actual use cases
func TestParallelLargeDatasets(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset tests in short mode")
	}

	testCases := []struct {
		name     string
		numItems int
		itemSize int
	}{
		{"4000x64KiB_realistic", 4000, 64 * 1024}, // Actual target use case
		{"256k_x_2KiB", 10000, 2 * 1024},          // Scaled version of 256k × 2KiB
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing %d items of %d bytes each (%.1f MB total)",
				tc.numItems, tc.itemSize, float64(tc.numItems*tc.itemSize)/(1024*1024))

			items := make([][]byte, tc.numItems)
			for i := 0; i < tc.numItems; i++ {
				items[i] = make([]byte, tc.itemSize)
				// Use a simple pattern instead of random for faster test execution
				for j := range items[i] {
					items[i][j] = byte(i + j)
				}
			}

			// Verify correctness (this will be slow for the original but proves correctness)
			expected := HashFromByteSlices(items)
			result := ParallelHashFromByteSlices(items)

			require.Equal(t, expected, result,
				"Large dataset test %s failed", tc.name)
		})
	}
}

// BenchmarkParallelComparison compares the optimized implementation
// against the original for both target use cases
func BenchmarkParallelComparison(b *testing.B) {
	testCases := []struct {
		name     string
		numItems int
		itemSize int
	}{
		{"64KiB_leaves", 1000, 64 * 1024},  // Scaled version of 4000×64KiB
		{"2KiB_leaves", 100_000, 2 * 1024}, // 2KiB leaves
		{"mixed_1KiB", 500, 1024},          // 1KiB leaves
		{"small_32B", 1000, 32},            // Small leaves
	}

	for _, tc := range testCases {
		// Create test data
		items := make([][]byte, tc.numItems)
		for i := 0; i < tc.numItems; i++ {
			items[i] = make([]byte, tc.itemSize)
			rand.Read(items[i])
		}

		b.Run(fmt.Sprintf("Original_%s", tc.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = HashFromByteSlices(items)
			}
		})

		b.Run(fmt.Sprintf("_%s", tc.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ParallelHashFromByteSlices(items)
			}
		})
	}
}
