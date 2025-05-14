package store

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	cmtstore "github.com/cometbft/cometbft/proto/tendermint/store"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/golang/snappy"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	// Directory names for different store components
	dataDir       = "data"
	blocksDir     = "blocks"
	validatorsDir = "validators"
	consensusDir  = "consensus"
	stateDir      = "state"
	metaExt       = ".meta"

	// Height range size for directory organization
	heightRangeSize = 1000

	// Thresholds for parallel processing
	minBlockSizeForParallel = 1024 * 1024 // 1MB
	maxParallelWriters      = 4

	// Compression settings
	compressionThreshold = 1024 * 64 // 64KB minimum size for compression
	// Cache settings
	defaultCacheSize  = 100
	extendedCacheSize = 1000
	blockCacheSize    = 50  // Fewer blocks but larger size
	metaCacheSize     = 200 // More metadata entries as they're smaller

	// File name for the persistent hash-to-height index
	hashIndexFile = "hash_to_height.idx"
)

// FileBlockStore implements both BlockStore and Store interfaces using the file system
//
// File Structure:
// The store uses a directory-based structure to organize all data:
//
//	baseDir/
//	  ├── data/
//	  │   ├── blocks/
//	  │   │   ├── 0000-0999/
//	  │   │   │   ├── block_0001.proto
//	  │   │   │   ├── meta_0001.proto
//	  │   │   │   └── commit_0001.proto
//	  │   │   └── ...
//	  └── state/
//	      ├── state.json
//	      └── hash_to_height.idx
type FileBlockStore struct {
	baseDir string
	mtx     cmtsync.RWMutex
	base    int64
	height  int64

	// Caches
	seenCommitCache          *lru.Cache[int64, *types.Commit]
	blockCommitCache         *lru.Cache[int64, *types.Commit]
	blockExtendedCommitCache *lru.Cache[int64, *types.ExtendedCommit]
	blockDataCache           *lru.Cache[int64, *blockCacheEntry]
	metaCache                *lru.Cache[int64, []byte]

	hashToHeight   map[string]int64
	hashToHeightMu cmtsync.RWMutex
}

// Helper function to get the directory for a given height range
func getHeightRangeDir(height int64) string {
	start := (height / heightRangeSize) * heightRangeSize
	end := start + heightRangeSize - 1
	return fmt.Sprintf("%04d-%04d", start, end)
}

// Helper functions for file operations
func (bs *FileBlockStore) getBlockPath(height int64) string {
	rangeDir := getHeightRangeDir(height)
	return filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, fmt.Sprintf("block_%d.proto", height))
}

func (bs *FileBlockStore) getMetaPath(height int64) string {
	rangeDir := getHeightRangeDir(height)
	return filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, fmt.Sprintf("meta_%d.proto", height))
}

func (bs *FileBlockStore) getCommitPath(height int64) string {
	rangeDir := getHeightRangeDir(height)
	return filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, fmt.Sprintf("commit_%d.proto", height))
}

func (bs *FileBlockStore) getExtendedCommitPath(height int64) string {
	rangeDir := getHeightRangeDir(height)
	return filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, fmt.Sprintf("extended_commit_%d.proto", height))
}

func (bs *FileBlockStore) getStatePath() string {
	return filepath.Join(bs.baseDir, stateDir, "state.json")
}

func (bs *FileBlockStore) getHashIndexPath() string {
	return filepath.Join(bs.baseDir, stateDir, hashIndexFile)
}

// NewFileBlockStore creates a new file-based store that implements both BlockStore and Store interfaces
func NewFileBlockStore(baseDir string) (*FileBlockStore, error) {
	dirs := []string{
		filepath.Join(baseDir, dataDir),
		filepath.Join(baseDir, dataDir, blocksDir),
		filepath.Join(baseDir, dataDir, validatorsDir),
		filepath.Join(baseDir, dataDir, consensusDir),
		filepath.Join(baseDir, stateDir),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	state, err := loadBlockStoreState(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load block store state: %w", err)
	}

	bs := &FileBlockStore{
		baseDir: baseDir,
		base:    state.Base,
		height:  state.Height,
	}

	if err := bs.addCaches(); err != nil {
		return nil, fmt.Errorf("failed to add caches: %w", err)
	}

	if err := bs.initHashIndex(); err != nil {
		return nil, fmt.Errorf("failed to initialize hash index: %w", err)
	}

	return bs, nil
}

// addCaches initializes the LRU caches used by the store
func (bs *FileBlockStore) addCaches() error {
	var err error
	bs.blockCommitCache, err = lru.New[int64, *types.Commit](100)
	if err != nil {
		return fmt.Errorf("failed to create block commit cache: %w", err)
	}

	bs.blockExtendedCommitCache, err = lru.New[int64, *types.ExtendedCommit](100)
	if err != nil {
		return fmt.Errorf("failed to create block extended commit cache: %w", err)
	}

	bs.seenCommitCache, err = lru.New[int64, *types.Commit](100)
	if err != nil {
		return fmt.Errorf("failed to create seen commit cache: %w", err)
	}

	bs.blockDataCache, err = lru.New[int64, *blockCacheEntry](blockCacheSize)
	if err != nil {
		return fmt.Errorf("failed to create block data cache: %w", err)
	}

	bs.metaCache, err = lru.New[int64, []byte](metaCacheSize)
	if err != nil {
		return fmt.Errorf("failed to create meta cache: %w", err)
	}

	return nil
}

// loadBlockStoreState loads the block store state from disk
func loadBlockStoreState(baseDir string) (cmtstore.BlockStoreState, error) {
	statePath := filepath.Join(baseDir, stateDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return cmtstore.BlockStoreState{}, nil
		}
		return cmtstore.BlockStoreState{}, fmt.Errorf("failed to read state file: %w", err)
	}

	var state cmtstore.BlockStoreState
	if err := proto.Unmarshal(data, &state); err != nil {
		return cmtstore.BlockStoreState{}, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return state, nil
}

// saveBlockStoreState saves the block store state to disk
func (bs *FileBlockStore) saveBlockStoreState() error {
	state := cmtstore.BlockStoreState{
		Base:   bs.base,
		Height: bs.height,
	}

	data, err := proto.Marshal(&state)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(bs.getStatePath(), data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// Close implements the BlockStore interface
func (bs *FileBlockStore) Close() error {
	if err := bs.saveHashIndex(); err != nil {
		return fmt.Errorf("failed to save hash index on close: %w", err)
	}
	return nil
}

// LoadBlock implements the BlockStore interface
func (bs *FileBlockStore) LoadBlock(height int64) *types.Block {
	// Check cache first
	if entry, ok := bs.blockDataCache.Get(height); ok {
		pbb := new(cmtproto.Block)
		if err := proto.Unmarshal(entry.data, pbb); err != nil {
			panic(fmt.Sprintf("Error unmarshaling cached block: %v", err))
		}

		block, err := types.BlockFromProto(pbb)
		if err != nil {
			panic(fmt.Sprintf("Error converting cached block from proto: %v", err))
		}

		return block
	}

	blockMeta := bs.LoadBlockMeta(height)
	if blockMeta == nil {
		return nil
	}

	// Read and decompress block file
	blockPath := bs.getBlockPath(height)
	data, err := os.ReadFile(blockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		panic(fmt.Sprintf("Error reading block file: %v", err))
	}

	decompressed, err := decompressData(data)
	if err != nil {
		panic(fmt.Sprintf("Error decompressing block data: %v", err))
	}

	pbb := new(cmtproto.Block)
	if err := proto.Unmarshal(decompressed, pbb); err != nil {
		panic(fmt.Sprintf("Error unmarshaling block: %v", err))
	}

	block, err := types.BlockFromProto(pbb)
	if err != nil {
		panic(fmt.Sprintf("Error converting block from proto: %v", err))
	}

	// Cache the block data
	bs.blockDataCache.Add(height, &blockCacheEntry{
		data:      decompressed,
		timestamp: time.Now(),
	})

	return block
}

// LoadBlockByHash implements the BlockStore interface
func (bs *FileBlockStore) LoadBlockByHash(hash []byte) *types.Block {
	// First find the height from the hash
	height, err := bs.getHeightFromHash(hash)
	if err != nil {
		return nil
	}
	return bs.LoadBlock(height)
}

// LoadBlockMeta implements the BlockStore interface
func (bs *FileBlockStore) LoadBlockMeta(height int64) *types.BlockMeta {
	metaPath := bs.getMetaPath(height)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		panic(fmt.Sprintf("Error reading block meta file: %v", err))
	}

	pbbm := new(cmtproto.BlockMeta)
	if err := proto.Unmarshal(data, pbbm); err != nil {
		panic(fmt.Sprintf("Error unmarshaling block meta: %v", err))
	}

	blockMeta, err := types.BlockMetaFromTrustedProto(pbbm)
	if err != nil {
		panic(fmt.Sprintf("Error converting block meta from proto: %v", err))
	}

	return blockMeta
}

// LoadBlockPart implements the BlockStore interface.
// It reconstructs the part from the full block data, as individual parts are not stored.
func (bs *FileBlockStore) LoadBlockPart(height int64, index int) *types.Part {
	fullBlock := bs.LoadBlock(height)
	if fullBlock == nil {
		return nil
	}

	partSet, err := fullBlock.MakePartSet(types.BlockPartSizeBytes)
	if err != nil {
		panic(fmt.Sprintf("Error creating part set for block %d: %v", height, err))
	}
	if partSet == nil || index < 0 || index >= int(partSet.Total()) {
		return nil
	}

	return partSet.GetPart(index)
}

// SaveBlock implements the BlockStore interface
//
// This method saves a block and its associated data to the filesystem.
// The block is saved in multiple files:
// 1. Block file: Contains the full block data.
// 2. Meta file: Contains block metadata.
// 3. Commit file: Contains the commit data.
//
// Note: This method panics on errors to match the behavior of the original BlockStore.
// Future versions should consider returning errors instead.
func (bs *FileBlockStore) SaveBlock(block *types.Block, blockParts *types.PartSet, seenCommit *types.Commit) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	height := block.Height

	// Create height range directory (partsDir is no longer created)
	rangeDir := getHeightRangeDir(height)
	blockRangeDir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir)
	if err := os.MkdirAll(blockRangeDir, 0755); err != nil {
		panic(fmt.Sprintf("Error creating directory %s: %v", blockRangeDir, err))
	}

	// Prepare all data in memory first
	blockProto, err := block.ToProto()
	if err != nil {
		panic(fmt.Sprintf("Error converting block to proto: %v", err))
	}
	blockData, err := proto.Marshal(blockProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block: %v", err))
	}

	blockMeta := types.NewBlockMeta(block, blockParts)
	metaProto := blockMeta.ToProto()
	metaData, err := proto.Marshal(metaProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block meta: %v", err))
	}

	// Prepare commit data if available
	var commitData []byte
	if seenCommit != nil {
		commitProto := seenCommit.ToProto()
		commitData, err = proto.Marshal(commitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling commit: %v", err))
		}
	}

	// Determine if we should use parallel processing (based on block, meta, commit only)
	useParallel := len(blockData) > minBlockSizeForParallel // Simplified condition

	if useParallel {
		var wg sync.WaitGroup
		errChan := make(chan error, 3) // Buffer for block, meta, commit

		// Write block file
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := bs.writeFileWithBuffer(bs.getBlockPath(height), blockData); err != nil {
				errChan <- fmt.Errorf("error writing block file: %w", err)
			}
		}()

		// Write meta file
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := bs.writeFileWithBuffer(bs.getMetaPath(height), metaData); err != nil {
				errChan <- fmt.Errorf("error writing block meta file: %w", err)
			}
		}()

		// Write commit file if available
		if seenCommit != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bs.writeFileWithBuffer(bs.getCommitPath(height), commitData); err != nil {
					errChan <- fmt.Errorf("error writing commit file: %w", err)
				}
			}()
		}

		// Wait for all writes to complete
		wg.Wait()
		close(errChan)

		// Check for any errors
		for err := range errChan {
			panic(err)
		}
	} else {
		// Use sequential processing for small blocks
		if err := bs.writeFileWithBuffer(bs.getBlockPath(height), blockData); err != nil {
			panic(fmt.Sprintf("Error writing block file: %v", err))
		}

		if err := bs.writeFileWithBuffer(bs.getMetaPath(height), metaData); err != nil {
			panic(fmt.Sprintf("Error writing block meta file: %v", err))
		}

		if seenCommit != nil {
			if err := bs.writeFileWithBuffer(bs.getCommitPath(height), commitData); err != nil {
				panic(fmt.Sprintf("Error writing commit file: %v", err))
			}
		}
	}

	// Update state
	if bs.height < height {
		bs.height = height
	}
	if bs.base == 0 {
		bs.base = height
	}

	if err := bs.saveBlockStoreState(); err != nil {
		panic(fmt.Sprintf("Error saving block store state: %v", err))
	}

	// Update hash index
	bs.updateHashIndex(block.Height, blockMeta.BlockID.Hash)
}

// Helper function to get height from hash.
// Relies solely on the in-memory index (populated by initHashIndex).
// The disk scanning fallback has been removed to ensure O(1) access if the item is in the map.
func (bs *FileBlockStore) getHeightFromHash(hash []byte) (int64, error) {
	if len(hash) == 0 {
		return 0, fmt.Errorf("empty hash")
	}

	bs.hashToHeightMu.RLock()
	height, ok := bs.hashToHeight[string(hash)]
	bs.hashToHeightMu.RUnlock()

	if !ok {
		// If hash is not in the map, it means it's not known to the store via its index.
		// No disk scan is performed here to maintain O(1) lookup, assuming initHashIndex built it correctly.
		return 0, fmt.Errorf("block with hash %x not found in pre-loaded hash-to-height index", hash)
	}
	return height, nil
}

// LoadBlockCommit implements the BlockStore interface
func (bs *FileBlockStore) LoadBlockCommit(height int64) *types.Commit {
	// Check cache first
	if commit, ok := bs.blockCommitCache.Get(height); ok {
		return commit
	}

	commitPath := bs.getCommitPath(height)
	data, err := os.ReadFile(commitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		panic(fmt.Sprintf("Error reading commit file: %v", err))
	}

	pbc := new(cmtproto.Commit)
	if err := proto.Unmarshal(data, pbc); err != nil {
		panic(fmt.Sprintf("Error unmarshaling commit: %v", err))
	}

	commit, err := types.CommitFromProto(pbc)
	if err != nil {
		panic(fmt.Sprintf("Error converting commit from proto: %v", err))
	}

	// Cache the commit
	bs.blockCommitCache.Add(height, commit)

	return commit
}

// LoadBlockExtendedCommit implements the BlockStore interface
func (bs *FileBlockStore) LoadBlockExtendedCommit(height int64) *types.ExtendedCommit {
	// Check cache first
	if commit, ok := bs.blockExtendedCommitCache.Get(height); ok {
		return commit
	}

	// Try loading extended commit first
	commitPath := bs.getExtendedCommitPath(height)
	data, err := os.ReadFile(commitPath)
	if err != nil {
		if !os.IsNotExist(err) {
			panic(fmt.Sprintf("Error reading extended commit file: %v", err))
		}

		// Try loading from the regular commit path for backward compatibility
		commitPath = bs.getCommitPath(height)
		data, err = os.ReadFile(commitPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			panic(fmt.Sprintf("Error reading commit file: %v", err))
		}

		// Try to unmarshal as regular commit
		pbc := new(cmtproto.Commit)
		if err := proto.Unmarshal(data, pbc); err != nil {
			panic(fmt.Sprintf("Error unmarshaling commit: %v", err))
		}

		commit, err := types.CommitFromProto(pbc)
		if err != nil {
			panic(fmt.Sprintf("Error converting commit from proto: %v", err))
		}

		// Convert regular commit to extended commit
		extCommit := commit.WrappedExtendedCommit()

		// Cache the commit
		bs.blockExtendedCommitCache.Add(height, extCommit)

		return extCommit
	}

	// Try to unmarshal as extended commit
	pbc := new(cmtproto.ExtendedCommit)
	if err := proto.Unmarshal(data, pbc); err != nil {
		panic(fmt.Sprintf("Error unmarshaling extended commit: %v", err))
	}

	commit, err := types.ExtendedCommitFromProto(pbc)
	if err != nil {
		panic(fmt.Sprintf("Error converting extended commit from proto: %v", err))
	}

	// Cache the commit
	bs.blockExtendedCommitCache.Add(height, commit)

	return commit
}

// LoadSeenCommit implements the BlockStore interface
func (bs *FileBlockStore) LoadSeenCommit(height int64) *types.Commit {
	// Check cache first
	if commit, ok := bs.seenCommitCache.Get(height); ok {
		return commit
	}

	// For file-based store, we use the same commit file
	commit := bs.LoadBlockCommit(height)
	if commit != nil {
		bs.seenCommitCache.Add(height, commit)
	}

	return commit
}

// SaveSeenCommit implements the BlockStore interface
func (bs *FileBlockStore) SaveSeenCommit(height int64, seenCommit *types.Commit) error {
	if seenCommit == nil {
		return fmt.Errorf("cannot save nil seen commit")
	}

	// Create height range directory
	rangeDir := getHeightRangeDir(height)
	dir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	commitProto := seenCommit.ToProto()
	commitData, err := proto.Marshal(commitProto)
	if err != nil {
		return fmt.Errorf("failed to marshal commit: %w", err)
	}

	if err := os.WriteFile(bs.getCommitPath(height), commitData, 0644); err != nil {
		return fmt.Errorf("failed to write commit file: %w", err)
	}

	// Update cache
	bs.seenCommitCache.Add(height, seenCommit)

	return nil
}

// PruneBlocks implements the BlockStore interface
//
// This method removes blocks up to (but not including) the given height.
// It deletes all associated files (block, meta, commit) for each pruned block.
func (bs *FileBlockStore) PruneBlocks(height int64, state sm.State) (uint64, int64, error) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	if height <= 0 {
		return 0, 0, fmt.Errorf("height must be greater than 0")
	}

	if height > bs.height {
		return 0, 0, fmt.Errorf("cannot prune beyond the latest height %d", bs.height)
	}

	if height < bs.base {
		return 0, 0, fmt.Errorf("cannot prune to height %d, it is below the base height %d", height, bs.base)
	}

	// Calculate the number of blocks to prune
	blocksToPrune := height - bs.base + 1
	if blocksToPrune <= 0 {
		return 0, 0, nil
	}

	// Clear caches for pruned blocks
	for h := bs.base; h <= height; h++ {
		bs.blockDataCache.Remove(h)
		bs.metaCache.Remove(h)
		bs.seenCommitCache.Remove(h)
		bs.blockCommitCache.Remove(h)
		bs.blockExtendedCommitCache.Remove(h)
	}

	// Group heights by range directory for batch processing
	rangeDirs := make(map[string][]int64)
	for h := bs.base; h <= height; h++ {
		rangeDir := getHeightRangeDir(h)
		rangeDirs[rangeDir] = append(rangeDirs[rangeDir], h)
	}

	// Use a WaitGroup to wait for all deletions to complete
	var wg sync.WaitGroup
	errChan := make(chan error, len(rangeDirs))

	// Process each range directory in parallel
	for rangeDir, heights := range rangeDirs {
		wg.Add(1)
		go func(dir string, blockHeights []int64) {
			defer wg.Done()

			// Get all files to delete for this range
			var filesToDelete []string

			// Add block, meta, and commit files
			for _, h := range blockHeights {
				filesToDelete = append(filesToDelete,
					bs.getBlockPath(h),
					bs.getMetaPath(h),
					bs.getCommitPath(h),
					bs.getExtendedCommitPath(h),
				)
			}

			// Delete all files in parallel
			var fileWg sync.WaitGroup
			fileErrChan := make(chan error, len(filesToDelete))

			for _, file := range filesToDelete {
				fileWg.Add(1)
				go func(f string) {
					defer fileWg.Done()
					if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
						fileErrChan <- fmt.Errorf("failed to remove file %s: %w", f, err)
					}
				}(file)
			}

			fileWg.Wait()
			close(fileErrChan)

			// Check for any file deletion errors
			for err := range fileErrChan {
				errChan <- err
				return
			}

			// Try to remove empty directories
			os.Remove(filepath.Join(bs.baseDir, dataDir, blocksDir, dir))
		}(rangeDir, heights)
	}

	// Wait for all range directories to be processed
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		return 0, 0, err
	}

	// Update base height
	oldBase := bs.base
	bs.base = height + 1

	// Save state
	if err := bs.saveBlockStoreState(); err != nil {
		return 0, 0, fmt.Errorf("failed to save block store state: %w", err)
	}

	// Remove pruned blocks from hash index (in-memory and on-disk)
	bs.hashToHeightMu.Lock()
	changed := false
	for hash, h := range bs.hashToHeight {
		if h <= height {
			delete(bs.hashToHeight, hash)
			changed = true
		}
	}
	bs.hashToHeightMu.Unlock()

	if changed {
		if err := bs.saveHashIndex(); err != nil {
			panic(fmt.Sprintf("Critical: failed to save hash index after pruning: %v", err))
		}
	}

	return uint64(blocksToPrune), oldBase, nil
}

// DeleteLatestBlock implements the BlockStore interface
func (bs *FileBlockStore) DeleteLatestBlock() error {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	if bs.height == 0 {
		return fmt.Errorf("no blocks to delete")
	}

	currentHeight := bs.height
	oldBase := bs.base // Capture oldBase before it's potentially modified

	// Clear caches for the deleted block
	bs.blockDataCache.Remove(currentHeight)
	bs.metaCache.Remove(currentHeight)
	bs.seenCommitCache.Remove(currentHeight)
	bs.blockCommitCache.Remove(currentHeight)
	bs.blockExtendedCommitCache.Remove(currentHeight)

	// Remove block files
	blockPath := bs.getBlockPath(currentHeight)
	if err := os.Remove(blockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove block file: %w", err)
	}

	// Remove meta files
	metaPath := bs.getMetaPath(currentHeight)
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove meta file: %w", err)
	}

	// Remove commit files
	commitPath := bs.getCommitPath(currentHeight)
	if err := os.Remove(commitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove commit file: %w", err)
	}

	// Remove extended commit files (This was correct, it is not related to part removal, it is a separate file type)
	extendedCommitPath := bs.getExtendedCommitPath(currentHeight)
	if err := os.Remove(extendedCommitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove extended commit file: %w", err)
	}

	// Update height
	bs.height = currentHeight - 1
	if bs.height < bs.base {
		bs.base = bs.height
		if bs.height == 0 {
			bs.base = 0
		}
	}
	if bs.height == 0 && oldBase == 1 {
		bs.base = 0
	}

	// Save state
	if err := bs.saveBlockStoreState(); err != nil {
		return fmt.Errorf("failed to save block store state: %w", err)
	}

	// Remove from hash index (in-memory and on-disk)
	bs.hashToHeightMu.Lock()
	changed := false
	var deletedHash string
	for hash, h := range bs.hashToHeight {
		if h == currentHeight {
			deletedHash = hash
			delete(bs.hashToHeight, hash)
			changed = true
			break // Assuming one hash per height
		}
	}
	bs.hashToHeightMu.Unlock()

	if changed {
		if err := bs.saveHashIndex(); err != nil {
			panic(fmt.Sprintf("Critical: failed to save hash index after deleting latest block for hash %s: %v", deletedHash, err))
		}
	}

	return nil
}

// Add this helper function for preparing block data.
// It prepares marshaled block and meta data.
func (bs *FileBlockStore) prepareBlockData(block *types.Block, blockParts *types.PartSet) ([]byte, []byte, error) {
	// Prepare block data
	blockProto, err := block.ToProto()
	if err != nil {
		return nil, nil, fmt.Errorf("error converting block to proto: %w", err)
	}
	blockData, err := proto.Marshal(blockProto)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling block: %w", err)
	}

	// Prepare meta data
	blockMeta := types.NewBlockMeta(block, blockParts)
	metaProto := blockMeta.ToProto()
	metaData, err := proto.Marshal(metaProto)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling block meta: %w", err)
	}

	return blockData, metaData, nil
}

// Add compression helper
func compressData(data []byte) ([]byte, error) {
	if len(data) < compressionThreshold {
		return data, nil
	}
	var buf bytes.Buffer
	writer := snappy.NewBufferedWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Add decompression helper
func decompressData(data []byte) ([]byte, error) {
	if len(data) < compressionThreshold {
		return data, nil
	}
	reader := snappy.NewReader(bytes.NewReader(data))
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("snappy decompress failed for data of len %d: %w. Original data might be corrupted or was not compressed as expected", len(data), err)
	}
	return decompressed, nil
}

// Add this type for block data caching
type blockCacheEntry struct {
	data []byte
	meta []byte
	// extendedCommit and regularCommit store the marshaled commit data
	extendedCommit []byte
	regularCommit  []byte
	timestamp      time.Time
}

// Modify SaveBlockWithExtendedCommit to use compression and enhanced caching.
// Individual block parts are not saved to separate files.
func (bs *FileBlockStore) SaveBlockWithExtendedCommit(block *types.Block, blockParts *types.PartSet, seenExtendedCommit *types.ExtendedCommit) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	height := block.Height

	rangeDir := getHeightRangeDir(height)
	blockRangeDir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir)

	if err := os.MkdirAll(blockRangeDir, 0755); err != nil {
		panic(fmt.Sprintf("Error creating directory %s: %v", blockRangeDir, err))
	}

	// Prepare and compress all data first
	blockData, metaData, err := bs.prepareBlockData(block, blockParts)
	if err != nil {
		panic(fmt.Sprintf("Error preparing block data: %v", err))
	}

	// Prepare extended commit data
	var extendedCommitData, regularCommitData []byte
	if seenExtendedCommit != nil {
		extendedCommitProto := seenExtendedCommit.ToProto()
		extendedCommitData, err = proto.Marshal(extendedCommitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling extended commit: %v", err))
		}

		regularCommit := seenExtendedCommit.ToCommit()
		regularCommitProto := regularCommit.ToProto()
		regularCommitData, err = proto.Marshal(regularCommitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling regular commit: %v", err))
		}
	}

	// Cache the data
	cacheEntry := &blockCacheEntry{
		data:           blockData,
		meta:           metaData,
		extendedCommit: extendedCommitData,
		regularCommit:  regularCommitData,
		timestamp:      time.Now(),
	}
	bs.blockDataCache.Add(height, cacheEntry)
	if metaData != nil { // Meta cache stores raw bytes
		bs.metaCache.Add(height, metaData)
	}

	// Determine if we should use parallel processing
	totalSize := len(blockData) + len(metaData)
	if seenExtendedCommit != nil {
		totalSize += len(extendedCommitData) + len(regularCommitData)
	}
	useParallel := totalSize > minBlockSizeForParallel

	if useParallel {
		var wg sync.WaitGroup
		// Adjust error channel size based on actual number of goroutines
		numGoRoutines := 0
		if blockData != nil {
			numGoRoutines++
		}
		if metaData != nil {
			numGoRoutines++
		}

		if seenExtendedCommit != nil {
			if extendedCommitData != nil {
				numGoRoutines++
			}
			if regularCommitData != nil {
				numGoRoutines++
			}
		}
		errChan := make(chan error, numGoRoutines)

		// Write block and meta files
		if blockData != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bs.writeCompressedFile(bs.getBlockPath(height), blockData); err != nil {
					errChan <- fmt.Errorf("error writing block file: %w", err)
				}
			}()
		}
		if metaData != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bs.writeCompressedFile(bs.getMetaPath(height), metaData); err != nil {
					errChan <- fmt.Errorf("error writing block meta file: %w", err)
				}
			}()
		}

		// Write commit files if available
		if seenExtendedCommit != nil {
			if extendedCommitData != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := bs.writeCompressedFile(bs.getExtendedCommitPath(height), extendedCommitData); err != nil {
						errChan <- fmt.Errorf("error writing extended commit file: %w", err)
					}
				}()
			}
			if regularCommitData != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := bs.writeCompressedFile(bs.getCommitPath(height), regularCommitData); err != nil {
						errChan <- fmt.Errorf("error writing regular commit file: %w", err)
					}
				}()
			}
		}

		wg.Wait()
		close(errChan)

		for err := range errChan {
			panic(err)
		}
	} else {
		// Sequential processing for small blocks
		if err := bs.writeCompressedFile(bs.getBlockPath(height), blockData); err != nil {
			panic(fmt.Sprintf("Error writing block file: %v", err))
		}
		if err := bs.writeCompressedFile(bs.getMetaPath(height), metaData); err != nil {
			panic(fmt.Sprintf("Error writing block meta file: %v", err))
		}

		if seenExtendedCommit != nil {
			if err := bs.writeCompressedFile(bs.getExtendedCommitPath(height), extendedCommitData); err != nil {
				panic(fmt.Sprintf("Error writing extended commit file: %v", err))
			}
			if err := bs.writeCompressedFile(bs.getCommitPath(height), regularCommitData); err != nil {
				panic(fmt.Sprintf("Error writing regular commit file: %v", err))
			}
		}
	}

	// Update state and caches
	if bs.height < height {
		bs.height = height
	}
	if bs.base == 0 {
		bs.base = height
	}

	if seenExtendedCommit != nil {
		bs.blockExtendedCommitCache.Add(height, seenExtendedCommit)
	}

	if err := bs.saveBlockStoreState(); err != nil {
		panic(fmt.Sprintf("Error saving block store state: %v", err))
	}

	// Update hash index
	blockMeta := types.NewBlockMeta(block, blockParts)
	bs.updateHashIndex(block.Height, blockMeta.BlockID.Hash)
}

// Add this method to write compressed data with buffering
func (bs *FileBlockStore) writeCompressedFile(path string, data []byte) error {
	// Create directory if it doesn't exist (writeFileWithBuffer also does this, but good to be sure)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Compress data only if it's above the threshold
	var dataToWrite []byte
	var err error
	if len(data) >= compressionThreshold {
		dataToWrite, err = compressData(data) // compressData itself does not check threshold internally
		if err != nil {
			return fmt.Errorf("compression failed for %s: %w", path, err)
		}
	} else {
		dataToWrite = data // Write original data if below threshold
	}

	return bs.writeFileWithBuffer(path, dataToWrite)
}

// SaveTxInfo implements the BlockStore interface
func (bs *FileBlockStore) SaveTxInfo(block *types.Block, txResponseCodes []uint32, logs []string) error {
	if block == nil {
		return fmt.Errorf("cannot save tx info for nil block")
	}

	if len(txResponseCodes) != len(block.Data.Txs) {
		return fmt.Errorf("number of response codes (%d) does not match number of transactions (%d)",
			len(txResponseCodes), len(block.Data.Txs))
	}

	if len(logs) != len(block.Data.Txs) {
		return fmt.Errorf("number of logs (%d) does not match number of transactions (%d)",
			len(logs), len(block.Data.Txs))
	}

	// Create tx info directory if it doesn't exist
	txInfoDir := filepath.Join(bs.baseDir, "tx_info")
	if err := os.MkdirAll(txInfoDir, 0755); err != nil {
		return fmt.Errorf("failed to create tx info directory: %w", err)
	}

	// Save tx info for each transaction
	for i, tx := range block.Data.Txs {
		currentLog := logs[i]
		if txResponseCodes[i] == abci.CodeTypeOK {
			currentLog = ""
		}

		txInfo := &cmtstore.TxInfo{
			Height: block.Height,
			Index:  uint32(i),
			Code:   txResponseCodes[i],
			Error:  currentLog,
		}

		txInfoData, err := proto.Marshal(txInfo)
		if err != nil {
			return fmt.Errorf("failed to marshal tx info: %w", err)
		}

		txHash := tx.Hash()
		txInfoPath := filepath.Join(txInfoDir, fmt.Sprintf("%x.tx", txHash))
		if err := os.WriteFile(txInfoPath, txInfoData, 0644); err != nil {
			return fmt.Errorf("failed to write tx info file: %w", err)
		}
	}

	return nil
}

// LoadTxInfo loads transaction info for a given transaction hash
func (bs *FileBlockStore) LoadTxInfo(txHash []byte) *cmtstore.TxInfo {
	bs.mtx.RLock() // Should be RLock as it's a read operation
	defer bs.mtx.RUnlock()

	// Read the tx info file
	txInfoPath := filepath.Join(bs.baseDir, "tx_info", fmt.Sprintf("%x.tx", txHash))
	data, err := os.ReadFile(txInfoPath)
	if err != nil {
		return nil
	}

	// Unmarshal the tx info
	var txInfo cmtstore.TxInfo
	err = proto.Unmarshal(data, &txInfo)
	if err != nil {
		return nil
	}

	return &txInfo
}

// LoadBaseMeta implements the BlockStore interface
func (bs *FileBlockStore) LoadBaseMeta() *types.BlockMeta {
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()
	if bs.base == 0 {
		return nil
	}
	return bs.LoadBlockMeta(bs.base)
}

// LoadBlockMetaByHash implements the BlockStore interface
func (bs *FileBlockStore) LoadBlockMetaByHash(hash []byte) *types.BlockMeta {
	// First find the height from the hash
	height, err := bs.getHeightFromHash(hash)
	if err != nil {
		return nil
	}
	return bs.LoadBlockMeta(height)
}

// Base returns the first known contiguous block height, or 0 for empty block stores.
func (bs *FileBlockStore) Base() int64 {
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()
	return bs.base
}

// Height returns the last known contiguous block height, or 0 for empty block stores.
func (bs *FileBlockStore) Height() int64 {
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()
	return bs.height
}

// Size returns the number of blocks in the block store.
func (bs *FileBlockStore) Size() int64 {
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()
	if bs.height == 0 {
		return 0
	}
	return bs.height - bs.base + 1
}

// IsEmpty returns true if the block store is empty
func (bs *FileBlockStore) IsEmpty() bool {
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()
	return bs.height == 0
}

// loadHashIndex attempts to load the hash-to-height index from disk.
func (bs *FileBlockStore) loadHashIndex() (map[string]int64, error) {
	indexPath := bs.getHashIndexPath()
	file, err := os.Open(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]int64), nil // Return empty map if not found, not an error
		}
		return nil, fmt.Errorf("failed to open hash index file %s: %w", indexPath, err)
	}
	defer file.Close()

	// Use a buffered reader for potentially better performance with gob
	bufReader := bufio.NewReader(file)
	decoder := gob.NewDecoder(bufReader)
	var index map[string]int64
	if err := decoder.Decode(&index); err != nil {
		if err == io.EOF {
			return make(map[string]int64), nil
		}
		return nil, fmt.Errorf("failed to decode hash index from %s: %w", indexPath, err)
	}
	return index, nil
}

// saveHashIndex saves the current in-memory hash-to-height index to disk.
// This function creates a copy of the map to avoid holding the lock for too long during I/O.
func (bs *FileBlockStore) saveHashIndex() error {
	bs.hashToHeightMu.RLock()
	// Create a copy of the map to avoid holding the lock during file I/O
	indexToSave := make(map[string]int64, len(bs.hashToHeight))
	for k, v := range bs.hashToHeight {
		indexToSave[k] = v
	}
	bs.hashToHeightMu.RUnlock()

	if len(indexToSave) == 0 {
		return nil
	}

	indexPath := bs.getHashIndexPath()
	file, err := os.OpenFile(indexPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open hash index file %s for writing: %w", indexPath, err)
	}
	defer file.Close()

	bufWriter := bufio.NewWriter(file)
	encoder := gob.NewEncoder(bufWriter)
	if err := encoder.Encode(indexToSave); err != nil {
		return fmt.Errorf("failed to encode hash index to %s: %w", indexPath, err)
	}
	return bufWriter.Flush()
}

// initHashIndex initializes the hash index, trying to load from disk first.
func (bs *FileBlockStore) initHashIndex() error {
	bs.hashToHeightMu.Lock()

	loadedIndex, err := bs.loadHashIndex() // This is called without the lock held by this func
	if err != nil {
		fmt.Printf("Failed to load hash index from disk (error: %v), will rebuild by scanning.\n", err)
		bs.hashToHeight = make(map[string]int64)
	} else if len(loadedIndex) > 0 {
		bs.hashToHeight = loadedIndex
		fmt.Printf("Successfully loaded hash index from disk with %d entries.\n", len(bs.hashToHeight))
		bs.hashToHeightMu.Unlock()
		return nil
	} else {
		bs.hashToHeight = make(map[string]int64)
	}

	// Fallback: If index not loaded or empty, build it by scanning directories
	// The lock is still held here.
	blocksDir := filepath.Join(bs.baseDir, dataDir, blocksDir)
	entries, readDirErr := os.ReadDir(blocksDir)
	if readDirErr != nil {
		if os.IsNotExist(readDirErr) {
			bs.hashToHeightMu.Unlock()
			return bs.saveHashIndex()
		}
		bs.hashToHeightMu.Unlock() // Unlock before returning error
		return fmt.Errorf("failed to read blocks directory for hash index init: %w", readDirErr)
	}

	rangeDirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(entry.Name(), "-") {
			rangeDirs = append(rangeDirs, entry.Name())
		}
	}
	sort.Strings(rangeDirs)

	for _, rangeDir := range rangeDirs {
		bounds := strings.Split(rangeDir, "-")
		if len(bounds) != 2 {
			continue
		}
		start, convErr := strconv.ParseInt(bounds[0], 10, 64)
		if convErr != nil {
			continue
		}
		end, convErr := strconv.ParseInt(bounds[1], 10, 64)
		if convErr != nil {
			continue
		}

		metaDir := filepath.Join(blocksDir, rangeDir)
		metaEntries, readErr := os.ReadDir(metaDir)
		if readErr != nil {
			continue
		}

		for _, metaEntry := range metaEntries {
			if !metaEntry.IsDir() && strings.HasPrefix(metaEntry.Name(), "meta_") && strings.HasSuffix(metaEntry.Name(), ".proto") {
				heightStr := strings.TrimPrefix(metaEntry.Name(), "meta_")
				heightStr = strings.TrimSuffix(heightStr, ".proto")
				height, parseErr := strconv.ParseInt(heightStr, 10, 64)
				if parseErr != nil || height < start || height > end {
					if parseErr != nil {
						fmt.Printf("Warning: failed to parse height from meta file %s: %v\n", metaEntry.Name(), parseErr)
					}
					continue
				}

				metaFilePath := bs.getMetaPath(height)
				metaDataBytes, err := os.ReadFile(metaFilePath)
				if err != nil {
					fmt.Printf("Warning: failed to read meta file %s for hash index: %v\n", metaFilePath, err)
					continue
				}
				pbbm := new(cmtproto.BlockMeta)
				if err := proto.Unmarshal(metaDataBytes, pbbm); err != nil {
					fmt.Printf("Warning: failed to unmarshal meta file %s for hash index: %v\n", metaFilePath, err)
					continue
				}
				meta, err := types.BlockMetaFromTrustedProto(pbbm)
				if err != nil {
					fmt.Printf("Warning: failed to convert meta from proto for %s for hash index: %v\n", metaFilePath, err)
					continue
				}

				if meta != nil && len(meta.BlockID.Hash) > 0 {
					bs.hashToHeight[string(meta.BlockID.Hash)] = height
				}
			}
		}
	}

	bs.hashToHeightMu.Unlock()

	saveErr := bs.saveHashIndex()
	if saveErr != nil {
		fmt.Printf("Warning: failed to save newly built hash index to disk: %v\n", saveErr)
	}
	return saveErr
}

// updateHashIndex updates the in-memory hash index.
// It no longer persists to disk on every call to improve SaveBlock performance.
func (bs *FileBlockStore) updateHashIndex(height int64, hash []byte) {
	if len(hash) == 0 {
		return
	}
	bs.hashToHeightMu.Lock()
	bs.hashToHeight[string(hash)] = height
	bs.hashToHeightMu.Unlock()
}

// Add back the writeFileWithBuffer helper function
func (bs *FileBlockStore) writeFileWithBuffer(path string, data []byte) error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Create file with buffer
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer file.Close()

	// Use buffered writer
	writer := bufio.NewWriterSize(file, 32*1024) // 32KB buffer
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("failed to write to file %s: %w", path, err)
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush buffer for file %s: %w", path, err)
	}

	return nil
}
