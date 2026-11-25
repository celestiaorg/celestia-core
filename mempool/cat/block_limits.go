package cat

import (
	abci "github.com/cometbft/cometbft/abci/types"
)

// reapUsage tracks what the txs reaped so far use of the app's per-block
// limits, so that a reap can pass over a tx the app would skip when it builds
// the proposal.
type reapUsage struct {
	limits abci.BlockLimits
	total  abci.TxUsage
	// limited is false when the app has no limits. The reap then skips add.
	limited bool
}

func newReapUsage(limits abci.BlockLimits) *reapUsage {
	return &reapUsage{
		limits:  limits,
		limited: limits != abci.BlockLimits{},
	}
}

// add counts a tx with the given usage towards the totals if it stays within
// every limit on top of the txs already added. It reports whether it did.
// A limit of zero is not checked.
func (u *reapUsage) add(tx *abci.TxUsage) bool {
	if !withinLimit(u.total.SdkGas, tx.SdkGas, u.limits.MaxSdkGas) ||
		!withinLimit(u.total.SdkMessages, tx.SdkMessages, u.limits.MaxSdkMessages) ||
		!withinLimit(u.total.ZkProofMessages, tx.ZkProofMessages, u.limits.MaxZkProofMessages) ||
		!withinLimit(u.total.IsmSignatures, tx.IsmSignatures, u.limits.MaxIsmSignatures) {
		return false
	}
	u.total.SdkGas += tx.SdkGas
	u.total.SdkMessages += tx.SdkMessages
	u.total.ZkProofMessages += tx.ZkProofMessages
	u.total.IsmSignatures += tx.IsmSignatures
	return true
}

// withinLimit reports whether used+add stays within limit. A limit of zero
// means there is no limit. It subtracts rather than adds so that a large add
// cannot overflow the sum.
func withinLimit(used, add, limit uint64) bool {
	if limit == 0 {
		return true
	}
	if used > limit {
		return false
	}
	return add <= limit-used
}
