package store

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbm "github.com/cometbft/cometbft-db"
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

	// Compression settings
	compressionThreshold = 1024 * 64 // 64KB minimum size for compression
	// Cache settings
	defaultCacheSize  = 100
	extendedCacheSize = 1000
	blockCacheSize    = 50  // Fewer blocks but larger size
	metaCacheSize     = 200 // More metadata entries as they're smaller
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

	// Database for hash -> height index
	db dbm.DB
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

	// Open the database for hash -> height index
	db, err := dbm.NewDB("hash_index", "goleveldb", filepath.Join(baseDir, "hash_index"))
	if err != nil {
		return nil, fmt.Errorf("failed to create hash index database: %w", err)
	}

	bs := &FileBlockStore{
		baseDir: baseDir,
		base:    state.Base,
		height:  state.Height,
		db:      db,
	}

	if err := bs.addCaches(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to add caches: %w", err)
	}

	if err := bs.initHashIndex(); err != nil {
		db.Close()
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
	return bs.db.Close()
}

// LoadBlock implements the BlockStore interface
func (bs *FileBlockStore) LoadBlock(height int64) *types.Block {
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()

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

	// Read and decompress block file directly without checking meta
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
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()

	// Check meta cache first
	if cachedMetaBytes, ok := bs.metaCache.Get(height); ok {
		pbbm := new(cmtproto.BlockMeta)
		if err := proto.Unmarshal(cachedMetaBytes, pbbm); err != nil {
			panic(fmt.Sprintf("Error unmarshaling cached block meta: %v", err))
		}
		blockMeta, err := types.BlockMetaFromTrustedProto(pbbm)
		if err != nil {
			panic(fmt.Sprintf("Error converting cached block meta from proto: %v", err))
		}
		return blockMeta
	}

	// Load the block directly without recursion
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

	// Create block meta from block
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	if err != nil {
		panic(fmt.Sprintf("Error creating part set for block %d: %v", height, err))
	}
	blockMeta := types.NewBlockMeta(block, partSet)

	// Cache the meta information
	metaProto := blockMeta.ToProto()
	metaData, err := proto.Marshal(metaProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block meta: %v", err))
	}
	bs.metaCache.Add(height, metaData)

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
func (bs *FileBlockStore) SaveBlock(block *types.Block, blockParts *types.PartSet, seenCommit *types.Commit) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	height := block.Height

	// Create height range directory
	rangeDir := getHeightRangeDir(height)
	blockRangeDir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir)
	if err := os.MkdirAll(blockRangeDir, 0755); err != nil {
		panic(fmt.Sprintf("Error creating directory %s: %v", blockRangeDir, err))
	}

	// Prepare block data
	blockProto, err := block.ToProto()
	if err != nil {
		panic(fmt.Sprintf("Error converting block to proto: %v", err))
	}
	blockData, err := proto.Marshal(blockProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block: %v", err))
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

	// Save block data
	compressedBlockData, err := compressData(blockData)
	if err != nil {
		panic(fmt.Sprintf("Error compressing block data: %v", err))
	}
	if err := bs.writeFileWithBuffer(bs.getBlockPath(height), compressedBlockData); err != nil {
		panic(fmt.Sprintf("Error writing block file: %v", err))
	}

	// Save commit data if available
	if commitData != nil {
		if err := bs.writeFileWithBuffer(bs.getCommitPath(height), commitData); err != nil {
			panic(fmt.Sprintf("Error writing commit file: %v", err))
		}
	}

	// Update hash index
	bs.updateHashIndex(height, block.Hash())

	// Update block store state
	if height > bs.height {
		bs.height = height
	}
	if bs.base == 0 {
		bs.base = height
	}
	if err := bs.saveBlockStoreState(); err != nil {
		panic(fmt.Sprintf("Error saving block store state: %v", err))
	}
}

// Helper function to get height from hash using the KV database
func (bs *FileBlockStore) getHeightFromHash(hash []byte) (int64, error) {
	if len(hash) == 0 {
		return 0, fmt.Errorf("empty hash")
	}

	bz, err := bs.db.Get(calcBlockHashKey(hash))
	if err != nil {
		return 0, fmt.Errorf("failed to get height from hash: %w", err)
	}
	if len(bz) == 0 {
		return 0, fmt.Errorf("block with hash %x not found", hash)
	}

	height, err := strconv.ParseInt(string(bz), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse height from %s: %w", string(bz), err)
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
	bs.mtx.RLock()
	defer bs.mtx.RUnlock()

	// Check cache first
	if commit, ok := bs.seenCommitCache.Get(height); ok {
		return commit
	}

	// Try loading from extended commit first
	extCommit := bs.LoadBlockExtendedCommit(height)
	if extCommit != nil {
		commit := extCommit.ToCommit()
		bs.seenCommitCache.Add(height, commit)
		return commit
	}

	// Try loading from regular commit file
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
	bs.seenCommitCache.Add(height, commit)

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

// PruneBlocks removes blocks up to the given height
func (bs *FileBlockStore) PruneBlocks(height int64, state sm.State) (uint64, int64, error) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	if height <= 0 {
		return 0, 0, fmt.Errorf("invalid height %d", height)
	}
	if height > bs.height {
		return 0, 0, fmt.Errorf("cannot prune beyond the latest height %d", bs.height)
	}
	if height < bs.base {
		return 0, 0, fmt.Errorf("cannot prune below base height %d", bs.base)
	}

	pruned := uint64(0)
	previousBase := bs.base

	// Group files by range directory for more efficient deletion
	rangeMap := make(map[string][]int64)
	for h := bs.base; h <= height; h++ {
		rangeDir := getHeightRangeDir(h)
		rangeMap[rangeDir] = append(rangeMap[rangeDir], h)
	}

	// Process each range directory
	for rangeDir, heights := range rangeMap {
		// Remove files for each height in this range
		for _, h := range heights {
			// Remove block file
			if err := os.Remove(bs.getBlockPath(h)); err != nil && !os.IsNotExist(err) {
				return pruned, previousBase, fmt.Errorf("failed to remove block file at height %d: %w", h, err)
			}

			// Remove commit files
			if err := os.Remove(bs.getCommitPath(h)); err != nil && !os.IsNotExist(err) {
				return pruned, previousBase, fmt.Errorf("failed to remove commit file at height %d: %w", h, err)
			}
			if err := os.Remove(bs.getExtendedCommitPath(h)); err != nil && !os.IsNotExist(err) {
				return pruned, previousBase, fmt.Errorf("failed to remove extended commit file at height %d: %w", h, err)
			}

			// Clear caches
			bs.blockDataCache.Remove(h)
			bs.metaCache.Remove(h)
			bs.blockCommitCache.Remove(h)
			bs.blockExtendedCommitCache.Remove(h)
			bs.seenCommitCache.Remove(h)

			pruned++
		}

		// Try to remove the range directory if empty
		rangePath := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir)
		if empty, err := isDirEmpty(rangePath); err == nil && empty {
			if err := os.Remove(rangePath); err != nil && !os.IsNotExist(err) {
				// Non-critical error, just log it
				fmt.Printf("Warning: failed to remove empty range directory %s: %v\n", rangePath, err)
			}
		}
	}

	// Update base height to be one more than the pruned height
	bs.base = height + 1

	// Save updated state
	if err := bs.saveBlockStoreState(); err != nil {
		return pruned, previousBase, fmt.Errorf("failed to save block store state: %w", err)
	}

	return pruned, previousBase, nil
}

// Helper function to check if a directory is empty
func isDirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

// DeleteLatestBlock implements the BlockStore interface
func (bs *FileBlockStore) DeleteLatestBlock() error {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	if bs.height == 0 {
		return fmt.Errorf("no blocks to delete")
	}

	height := bs.height

	// Remove block file
	if err := os.Remove(bs.getBlockPath(height)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove block file: %w", err)
	}

	// Remove commit files
	if err := os.Remove(bs.getCommitPath(height)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove commit file: %w", err)
	}
	if err := os.Remove(bs.getExtendedCommitPath(height)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove extended commit file: %w", err)
	}

	// Clear caches
	bs.blockDataCache.Remove(height)
	bs.metaCache.Remove(height)
	bs.blockCommitCache.Remove(height)
	bs.blockExtendedCommitCache.Remove(height)
	bs.seenCommitCache.Remove(height)

	// Update state
	bs.height--
	if bs.height < bs.base {
		bs.base = bs.height
	}

	return bs.saveBlockStoreState()
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
	data      []byte
	timestamp time.Time
}

// SaveBlockWithExtendedCommit saves a block with extended commit information
func (bs *FileBlockStore) SaveBlockWithExtendedCommit(block *types.Block, blockParts *types.PartSet, seenExtendedCommit *types.ExtendedCommit) {
	bs.mtx.Lock()
	defer bs.mtx.Unlock()

	height := block.Height

	// Create height range directory
	rangeDir := getHeightRangeDir(height)
	blockRangeDir := filepath.Join(bs.baseDir, dataDir, blocksDir, rangeDir)
	if err := os.MkdirAll(blockRangeDir, 0755); err != nil {
		panic(fmt.Sprintf("Error creating directory %s: %v", blockRangeDir, err))
	}

	// Prepare block data
	blockProto, err := block.ToProto()
	if err != nil {
		panic(fmt.Sprintf("Error converting block to proto: %v", err))
	}
	blockData, err := proto.Marshal(blockProto)
	if err != nil {
		panic(fmt.Sprintf("Error marshaling block: %v", err))
	}

	// Prepare extended commit data
	var extendedCommitData []byte
	if seenExtendedCommit != nil {
		extendedCommitProto := seenExtendedCommit.ToProto()
		extendedCommitData, err = proto.Marshal(extendedCommitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling extended commit: %v", err))
		}

		// Also save regular commit for backward compatibility
		commitProto := seenExtendedCommit.ToCommit().ToProto()
		commitData, err := proto.Marshal(commitProto)
		if err != nil {
			panic(fmt.Sprintf("Error marshaling commit: %v", err))
		}
		if err := bs.writeFileWithBuffer(bs.getCommitPath(height), commitData); err != nil {
			panic(fmt.Sprintf("Error writing commit file: %v", err))
		}
	}

	// Save block data
	compressedBlockData, err := compressData(blockData)
	if err != nil {
		panic(fmt.Sprintf("Error compressing block data: %v", err))
	}
	if err := bs.writeFileWithBuffer(bs.getBlockPath(height), compressedBlockData); err != nil {
		panic(fmt.Sprintf("Error writing block file: %v", err))
	}

	// Save extended commit data if available
	if extendedCommitData != nil {
		if err := bs.writeFileWithBuffer(bs.getExtendedCommitPath(height), extendedCommitData); err != nil {
			panic(fmt.Sprintf("Error writing extended commit file: %v", err))
		}
	}

	// Update hash index
	bs.updateHashIndex(height, block.Hash())

	// Update block store state
	if height > bs.height {
		bs.height = height
	}
	if bs.base == 0 {
		bs.base = height
	}
	if err := bs.saveBlockStoreState(); err != nil {
		panic(fmt.Sprintf("Error saving block store state: %v", err))
	}

	// Update caches
	if seenExtendedCommit != nil {
		bs.seenCommitCache.Add(height, seenExtendedCommit.ToCommit())
		bs.blockExtendedCommitCache.Add(height, seenExtendedCommit)
		bs.blockCommitCache.Add(height, seenExtendedCommit.ToCommit())
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

// initHashIndex initializes the hash index by scanning blocks if needed
func (bs *FileBlockStore) initHashIndex() error {
	// Check if we have any entries in the database
	iter, err := bs.db.Iterator(nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}
	hasEntries := iter.Valid()
	iter.Close()

	if hasEntries {
		return nil // Database already has entries, no need to rebuild
	}

	// Scan blocks directory to build the index
	batch := bs.db.NewBatch()
	defer batch.Close()

	blocksDir := filepath.Join(bs.baseDir, dataDir, blocksDir)
	entries, err := os.ReadDir(blocksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read blocks directory: %w", err)
	}

	batchSize := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.Contains(entry.Name(), "-") {
			continue
		}

		// Process each block in the range directory
		rangeDir := filepath.Join(blocksDir, entry.Name())
		blocks, err := os.ReadDir(rangeDir)
		if err != nil {
			continue
		}

		for _, block := range blocks {
			if !strings.HasPrefix(block.Name(), "block_") {
				continue
			}

			heightStr := strings.TrimPrefix(block.Name(), "block_")
			heightStr = strings.TrimSuffix(heightStr, ".proto")
			height, err := strconv.ParseInt(heightStr, 10, 64)
			if err != nil {
				continue
			}

			// Load block to get its hash
			blockData := bs.LoadBlock(height)
			if blockData == nil {
				continue
			}

			hash := blockData.Hash()
			if err := batch.Set(calcBlockHashKey(hash), []byte(fmt.Sprintf("%d", height))); err != nil {
				return fmt.Errorf("failed to add hash->height mapping to batch: %w", err)
			}
			batchSize++

			// Write batch every 1000 entries to avoid memory issues
			if batchSize >= 1000 {
				if err := batch.Write(); err != nil {
					return fmt.Errorf("failed to write batch: %w", err)
				}
				batch.Close()
				batch = bs.db.NewBatch()
				batchSize = 0
			}
		}
	}

	if batchSize > 0 {
		if err := batch.Write(); err != nil {
			return fmt.Errorf("failed to write final batch: %w", err)
		}
	}

	return nil
}

// updateHashIndex updates the hash->height mapping in the database
func (bs *FileBlockStore) updateHashIndex(height int64, hash []byte) {
	if len(hash) == 0 {
		return
	}
	if err := bs.db.Set(calcBlockHashKey(hash), []byte(fmt.Sprintf("%d", height))); err != nil {
		panic(fmt.Sprintf("Failed to update hash index: %v", err))
	}
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
