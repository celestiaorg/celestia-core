package blocksync

import (
	"github.com/go-kit/kit/metrics"

	"github.com/cometbft/cometbft/types"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "blocksync"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by this package.
type Metrics struct {
	// Syncing is a gauge that is 1 if a node is block syncing, 0 otherwise.
	Syncing metrics.Gauge
	// NumTxs is the number of transactions in the latest block.
	NumTxs metrics.Gauge
	// TotalTxs is the cumulative total number of transactions processed.
	TotalTxs metrics.Gauge
	// BlockSizeBytes is the size of the latest block in bytes.
	BlockSizeBytes metrics.Gauge
	// LatestBlockHeight is the height of the latest block processed.
	LatestBlockHeight metrics.Gauge
}

// recordBlockMetrics updates the gauges and counters related to block processing.
func (m *Metrics) recordBlockMetrics(block *types.Block) {
	numTxs := float64(len(block.Data.Txs))
	
	// Record the number of transactions in the latest block.
	m.NumTxs.Set(numTxs) //nolint:staticcheck // Metrics library Set method does not return error.

	// Accumulate the total number of transactions.
	m.TotalTxs.Add(numTxs) //nolint:staticcheck // Metrics library Add method does not return error.

	// Record the size of the block.
	m.BlockSizeBytes.Set(float64(block.Size()))

	// Record the height of the block.
	m.LatestBlockHeight.Set(float64(block.Height))
}
