package blocksync

import (
	"github.com/go-kit/kit/metrics"

	"github.com/cometbft/cometbft/types"
)

const (
	// MetricsSubsystem is the subsystem identifier for all metrics exposed by this package.
	MetricsSubsystem = "blocksync"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains metrics exposed by the blocksync package.
type Metrics struct {
	// Whether or not a node is currently block syncing. 1 if yes, 0 if no.
	Syncing metrics.Gauge
	
	// Number of transactions in the latest block processed. (Gauge for instantaneous value)
	NumTxs metrics.Gauge
	
	// Total cumulative number of transactions processed since startup. (COUNTER for cumulative value)
	// Changed from Gauge to Counter for semantic correctness in Prometheus/OpenMetrics.
	TotalTxs metrics.Counter 
	
	// Size of the latest block in bytes.
	BlockSizeBytes metrics.Gauge
	
	// The height of the latest block processed.
	LatestBlockHeight metrics.Gauge
}

// UpdateMetricsForBlock records the instantaneous metrics (Gauge) and cumulative metrics (Counter)
// for a newly processed block.
func (m *Metrics) UpdateMetricsForBlock(block *types.Block) {
	// Cast the transaction count once for cleaner metric updates.
	txCount := float64(len(block.Data.Txs))
	
	// Gauge: Instantaneous count of transactions in this specific block.
	m.NumTxs.Set(txCount)
	
	// Counter: Adds the count to the cumulative total.
	m.TotalTxs.Add(txCount) 
	
	// Gauge: Instantaneous size of the block.
	m.BlockSizeBytes.Set(float64(block.Size()))
	
	// Gauge: Latest block height.
	m.LatestBlockHeight.Set(float64(block.Height))
}
