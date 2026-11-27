package cat

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

func TestWithinLimit(t *testing.T) {
	testCases := []struct {
		name  string
		used  uint64
		add   uint64
		limit uint64
		want  bool
	}{
		{name: "zero limit means no limit", used: 5, add: math.MaxUint64, limit: 0, want: true},
		{name: "below the limit", used: 1, add: 2, limit: 10, want: true},
		{name: "exactly the limit", used: 4, add: 6, limit: 10, want: true},
		{name: "one over the limit", used: 5, add: 6, limit: 10, want: false},
		{name: "add alone over the limit", used: 0, add: 11, limit: 10, want: false},
		{name: "zero add at the limit", used: 10, add: 0, limit: 10, want: true},
		{name: "sum would overflow", used: 1, add: math.MaxUint64, limit: 10, want: false},
		{name: "used already over the limit", used: 11, add: 0, limit: 10, want: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, withinLimit(tc.used, tc.add, tc.limit))
		})
	}
}

// reapTestTx describes a tx that the reap tests put straight into the store.
type reapTestTx struct {
	name     string
	signer   string
	sequence uint64
	priority int64
	usage    abci.TxUsage
}

// newReapTestPool returns a pool that holds the given txs and a lookup from
// tx key to name.
func newReapTestPool(t *testing.T, txs []reapTestTx) (*TxPool, map[types.TxKey]string) {
	t.Helper()
	txmp := &TxPool{store: newStore()}
	names := make(map[types.TxKey]string, len(txs))
	for _, tx := range txs {
		cachedTx := types.Tx(tx.name).ToCachedTx()
		var signer []byte
		if tx.signer != "" {
			signer = []byte(tx.signer)
		}
		wtx := newWrappedTx(cachedTx, 1, 1, tx.priority, signer, tx.sequence, false)
		wtx.usage = tx.usage
		require.True(t, txmp.store.set(wtx))
		names[cachedTx.Key()] = tx.name
	}
	return txmp, names
}

func reapedNames(txmp *TxPool, names map[types.TxKey]string, maxBytes int64) []string {
	reaped := txmp.ReapMaxBytesMaxGas(maxBytes, -1)
	out := make([]string, 0, len(reaped))
	for _, tx := range reaped {
		out = append(out, names[tx.Key()])
	}
	return out
}

func TestReapAppliesBlockLimits(t *testing.T) {
	limits := abci.BlockLimits{
		MaxSdkGas:          100,
		MaxSdkMessages:     10,
		MaxZkProofMessages: 4,
		MaxIsmSignatures:   8,
	}

	// For each limit: a first tx uses a little of it, a second uses all of
	// it, and a third uses a little. The second cannot fit after the first.
	testCases := []struct {
		name  string
		small abci.TxUsage
		full  abci.TxUsage
	}{
		{
			name:  "sdk gas",
			small: abci.TxUsage{SdkGas: 1},
			full:  abci.TxUsage{SdkGas: 100},
		},
		{
			name:  "sdk messages",
			small: abci.TxUsage{SdkMessages: 1},
			full:  abci.TxUsage{SdkMessages: 10},
		},
		{
			name:  "zk proof messages",
			small: abci.TxUsage{ZkProofMessages: 1},
			full:  abci.TxUsage{ZkProofMessages: 4},
		},
		{
			name:  "ism signatures",
			small: abci.TxUsage{IsmSignatures: 1},
			full:  abci.TxUsage{IsmSignatures: 8},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			txs := []reapTestTx{
				{name: "first", priority: 30, usage: tc.small},
				{name: "full-", priority: 20, usage: tc.full},
				{name: "third", priority: 10, usage: tc.small},
			}
			// Room for two of the three txs, which are 5 bytes each.
			const maxBytes = 10

			t.Run("without limits the reap goes by bytes alone", func(t *testing.T) {
				txmp, names := newReapTestPool(t, txs)
				require.Equal(t, []string{"first", "full-"}, reapedNames(txmp, names, maxBytes))
			})

			t.Run("with limits the tx that does not fit takes no room", func(t *testing.T) {
				txmp, names := newReapTestPool(t, txs)
				txmp.SetBlockLimits(limits)
				require.Equal(t, []string{"first", "third"}, reapedNames(txmp, names, maxBytes))
			})

			t.Run("the full tx is reaped when it comes first", func(t *testing.T) {
				txmp, names := newReapTestPool(t, txs[1:])
				txmp.SetBlockLimits(limits)
				require.Equal(t, []string{"full-"}, reapedNames(txmp, names, maxBytes))
			})
		})
	}
}

func TestReapDoesNotLimitTxsWithoutUsage(t *testing.T) {
	// Blob txs report no usage, so the limits never hold them back.
	txs := []reapTestTx{
		{name: "sdk-1", priority: 40, usage: abci.TxUsage{SdkGas: 100, SdkMessages: 10}},
		{name: "blob1", priority: 30},
		{name: "sdk-2", priority: 20, usage: abci.TxUsage{SdkGas: 1, SdkMessages: 1}},
		{name: "blob2", priority: 10},
	}
	txmp, names := newReapTestPool(t, txs)
	txmp.SetBlockLimits(abci.BlockLimits{MaxSdkGas: 100, MaxSdkMessages: 10})

	require.Equal(t, []string{"sdk-1", "blob1", "blob2"}, reapedNames(txmp, names, -1))
}

func TestReapStopsSignerAtFirstTxOverLimit(t *testing.T) {
	// alice's second tx does not fit, so her third is not reaped either: it
	// would leave a sequence gap. bob is not affected.
	txs := []reapTestTx{
		{name: "alice-1", signer: "alice", sequence: 1, priority: 30, usage: abci.TxUsage{SdkMessages: 1}},
		{name: "alice-2", signer: "alice", sequence: 2, priority: 30, usage: abci.TxUsage{SdkMessages: 10}},
		{name: "alice-3", signer: "alice", sequence: 3, priority: 30, usage: abci.TxUsage{SdkMessages: 1}},
		{name: "bob-1", signer: "bob", sequence: 1, priority: 10, usage: abci.TxUsage{SdkMessages: 1}},
	}
	txmp, names := newReapTestPool(t, txs)
	txmp.SetBlockLimits(abci.BlockLimits{MaxSdkMessages: 10})

	require.Equal(t, []string{"alice-1", "bob-1"}, reapedNames(txmp, names, -1))
}

func TestBlockLimitsDefaultToNoLimits(t *testing.T) {
	txmp := &TxPool{store: newStore()}
	require.Equal(t, abci.BlockLimits{}, txmp.BlockLimits())

	limits := abci.BlockLimits{MaxSdkGas: 1, MaxSdkMessages: 2, MaxZkProofMessages: 3, MaxIsmSignatures: 4}
	txmp.SetBlockLimits(limits)
	require.Equal(t, limits, txmp.BlockLimits())
}

func TestPoolStoresUsageFromCheckTx(t *testing.T) {
	txmp := setup(t, 0)
	tx := types.Tx(newTx(0, 0, []byte("usage"), 1))
	require.NoError(t, txmp.CheckTx(tx, nil, mempool.TxInfo{}))

	wtx := txmp.store.get(tx.Key())
	require.NotNil(t, wtx)
	require.Equal(t, testTxUsage, wtx.usage)
}
