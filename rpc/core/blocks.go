package core

import (
	"errors"
	"fmt"
	"sort"

	"github.com/tendermint/tendermint/crypto/merkle"
	blockidxnull "github.com/tendermint/tendermint/state/indexer/block/null"

	tmmath "github.com/tendermint/tendermint/libs/math"
	tmquery "github.com/tendermint/tendermint/libs/pubsub/query"
	"github.com/tendermint/tendermint/pkg/consts"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	"github.com/tendermint/tendermint/types"
)

// BlockchainInfo gets block headers for minHeight <= height <= maxHeight.
// Block headers are returned in descending order (highest first).
// More: https://docs.tendermint.com/master/rpc/#/Info/blockchain
func BlockchainInfo(ctx *rpctypes.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	// maximum 20 block metas
	const limit int64 = 20
	var err error
	env := GetEnvironment()
	minHeight, maxHeight, err = filterMinMax(
		env.BlockStore.Base(),
		env.BlockStore.Height(),
		minHeight,
		maxHeight,
		limit)
	if err != nil {
		return nil, err
	}
	env.Logger.Debug("BlockchainInfoHandler", "maxHeight", maxHeight, "minHeight", minHeight)

	blockMetas := []*types.BlockMeta{}
	for height := maxHeight; height >= minHeight; height-- {
		blockMeta := env.BlockStore.LoadBlockMeta(height)
		blockMetas = append(blockMetas, blockMeta)
	}

	return &ctypes.ResultBlockchainInfo{
		LastHeight: env.BlockStore.Height(),
		BlockMetas: blockMetas}, nil
}

// error if either min or max are negative or min > max
// if 0, use blockstore base for min, latest block height for max
// enforce limit.
func filterMinMax(base, height, min, max, limit int64) (int64, int64, error) {
	// filter negatives
	if min < 0 || max < 0 {
		return min, max, fmt.Errorf("heights must be non-negative")
	}

	// adjust for default values
	if min == 0 {
		min = 1
	}
	if max == 0 {
		max = height
	}

	// limit max to the height
	max = tmmath.MinInt64(height, max)

	// limit min to the base
	min = tmmath.MaxInt64(base, min)

	// limit min to within `limit` of max
	// so the total number of blocks returned will be `limit`
	min = tmmath.MaxInt64(min, max-limit+1)

	if min > max {
		return min, max, fmt.Errorf("min height %d can't be greater than max height %d", min, max)
	}
	return min, max, nil
}

// Block gets block at a given height.
// If no height is provided, it will fetch the latest block.
// More: https://docs.tendermint.com/master/rpc/#/Info/block
func Block(ctx *rpctypes.Context, heightPtr *int64) (*ctypes.ResultBlock, error) {
	height, err := getHeight(GetEnvironment().BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	block := GetEnvironment().BlockStore.LoadBlock(height)
	blockMeta := GetEnvironment().BlockStore.LoadBlockMeta(height)
	if blockMeta == nil {
		return &ctypes.ResultBlock{BlockID: types.BlockID{}, Block: block}, nil
	}
	return &ctypes.ResultBlock{BlockID: blockMeta.BlockID, Block: block}, nil
}

// BlockByHash gets block by hash.
// More: https://docs.tendermint.com/master/rpc/#/Info/block_by_hash
func BlockByHash(ctx *rpctypes.Context, hash []byte) (*ctypes.ResultBlock, error) {
	env := GetEnvironment()
	block := env.BlockStore.LoadBlockByHash(hash)
	if block == nil {
		return &ctypes.ResultBlock{BlockID: types.BlockID{}, Block: nil}, nil
	}
	// If block is not nil, then blockMeta can't be nil.
	blockMeta := env.BlockStore.LoadBlockMeta(block.Height)
	return &ctypes.ResultBlock{BlockID: blockMeta.BlockID, Block: block}, nil
}

// Commit gets block commit at a given height.
// If no height is provided, it will fetch the commit for the latest block.
// More: https://docs.tendermint.com/master/rpc/#/Info/commit
func Commit(ctx *rpctypes.Context, heightPtr *int64) (*ctypes.ResultCommit, error) {
	env := GetEnvironment()
	height, err := getHeight(env.BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	blockMeta := env.BlockStore.LoadBlockMeta(height)
	if blockMeta == nil {
		return nil, nil
	}
	header := blockMeta.Header

	// If the next block has not been committed yet,
	// use a non-canonical commit
	if height == env.BlockStore.Height() {
		commit := env.BlockStore.LoadSeenCommit(height)
		return ctypes.NewResultCommit(&header, commit, false), nil
	}

	// Return the canonical commit (comes from the block at height+1)
	commit := env.BlockStore.LoadBlockCommit(height)
	return ctypes.NewResultCommit(&header, commit, true), nil
}

// DataCommitment collects the data roots over a provided ordered range of blocks,
// and then creates a new Merkle root of those data roots.
func DataCommitment(ctx *rpctypes.Context, beginBlock uint64, endBlock uint64) (*ctypes.ResultDataCommitment, error) {
	err := validateDataCommitmentRange(beginBlock, endBlock)
	if err != nil {
		return nil, err
	}
	heights := generateHeightsList(beginBlock, endBlock)
	blockResults := fetchBlocks(heights, len(heights), 0)
	root := hashDataRoots(blockResults)
	// Create data commitment
	return &ctypes.ResultDataCommitment{DataCommitment: root}, nil
}

// generateHeightsList takes a begin and end block, then generates a list of heights
// containing the elements of the range [beginBlock, endBlock].
func generateHeightsList(beginBlock uint64, endBlock uint64) []int64 {
	heights := make([]int64, endBlock-beginBlock+1)
	for i := beginBlock; i <= endBlock; i++ {
		heights[i-beginBlock] = int64(i)
	}
	return heights
}

// validateDataCommitmentRange runs basic checks on the asc sorted list of heights
// that will be used subsequently in generating data commitments over the defined set of heights.
func validateDataCommitmentRange(beginBlock uint64, endBlock uint64) error {
	env := GetEnvironment()
	heightsRange := endBlock - beginBlock + 1
	if heightsRange > uint64(consts.DataCommitmentBlocksLimit) {
		return fmt.Errorf("the query exceeds the limit of allowed blocks %d", consts.DataCommitmentBlocksLimit)
	}
	if heightsRange == 0 {
		return fmt.Errorf("cannot create the data commitments for an empty set of blocks")
	}
	if beginBlock > endBlock {
		return fmt.Errorf("end block is smaller than begin block")
	}
	if endBlock > uint64(env.BlockStore.Height()) {
		return fmt.Errorf(
			"end block %d is higher than current chain height %d",
			endBlock,
			env.BlockStore.Height(),
		)
	}
	has, err := env.BlockIndexer.Has(int64(endBlock))
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf(
			"end block %d is still not indexed",
			endBlock,
		)
	}
	return nil
}

// hashDataRoots hashes a list of blocks data hashes and returns their merkle root.
func hashDataRoots(blocks []*ctypes.ResultBlock) []byte {
	dataRoots := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		dataRoots = append(dataRoots, block.Block.DataHash)
	}
	root := merkle.HashFromByteSlices(dataRoots)
	return root
}

// BlockResults gets ABCIResults at a given height.
// If no height is provided, it will fetch results for the latest block.
//
// Results are for the height of the block containing the txs.
// Thus response.results.deliver_tx[5] is the results of executing
// getBlock(h).Txs[5]
// More: https://docs.tendermint.com/master/rpc/#/Info/block_results
func BlockResults(ctx *rpctypes.Context, heightPtr *int64) (*ctypes.ResultBlockResults, error) {
	env := GetEnvironment()
	height, err := getHeight(env.BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	results, err := env.StateStore.LoadABCIResponses(height)
	if err != nil {
		return nil, err
	}

	return &ctypes.ResultBlockResults{
		Height:                height,
		TxsResults:            results.DeliverTxs,
		BeginBlockEvents:      results.BeginBlock.Events,
		EndBlockEvents:        results.EndBlock.Events,
		ValidatorUpdates:      results.EndBlock.ValidatorUpdates,
		ConsensusParamUpdates: results.EndBlock.ConsensusParamUpdates,
	}, nil
}

// BlockSearch searches for a paginated set of blocks matching BeginBlock and
// EndBlock event search criteria.
func BlockSearch(
	ctx *rpctypes.Context,
	query string,
	pagePtr, perPagePtr *int,
	orderBy string,
) (*ctypes.ResultBlockSearch, error) {

	results, err := heightsByQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	// sort results (must be done before pagination)
	err = sortBlocks(results, orderBy)
	if err != nil {
		return nil, err
	}

	// paginate results
	totalCount := len(results)
	perPage := validatePerPage(perPagePtr)

	page, err := validatePage(pagePtr, perPage, totalCount)
	if err != nil {
		return nil, err
	}

	skipCount := validateSkipCount(page, perPage)
	pageSize := tmmath.MinInt(perPage, totalCount-skipCount)

	apiResults := fetchBlocks(results, pageSize, skipCount)

	return &ctypes.ResultBlockSearch{Blocks: apiResults, TotalCount: totalCount}, nil
}

// heightsByQuery returns a list of heights corresponding to the provided query.
func heightsByQuery(ctx *rpctypes.Context, query string) ([]int64, error) {
	env := GetEnvironment()
	// skip if block indexing is disabled
	if _, ok := env.BlockIndexer.(*blockidxnull.BlockerIndexer); ok {
		return nil, errors.New("block indexing is disabled")
	}

	q, err := tmquery.New(query)
	if err != nil {
		return nil, err
	}

	results, err := env.BlockIndexer.Search(ctx.Context(), q)
	return results, err
}

// sortBlocks takes a list of block heights and sorts them according to the order: "asc" or "desc".
// If `orderBy` is blank, then it is considered descending.
func sortBlocks(results []int64, orderBy string) error {
	switch orderBy {
	case "desc", "":
		sort.Slice(results, func(i, j int) bool { return results[i] > results[j] })

	case "asc":
		sort.Slice(results, func(i, j int) bool { return results[i] < results[j] })

	default:
		return errors.New("expected order_by to be either `asc` or `desc` or empty")
	}
	return nil
}

// fetchBlocks takes a list of block heights and fetches them.
func fetchBlocks(results []int64, pageSize int, skipCount int) []*ctypes.ResultBlock {
	env := GetEnvironment()
	apiResults := make([]*ctypes.ResultBlock, 0, pageSize)
	for i := skipCount; i < skipCount+pageSize; i++ {
		block := env.BlockStore.LoadBlock(results[i])
		if block != nil {
			blockMeta := env.BlockStore.LoadBlockMeta(block.Height)
			if blockMeta != nil {
				apiResults = append(apiResults, &ctypes.ResultBlock{
					Block:   block,
					BlockID: blockMeta.BlockID,
				})
			}
		}
	}
	return apiResults
}
