package propagation

import (
	"context"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
)

const storedTestHeight = int64(5)

// recordingPeer is a mock peer that records the recovery parts sent to it and
// accepts at most capacity of them, like a peer with a bounded send queue.
type recordingPeer struct {
	*mock.Peer
	mtx      sync.Mutex
	capacity int
	attempts int
	sent     []*propproto.RecoveryPart
}

func newRecordingPeer(capacity int) *recordingPeer {
	return &recordingPeer{Peer: mock.NewPeer(net.IP{127, 0, 0, 1}), capacity: capacity}
}

func (rp *recordingPeer) TrySend(e p2p.Envelope) bool {
	rp.mtx.Lock()
	defer rp.mtx.Unlock()
	part, ok := e.Message.(*propproto.RecoveryPart)
	if !ok {
		return true
	}
	rp.attempts++
	if len(rp.sent) >= rp.capacity {
		return false
	}
	rp.sent = append(rp.sent, part)
	return true
}

func (rp *recordingPeer) sentParts() []*propproto.RecoveryPart {
	rp.mtx.Lock()
	defer rp.mtx.Unlock()
	return append([]*propproto.RecoveryPart(nil), rp.sent...)
}

// newStoredBlockReactor returns a reactor that has committed past
// storedTestHeight and has a ~8 MB block (123 original parts) stored at
// storedTestHeight.
func newStoredBlockReactor(t *testing.T) (*Reactor, *types.PartSet) {
	t.Helper()
	reactors, _ := testBlockPropReactors(1, defaultTestP2PConf())
	r := reactors[0]

	txs := make(types.Txs, 64)
	for i := range txs {
		txs[i] = make([]byte, 125_000)
		txs[i][0] = byte(i)
	}
	block := types.MakeBlock(storedTestHeight, types.Data{Txs: txs}, &types.Commit{Height: storedTestHeight - 1}, nil)
	block.ProposerAddress = mockPubKey.Address()
	ps, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	r.store.SaveBlock(block, ps, &types.Commit{Height: storedTestHeight})

	r.pmtx.Lock()
	r.height = storedTestHeight + 10
	r.pmtx.Unlock()
	return r, ps
}

func addRecordingPeer(t *testing.T, r *Reactor, capacity int) *recordingPeer {
	t.Helper()
	rp := newRecordingPeer(capacity)
	r.setPeer(rp.ID(), newPeerState(context.Background(), rp, log.NewNopLogger()))
	return rp
}

func storedWant(total uint32, missing int32, indices ...int) *proptypes.WantParts {
	ba := bits.NewBitArray(int(total) * 2)
	for _, i := range indices {
		ba.SetIndex(i, true)
	}
	return &proptypes.WantParts{
		Parts:             ba,
		Height:            storedTestHeight,
		Round:             0,
		Prove:             true,
		MissingPartsCount: missing,
	}
}

func allOriginal(total uint32) []int {
	out := make([]int, total)
	for i := range out {
		out[i] = i
	}
	return out
}

// TestStoredHeightWantIsCheap is a regression test for CELESTIA-308. A small
// WantParts{Prove: true} for a committed height used to load, unmarshal and
// verify the whole stored block, even when the peer asked for one part.
func TestStoredHeightWantIsCheap(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	want := storedWant(ps.Total(), 1, 0)
	require.NoError(t, want.ValidateBasic())

	const n = 100
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < n; i++ {
		r.handleWants(rp.ID(), want)
	}
	runtime.ReadMemStats(&after)

	allocPerWant := (after.TotalAlloc - before.TotalAlloc) / n
	// One part is 64 KiB. Allow a few parts' worth of work per want.
	require.Less(t, allocPerWant, uint64(1<<20),
		"each stored-height want allocates %d bytes", allocPerWant)
	require.Len(t, rp.sentParts(), n)
}

func TestStoredHeightWantServesRequestedPartsWithProofs(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)

	parity := int(ps.Total()) + 1
	r.handleWants(rp.ID(), storedWant(ps.Total(), 3, 0, 7, parity))

	sent := rp.sentParts()
	require.Len(t, sent, 2, "only the stored original parts are served")
	root := ps.Header().Hash
	for i, wantIdx := range []uint32{0, 7} {
		got := sent[i]
		require.Equal(t, storedTestHeight, got.Height)
		require.Equal(t, wantIdx, got.Index)
		require.Equal(t, ps.GetPart(int(wantIdx)).Bytes.Bytes(), got.Data)
		require.NotNil(t, got.Proof)
		proof, err := merkle.ProofFromProto(&got.Proof, false)
		require.NoError(t, err)
		require.NoError(t, proof.Verify(root, got.Data))
	}
}

func TestStoredHeightWantRespectsMissingPartsCount(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)

	r.handleWants(rp.ID(), storedWant(ps.Total(), 4, allOriginal(ps.Total())...))
	require.Len(t, rp.sentParts(), 4)
}

// A peer that does not read fills its send queue. Once a send fails, the
// reactor must stop loading parts from the store for that want.
func TestStoredHeightWantStopsWhenSendQueueIsFull(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 2)

	r.handleWants(rp.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, rp.sentParts(), 2)
	require.Equal(t, 3, rp.attempts, "must stop after the first failed send")
}

// All peers share one budget for parts loaded from the block store.
func TestStoredHeightWantsShareGlobalBudget(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	r.storedPartsBudget = newTokenBucket(0, 5)
	peers := []*recordingPeer{addRecordingPeer(t, r, 1_000_000), addRecordingPeer(t, r, 1_000_000)}

	for _, rp := range peers {
		for i := 0; i < 10; i++ {
			r.handleWants(rp.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
		}
	}
	total := 0
	for _, rp := range peers {
		total += len(rp.sentParts())
	}
	require.Equal(t, 5, total)
}

// Wants for heights that are not committed must not be served from the store.
func TestUncommittedHeightWantIsNotServedFromStore(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	r.pmtx.Lock()
	r.height = storedTestHeight
	r.pmtx.Unlock()

	r.handleWants(rp.ID(), storedWant(ps.Total(), 1, 0))
	require.Empty(t, rp.sentParts())
}

func TestTokenBucket(t *testing.T) {
	now := time.Unix(0, 0)
	b := newTokenBucket(10, 3)
	b.now = func() time.Time { return now }
	b.last = now

	for i := 0; i < 3; i++ {
		require.True(t, b.allow())
	}
	require.False(t, b.allow(), "burst is used up")

	now = now.Add(100 * time.Millisecond)
	require.True(t, b.allow(), "one token refills after 100ms at 10/s")
	require.False(t, b.allow())

	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		require.True(t, b.allow())
	}
	require.False(t, b.allow(), "refill is capped at the burst size")
}
