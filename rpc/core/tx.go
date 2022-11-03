package core

import (
	"errors"
	"fmt"
	"sort"

	abcitypes "github.com/tendermint/tendermint/abci/types"
	tmmath "github.com/tendermint/tendermint/libs/math"
	tmquery "github.com/tendermint/tendermint/libs/pubsub/query"
	"github.com/tendermint/tendermint/pkg/consts"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/state/txindex/null"
	"github.com/tendermint/tendermint/types"
)

// Tx allows you to query the transaction results. `nil` could mean the
// transaction is in the mempool, invalidated, or was not sent in the first
// place.
// More: https://docs.tendermint.com/v0.34/rpc/#/Info/tx
func Tx(ctx *rpctypes.Context, hash []byte, prove bool) (*ctypes.ResultTx, error) {
	// if index is disabled, return error
	if _, ok := env.TxIndexer.(*null.TxIndex); ok {
		return nil, fmt.Errorf("transaction indexing is disabled")
	}

	r, err := env.TxIndexer.Get(hash)
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, fmt.Errorf("tx (%X) not found", hash)
	}

	height := r.Height
	index := r.Index

	var txProof types.TxProof
	if prove {
		txProof, err = proveTx(height, index)
		if err != nil {
			return nil, err
		}
	}

	return &ctypes.ResultTx{
		Hash:     hash,
		Height:   height,
		Index:    index,
		TxResult: r.Result,
		Tx:       r.Tx,
		Proof:    txProof,
	}, nil
}

func TxShares(ctx *rpctypes.Context, hash []byte) (*types.TxShares, error) {
	// if index is disabled, return error
	if _, ok := env.TxIndexer.(*null.TxIndex); ok {
		return nil, fmt.Errorf("transaction indexing is disabled")
	}

	r, err := env.TxIndexer.Get(hash)
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, fmt.Errorf("tx (%X) not found", hash)
	}

	height := r.Height
	index := r.Index

	shares, err := txShares(height, index)
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// TxSearch allows you to query for multiple transactions results. It returns a
// list of transactions (maximum ?per_page entries) and the total count.
// NOTE: proveTx isn't respected but is left in the function signature to
// conform to the endpoint exposed by Tendermint
// More: https://docs.tendermint.com/master/rpc/#/Info/tx_search
func TxSearch(
	ctx *rpctypes.Context,
	query string,
	prove bool,
	pagePtr, perPagePtr *int,
	orderBy string,
) (*ctypes.ResultTxSearch, error) {

	// if index is disabled, return error
	if _, ok := env.TxIndexer.(*null.TxIndex); ok {
		return nil, errors.New("transaction indexing is disabled")
	} else if len(query) > maxQueryLength {
		return nil, errors.New("maximum query length exceeded")
	}

	q, err := tmquery.New(query)
	if err != nil {
		return nil, err
	}

	results, err := env.TxIndexer.Search(ctx.Context(), q)
	if err != nil {
		return nil, err
	}

	// sort results (must be done before pagination)
	switch orderBy {
	case "desc":
		sort.Slice(results, func(i, j int) bool {
			if results[i].Height == results[j].Height {
				return results[i].Index > results[j].Index
			}
			return results[i].Height > results[j].Height
		})
	case "asc", "":
		sort.Slice(results, func(i, j int) bool {
			if results[i].Height == results[j].Height {
				return results[i].Index < results[j].Index
			}
			return results[i].Height < results[j].Height
		})
	default:
		return nil, errors.New("expected order_by to be either `asc` or `desc` or empty")
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

	apiResults := make([]*ctypes.ResultTx, 0, pageSize)
	for i := skipCount; i < skipCount+pageSize; i++ {
		r := results[i]

		var txProof types.TxProof
		if prove {
			txProof, err = proveTx(r.Height, r.Index)
			if err != nil {
				return nil, err
			}
		}

		apiResults = append(apiResults, &ctypes.ResultTx{
			Hash:     types.Tx(r.Tx).Hash(),
			Height:   r.Height,
			Index:    r.Index,
			TxResult: r.Result,
			Tx:       r.Tx,
			Proof:    txProof,
		})
	}

	return &ctypes.ResultTxSearch{Txs: apiResults, TotalCount: totalCount}, nil
}

func proveTx(height int64, index uint32) (types.TxProof, error) {
	var (
		pTxProof tmproto.TxProof
		txProof  types.TxProof
	)
	rawBlock, err := loadRawBlock(env.BlockStore, height)
	if err != nil {
		return txProof, err
	}
	res, err := env.ProxyAppQuery.QuerySync(abcitypes.RequestQuery{
		Data: rawBlock,
		Path: fmt.Sprintf(consts.TxInclusionProofQueryPath, index),
	})
	if err != nil {
		return txProof, err
	}
	err = pTxProof.Unmarshal(res.Value)
	if err != nil {
		return txProof, err
	}
	txProof, err = types.TxProofFromProto(pTxProof)
	if err != nil {
		return txProof, err
	}
	return txProof, nil
}

func txShares(height int64, index uint32) (*types.TxShares, error) {
	var pTxShares tmproto.TxShares
	rawBlock, err := loadRawBlock(env.BlockStore, height)
	if err != nil {
		return nil, err
	}
	res, err := env.ProxyAppQuery.QuerySync(abcitypes.RequestQuery{
		Data: rawBlock,
		Path: fmt.Sprintf(consts.TxSharesQueryPath, index),
	})
	if err != nil {
		return nil, err
	}
	err = pTxShares.Unmarshal(res.Value)
	if err != nil {
		return nil, err
	}
	return &types.TxShares{
		StartingShare: pTxShares.StartingShare,
		EndShare:      pTxShares.EndShare,
	}, nil
}

func ProveSharesWithCtx(
	ctx *rpctypes.Context,
	height int64,
	startShare uint64,
	endShare uint64,
) (types.SharesProof, error) {
	return ProveShares(height, startShare, endShare)
}

func ProveRowsWithCtx(
	ctx *rpctypes.Context,
	height int64,
	startingRow uint32,
	endingRow uint32,
) (types.RowsProof, error) {
	return ProveRows(height, startingRow, endingRow)
}

// TODO change TxProof to ShareProof
func ProveShares(height int64, startShare uint64, endShare uint64) (types.SharesProof, error) {
	var (
		pSharesProof tmproto.SharesProof
		sharesProof  types.SharesProof
	)
	rawBlock, err := loadRawBlock(env.BlockStore, height)
	if err != nil {
		return sharesProof, err
	}
	res, err := env.ProxyAppQuery.QuerySync(abcitypes.RequestQuery{
		Data: rawBlock,
		Path: fmt.Sprintf(consts.ShareInclusionProofQueryPath, startShare, endShare),
	})
	if err != nil {
		return sharesProof, err
	}
	err = pSharesProof.Unmarshal(res.Value)
	if err != nil {
		return sharesProof, err
	}
	sharesProof, err = types.SharesFromProto(pSharesProof)
	if err != nil {
		return sharesProof, err
	}
	return sharesProof, nil
}

// TODO change TxProof to ShareProof
func ProveRows(height int64, startingRow, endingRow uint32) (types.RowsProof, error) {
	var (
		pRowsProof tmproto.RowsProof
		rowsProof  types.RowsProof
	)
	rawBlock, err := loadRawBlock(env.BlockStore, height)
	if err != nil {
		return rowsProof, err
	}
	res, err := env.ProxyAppQuery.QuerySync(abcitypes.RequestQuery{
		Data: rawBlock,
		Path: fmt.Sprintf(consts.RowsInclusionProofQueryPath, startingRow, endingRow),
	})
	if err != nil {
		return rowsProof, err
	}
	err = pRowsProof.Unmarshal(res.Value)
	if err != nil {
		return rowsProof, err
	}
	rowsProof, err = types.RowsFromProto(pRowsProof)
	if err != nil {
		return rowsProof, err
	}
	return rowsProof, nil
}

func loadRawBlock(bs state.BlockStore, height int64) ([]byte, error) {
	var blockMeta = bs.LoadBlockMeta(height)
	if blockMeta == nil {
		return nil, fmt.Errorf("no block found for height %d", height)
	}

	buf := []byte{}
	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part := bs.LoadBlockPart(height, i)
		// If the part is missing (e.g. since it has been deleted after we
		// loaded the block meta) we consider the whole block to be missing.
		if part == nil {
			return nil, fmt.Errorf("missing block part at height %d part %d", height, i)
		}
		buf = append(buf, part.Bytes...)
	}
	return buf, nil
}
