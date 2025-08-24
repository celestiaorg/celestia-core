package merkle

import (
	"crypto/sha256"
	"runtime"
	"sync"
)

// ParallelHashFromByteSlices is the single optimized implementation
// that combines the best techniques for both target use cases:
// 1. 4000 × 64KiB leaves (~256MB total)
// 2. Up to 256,000 × 2KiB leaves (~512MB total)
//
// This implementation maintains RFC-6962 compliance and produces identical
// results to the original HashFromByteSlices function while providing
// significant performance improvements for large datasets.
func ParallelHashFromByteSlices(items [][]byte) []byte {
	switch len(items) {
	case 0:
		return emptyHash()
	case 1:
		return leafHash(items[0])
	case 2:
		// Direct computation for 2 items is faster than parallelization overhead
		return innerHash(leafHash(items[0]), leafHash(items[1]))
	default:
		return parallelHash(items)
	}
}

func parallelHash(items [][]byte) []byte {
	numItems := len(items)
	numWorkers := runtime.NumCPU()

	// Adaptive threshold based on dataset characteristics
	var useParallel bool
	if numItems >= 8 {
		// Estimate total data size to choose optimal strategy
		avgLeafSize := estimateAverageLeafSize(items)

		if avgLeafSize >= 1024 { // >= 1KiB leaves (like 2KiB, 64KiB use cases)
			useParallel = true
		} else if numItems >= 32 { // Small leaves need more items to justify parallel overhead
			useParallel = true
		}
	}

	if !useParallel {
		return HashFromByteSlices(items)
	}

	// Phase 1: Parallel leaf hash computation
	leafHashes := computeLeafHashesParallel(items, numWorkers)

	// Phase 2: Build balanced tree using same structure as original
	return buildBalancedTree(leafHashes, numWorkers)
}

// estimateAverageLeafSize provides a fast estimate of average leaf size
func estimateAverageLeafSize(items [][]byte) int {
	if len(items) == 0 {
		return 0
	}

	// Sample first few items to estimate average size without scanning everything
	sampleSize := min(len(items), 5)
	totalSize := 0

	for i := 0; i < sampleSize; i++ {
		totalSize += len(items[i])
	}

	return totalSize / sampleSize
}

// computeLeafHashesParallel efficiently computes all leaf hashes in parallel
func computeLeafHashesParallel(items [][]byte, numWorkers int) [][]byte {
	leafHashes := make([][]byte, len(items))

	// Use work-stealing pattern for optimal load balancing
	// This handles varying leaf sizes well (important for mixed workloads)
	var wg sync.WaitGroup
	work := make(chan int, len(items))

	// Queue all work
	for i := 0; i < len(items); i++ {
		work <- i
	}
	close(work)

	// Start workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker gets its own hash instance to avoid contention
			sha := sha256.New()
			for idx := range work {
				leafHashes[idx] = leafHashOpt(sha, items[idx])
			}
		}()
	}

	wg.Wait()
	return leafHashes
}

// buildBalancedTree builds the merkle tree maintaining the exact same
// structure as the original HashFromByteSlices implementation
func buildBalancedTree(leafHashes [][]byte, numWorkers int) []byte {
	return buildBalancedTreeRecursive(leafHashes, numWorkers)
}

func buildBalancedTreeRecursive(hashes [][]byte, maxWorkers int) []byte {
	switch len(hashes) {
	case 0:
		return emptyHash()
	case 1:
		return hashes[0]
	case 2:
		sha := sha256.New()
		return innerHashOpt(sha, hashes[0], hashes[1])
	default:
		// Use the same split point logic as the original to maintain tree structure
		k := getSplitPoint(int64(len(hashes)))

		var left, right []byte

		// Parallelize tree construction for larger subtrees
		// This threshold balances parallelization benefit vs overhead
		if len(hashes) >= 16 && maxWorkers > 1 {
			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				left = buildBalancedTreeRecursive(hashes[:k], maxWorkers/2)
			}()

			go func() {
				defer wg.Done()
				right = buildBalancedTreeRecursive(hashes[k:], maxWorkers/2)
			}()

			wg.Wait()
		} else {
			// Sequential for small subtrees
			left = buildBalancedTreeRecursive(hashes[:k], 1)
			right = buildBalancedTreeRecursive(hashes[k:], 1)
		}

		sha := sha256.New()
		return innerHashOpt(sha, left, right)
	}
}

// min helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
