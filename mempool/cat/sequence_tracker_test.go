package cat

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

func recordSeen(t *testing.T, tracker *sequenceTracker, signer []byte, txKey types.TxKey, sequence uint64, peerID uint16) {
	t.Helper()
	msg := &protomem.SeenTx{
		TxKey:       txKey[:],
		Signer:      signer,
		Sequence:    sequence,
		MinSequence: 1,
		MaxSequence: sequence,
	}
	tracker.recordSeenTx(msg, txKey, peerID)
}

func TestSequenceTrackerRecordAndGet(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")
	key := types.Tx("tx1").Key()

	recordSeen(t, tracker, signer, key, 1, 5)

	entry := tracker.getByTxKey(key)
	require.NotNil(t, entry)
	require.Equal(t, uint64(1), entry.sequence)
	require.Equal(t, []uint16{5}, entry.peerIDs())

	entries := tracker.entriesForSigner(signer)
	require.Len(t, entries, 1)
	require.Equal(t, key, entries[0].txKey)
}

func TestSequenceTrackerRejectsOutOfRangeSequence(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")
	key := types.Tx("invalid").Key()

	msg := &protomem.SeenTx{
		TxKey:       key[:],
		Signer:      signer,
		Sequence:    10,
		MinSequence: 1,
		MaxSequence: 5,
	}
	tracker.recordSeenTx(msg, key, 1)
	require.Empty(t, tracker.entriesForSigner(signer))
}

func TestSequenceTrackerRemoveByTxKey(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")
	key := types.Tx("tx-remove").Key()

	recordSeen(t, tracker, signer, key, 3, 6)
	tracker.removeByTxKey(key)
	require.Empty(t, tracker.entriesForSigner(signer))
}

func TestSequenceTrackerMarkRequestedAndFailed(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")
	key := types.Tx("tx-request").Key()

	recordSeen(t, tracker, signer, key, 7, 12)
	tracker.markRequested(key, 12)

	entry := tracker.getByTxKey(key)
	require.NotNil(t, entry)
	require.Equal(t, []uint16{12}, entry.peerIDs())

	tracker.markRequestFailed(key, 12)
	entry = tracker.getByTxKey(key)
	require.NotNil(t, entry)
	require.Nil(t, entry.peerIDs())
}

func TestSequenceTrackerRemovePeerClearsRanges(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")
	key := types.Tx("tx-peer").Key()

	recordSeen(t, tracker, signer, key, 4, 9)
	tracker.markRequested(key, 9)
	tracker.removePeer(9)

	entry := tracker.getByTxKey(key)
	require.NotNil(t, entry)
	require.Nil(t, entry.peerIDs())
}

func TestSequenceTrackerConcurrentAccess(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	const total = 2000
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			key := types.Tx(fmt.Sprintf("tx-%d", i)).Key()
			recordSeen(t, tracker, signer, key, uint64(i+1), uint16(i%5+1))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			tracker.entriesForSigner(signer)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			tracker.removePeer(uint16(i%5 + 1))
		}
	}()

	wg.Wait()
}
