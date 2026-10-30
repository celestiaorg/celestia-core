package kv

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/pubsub/query"
	"github.com/cometbft/cometbft/state/indexer"
	"github.com/cometbft/cometbft/types"
)

// TestBlockSearchCapsLaterRange checks that a later range condition keeps only
// heights already in the earlier results, so its broad standalone match set
// does not hit the cap. Search visits ranges in map order, so the earlier
// results are seeded directly.
func TestBlockSearchCapsLaterRange(t *testing.T) {
	idx := New(dbm.NewMemDB(), WithMaxSearchResults(2))
	for i := 1; i <= 10; i++ {
		require.NoError(t, idx.Index(types.EventDataNewBlockEvents{
			Height: int64(i), Events: []abci.Event{{Type: "transfer", Attributes: []abci.EventAttribute{
				{Key: "amount", Value: fmt.Sprint(i), Index: true},
			}}},
		}))
	}
	ctx := context.Background()
	narrow, prefix, heightInfo := blockQueryRange(t, "transfer.amount < 2")
	filtered, err := idx.matchRange(ctx, narrow, prefix, map[string][]byte{}, true, heightInfo)
	require.NoError(t, err)
	require.Len(t, filtered, 1)

	broad, prefix, heightInfo := blockQueryRange(t, "transfer.amount > 0")
	_, err = idx.matchRange(ctx, broad, prefix, map[string][]byte{}, true, heightInfo)
	require.ErrorIs(t, err, indexer.ErrTooManyResults, "the broad range alone must exceed the cap")
	results, err := idx.matchRange(ctx, broad, prefix, filtered, false, heightInfo)
	require.NoError(t, err)
	require.Len(t, results, 1)
}

func blockQueryRange(t *testing.T, q string) (indexer.QueryRange, []byte, HeightInfo) {
	t.Helper()
	conditions, heightInfo, _ := dedupHeight(query.MustCompile(q).Syntax())
	ranges, _, heightRange := indexer.LookForRangesWithHeight(conditions)
	heightInfo.heightRange = heightRange
	require.Len(t, ranges, 1)
	for _, qr := range ranges {
		prefix, err := keyPrefix(qr.Key)
		require.NoError(t, err)
		return qr, prefix, heightInfo
	}
	return indexer.QueryRange{}, nil, heightInfo
}
