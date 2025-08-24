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

// BenchmarkParallelProofGeneration benchmarks proof generation for the
// target use case: 4000 × 64KiB leaves
func BenchmarkParallelProofGeneration(b *testing.B) {
	const numLeaves = 4000
	const leafSize = 64 * 1024 // 64KiB

	// Create test data - use simple pattern for consistent benchmarking
	items := make([][]byte, numLeaves)
	for i := 0; i < numLeaves; i++ {
		items[i] = make([]byte, leafSize)
		// Fill with pattern to avoid zero optimization
		for j := range items[i] {
			items[i][j] = byte(i + j)
		}
	}

	b.Run("Original_ProofsFromByteSlices", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(numLeaves * leafSize))
		for i := 0; i < b.N; i++ {
			_, proofs := ProofsFromByteSlices(items)
			if len(proofs) != numLeaves {
				b.Fatalf("Expected %d proofs, got %d", numLeaves, len(proofs))
			}
		}
	})

	b.Run("Parallel_ProofsFromByteSlices", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(numLeaves * leafSize))
		for i := 0; i < b.N; i++ {
			_, proofs := ParallelProofsFromByteSlices(items)
			if len(proofs) != numLeaves {
				b.Fatalf("Expected %d proofs, got %d", numLeaves, len(proofs))
			}
		}
	})
}

// BenchmarkParallelComparison compares the optimized implementation
// against the original for both target use cases
func BenchmarkParallelComparison(b *testing.B) {
	testCases := []struct {
		name     string
		numItems int
		itemSize int
	}{
		{"1000 64KiB_leaves", 1000, 64 * 1024},     // Scaled version of 4000×64KiB
		{"100_000 2KiB_leaves", 100_000, 2 * 1024}, // 2KiB leaves
		{"1000 1KiB leaves", 500, 1024},            // 1KiB leaves
		{"1000 32B leaves", 1000, 32},              // Small leaves
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

// TestParallelProofGeneration tests the parallel proof generation
// implementation for correctness and compatibility
func TestParallelProofGeneration(t *testing.T) {
	testCases := []struct {
		name     string
		numItems int
		itemSize int
	}{
		{"empty", 0, 0},
		{"single_small", 1, 32},
		{"single_large", 1, 64 * 1024},
		{"two_items", 2, 1024},
		{"small_many", 100, 32},
		{"medium_few", 10, 64 * 1024},
		{"target_scaled", 100, 64 * 1024}, // Scaled version of 4000×64KiB
		{"many_small", 1000, 2 * 1024},    // 2KiB items
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var items [][]byte
			if tc.numItems > 0 {
				items = make([][]byte, tc.numItems)
				for i := 0; i < tc.numItems; i++ {
					items[i] = make([]byte, tc.itemSize)
					for j := range items[i] {
						items[i][j] = byte(i + j) // Deterministic pattern
					}
				}
			}

			// Test both implementations
			originalRoot, originalProofs := ProofsFromByteSlices(items)
			parallelRoot, parallelProofs := ParallelProofsFromByteSlices(items)

			// Root hashes must be identical
			require.Equal(t, originalRoot, parallelRoot,
				"Root hashes must match for %s", tc.name)

			// Number of proofs must match
			require.Equal(t, len(originalProofs), len(parallelProofs),
				"Number of proofs must match for %s", tc.name)

			// Each proof must be identical
			for i := range originalProofs {
				orig := originalProofs[i]
				par := parallelProofs[i]

				assert.Equal(t, orig.Total, par.Total, "Total mismatch at index %d", i)
				assert.Equal(t, orig.Index, par.Index, "Index mismatch at index %d", i)
				assert.Equal(t, orig.LeafHash, par.LeafHash, "LeafHash mismatch at index %d", i)
				assert.Equal(t, orig.Aunts, par.Aunts, "Aunts mismatch at index %d", i)

				// Verify each proof can verify against the root
				if len(items) > i {
					err := par.Verify(parallelRoot, items[i])
					require.NoError(t, err, "Parallel proof %d should verify", i)
				}
			}
		})
	}
}

// TestParallelProofGenerationCompatibility ensures 100% compatibility
// with all existing proof test cases from proof_test.go
func TestParallelProofGenerationCompatibility(t *testing.T) {
	// Test cases from TestProofsFromLeafHashesAndByteSlices
	testCases := []struct {
		name  string
		input [][]byte
	}{
		{
			name:  "Empty input",
			input: [][]byte{},
		},
		{
			name:  "Single element",
			input: [][]byte{[]byte("A")},
		},
		{
			name:  "Two elements",
			input: [][]byte{[]byte("A"), []byte("B")},
		},
		{
			name:  "Three elements",
			input: [][]byte{[]byte("A"), []byte("B"), []byte("C")},
		},
		{
			name:  "Four elements",
			input: [][]byte{[]byte("A"), []byte("B"), []byte("C"), []byte("D")},
		},
		{
			name:  "Duplicates",
			input: [][]byte{[]byte("A"), []byte("A"), []byte("B"), []byte("B")},
		},
		{
			name:  "Varying sizes",
			input: [][]byte{[]byte("short"), []byte("medium-size"), []byte("a much longer string with more entropy")},
		},
		{
			name:  "Non-UTF-8 bytes",
			input: [][]byte{{0xff, 0xfe, 0xfd}, {0x00, 0x01, 0x02}, {0x80, 0x81, 0x82}},
		},
		{
			name:  "Leading/trailing zeros",
			input: [][]byte{{0x00, 0x00, 0x00}, {0xff, 0xff, 0xff}, {0x00, 0x01, 0x02, 0x00}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test original implementation
			root1, proofs1 := ProofsFromByteSlices(tc.input)

			// Test parallel implementation
			root2, proofs2 := ParallelProofsFromByteSlices(tc.input)

			// Root hashes should be identical
			require.Equal(t, root1, root2, "root hashes should be equal")

			// Proofs should be identical in length
			require.Equal(t, len(proofs1), len(proofs2), "proofs length should be equal")

			// Each proof should match exactly
			for i := range proofs1 {
				require.NotNil(t, proofs1[i], "proofs1[%d] should not be nil", i)
				require.NotNil(t, proofs2[i], "proofs2[%d] should not be nil", i)

				assert.Equal(t, proofs1[i].Total, proofs2[i].Total, "Total field mismatch")
				assert.Equal(t, proofs1[i].Index, proofs2[i].Index, "Index field mismatch")
				assert.Equal(t, proofs1[i].LeafHash, proofs2[i].LeafHash, "LeafHash field mismatch")
				assert.Equal(t, proofs1[i].Aunts, proofs2[i].Aunts, "Aunts field mismatch")
			}

			// Verify all proofs work correctly
			for i, proof := range proofs2 {
				if len(tc.input) > i {
					err := proof.Verify(root2, tc.input[i])
					require.NoError(t, err, "proof %d should verify", i)
				}
			}
		})
	}
}

// TestParallelProofGenerationConsistency tests that parallel proof generation
// produces consistent results across multiple runs (no race conditions)
func TestParallelProofGenerationConsistency(t *testing.T) {
	const numItems = 100
	const itemSize = 8 * 1024 // 8KiB items

	items := make([][]byte, numItems)
	for i := 0; i < numItems; i++ {
		items[i] = make([]byte, itemSize)
		for j := range items[i] {
			items[i][j] = byte(i + j)
		}
	}

	// Run multiple times to check for race conditions
	const numRuns = 20
	firstRoot, firstProofs := ParallelProofsFromByteSlices(items)

	for run := 1; run < numRuns; run++ {
		root, proofs := ParallelProofsFromByteSlices(items)

		// Root must be consistent
		require.Equal(t, firstRoot, root,
			"Root hash inconsistent on run %d", run)

		// All proofs must be consistent
		require.Equal(t, len(firstProofs), len(proofs),
			"Proof count inconsistent on run %d", run)

		for i := range firstProofs {
			assert.Equal(t, firstProofs[i].Total, proofs[i].Total,
				"Proof %d Total inconsistent on run %d", i, run)
			assert.Equal(t, firstProofs[i].Index, proofs[i].Index,
				"Proof %d Index inconsistent on run %d", i, run)
			assert.Equal(t, firstProofs[i].LeafHash, proofs[i].LeafHash,
				"Proof %d LeafHash inconsistent on run %d", i, run)
			assert.Equal(t, firstProofs[i].Aunts, proofs[i].Aunts,
				"Proof %d Aunts inconsistent on run %d", i, run)
		}
	}
}

// TestParallelProofGenerationLargeDataset tests proof generation with 
// datasets similar to the actual use case
func TestParallelProofGenerationLargeDataset(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset test in short mode")
	}

	testCases := []struct {
		name     string
		numItems int
		itemSize int
	}{
		{"realistic_4000x64KiB", 4000, 64 * 1024}, // Actual target use case
		{"many_2KiB", 10000, 2 * 1024},           // Many small items
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing proof generation for %d items of %d bytes each (%.1f MB total)",
				tc.numItems, tc.itemSize, float64(tc.numItems*tc.itemSize)/(1024*1024))

			items := make([][]byte, tc.numItems)
			for i := 0; i < tc.numItems; i++ {
				items[i] = make([]byte, tc.itemSize)
				// Use a simple pattern for faster test execution
				for j := range items[i] {
					items[i][j] = byte(i + j)
				}
			}

			// Generate proofs using parallel implementation
			root, proofs := ParallelProofsFromByteSlices(items)

			// Verify we got the right number of proofs
			require.Equal(t, tc.numItems, len(proofs),
				"Should generate proof for each item")

			// Verify a sample of proofs work correctly
			sampleIndices := []int{0, 1, tc.numItems/2, tc.numItems - 2, tc.numItems - 1}
			for _, idx := range sampleIndices {
				if idx < tc.numItems {
					err := proofs[idx].Verify(root, items[idx])
					require.NoError(t, err,
						"Proof %d should verify correctly", idx)
				}
			}
		})
	}
}
