package types

const (
	// BlockTxLimitAppVersion activates the transaction-count limit. Older
	// blocks retain their original validity rules during replay and sync.
	BlockTxLimitAppVersion uint64 = 11

	// MaxBlockTxs bounds transaction-indexed allocations in Core and the app.
	// It matches v11's combined SDK, PFB and PFF message capacity.
	MaxBlockTxs = 1000
)
