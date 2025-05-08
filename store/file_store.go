package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	cmtstore "github.com/cometbft/cometbft/proto/tendermint/store"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
	"github.com/cosmos/gogoproto/proto"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	// Directory names for different store components
	dataDir       = "data"
	blocksDir     = "blocks"
	validatorsDir = "validators"
	consensusDir  = "consensus"
	stateDir      = "state"
	partsDir      = "parts"
	metaExt       = ".meta"

	// Height range size for directory organization
	heightRangeSize = 1000
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
//	      └── state.json
type FileBlockStore struct {
	baseDir string
	mtx     cmtsync.RWMutex
	base    int64
	height  int64

	// Caches
	seenCommitCache          *lru.Cache[int64, *types.Commit]
	blockCommitCache         *lru.Cache[int64, *types.Commit]
	blockExtendedCommitCache *lru.Cache[int64, *types.ExtendedCommit]
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

func (bs *FileBlockStore) getPartPath(height int64, index int) string {
	rangeDir := getHeightRangeDir(height)
	return filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, partsDir, fmt.Sprintf("part_%d_%d.proto", height, index))
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
		return nil, fmt.Errorf("failed to initialize caches: %w", err)
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
	// Nothing to do for file-based store
	return nil
}

// LoadBlock implements the BlockStore interface
func (bs *FileBlockStore) LoadBlock(height int64) *types.Block {
	blockMeta := bs.LoadBlockMeta(height)
	if blockMeta == nil {
		return nil
	}

	// Read block file
	blockPath := bs.getBlockPath(height)
	data, err := os.ReadFile(blockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		panic(fmt.Sprintf("Error reading block file: %v", err))
	}

	pbb := new(cmtproto.Block)
	if err := proto.Unmarshal(data, pbb); err != nil {
		panic(fmt.Sprintf("Error unmarshaling block: %v", err))
	}

	block, err := types.BlockFromProto(pbb)
	if err != nil {
		panic(fmt.Sprintf("Error converting block from proto: %v", err))
	}

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

// LoadBlockPart implements the BlockStore interface
func (bs *FileBlockStore) LoadBlockPart(height int64, index int) *types.Part {
	partPath := bs.getPartPath(height, index)
	data, err := os.ReadFile(partPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		panic(fmt.Sprintf("Error reading block part file: %v", err))
	}

	pbpart := new(cmtproto.Part)
	if err := proto.Unmarshal(data, pbpart); err != nil {
		panic(fmt.Sprintf("Error unmarshaling block part: %v", err))
	}

	part, err := types.PartFromProto(pbpart)
	if err != nil {
		panic(fmt.Sprintf("Error converting block part from proto: %v", err))
	}

	return part
}

// SaveBlock implements the BlockStore interface
//
// This method saves a block and its associated data to the filesystem.
// The block is saved in multiple files:
// 1. Block file: Contains the full block data
// 2. Meta file: Contains block metadata
// 3. Part files: Contains block parts
// 4. Commit file: Contains the commit data
//
// Note: This method panics on errors to match the behavior of the original BlockStore.
// Future versions should consider returning errors instead.
func (bs *FileBlockStore) SaveBlock(block *types.Block, blockParts *types.PartSet, seenCommit *types.Commit) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	height := block.Height

	// Create height range directory and its subdirectories
	rangeDir := getHeightRangeDir(height)
	dirs := []string{
		filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir),
		filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, partsDir),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(fmt.Sprintf("Error creating directory %s: %v", dir, err))
		}
	}

	// Save block
	blockProto, err := block.ToProto()
	if err != nil {
		panic(fmt.Sprintf("Error converting block to proto: %v", err))
	}

	blockData, err := proto.Marshal(blockProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block: %v", err))
	}

	if err := os.WriteFile(bs.getBlockPath(height), blockData, 0644); err != nil {
		panic(fmt.Sprintf("Error writing block file: %v", err))
	}

	// Save block meta
	blockMeta := types.NewBlockMeta(block, blockParts)
	metaProto := blockMeta.ToProto()
	metaData, err := proto.Marshal(metaProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block meta: %v", err))
	}

	if err := os.WriteFile(bs.getMetaPath(height), metaData, 0644); err != nil {
		panic(fmt.Sprintf("Error writing block meta file: %v", err))
	}

	// Save block parts
	total := int(blockParts.Total())
	for i := 0; i < total; i++ {
		part := blockParts.GetPart(i)
		partProto, err := part.ToProto()
		if err != nil {
			panic(fmt.Sprintf("Error converting part to proto: %v", err))
		}
		partData, err := proto.Marshal(partProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling block part: %v", err))
		}

		if err := os.WriteFile(bs.getPartPath(height, i), partData, 0644); err != nil {
			panic(fmt.Sprintf("Error writing block part file: %v", err))
		}
	}

	// Save commit
	if seenCommit != nil {
		commitProto := seenCommit.ToProto()
		commitData, err := proto.Marshal(commitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling commit: %v", err))
		}

		if err := os.WriteFile(bs.getCommitPath(height), commitData, 0644); err != nil {
			panic(fmt.Sprintf("Error writing commit file: %v", err))
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
}

// Helper function to get height from hash
func (bs *FileBlockStore) getHeightFromHash(hash []byte) (int64, error) {
	blocksDir := filepath.Join(bs.baseDir, dataDir, blocksDir)
	entries, err := os.ReadDir(blocksDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read blocks directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rangeDir := filepath.Join(blocksDir, entry.Name())
		metaEntries, err := os.ReadDir(rangeDir)
		if err != nil {
			continue
		}
		for _, metaEntry := range metaEntries {
			if metaEntry.IsDir() {
				continue
			}
			if !strings.HasPrefix(metaEntry.Name(), "meta_") {
				continue
			}
			heightStr := strings.TrimPrefix(metaEntry.Name(), "meta_")
			heightStr = strings.TrimSuffix(heightStr, ".proto")
			height, err := strconv.ParseInt(heightStr, 10, 64)
			if err != nil {
				continue
			}
			meta := bs.LoadBlockMeta(height)
			if meta != nil && bytes.Equal(meta.BlockID.Hash, hash) {
				return height, nil
			}
		}
	}
	return 0, fmt.Errorf("block with hash %x not found", hash)
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
// It deletes all associated files (block, meta, parts, commit) for each pruned block.
//
// Note: This method should be used with caution as it permanently deletes data.
// Consider implementing a backup mechanism or making the deletion reversible.
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

	// Prune blocks
	for h := bs.base; h <= height; h++ {
		// Remove block files
		blockPath := bs.getBlockPath(h)
		if err := os.Remove(blockPath); err != nil && !os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("failed to remove block file at height %d: %w", h, err)
		}

		// Remove meta files
		metaPath := bs.getMetaPath(h)
		if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("failed to remove meta file at height %d: %w", h, err)
		}

		// Remove commit files
		commitPath := bs.getCommitPath(h)
		if err := os.Remove(commitPath); err != nil && !os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("failed to remove commit file at height %d: %w", h, err)
		}

		// Remove part files
		rangeDir := getHeightRangeDir(h)
		partsDir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, partsDir)
		entries, err := os.ReadDir(partsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, 0, fmt.Errorf("failed to read parts directory: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			// Check if the part file belongs to this height
			if len(entry.Name()) > 5 && entry.Name()[:len(entry.Name())-5] == fmt.Sprintf("%d_", h) {
				partPath := filepath.Join(partsDir, entry.Name())
				if err := os.Remove(partPath); err != nil && !os.IsNotExist(err) {
					return 0, 0, fmt.Errorf("failed to remove part file %s: %w", entry.Name(), err)
				}
			}
		}
	}

	// Update base height
	oldBase := bs.base
	bs.base = height + 1

	// Save state
	if err := bs.saveBlockStoreState(); err != nil {
		return 0, 0, fmt.Errorf("failed to save block store state: %w", err)
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

	height := bs.height

	// Remove block files
	blockPath := bs.getBlockPath(height)
	if err := os.Remove(blockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove block file: %w", err)
	}

	// Remove meta files
	metaPath := bs.getMetaPath(height)
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove meta file: %w", err)
	}

	// Remove commit files
	commitPath := bs.getCommitPath(height)
	if err := os.Remove(commitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove commit file: %w", err)
	}

	// Remove part files
	rangeDir := getHeightRangeDir(height)
	partsDir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, partsDir)
	entries, err := os.ReadDir(partsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read parts directory: %w", err)
		}
	} else {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			// Check if the part file belongs to this height
			if len(entry.Name()) > 5 && entry.Name()[:len(entry.Name())-5] == fmt.Sprintf("%d_", height) {
				partPath := filepath.Join(partsDir, entry.Name())
				if err := os.Remove(partPath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("failed to remove part file %s: %w", entry.Name(), err)
				}
			}
		}
	}

	// Update height
	bs.height = height - 1
	if bs.height < bs.base {
		bs.base = bs.height
	}

	// Save state
	if err := bs.saveBlockStoreState(); err != nil {
		return fmt.Errorf("failed to save block store state: %w", err)
	}

	return nil
}

// SaveBlockWithExtendedCommit implements the BlockStore interface
func (bs *FileBlockStore) SaveBlockWithExtendedCommit(block *types.Block, blockParts *types.PartSet, seenExtendedCommit *types.ExtendedCommit) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	height := block.Height

	// Create height range directory and its subdirectories
	rangeDir := getHeightRangeDir(height)
	dirs := []string{
		filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir),
		filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir, partsDir),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(fmt.Sprintf("Error creating directory %s: %v", dir, err))
		}
	}

	// Save block
	blockProto, err := block.ToProto()
	if err != nil {
		panic(fmt.Sprintf("Error converting block to proto: %v", err))
	}

	blockData, err := proto.Marshal(blockProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block: %v", err))
	}

	if err := os.WriteFile(bs.getBlockPath(height), blockData, 0644); err != nil {
		panic(fmt.Sprintf("Error writing block file: %v", err))
	}

	// Save block meta
	blockMeta := types.NewBlockMeta(block, blockParts)
	metaProto := blockMeta.ToProto()
	metaData, err := proto.Marshal(metaProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block meta: %v", err))
	}

	if err := os.WriteFile(bs.getMetaPath(height), metaData, 0644); err != nil {
		panic(fmt.Sprintf("Error writing block meta file: %v", err))
	}

	// Save block parts
	total := int(blockParts.Total())
	for i := 0; i < total; i++ {
		part := blockParts.GetPart(i)
		partProto, err := part.ToProto()
		if err != nil {
			panic(fmt.Sprintf("Error converting part to proto: %v", err))
		}
		partData, err := proto.Marshal(partProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling block part: %v", err))
		}

		if err := os.WriteFile(bs.getPartPath(height, i), partData, 0644); err != nil {
			panic(fmt.Sprintf("Error writing block part file: %v", err))
		}
	}

	// Save extended commit
	if seenExtendedCommit != nil {
		// Save as extended commit
		commitProto := seenExtendedCommit.ToProto()
		commitData, err := proto.Marshal(commitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling extended commit: %v", err))
		}

		if err := os.WriteFile(bs.getExtendedCommitPath(height), commitData, 0644); err != nil {
			panic(fmt.Sprintf("Error writing extended commit file: %v", err))
		}

		// Also save as regular commit for backward compatibility
		regularCommit := seenExtendedCommit.ToCommit()
		regularCommitProto := regularCommit.ToProto()
		regularCommitData, err := proto.Marshal(regularCommitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling regular commit: %v", err))
		}

		if err := os.WriteFile(bs.getCommitPath(height), regularCommitData, 0644); err != nil {
			panic(fmt.Sprintf("Error writing commit file: %v", err))
		}

		// Update cache
		bs.blockExtendedCommitCache.Add(height, seenExtendedCommit)
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
	bs.mtx.RLock()
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
