package types

const (
	// BlockTxSafetyLimitAppVersion activates the compatibility-preserving
	// resource ceiling used by celestia-app v10.
	BlockTxSafetyLimitAppVersion uint64 = 10

	// BlockTxLimitAppVersion activates the transaction-count limit. Older
	// blocks retain their original validity rules during replay and sync.
	BlockTxLimitAppVersion uint64 = 11

	// MaxBlockTxsV10 is a resource ceiling, not a new protocol capacity. A v10
	// transaction accepted by ProcessProposal contains a valid signature of at
	// least 64 bytes. Even ignoring protobuf overhead, Core's 128 MiB hard block
	// limit cannot contain this many accepted transactions.
	MaxBlockTxsV10 = DefaultMaxBlockSizeBytes/64 + 1

	// MaxBlockTxs bounds transaction-indexed allocations in Core and the app.
	// It matches v11's combined SDK, PFB and PFF message capacity.
	MaxBlockTxs = 1000
)

// MaxBlockTxsForAppVersion returns the transaction ceiling for appVersion.
// Zero means that historical decoding retains its original behavior.
func MaxBlockTxsForAppVersion(appVersion uint64) int {
	switch {
	case appVersion >= BlockTxLimitAppVersion:
		return MaxBlockTxs
	case appVersion >= BlockTxSafetyLimitAppVersion:
		return MaxBlockTxsV10
	default:
		return 0
	}
}
