package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/types"
)

// cacheTestProposal caches a complete block at the reactor's current height
// and the given round, and returns that height.
func cacheTestProposal(t *testing.T, r *Reactor, round int32, ps *types.PartSet) int64 {
	t.Helper()
	r.pmtx.Lock()
	defer r.pmtx.Unlock()
	height := r.height
	if r.proposals[height] == nil {
		r.proposals[height] = make(map[int32]*proposalData)
	}
	r.proposals[height][round] = &proposalData{block: proptypes.NewCombinedPartSetFromOriginal(ps, false)}
	return height
}

// makeTestPartSet returns the part set of a small block whose content depends
// on seed, so different seeds give different part set hashes.
func makeTestPartSet(t *testing.T, seed byte) *types.PartSet {
	t.Helper()
	txs := make(types.Txs, 4)
	for i := range txs {
		txs[i] = make([]byte, 100_000)
		txs[i][0] = seed
		txs[i][1] = byte(i)
	}
	block := types.MakeBlock(1, types.Data{Txs: txs}, &types.Commit{}, nil)
	ps, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	return ps
}

// wantAllParts returns a want for every original and parity part of a block
// with the given number of original parts.
func wantAllParts(height int64, round int32, originalTotal uint32, prove bool) *proptypes.WantParts {
	parts := bits.NewBitArray(int(originalTotal) * 2)
	parts.Fill()
	return &proptypes.WantParts{
		Parts:             parts,
		Height:            height,
		Round:             round,
		Prove:             prove,
		MissingPartsCount: int32(originalTotal),
	}
}

// A peer that repeats a want gets each part at most MaxServesPerPart times.
func TestHandleWantsCapsRepeatedWants(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	height := cacheTestProposal(t, r, 0, ps)
	total := int(ps.Total())
	want := wantAllParts(height, 0, ps.Total(), false)

	r.handleWants(rp.ID(), want)
	require.Len(t, rp.sentParts(), total)

	// a peer that asks again, for example after it dropped a part, is served again
	r.handleWants(rp.ID(), want)
	require.Len(t, rp.sentParts(), 2*total)

	for i := 0; i < 50; i++ {
		r.handleWants(rp.ID(), want)
	}
	require.Len(t, rp.sentParts(), MaxServesPerPart*total)

	sendsPerPart := make(map[uint32]int)
	for _, part := range rp.sentParts() {
		sendsPerPart[part.Index]++
	}
	for index, sends := range sendsPerPart {
		require.Equal(t, MaxServesPerPart, sends, "part %d", index)
	}
}

// A round below -1 selects the latest cached round. Naming it by different
// rounds must not give the peer a new allowance each time.
func TestHandleWantsCapsRepeatedWantsAcrossAliasRounds(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	height := cacheTestProposal(t, r, 0, ps)
	total := int(ps.Total())

	r.handleWants(rp.ID(), wantAllParts(height, 0, ps.Total(), true))
	for round := int32(-2); round > -100; round-- {
		r.handleWants(rp.ID(), wantAllParts(height, round, ps.Total(), true))
	}
	require.Len(t, rp.sentParts(), MaxServesPerPart*total)

	p := r.getPeer(rp.ID())
	require.NotNil(t, p)
	p.mtx.RLock()
	defer p.mtx.RUnlock()
	require.Len(t, p.servedParts[height], 1, "served parts must be tracked under the cached round only")
}

// The same block proposed again in a later round is served again: the peer
// keeps a separate part set per round.
func TestHandleWantsServesSameBlockInLaterRound(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	height := cacheTestProposal(t, r, 0, ps)
	total := int(ps.Total())

	for i := 0; i < MaxServesPerPart+1; i++ {
		r.handleWants(rp.ID(), wantAllParts(height, 0, ps.Total(), true))
	}
	require.Len(t, rp.sentParts(), MaxServesPerPart*total)

	cacheTestProposal(t, r, 1, ps)
	r.handleWants(rp.ID(), wantAllParts(height, 1, ps.Total(), true))
	require.Len(t, rp.sentParts(), (MaxServesPerPart+1)*total)
}

// When the block cached at a round is replaced, the parts of the new block
// are served even if the old block's parts reached the cap.
func TestHandleWantsServesReplacedBlock(t *testing.T) {
	r, _ := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	first, second := makeTestPartSet(t, 1), makeTestPartSet(t, 2)
	require.Equal(t, first.Total(), second.Total())
	require.NotEqual(t, first.Hash(), second.Hash())
	total := int(first.Total())

	height := cacheTestProposal(t, r, 0, first)
	for i := 0; i < MaxServesPerPart+1; i++ {
		r.handleWants(rp.ID(), wantAllParts(height, 0, first.Total(), true))
	}
	require.Len(t, rp.sentParts(), MaxServesPerPart*total)

	cacheTestProposal(t, r, 0, second)
	r.handleWants(rp.ID(), wantAllParts(height, 0, second.Total(), true))
	sent := rp.sentParts()
	require.Len(t, sent, (MaxServesPerPart+1)*total)
	for _, part := range sent[MaxServesPerPart*total:] {
		require.Equal(t, second.GetPart(int(part.Index)).Bytes.Bytes(), part.Data, "part %d", part.Index)
	}
}

// A peer that does not read fills its send queue. Once a send fails, the
// reactor must stop building parts for that want, and must not count the
// parts it did not send.
func TestHandleWantsStopsWhenSendQueueIsFull(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 2)
	height := cacheTestProposal(t, r, 0, ps)

	r.handleWants(rp.ID(), wantAllParts(height, 0, ps.Total(), false))
	require.Len(t, rp.sentParts(), 2)
	require.Equal(t, 3, rp.attempts, "must stop after the first failed send")

	p := r.getPeer(rp.ID())
	require.NotNil(t, p)
	p.mtx.RLock()
	defer p.mtx.RUnlock()
	counts := p.servedParts[height][0].counts
	require.Equal(t, []uint8{1, 1, 0}, counts[:3])
}

func TestPeerState_UnservedParts(t *testing.T) {
	const height, round = int64(3), int32(0)
	blockA, blockB := []byte("block a"), []byte("block b")
	wanted := createBitArray(4, []int{0, 2})

	t.Run("a part is unserved until it was sent MaxServesPerPart times", func(t *testing.T) {
		ps := newTestPeerState()
		for i := 0; i < MaxServesPerPart; i++ {
			require.Equal(t, []int{0, 2}, ps.UnservedParts(height, round, blockA, wanted).GetTrueIndices())
			ps.RecordServedParts(height, round, blockA, []int{0})
		}
		require.Equal(t, []int{2}, ps.UnservedParts(height, round, blockA, wanted).GetTrueIndices())
	})

	t.Run("another block at the same round starts from zero", func(t *testing.T) {
		ps := newTestPeerState()
		ps.UnservedParts(height, round, blockA, wanted)
		for i := 0; i < MaxServesPerPart; i++ {
			ps.RecordServedParts(height, round, blockA, []int{0, 2})
		}
		require.Empty(t, ps.UnservedParts(height, round, blockA, wanted).GetTrueIndices())
		require.Equal(t, []int{0, 2}, ps.UnservedParts(height, round, blockB, wanted).GetTrueIndices())
	})

	t.Run("sends of a replaced block are not counted for the new one", func(t *testing.T) {
		ps := newTestPeerState()
		ps.UnservedParts(height, round, blockA, wanted)
		ps.UnservedParts(height, round, blockB, wanted)
		for i := 0; i < MaxServesPerPart; i++ {
			ps.RecordServedParts(height, round, blockA, []int{0, 2})
		}
		require.Equal(t, []int{0, 2}, ps.UnservedParts(height, round, blockB, wanted).GetTrueIndices())
	})

	t.Run("recording without a tracked block or with a bad index does nothing", func(t *testing.T) {
		ps := newTestPeerState()
		require.NotPanics(t, func() {
			ps.RecordServedParts(height, round, blockA, []int{0})
			ps.UnservedParts(height, round, blockA, wanted)
			ps.RecordServedParts(height, round, blockA, []int{-1, 4})
		})
		require.Equal(t, []int{0, 2}, ps.UnservedParts(height, round, blockA, wanted).GetTrueIndices())
	})

	t.Run("prune and delete drop the counts", func(t *testing.T) {
		ps := newTestPeerState()
		ps.UnservedParts(height, round, blockA, wanted)
		ps.UnservedParts(height, round+1, blockA, wanted)
		ps.UnservedParts(height+1, round, blockA, wanted)

		ps.DeleteRound(height, round)
		require.Len(t, ps.servedParts[height], 1)
		ps.prune(height + 1)
		require.NotContains(t, ps.servedParts, height)
		ps.DeleteHeight(height + 1)
		require.Empty(t, ps.servedParts)
	})
}
