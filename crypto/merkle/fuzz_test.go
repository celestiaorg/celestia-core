package merkle

import (
	"bytes"
	"testing"

	"github.com/cometbft/cometbft/internal/fuzzinput"
)

// fuzzLeafSizes are the leaf size ranges FuzzParallelImplementations draws
// from. The 1 KiB boundary is where the parallel code switches strategy.
var fuzzLeafSizes = [...]struct{ min, max int }{
	{0, 32},                      // small leaves, including empty ones
	{32, 512},                    // medium leaves
	{512, 8192},                  // large leaves
	{1, 2048},                    // mixed sizes around minLeafSizeForParallel
	{65536 - 1024, 65536 + 1024}, // ~64 KiB leaves, like Celestia block parts
}

// FuzzParallelImplementations tests that all parallel implementations
// produce identical results to the original sequential implementation.
// Everything (item count, leaf sizes and bytes, derived trees and proof
// indices) is derived from the fuzz input, so every input replays exactly.
func FuzzParallelImplementations(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5})
	f.Add([]byte{0xFF, 0xAB, 0xCD, 0xEF})
	f.Add(make([]byte, 1000))
	f.Add([]byte{0})                                    // empty tree
	f.Add([]byte{40, 0, 0, 0, 0, 0, 1, 2, 3})           // >= minItemsForSmallLeaves small leaves
	f.Add([]byte{9, 2, 0x00, 0x04, 0x01, 0x02, 0x03})   // >= minItemsForParallel large leaves
	f.Add([]byte{100, 4, 0xFF, 0xFF, 0xAA, 0x55, 5, 1}) // ~64 KiB leaves

	f.Fuzz(func(t *testing.T, data []byte) {
		r := fuzzinput.New(data)

		numItems := r.Intn(129) // 0..128
		sizes := fuzzLeafSizes[r.Intn(len(fuzzLeafSizes))]
		items := make([][]byte, numItems)
		for i := range items {
			size := sizes.min + int(r.Uint16())%(sizes.max-sizes.min+1)
			items[i] = fuzzLeaf(r.Bytes(32), i, size)
		}
		testParallelCorrectness(t, items, fuzzIndices(r, numItems))

		// Trees made of fuzz-chosen items (repeats allowed) stress other
		// shapes, including power-of-two and non-power-of-two counts.
		for range r.Intn(4) {
			if numItems == 0 {
				break
			}
			sub := make([][]byte, r.Intn(129))
			for i := range sub {
				sub[i] = items[r.Intn(numItems)]
			}
			testParallelCorrectness(t, sub, fuzzIndices(r, len(sub)))
		}
	})
}

// fuzzLeaf returns a leaf of exactly size bytes whose content is seed
// repeated and mixed with the item index, so leaves differ even when the
// fuzz input runs out.
func fuzzLeaf(seed []byte, index, size int) []byte {
	leaf := make([]byte, size)
	for j := range leaf {
		b := byte(index*31 + j)
		if len(seed) > 0 {
			b ^= seed[j%len(seed)]
		}
		leaf[j] = b
	}
	return leaf
}

// fuzzIndices returns up to eight fuzz-chosen proof indices in [0, n), plus
// the first and the last index.
func fuzzIndices(r *fuzzinput.Reader, n int) []int {
	if n == 0 {
		return nil
	}
	indices := []int{0, n - 1}
	for range r.Intn(9) {
		indices = append(indices, int(r.Uint16())%n)
	}
	return indices
}

func testParallelCorrectness(t *testing.T, items [][]byte, verify []int) {
	t.Helper()
	// Get reference result from original implementation
	expected := HashFromByteSlices(items)

	// Test the optimized parallel implementation
	if result := ParallelHashFromByteSlices(items); !bytes.Equal(expected, result) {
		t.Fatalf("ParallelHashFromByteSlices produced %x, HashFromByteSlices %x (%d items)", result, expected, len(items))
	}

	// Also test proof generation correctness
	expectedRoot, expectedProofs := ProofsFromByteSlices(items)
	actualRoot, actualProofs := ParallelProofsFromByteSlices(items)
	compareProofs(t, "ParallelProofsFromByteSlices", expectedRoot, expectedProofs, actualRoot, actualProofs)
	if !bytes.Equal(expected, actualRoot) {
		t.Fatalf("ParallelProofsFromByteSlices root %x differs from HashFromByteSlices %x", actualRoot, expected)
	}

	leafHashes := make([][]byte, len(items))
	for i, item := range items {
		leafHashes[i] = leafHash(item)
	}
	expectedRoot, expectedProofs = ProofsFromLeafHashes(leafHashes)
	leafRoot, leafProofs := ParallelProofsFromLeafHashes(leafHashes)
	compareProofs(t, "ParallelProofsFromLeafHashes", expectedRoot, expectedProofs, leafRoot, leafProofs)

	// Verify the proofs can verify against the root with the original item
	for _, i := range verify {
		if err := actualProofs[i].Verify(actualRoot, items[i]); err != nil {
			t.Fatalf("parallel proof %d of %d failed verification: %v", i, len(items), err)
		}
		if err := leafProofs[i].Verify(leafRoot, items[i]); err != nil {
			t.Fatalf("parallel leaf-hash proof %d of %d failed verification: %v", i, len(items), err)
		}
	}
}

// compareProofs checks that a parallel implementation returned exactly the
// root and proofs of its sequential counterpart.
func compareProofs(t *testing.T, name string, expectedRoot []byte, expectedProofs []*Proof, actualRoot []byte, actualProofs []*Proof) {
	t.Helper()
	if !bytes.Equal(expectedRoot, actualRoot) {
		t.Fatalf("%s root %x differs from sequential %x (%d proofs)", name, actualRoot, expectedRoot, len(expectedProofs))
	}
	if len(expectedProofs) != len(actualProofs) {
		t.Fatalf("%s returned %d proofs, sequential %d", name, len(actualProofs), len(expectedProofs))
	}
	for i, expected := range expectedProofs {
		actual := actualProofs[i]
		if expected.Total != actual.Total || expected.Index != actual.Index {
			t.Fatalf("%s proof %d: total/index %d/%d, sequential %d/%d", name, i, actual.Total, actual.Index, expected.Total, expected.Index)
		}
		if !bytes.Equal(expected.LeafHash, actual.LeafHash) {
			t.Fatalf("%s proof %d LeafHash differs", name, i)
		}
		if len(expected.Aunts) != len(actual.Aunts) {
			t.Fatalf("%s proof %d has %d aunts, sequential %d", name, i, len(actual.Aunts), len(expected.Aunts))
		}
		for j := range expected.Aunts {
			if !bytes.Equal(expected.Aunts[j], actual.Aunts[j]) {
				t.Fatalf("%s proof %d aunt %d differs", name, i, j)
			}
		}
	}
}

// TestParallelImplementationsProperty checks deterministic item counts and
// sizes on both sides of the parallel thresholds.
func TestParallelImplementationsProperty(t *testing.T) {
	for _, numItems := range []int{1, 2, 3, 7, 8, 9, 31, 32, 33, 64, 100} {
		for _, itemSize := range []int{1, 31, 32, 33, 512, 1000, 1023, 1024, 1025} {
			items := make([][]byte, numItems)
			for i := range items {
				items[i] = fuzzLeaf([]byte{byte(numItems), byte(itemSize)}, i, itemSize)
			}
			if !bytes.Equal(HashFromByteSlices(items), ParallelHashFromByteSlices(items)) {
				t.Errorf("ParallelHashFromByteSlices differs for %d items of %d bytes", numItems, itemSize)
			}
		}
	}
}

// TestParallelImplementationsLargeDataset tests with realistic dataset sizes
func TestParallelImplementationsLargeDataset(t *testing.T) {
	// Test with dataset similar to actual use case: 4000 items of 64KiB each
	const numItems = 128       // Reduced for testing, but same pattern
	const itemSize = 1024 * 64 // 64KiB

	items := make([][]byte, numItems)
	for i := range items {
		items[i] = fuzzLeaf([]byte("large dataset"), i, itemSize)
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
