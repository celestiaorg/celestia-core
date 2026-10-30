package types

const (
	// BlockTxLimitAppVersion activates MaxBlockTxs.
	BlockTxLimitAppVersion uint64 = 11

	// MaxBlockTxsCeiling bounds decoding below BlockTxLimitAppVersion. No
	// committable block reaches it: since app v2 every tx carries a 64-byte
	// signature, and v1 mainnet blocks were under 2 MB.
	MaxBlockTxsCeiling = DefaultMaxBlockSizeBytes/64 + 1

	// MaxBlockTxs matches app v11's combined SDK, PFB and PFF message capacity.
	MaxBlockTxs = 1000
)

// MaxBlockTxsForAppVersion returns the transaction limit for a block at appVersion.
func MaxBlockTxsForAppVersion(appVersion uint64) int {
	if appVersion >= BlockTxLimitAppVersion {
		return MaxBlockTxs
	}
	return MaxBlockTxsCeiling
}
