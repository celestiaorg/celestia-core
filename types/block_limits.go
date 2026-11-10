package types

import "errors"

// ErrTooManyBlockTxs indicates that a block exceeds its transaction-count limit.
var ErrTooManyBlockTxs = errors.New("block exceeds maximum transaction count")

const (
	// BlockTxSafetyLimitAppVersion activates MaxBlockTxsV10 for live proposals.
	BlockTxSafetyLimitAppVersion uint64 = 10

	// BlockTxLimitAppVersion activates MaxBlockTxs.
	BlockTxLimitAppVersion uint64 = 11

	// MaxBlockTxsV10 is a resource ceiling above any committable block: since app
	// v2 every tx carries a 64-byte signature, and v1 mainnet blocks were < 2 MB.
	MaxBlockTxsV10 = DefaultMaxBlockSizeBytes/64 + 1

	// MaxBlockTxs matches app v11's combined SDK, PFB and PFF message capacity.
	MaxBlockTxs = 1000
)

// MaxBlockTxsForAppVersion returns the transaction limit for a block at
// appVersion. Zero means no limit.
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
