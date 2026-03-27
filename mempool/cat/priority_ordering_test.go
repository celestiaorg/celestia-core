package cat

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

// TestReapOrderByPriority is a table-driven test that verifies the CAT mempool
// returns transactions in descending priority order via both ReapMaxBytesMaxGas
// and ReapMaxTxs.
func TestReapOrderByPriority(t *testing.T) {
	type txSpec struct {
		sender   int
		priority int64
	}

	tests := []struct {
		name string
		// txs to add to the mempool. Each tx has a unique sender to avoid
		// signer-based grouping effects.
		txs []txSpec
		// expected priority ordering after reaping (descending).
		wantPriorities []int64
	}{
		{
			name: "distinct priorities are returned in descending order",
			txs: []txSpec{
				{sender: 0, priority: 1},
				{sender: 1, priority: 5},
				{sender: 2, priority: 3},
				{sender: 3, priority: 10},
				{sender: 4, priority: 7},
			},
			wantPriorities: []int64{10, 7, 5, 3, 1},
		},
		{
			name: "single transaction",
			txs: []txSpec{
				{sender: 0, priority: 42},
			},
			wantPriorities: []int64{42},
		},
		{
			name: "all same priority preserves some order (FIFO for ties)",
			txs: []txSpec{
				{sender: 0, priority: 5},
				{sender: 1, priority: 5},
				{sender: 2, priority: 5},
			},
			wantPriorities: []int64{5, 5, 5},
		},
		{
			name: "two priority levels",
			txs: []txSpec{
				{sender: 0, priority: 1},
				{sender: 1, priority: 100},
				{sender: 2, priority: 1},
				{sender: 3, priority: 100},
			},
			wantPriorities: []int64{100, 100, 1, 1},
		},
		{
			name: "already sorted input stays sorted",
			txs: []txSpec{
				{sender: 0, priority: 100},
				{sender: 1, priority: 50},
				{sender: 2, priority: 25},
				{sender: 3, priority: 10},
			},
			wantPriorities: []int64{100, 50, 25, 10},
		},
		{
			name: "reverse sorted input gets reordered",
			txs: []txSpec{
				{sender: 0, priority: 10},
				{sender: 1, priority: 25},
				{sender: 2, priority: 50},
				{sender: 3, priority: 100},
			},
			wantPriorities: []int64{100, 50, 25, 10},
		},
		{
			name: "large spread in priorities",
			txs: []txSpec{
				{sender: 0, priority: 1},
				{sender: 1, priority: 1000000},
				{sender: 2, priority: 500},
			},
			wantPriorities: []int64{1000000, 500, 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txmp := setup(t, 0)

			// Build a lookup from tx key to priority for verification.
			priorityByKey := make(map[types.TxKey]int64)

			for _, spec := range tc.txs {
				tx := types.Tx(newTx(spec.sender, 0, []byte(fmt.Sprintf("msg-%d", spec.sender)), spec.priority))
				err := txmp.CheckTx(tx, nil, mempool.TxInfo{})
				require.NoError(t, err)
				priorityByKey[tx.Key()] = spec.priority
			}
			require.Equal(t, len(tc.txs), txmp.Size())

			t.Run("ReapMaxTxs", func(t *testing.T) {
				reaped := txmp.ReapMaxTxs(-1)
				require.Len(t, reaped, len(tc.txs))

				gotPriorities := make([]int64, len(reaped))
				for i, cachedTx := range reaped {
					gotPriorities[i] = priorityByKey[cachedTx.Key()]
				}
				assert.Equal(t, tc.wantPriorities, gotPriorities)
			})

			t.Run("ReapMaxBytesMaxGas", func(t *testing.T) {
				reaped := txmp.ReapMaxBytesMaxGas(-1, -1)
				require.Len(t, reaped, len(tc.txs))

				gotPriorities := make([]int64, len(reaped))
				for i, cachedTx := range reaped {
					gotPriorities[i] = priorityByKey[cachedTx.Key()]
				}
				assert.Equal(t, tc.wantPriorities, gotPriorities)
			})
		})
	}
}

// TestReapPriorityWithSameSenderSequenceOrdering verifies that transactions
// from the same sender are grouped together and ordered by sequence number,
// while different senders are ordered by their aggregated priority.
func TestReapPriorityWithSameSenderSequenceOrdering(t *testing.T) {
	txmp := setup(t, 0)

	// Add transactions from two senders with different priorities.
	// Sender A has high priority txs, sender B has low priority txs.
	// Each sender has multiple txs that should be ordered by sequence.
	txsA := []struct {
		priority int64
		sequence int
	}{
		{priority: 100, sequence: 0},
		{priority: 100, sequence: 1},
	}
	txsB := []struct {
		priority int64
		sequence int
	}{
		{priority: 10, sequence: 0},
		{priority: 10, sequence: 1},
	}

	for _, spec := range txsA {
		// The test application parses "sender=key=priority" and returns
		// the sender from the first part and priority from the last part.
		tx := types.Tx(fmt.Sprintf("sender-A-%d-0=%X=%d", spec.sequence, []byte{byte(spec.sequence)}, spec.priority))
		require.NoError(t, txmp.CheckTx(tx, nil, mempool.TxInfo{}))
	}
	for _, spec := range txsB {
		tx := types.Tx(fmt.Sprintf("sender-B-%d-0=%X=%d", spec.sequence, []byte{byte(spec.sequence + 10)}, spec.priority))
		require.NoError(t, txmp.CheckTx(tx, nil, mempool.TxInfo{}))
	}

	require.Equal(t, 4, txmp.Size())

	reaped := txmp.ReapMaxTxs(-1)
	require.Len(t, reaped, 4)

	// All of sender A's txs (high priority) should come before sender B's txs (low priority).
	// Within each sender, txs should be in sequence order.
	reapedPriorities := make([]int64, len(reaped))
	for i, cachedTx := range reaped {
		wtx := txmp.store.get(cachedTx.Key())
		require.NotNil(t, wtx)
		reapedPriorities[i] = wtx.priority
	}

	// First two should be high priority (sender A), last two low priority (sender B).
	assert.Equal(t, int64(100), reapedPriorities[0])
	assert.Equal(t, int64(100), reapedPriorities[1])
	assert.Equal(t, int64(10), reapedPriorities[2])
	assert.Equal(t, int64(10), reapedPriorities[3])
}

// TestReapPriorityStressWithManyTransactions adds many transactions with random
// priorities and verifies the reaped result is in non-increasing priority order.
func TestReapPriorityStressWithManyTransactions(t *testing.T) {
	txmp := setup(t, 0)

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	numTxs := 200

	priorityByKey := make(map[types.TxKey]int64)
	for i := 0; i < numTxs; i++ {
		priority := int64(rng.Intn(10000) + 1)
		tx := types.Tx(newTx(i, 0, []byte(fmt.Sprintf("stress-%d", i)), priority))
		require.NoError(t, txmp.CheckTx(tx, nil, mempool.TxInfo{}))
		priorityByKey[tx.Key()] = priority
	}
	require.Equal(t, numTxs, txmp.Size())

	t.Run("ReapMaxTxs all", func(t *testing.T) {
		reaped := txmp.ReapMaxTxs(-1)
		require.Len(t, reaped, numTxs)
		assertNonIncreasingPriority(t, reaped, priorityByKey)
	})

	t.Run("ReapMaxBytesMaxGas all", func(t *testing.T) {
		reaped := txmp.ReapMaxBytesMaxGas(-1, -1)
		require.Len(t, reaped, numTxs)
		assertNonIncreasingPriority(t, reaped, priorityByKey)
	})

	t.Run("ReapMaxTxs partial", func(t *testing.T) {
		reaped := txmp.ReapMaxTxs(50)
		require.Len(t, reaped, 50)
		assertNonIncreasingPriority(t, reaped, priorityByKey)
	})

	t.Run("ReapMaxBytesMaxGas partial by gas", func(t *testing.T) {
		reaped := txmp.ReapMaxBytesMaxGas(-1, 50)
		require.Len(t, reaped, 50)
		assertNonIncreasingPriority(t, reaped, priorityByKey)
	})
}

// TestReapPriorityAggregationEdgeCases demonstrates edge cases where the
// signer-based txSet grouping with aggregated priority causes transactions to
// be reaped in non-strict individual priority order.
//
// The CAT mempool groups transactions by signer and uses a gas-weighted average
// priority per signer set. This means a high-priority transaction from a sender
// who also has low-priority transactions can be reaped AFTER a lower-priority
// transaction from a different sender whose aggregate is higher.
func TestReapPriorityAggregationEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		// txs to add. The sender string determines the signer grouping.
		txs []struct {
			sender   string
			key      string
			priority int64
		}
		// expected per-tx priorities in reap order
		wantPriorities []int64
		// if true, the reap order violates strict per-tx priority ordering
		violatesStrictOrder bool
	}{
		{
			name: "low-priority tx from same sender drags down high-priority tx",
			txs: []struct {
				sender   string
				key      string
				priority int64
			}{
				// Sender A: priorities 100 and 1 → aggregate = (100+1)/2 = 50
				{sender: "alice", key: "01", priority: 100},
				{sender: "alice", key: "02", priority: 1},
				// Sender B: priority 60 → aggregate = 60
				{sender: "bob", key: "03", priority: 60},
			},
			// Bob's set (aggregate 60) comes before Alice's set (aggregate 50).
			// So bob's tx at priority 60 is reaped before alice's tx at priority 100.
			wantPriorities:      []int64{60, 100, 1},
			violatesStrictOrder: true,
		},
		{
			name: "many low-priority txs from same sender severely drag down one high-priority tx",
			txs: []struct {
				sender   string
				key      string
				priority int64
			}{
				// Sender A: one high-priority tx, two very low-priority txs
				// aggregate = (1000 + 1 + 1) / 3 = 334
				{sender: "alice", key: "01", priority: 1000},
				{sender: "alice", key: "02", priority: 1},
				{sender: "alice", key: "03", priority: 1},
				// Sender B: moderate priority → aggregate = 500
				{sender: "bob", key: "04", priority: 500},
			},
			// Bob (aggregate 500) before Alice (aggregate 334).
			// Bob's tx at priority 500 is reaped before Alice's tx at priority 1000.
			wantPriorities:      []int64{500, 1000, 1, 1},
			violatesStrictOrder: true,
		},
		{
			name: "uniform priorities within each sender preserves cross-sender ordering",
			txs: []struct {
				sender   string
				key      string
				priority int64
			}{
				// Sender A: all priority 100 → aggregate = 100
				{sender: "alice", key: "01", priority: 100},
				{sender: "alice", key: "02", priority: 100},
				// Sender B: all priority 50 → aggregate = 50
				{sender: "bob", key: "03", priority: 50},
				{sender: "bob", key: "04", priority: 50},
			},
			// No violation: Alice (100) before Bob (50).
			wantPriorities:      []int64{100, 100, 50, 50},
			violatesStrictOrder: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txmp := setup(t, 0)

			priorityByKey := make(map[types.TxKey]int64)
			for _, spec := range tc.txs {
				tx := types.Tx(fmt.Sprintf("%s=%s=%d", spec.sender, spec.key, spec.priority))
				err := txmp.CheckTx(tx, nil, mempool.TxInfo{})
				require.NoError(t, err)
				priorityByKey[tx.Key()] = spec.priority
			}
			require.Equal(t, len(tc.txs), txmp.Size())

			reaped := txmp.ReapMaxTxs(-1)
			require.Len(t, reaped, len(tc.txs))

			gotPriorities := make([]int64, len(reaped))
			for i, cachedTx := range reaped {
				gotPriorities[i] = priorityByKey[cachedTx.Key()]
			}

			// Verify the actual reap order matches expectations.
			assert.Equal(t, tc.wantPriorities, gotPriorities,
				"reap order should match expected order based on aggregated set priorities")

			// Verify whether strict per-tx priority ordering is violated.
			strictlyOrdered := true
			for i := 0; i < len(gotPriorities)-1; i++ {
				if gotPriorities[i] < gotPriorities[i+1] {
					strictlyOrdered = false
					break
				}
			}

			if tc.violatesStrictOrder {
				assert.False(t, strictlyOrdered,
					"expected strict per-tx priority ordering to be violated due to aggregation")
			} else {
				assert.True(t, strictlyOrdered,
					"expected strict per-tx priority ordering to be preserved")
			}
		})
	}
}

// assertNonIncreasingPriority asserts that the reaped transactions are in
// non-increasing priority order (i.e. each tx has priority >= the next tx).
func assertNonIncreasingPriority(t *testing.T, reaped []*types.CachedTx, priorityByKey map[types.TxKey]int64) {
	t.Helper()
	for i := 0; i < len(reaped)-1; i++ {
		currPriority := priorityByKey[reaped[i].Key()]
		nextPriority := priorityByKey[reaped[i+1].Key()]
		assert.GreaterOrEqualf(t, currPriority, nextPriority,
			"tx at index %d (priority %d) should have >= priority than tx at index %d (priority %d)",
			i, currPriority, i+1, nextPriority,
		)
	}
}
