package chunked

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"

	cmtbits "github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func makeBody(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func insertParamsFor(enc *EncodedTx, originPeer uint16) InsertParams {
	body := enc.Body
	key := types.Tx(body).Key()
	return InsertParams{
		TxKey:      key,
		PartsRoot:  enc.PartsRoot,
		NumParts:   enc.NumParts(),
		LastLength: enc.LastLength,
		LeafHashes: enc.LeafHashes,
		OriginPeer: originPeer,
	}
}

func TestStoreInsertCollecting(t *testing.T) {
	body := makeBody(t, 3*ChunkSize+10)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore()
	state, err := s.Insert(insertParamsFor(enc, 7))
	require.NoError(t, err)
	require.Equal(t, StateCollecting, state.State())
	require.Equal(t, uint32(8), state.NumParts) // 4 originals + 4 parity
	require.Equal(t, uint32(4), state.K)
	require.False(t, state.CanDecode())

	// Inserting the same key again must fail.
	_, err = s.Insert(insertParamsFor(enc, 7))
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestStoreInstallAndReconstruct(t *testing.T) {
	body := makeBody(t, 4*ChunkSize-37)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore()
	state, err := s.Insert(insertParamsFor(enc, 1))
	require.NoError(t, err)

	// Install K originals.
	k := int(state.K)
	for i := 0; i < k; i++ {
		require.NoError(t, s.ChargeChunk(state, len(enc.Originals[i])))
		justCompleted, err := state.Install(uint32(i), enc.Originals[i])
		require.NoError(t, err)
		// Should complete on the K-th install.
		if i == k-1 {
			require.True(t, justCompleted)
		} else {
			require.False(t, justCompleted)
		}
	}
	require.True(t, state.CanDecode())

	got, err := s.ReconstructAndVerify(state)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got))
	require.Equal(t, StateReconstructed, state.State())
}

func TestStoreReconstructFromParity(t *testing.T) {
	body := makeBody(t, 6*ChunkSize)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore()
	state, err := s.Insert(insertParamsFor(enc, 2))
	require.NoError(t, err)

	// Install all K parity chunks instead.
	k := int(state.K)
	for i := 0; i < k; i++ {
		idx := uint32(k + i)
		require.NoError(t, s.ChargeChunk(state, len(enc.Parity[i])))
		_, err := state.Install(idx, enc.Parity[i])
		require.NoError(t, err)
	}
	require.True(t, state.CanDecode())

	got, err := s.ReconstructAndVerify(state)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got))
}

func TestStoreReconstructRejectsWrongBody(t *testing.T) {
	body := makeBody(t, 2*ChunkSize+5)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore()
	// Insert with a fake (wrong) tx_key.
	params := insertParamsFor(enc, 3)
	params.TxKey[0] ^= 0xff
	state, err := s.Insert(params)
	require.NoError(t, err)

	for i := uint32(0); i < state.K; i++ {
		require.NoError(t, s.ChargeChunk(state, len(enc.Originals[i])))
		_, err := state.Install(i, enc.Originals[i])
		require.NoError(t, err)
	}
	_, err = s.ReconstructAndVerify(state)
	require.Error(t, err)
}

func TestStoreMemoryCapsGlobal(t *testing.T) {
	s := NewStore().WithLimits(1000, 10_000, time.Hour)
	require.NoError(t, s.Reserve(1, 600))
	require.NoError(t, s.Reserve(2, 400))
	require.ErrorIs(t, s.Reserve(3, 1), ErrGlobalCapExceeded)
}

func TestStoreMemoryCapsPerPeer(t *testing.T) {
	s := NewStore().WithLimits(10_000, 500, time.Hour)
	require.NoError(t, s.Reserve(1, 400))
	require.ErrorIs(t, s.Reserve(1, 200), ErrPerPeerCapExceeded)
	// A different peer is unaffected.
	require.NoError(t, s.Reserve(2, 400))
}

func TestStoreSweepExpired(t *testing.T) {
	body := makeBody(t, 2*ChunkSize)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore().WithLimits(1<<30, 1<<30, 10*time.Millisecond)
	state, err := s.Insert(insertParamsFor(enc, 9))
	require.NoError(t, err)

	require.NoError(t, s.ChargeChunk(state, len(enc.Originals[0])))
	_, err = state.Install(0, enc.Originals[0])
	require.NoError(t, err)

	// Should not be expired yet.
	require.Empty(t, s.SweepExpired(time.Now()))

	time.Sleep(20 * time.Millisecond)
	evicted := s.SweepExpired(time.Now())
	require.Len(t, evicted, 1)
	require.False(t, s.Has(state.TxKey))
}

func TestStoreMissingFromPeer(t *testing.T) {
	body := makeBody(t, 4*ChunkSize)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore()
	state, err := s.Insert(insertParamsFor(enc, 1))
	require.NoError(t, err)

	// Peer 5 advertises chunks {0,2,5}; we already have 0.
	have := cmtbits.NewBitArray(int(state.NumParts))
	have.SetIndex(0, true)
	have.SetIndex(2, true)
	have.SetIndex(5, true)
	state.RecordHaves(5, have)

	require.NoError(t, s.ChargeChunk(state, len(enc.Originals[0])))
	_, err = state.Install(0, enc.Originals[0])
	require.NoError(t, err)

	missing := state.MissingFromPeer(5)
	require.NotNil(t, missing)
	require.False(t, missing.GetIndex(0)) // we already have it
	require.True(t, missing.GetIndex(2))
	require.True(t, missing.GetIndex(5))

	// Mark chunk 2 inflight from peer 5; MissingFromPeer must skip it.
	infl := cmtbits.NewBitArray(int(state.NumParts))
	infl.SetIndex(2, true)
	state.MarkInflight(5, infl)
	missing2 := state.MissingFromPeer(5)
	require.False(t, missing2.GetIndex(2))
	require.True(t, missing2.GetIndex(5))
}

func TestStoreInsertReconstructed(t *testing.T) {
	body := makeBody(t, 2*ChunkSize+1)
	enc, err := Encode(body)
	require.NoError(t, err)

	s := NewStore()
	state, err := s.InsertReconstructed(insertParamsFor(enc, 0), enc)
	require.NoError(t, err)
	require.Equal(t, StateReconstructed, state.State())
	require.True(t, bytes.Equal(body, state.Body))
	require.True(t, state.CanDecode())
}
