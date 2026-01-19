package cat

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

func recordSeen(t *testing.T, tracker *sequenceTracker, signer []byte, sequence uint64, peerID uint16) {
	t.Helper()
	txKey := types.Tx(fmt.Sprintf("tx-%d-%d", sequence, peerID)).Key()
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

	recordSeen(t, tracker, signer, 1, 5)

	entries := tracker.entriesForSigner(signer)
	require.Len(t, entries, 1)
	require.Equal(t, uint64(1), entries[0].sequence)
	require.Equal(t, []uint16{5}, entries[0].peerIDs())
}

func TestSequenceTrackerRejectsOutOfRangeSequence(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")
	txKey := types.Tx("tx-out-of-range").Key()

	msg := &protomem.SeenTx{
		TxKey:       txKey[:],
		Signer:      signer,
		Sequence:    10,
		MinSequence: 1,
		MaxSequence: 5,
	}
	tracker.recordSeenTx(msg, txKey, 1)
	require.Empty(t, tracker.entriesForSigner(signer))
}

func TestSequenceTrackerRemoveBySignerSequence(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	recordSeen(t, tracker, signer, 3, 6)
	tracker.removeBySignerSequence(signer, 3)
	require.Empty(t, tracker.entriesForSigner(signer))
}

func TestSequenceTrackerRemovePeerClearsRanges(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	recordSeen(t, tracker, signer, 4, 9)
	tracker.removePeer(9)

	entries := tracker.entriesForSigner(signer)
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].peerIDs())
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
			recordSeen(t, tracker, signer, uint64(i+1), uint16(i%5+1))
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
