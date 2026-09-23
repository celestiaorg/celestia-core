package kv

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/pubsub/query"
	"github.com/cometbft/cometbft/types"
)

func BenchmarkTxSearch(b *testing.B) {
	dbDir, err := os.MkdirTemp("", "benchmark_tx_search_test")
	if err != nil {
		b.Errorf("failed to create temporary directory: %s", err)
	}

	db, err := dbm.NewDB("benchmark_tx_search_test", dbm.PebbleDBBackend, dbDir)
	if err != nil {
		b.Errorf("failed to create database: %s", err)
	}

	indexer := NewTxIndex(db)

	for i := 0; i < 35000; i++ {
		events := []abci.Event{
			{
				Type: "transfer",
				Attributes: []abci.EventAttribute{
					{Key: "address", Value: fmt.Sprintf("address_%d", i%100), Index: true},
					{Key: "amount", Value: "50", Index: true},
				},
			},
		}

		txBz := make([]byte, 8)
		if _, err := rand.Read(txBz); err != nil {
			b.Errorf("failed produce random bytes: %s", err)
		}

		txResult := &abci.TxResult{
			Height: int64(i),
			Index:  0,
			Tx:     types.Tx(string(txBz)),
			Result: abci.ExecTxResult{
				Data:   []byte{0},
				Code:   abci.CodeTypeOK,
				Log:    "",
				Events: events,
			},
		}

		if err := indexer.Index(txResult); err != nil {
			b.Errorf("failed to index tx: %s", err)
		}
	}

	txQuery := query.MustCompile(`transfer.address = 'address_43' AND transfer.amount = 50`)

	b.ResetTimer()

	ctx := context.Background()

	for i := 0; i < b.N; i++ {
		if _, err := indexer.Search(ctx, txQuery); err != nil {
			b.Errorf("failed to query for txs: %s", err)
		}
	}
}

// BenchmarkSearchRefsBroad measures the memory a broad query materializes: a
// single condition that matches every indexed tx. searchRefs accumulates one
// map entry per match before any pagination, so B/op grows linearly with the
// match count. This is the growth a max_search_results cap bounds.
func BenchmarkSearchRefsBroad(b *testing.B) {
	const numTxs = 10000

	dbDir, err := os.MkdirTemp("", "benchmark_search_refs_broad")
	if err != nil {
		b.Fatalf("failed to create temporary directory: %s", err)
	}

	db, err := dbm.NewDB("benchmark_search_refs_broad", dbm.PebbleDBBackend, dbDir)
	if err != nil {
		b.Fatalf("failed to create database: %s", err)
	}

	indexer := NewTxIndex(db)

	for i := 0; i < numTxs; i++ {
		events := []abci.Event{
			{
				Type:       "transfer",
				Attributes: []abci.EventAttribute{{Key: "amount", Value: "50", Index: true}},
			},
		}

		txBz := make([]byte, 8)
		if _, err := rand.Read(txBz); err != nil {
			b.Fatalf("failed produce random bytes: %s", err)
		}

		txResult := &abci.TxResult{
			Height: int64(i),
			Index:  0,
			Tx:     types.Tx(string(txBz)),
			Result: abci.ExecTxResult{Code: abci.CodeTypeOK, Events: events},
		}

		if err := indexer.Index(txResult); err != nil {
			b.Fatalf("failed to index tx: %s", err)
		}
	}

	// transfer.amount = 50 matches every indexed tx.
	txQuery := query.MustCompile(`transfer.amount = 50`)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := indexer.searchRefs(ctx, txQuery); err != nil {
			b.Fatalf("failed to search refs: %s", err)
		}
	}
}
