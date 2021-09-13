package null

import (
	"context"
	"errors"

	abci "github.com/celestiaorg/celestia-core/abci/types"
	"github.com/celestiaorg/celestia-core/libs/pubsub/query"
	"github.com/celestiaorg/celestia-core/state/indexer"
)

var _ indexer.TxIndexer = (*TxIndex)(nil)

// TxIndex acts as a /dev/null.
type TxIndex struct{}

// Get on a TxIndex is disabled and panics when invoked.
func (txi *TxIndex) Get(hash []byte) (*abci.TxResult, error) {
	return nil, errors.New(`indexing is disabled (set 'tx_index = "kv"' in config)`)
}

// AddBatch is a noop and always returns nil.
func (txi *TxIndex) AddBatch(batch *indexer.Batch) error {
	return nil
}

// Index is a noop and always returns nil.
func (txi *TxIndex) Index(results []*abci.TxResult) error {
	return nil
}

func (txi *TxIndex) Search(ctx context.Context, q *query.Query) ([]*abci.TxResult, error) {
	return []*abci.TxResult{}, nil
}
