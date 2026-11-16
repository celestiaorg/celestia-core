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

// MaxBlockMsgsForAppVersion returns the app's per-block message caps by class (SDK,
// PayForBlobs, PayForFibre), which sum to MaxBlockTxs; ok is false for any version but v11.
func MaxBlockMsgsForAppVersion(appVersion uint64) (sdk, pfb, pff int, ok bool) {
	if appVersion == BlockTxLimitAppVersion {
		return 600, 200, 200, true
	}
	return 0, 0, 0, false
}
