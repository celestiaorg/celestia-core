package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

func TestTxKeyTrackerRecordAndLookup(t *testing.T) {
	tracker := newLegacyTxKeyTracker()
	signer := []byte("signer")
	txKey := types.Tx("tx-1").Key()

	tracker.recordSeenTx(&protomem.SeenTx{
		TxKey:    txKey[:],
		Signer:   signer,
		Sequence: 3,
	}, txKey, 9)

	entry := tracker.getByTxKey(txKey)
	require.NotNil(t, entry)
	require.Equal(t, signer, entry.signer)
	require.Equal(t, uint64(3), entry.sequence)
	require.Equal(t, uint16(9), entry.peer)

	lookupKey, ok := tracker.txKeyForSignerSequence(signer, 3)
	require.True(t, ok)
	require.Equal(t, txKey, lookupKey)
}

func TestTxKeyTrackerRemoveByTxKey(t *testing.T) {
	tracker := newLegacyTxKeyTracker()
	signer := []byte("signer")
	txKey := types.Tx("tx-2").Key()

	tracker.recordSeenTx(&protomem.SeenTx{
		TxKey:    txKey[:],
		Signer:   signer,
		Sequence: 5,
	}, txKey, 4)

	tracker.removeByTxKey(txKey)
	require.Nil(t, tracker.getByTxKey(txKey))
	_, ok := tracker.txKeyForSignerSequence(signer, 5)
	require.False(t, ok)
}

func TestTxKeyTrackerCleanupPeer(t *testing.T) {
	tracker := newLegacyTxKeyTracker()
	signer := []byte("signer")
	txKey := types.Tx("tx-3").Key()

	tracker.recordSeenTx(&protomem.SeenTx{
		TxKey:    txKey[:],
		Signer:   signer,
		Sequence: 7,
	}, txKey, 2)

	tracker.removePeer(2)
	entry := tracker.getByTxKey(txKey)
	require.NotNil(t, entry)
	require.Equal(t, uint16(0), entry.peer)

	tracker.markRequestFailed(txKey, 2)
	entry = tracker.getByTxKey(txKey)
	require.NotNil(t, entry)
	require.Equal(t, uint16(0), entry.peer)
}
