package core

import (
	"errors"
	"fmt"
	"sort"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/internal/state/indexer"
	"github.com/tendermint/tendermint/libs/bytes"
	tmmath "github.com/tendermint/tendermint/libs/math"
	tmquery "github.com/tendermint/tendermint/libs/pubsub/query"
	"github.com/tendermint/tendermint/pkg/consts"
	"github.com/tendermint/tendermint/rpc/coretypes"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	"github.com/tendermint/tendermint/types"
)

// BlockchainInfo gets block headers for minHeight <= height <= maxHeight.
//
// If maxHeight does not yet exist, blocks up to the current height will be
// returned. If minHeight does not exist (due to pruning), earliest existing
// height will be used.
//
// At most 20 items will be returned. Block headers are returned in descending
// order (highest first).
//
// More: https://docs.tendermint.com/master/rpc/#/Info/blockchain
func (env *Environment) BlockchainInfo(
	ctx *rpctypes.Context,
	minHeight, maxHeight int64) (*coretypes.ResultBlockchainInfo, error) {

	const limit int64 = 20

	var err error
	minHeight, maxHeight, err = filterMinMax(
		env.BlockStore.Base(),
		env.BlockStore.Height(),
		minHeight,
		maxHeight,
		limit)
	if err != nil {
		return nil, err
	}
	env.Logger.Debug("BlockchainInfo", "maxHeight", maxHeight, "minHeight", minHeight)

	blockMetas := make([]*types.BlockMeta, 0, maxHeight-minHeight+1)
	for height := maxHeight; height >= minHeight; height-- {
		blockMeta := env.BlockStore.LoadBlockMeta(height)
		if blockMeta != nil {
			blockMetas = append(blockMetas, blockMeta)
		}
	}

	return &coretypes.ResultBlockchainInfo{
		LastHeight: env.BlockStore.Height(),
		BlockMetas: blockMetas,
	}, nil
}

// error if either min or max are negative or min > max
// if 0, use blockstore base for min, latest block height for max
// enforce limit.
func filterMinMax(base, height, min, max, limit int64) (int64, int64, error) {
	// filter negatives
	if min < 0 || max < 0 {
		return min, max, coretypes.ErrZeroOrNegativeHeight
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
		return min, max, fmt.Errorf("%w: min height %d can't be greater than max height %d",
			coretypes.ErrInvalidRequest, min, max)
	}
	return min, max, nil
}

// Block gets block at a given height.
// If no height is provided, it will fetch the latest block.
// More: https://docs.tendermint.com/master/rpc/#/Info/block
func (env *Environment) Block(ctx *rpctypes.Context, heightPtr *int64) (*coretypes.ResultBlock, error) {
	height, err := env.getHeight(env.BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	blockMeta := env.BlockStore.LoadBlockMeta(height)
	if blockMeta == nil {
		return &coretypes.ResultBlock{BlockID: types.BlockID{}, Block: nil}, nil
	}

	block := env.BlockStore.LoadBlock(height)
	return &coretypes.ResultBlock{BlockID: blockMeta.BlockID, Block: block}, nil
}

// BlockByHash gets block by hash.
// More: https://docs.tendermint.com/master/rpc/#/Info/block_by_hash
func (env *Environment) BlockByHash(ctx *rpctypes.Context, hash bytes.HexBytes) (*coretypes.ResultBlock, error) {
	// N.B. The hash parameter is HexBytes so that the reflective parameter
	// decoding logic in the HTTP service will correctly translate from JSON.
	// See https://github.com/tendermint/tendermint/issues/6802 for context.

	block := env.BlockStore.LoadBlockByHash(hash)
	if block == nil {
		return &coretypes.ResultBlock{BlockID: types.BlockID{}, Block: nil}, nil
	}
	// If block is not nil, then blockMeta can't be nil.
	blockMeta := env.BlockStore.LoadBlockMeta(block.Height)
	return &coretypes.ResultBlock{BlockID: blockMeta.BlockID, Block: block}, nil
}

// Header gets block header at a given height.
// If no height is provided, it will fetch the latest header.
// More: https://docs.tendermint.com/master/rpc/#/Info/header
func (env *Environment) Header(ctx *rpctypes.Context, heightPtr *int64) (*coretypes.ResultHeader, error) {
	height, err := env.getHeight(env.BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	blockMeta := env.BlockStore.LoadBlockMeta(height)
	if blockMeta == nil {
		return &coretypes.ResultHeader{}, nil
	}

	return &coretypes.ResultHeader{Header: &blockMeta.Header}, nil
}

// HeaderByHash gets header by hash.
// More: https://docs.tendermint.com/master/rpc/#/Info/header_by_hash
func (env *Environment) HeaderByHash(ctx *rpctypes.Context, hash bytes.HexBytes) (*coretypes.ResultHeader, error) {
	// N.B. The hash parameter is HexBytes so that the reflective parameter
	// decoding logic in the HTTP service will correctly translate from JSON.
	// See https://github.com/tendermint/tendermint/issues/6802 for context.

	blockMeta := env.BlockStore.LoadBlockMetaByHash(hash)
	if blockMeta == nil {
		return &coretypes.ResultHeader{}, nil
	}

	return &coretypes.ResultHeader{Header: &blockMeta.Header}, nil
}

// Commit gets block commit at a given height.
// If no height is provided, it will fetch the commit for the latest block.
// More: https://docs.tendermint.com/master/rpc/#/Info/commit
func (env *Environment) Commit(ctx *rpctypes.Context, heightPtr *int64) (*coretypes.ResultCommit, error) {
	height, err := env.getHeight(env.BlockStore.Height(), heightPtr)
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
		commit := env.BlockStore.LoadSeenCommit()
		// NOTE: we can't yet ensure atomicity of operations in asserting
		// whether this is the latest height and retrieving the seen commit
		if commit != nil && commit.Height == height {
			return coretypes.NewResultCommit(&header, commit, false), nil
		}
	}

	// Return the canonical commit (comes from the block at height+1)
	commit := env.BlockStore.LoadBlockCommit(height)
	if commit == nil {
		return nil, nil
	}
	return coretypes.NewResultCommit(&header, commit, true), nil
}

// DataCommitment collects the data roots over a provided ordered range of blocks,
// and then creates a new Merkle root of those data roots.
func (env *Environment) DataCommitment(ctx *rpctypes.Context, query string) (*coretypes.ResultDataCommitment, error) {
	heights, err := searchBlocks(ctx, env, query)
	if err != nil {
		return nil, err
	}

	if len(heights) > consts.DataCommitmentBlocksLimit {
		return nil, fmt.Errorf("the query exceeds the limit of allowed blocks %d", consts.DataCommitmentBlocksLimit)
	} else if len(heights) == 0 {
		return nil, fmt.Errorf("cannot create the data commitments for an empty set of blocks")
	}

	err = sortBlocks(heights, "asc")
	if err != nil {
		return nil, err
	}

	if len(heights) > consts.DataCommitmentBlocksLimit {
		return nil, fmt.Errorf("the query exceeds the limit of allowed blocks %d", consts.DataCommitmentBlocksLimit)
	}

	if len(heights) == 0 {
		return nil, fmt.Errorf("cannot create the data commitments for an empty set of blocks")
	}

	err = sortBlocks(heights, "asc")
	if err != nil {
		return nil, err
	}

	blockResults := fetchBlocks(env, heights, len(heights), 0)
	root := hashDataRoots(blockResults)

	// Create data commitment
	return &coretypes.ResultDataCommitment{DataCommitment: root}, nil
}

// hashDataRoots hashes a list of blocks data hashes and returns their merkle root.
func hashDataRoots(blocks []*coretypes.ResultBlock) []byte {
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
func (env *Environment) BlockResults(ctx *rpctypes.Context, heightPtr *int64) (*coretypes.ResultBlockResults, error) {
	height, err := env.getHeight(env.BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	results, err := env.StateStore.LoadABCIResponses(height)
	if err != nil {
		return nil, err
	}

	var totalGasUsed int64
	for _, tx := range results.GetDeliverTxs() {
		totalGasUsed += tx.GetGasUsed()
	}

	return &coretypes.ResultBlockResults{
		Height:                height,
		TxsResults:            results.DeliverTxs,
		TotalGasUsed:          totalGasUsed,
		BeginBlockEvents:      results.BeginBlock.Events,
		EndBlockEvents:        results.EndBlock.Events,
		ValidatorUpdates:      results.EndBlock.ValidatorUpdates,
		ConsensusParamUpdates: results.EndBlock.ConsensusParamUpdates,
	}, nil
}

// BlockSearch searches for a paginated set of blocks matching BeginBlock and
// EndBlock event search criteria.
func (env *Environment) BlockSearch(
	ctx *rpctypes.Context,
	query string,
	pagePtr, perPagePtr *int,
	orderBy string,
) (*coretypes.ResultBlockSearch, error) {

	results, err := searchBlocks(ctx, env, query)
	if err != nil {
		return nil, err
	}

	err = sortBlocks(results, orderBy)
	if err != nil {
		return nil, err
	}

	// paginate results
	totalCount := len(results)
	perPage := env.validatePerPage(perPagePtr)

	page, err := validatePage(pagePtr, perPage, totalCount)
	if err != nil {
		return nil, err
	}

	skipCount := validateSkipCount(page, perPage)
	pageSize := tmmath.MinInt(perPage, totalCount-skipCount)

	apiResults := fetchBlocks(env, results, pageSize, skipCount)

	return &coretypes.ResultBlockSearch{Blocks: apiResults, TotalCount: totalCount}, nil
}

func searchBlocks(ctx *rpctypes.Context, env *Environment, query string) ([]int64, error) {
	if !indexer.KVSinkEnabled(env.EventSinks) {
		return nil, fmt.Errorf("block searching is disabled due to no kvEventSink")
	}

	q, err := tmquery.New(query)
	if err != nil {
		return nil, err
	}

	var kvsink indexer.EventSink
	for _, sink := range env.EventSinks {
		if sink.Type() == indexer.KV {
			kvsink = sink
		}
	}

	return kvsink.SearchBlockEvents(ctx.Context(), q)
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
func fetchBlocks(env *Environment, results []int64, pageSize int, skipCount int) []*coretypes.ResultBlock {
	apiResults := make([]*coretypes.ResultBlock, 0, pageSize)
	for i := skipCount; i < skipCount+pageSize; i++ {
		block := env.BlockStore.LoadBlock(results[i])
		if block != nil {
			blockMeta := env.BlockStore.LoadBlockMeta(block.Height)
			if blockMeta != nil {
				apiResults = append(apiResults, &coretypes.ResultBlock{
					Block:   block,
					BlockID: blockMeta.BlockID,
				})
			}
		}
	}
	return apiResults
}
