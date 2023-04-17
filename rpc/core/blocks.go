package core

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/pkg/consts"
	blockidxnull "github.com/tendermint/tendermint/state/indexer/block/null"

	cmtmath "github.com/tendermint/tendermint/libs/math"
	cmtquery "github.com/tendermint/tendermint/libs/pubsub/query"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
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
// More: https://docs.cometbft.com/v0.34/rpc/#/Info/blockchain
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
	max = cmtmath.MinInt64(height, max)

	// limit min to the base
	min = cmtmath.MaxInt64(base, min)

	// limit min to within `limit` of max
	// so the total number of blocks returned will be `limit`
	min = cmtmath.MaxInt64(min, max-limit+1)

	if min > max {
		return min, max, fmt.Errorf("min height %d can't be greater than max height %d", min, max)
	}
	return min, max, nil
}

// Block gets block at a given height.
// If no height is provided, it will fetch the latest block.
// More: https://docs.cometbft.com/v0.34/rpc/#/Info/block
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

// SignedBlock fetches the set of transactions at a specified height and all the relevant
// data to verify the transactions (i.e. using light client verification).
func SignedBlock(ctx *rpctypes.Context, heightPtr *int64) (*ctypes.ResultSignedBlock, error) {
	height, err := getHeight(GetEnvironment().BlockStore.Height(), heightPtr)
	if err != nil {
		return nil, err
	}

	block := GetEnvironment().BlockStore.LoadBlock(height)
	if block == nil {
		return nil, errors.New("block not found")
	}
	seenCommit := GetEnvironment().BlockStore.LoadSeenCommit(height)
	if seenCommit == nil {
		return nil, errors.New("seen commit not found")
	}
	validatorSet, err := GetEnvironment().StateStore.LoadValidators(height)
	if validatorSet == nil || err != nil {
		return nil, err
	}

	return &ctypes.ResultSignedBlock{
		Header:       block.Header,
		Commit:       *seenCommit,
		ValidatorSet: *validatorSet,
		Data:         block.Data,
	}, nil
}

// BlockByHash gets block by hash.
// More: https://docs.cometbft.com/v0.34/rpc/#/Info/block_by_hash
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
// More: https://docs.cometbft.com/v0.34/rpc/#/Info/commit
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
func DataCommitment(ctx *rpctypes.Context, firstBlock uint64, lastBlock uint64) (*ctypes.ResultDataCommitment, error) {
	err := validateDataCommitmentRange(firstBlock, lastBlock)
	if err != nil {
		return nil, err
	}
	heights := generateHeightsList(firstBlock, lastBlock)
	blockResults := fetchBlocks(heights, len(heights), 0)
	if len(blockResults) != len(heights) {
		return nil, fmt.Errorf("couldn't fetch all the blocks in the provided range")
	}
	root, err := hashDataRootTuples(blockResults)
	if err != nil {
		return nil, err
	}
	// Create data commitment
	return &ctypes.ResultDataCommitment{DataCommitment: root}, nil
}

// DataRootInclusionProof creates an inclusion proof of the data root of block
// height `height` in the set of blocks defined by `begin_block` and `end_block`.
func DataRootInclusionProof(
	ctx *rpctypes.Context,
	height int64,
	beginBlock uint64,
	endBlock uint64,
) (*ctypes.ResultDataRootInclusionProof, error) {
	err := validateDataRootInclusionProofRequest(uint64(height), beginBlock, endBlock)
	if err != nil {
		return nil, err
	}
	heights := generateHeightsList(beginBlock, endBlock)
	blockResults := fetchBlocks(heights, len(heights), 0)
	if len(blockResults) != len(heights) {
		return nil, fmt.Errorf("couldn't fetch all the blocks in the provided range")
	}
	proof, err := proveDataRootTuples(blockResults, height)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultDataRootInclusionProof{Proof: *proof}, nil
}

// padBytes Pad bytes to given length
func padBytes(byt []byte, length int) ([]byte, error) {
	l := len(byt)
	if l > length {
		return nil, fmt.Errorf(
			"cannot pad bytes because length of bytes array: %d is greater than given length: %d",
			l,
			length,
		)
	}
	if l == length {
		return byt, nil
	}
	tmp := make([]byte, length)
	copy(tmp[length-l:], byt)
	return tmp, nil
}

// EncodeDataRootTuple takes a height and a data root and returns the equivalent of
// `abi.encode(...)` in Ethereum.
// The encoded type is a DataRootTuple, which has the following ABI:
// ```
//
//	{
//	  "components": [
//	    {
//	      "internalType": "uint256",
//	      "name": "height",
//	      "type": "uint256"
//	    },
//	    {
//	      "internalType": "bytes32",
//	      "name": "dataRoot",
//	      "type": "bytes32"
//	    }
//	  ],
//	  "internalType": "structDataRootTuple",
//	  "name": "_tuple",
//	  "type": "tuple"
//	},
//
// ```
// padding the hex representation of the height to 32 bytes and concatenating the data root to it
// For more information, refer to:
// https://github.com/celestiaorg/quantum-gravity-bridge/blob/master/src/DataRootTuple.sol
func EncodeDataRootTuple(height uint64, dataRoot [32]byte) ([]byte, error) {
	hexRepresentation := strconv.FormatUint(height, 16)
	// Make sure hex representation has even length
	if len(hexRepresentation)%2 == 1 {
		hexRepresentation = "0" + hexRepresentation
	}
	hexBytes, hexErr := hex.DecodeString(hexRepresentation)
	if hexErr != nil {
		return nil, hexErr
	}
	paddedBytes, padErr := padBytes(hexBytes, 32)
	if padErr != nil {
		return nil, padErr
	}

	dataSlice := dataRoot[:]
	return append(paddedBytes, dataSlice...), nil
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
func validateDataCommitmentRange(firstBlock uint64, lastBlock uint64) error {
	if firstBlock == 0 {
		return fmt.Errorf("the first block is 0")
	}
	env := GetEnvironment()
	heightsRange := lastBlock - firstBlock + 1
	if heightsRange > uint64(consts.DataCommitmentBlocksLimit) {
		return fmt.Errorf("the query exceeds the limit of allowed blocks %d", consts.DataCommitmentBlocksLimit)
	}
	if heightsRange == 0 {
		return fmt.Errorf("cannot create the data commitments for an empty set of blocks")
	}
	if firstBlock > lastBlock {
		return fmt.Errorf("last block is smaller than first block")
	}
	if lastBlock > uint64(env.BlockStore.Height()) {
		return fmt.Errorf(
			"last block %d is higher than current chain height %d",
			lastBlock,
			env.BlockStore.Height(),
		)
	}
	has, err := env.BlockIndexer.Has(int64(lastBlock))
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf(
			"last block %d is still not indexed",
			lastBlock,
		)
	}
	return nil
}

// hashDataRootTuples hashes a list of blocks data root tuples, i.e. height and data root, and returns their merkle root.
func hashDataRootTuples(blocks []*ctypes.ResultBlock) ([]byte, error) {
	dataRootEncodedTuples := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		encodedTuple, err := EncodeDataRootTuple(uint64(block.Block.Height), *(*[32]byte)(block.Block.DataHash))
		if err != nil {
			return nil, err
		}
		dataRootEncodedTuples = append(dataRootEncodedTuples, encodedTuple)
	}
	root := merkle.HashFromByteSlices(dataRootEncodedTuples)
	return root, nil
}

// validateDataRootInclusionProofRequest validates the request to generate a data root
// inclusion proof.
func validateDataRootInclusionProofRequest(height uint64, firstBlock uint64, lastBlock uint64) error {
	err := validateDataCommitmentRange(firstBlock, lastBlock)
	if err != nil {
		return err
	}
	if height < firstBlock || height > lastBlock {
		return fmt.Errorf(
			"height %d should be in the interval first_block %d last_block %d",
			height,
			firstBlock,
			lastBlock,
		)
	}
	return nil
}

// proveDataRootTuples returns the merkle inclusion proof for a height.
func proveDataRootTuples(blocks []*ctypes.ResultBlock, height int64) (*merkle.Proof, error) {
	dataRootEncodedTuples := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		encodedTuple, err := EncodeDataRootTuple(uint64(block.Block.Height), *(*[32]byte)(block.Block.DataHash))
		if err != nil {
			return nil, err
		}
		dataRootEncodedTuples = append(dataRootEncodedTuples, encodedTuple)
	}
	_, proofs := merkle.ProofsFromByteSlices(dataRootEncodedTuples)
	return proofs[height-blocks[0].Block.Height], nil
}

// BlockResults gets ABCIResults at a given height.
// If no height is provided, it will fetch results for the latest block.
// When DiscardABCIResponses is enabled, an error will be returned.
//
// Results are for the height of the block containing the txs.
// Thus response.results.deliver_tx[5] is the results of executing
// getBlock(h).Txs[5]
// More: https://docs.cometbft.com/v0.34/rpc/#/Info/block_results
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

func BlockSearchMatchEvents(
	ctx *rpctypes.Context,
	query string,
	pagePtr, perPagePtr *int,
	orderBy string,
	matchEvents bool,
) (*ctypes.ResultBlockSearch, error) {
	if matchEvents {
		query = "match.events = 1 AND " + query
	} else {
		query = "match.events = 0 AND " + query
	}
	return BlockSearch(ctx, query, pagePtr, perPagePtr, orderBy)
}

// BlockSearch searches for a paginated set of blocks matching BeginBlock and
// EndBlock event search criteria.
func BlockSearch(
	ctx *rpctypes.Context,
	query string,
	pagePtr, perPagePtr *int,
	orderBy string,
) (*ctypes.ResultBlockSearch, error) {

	// skip if block indexing is disabled
	if _, ok := GetEnvironment().BlockIndexer.(*blockidxnull.BlockerIndexer); ok {
		return nil, errors.New("block indexing is disabled")
	}
	q, err := cmtquery.New(query)
	if err != nil {
		return nil, err
	}

	results, err := GetEnvironment().BlockIndexer.Search(ctx.Context(), q)
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
	pageSize := cmtmath.MinInt(perPage, totalCount-skipCount)

	apiResults := fetchBlocks(results, pageSize, skipCount)

	return &ctypes.ResultBlockSearch{Blocks: apiResults, TotalCount: totalCount}, nil
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
