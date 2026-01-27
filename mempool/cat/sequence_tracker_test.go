package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
)

func TestSequenceTrackerRecordRanges(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	msg := &protomem.SeenTx{
		Signer:      signer,
		MinSequence: 5,
		MaxSequence: 10,
	}
	tracker.recordSeenTx(msg, 7)

	ranges := tracker.rangesForSigner(signer)
	require.Len(t, ranges, 1)
	require.Equal(t, uint64(5), ranges[7].min)
	require.Equal(t, uint64(10), ranges[7].max)
}

func TestSequenceTrackerMonotonicUpdates(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 3, MaxSequence: 8}, 2)
	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 2, MaxSequence: 7}, 2)
	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 4, MaxSequence: 9}, 2)

	ranges := tracker.rangesForSigner(signer)
	require.Len(t, ranges, 1)
	require.Equal(t, uint64(4), ranges[2].min)
	require.Equal(t, uint64(9), ranges[2].max)
}

func TestSequenceTrackerRemoveBelowSequence(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 1, MaxSequence: 5}, 1)
	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 2, MaxSequence: 10}, 2)

	tracker.removeBelowSequence(signer, 6)

	ranges := tracker.rangesForSigner(signer)
	require.Len(t, ranges, 1)
	require.Equal(t, uint64(6), ranges[2].min)
	require.Equal(t, uint64(10), ranges[2].max)
}

func TestSequenceTrackerRemovePeer(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 1, MaxSequence: 5}, 3)
	tracker.removePeer(3)

	require.Nil(t, tracker.rangesForSigner(signer))
}

func TestSequenceTrackerPeersForSequence(t *testing.T) {
	tracker := newSequenceTracker()
	signer := []byte("signer")

	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 1, MaxSequence: 5}, 1)
	tracker.recordSeenTx(&protomem.SeenTx{Signer: signer, MinSequence: 6, MaxSequence: 10}, 2)

	peers := tracker.getPeersForSignerSequence(signer, 4)
	require.Len(t, peers, 1)
	require.Equal(t, uint16(1), peers[0])
}
