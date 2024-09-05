package cat

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/types"
)

func TestPriorityBroadcastQueue(t *testing.T) {
	q := newPriorityBroadcastQueue()

	// if they are the same priority, they should be popped in the order they were added
	tx1 := types.Tx("tx1")
	key1 := tx1.Key()
	q.BroadcastToAll(1, key1)
	tx2 := types.Tx("tx2")
	key2 := tx2.Key()
	q.BroadcastToAll(1, key2)
	q.processIncomingTxs()

	outputKey, _ := q.Pop()
	require.Equal(t, key1, outputKey)

	outputKey, _ = q.Pop()
	require.Equal(t, key2, outputKey)

	q.BroadcastToAll(1, key1)
	q.BroadcastToAll(2, key2)
	q.processIncomingTxs()

	outputKey, _ = q.Pop()
	require.Equal(t, key2, outputKey)

	outputKey, _ = q.Pop()
	require.Equal(t, key1, outputKey)
}
