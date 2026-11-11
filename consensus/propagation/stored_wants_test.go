package propagation

import (
	"context"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
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

// recordingPeer is a mock peer that counts the wants sent to it, records the recovery
// parts and accepts at most capacity of them, like a peer with a bounded send queue.
type recordingPeer struct {
	*mock.Peer
	mtx      sync.Mutex
	capacity int
	attempts int
	sent     []*propproto.RecoveryPart
	wants    int
}

func newRecordingPeer(capacity int) *recordingPeer {
	return &recordingPeer{Peer: mock.NewPeer(net.IP{127, 0, 0, 1}), capacity: capacity}
}

func (rp *recordingPeer) TrySend(e p2p.Envelope) bool {
	rp.mtx.Lock()
	defer rp.mtx.Unlock()
	if _, ok := e.Message.(*propproto.WantParts); ok {
		rp.wants++
		return true
	}
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

func (rp *recordingPeer) sentWants() int {
	rp.mtx.Lock()
	defer rp.mtx.Unlock()
	return rp.wants
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
	reactors, switches := testBlockPropReactors(1, defaultTestP2PConf())
	stopSwitchesOnCleanup(t, switches)
	r := reactors[0]
	return r, storeTestBlock(t, r)
}

// storeTestBlock stores a ~8 MB block (123 original parts) at
// storedTestHeight and moves the reactor past that height.
func storeTestBlock(t *testing.T, r *Reactor) *types.PartSet {
	t.Helper()
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
	return ps
}

// A catchup request that the server drops (here, because the stored parts
// budget is empty) must be sent again later, or the requester stays stuck.
func TestDroppedStoredCatchupRequestIsRetried(t *testing.T) {
	reactors, switches := testBlockPropReactors(2, defaultTestP2PConf())
	stopSwitchesOnCleanup(t, switches)
	server, client := reactors[0], reactors[1]
	ps := storeTestBlock(t, server)

	server.storedPartsBudget = newTokenBucket(0, 0)
	// Count budget checks to tell when the server has processed the request.
	var budgetChecks atomic.Int64
	server.storedPartsBudget.now = func() time.Time {
		budgetChecks.Add(1)
		return time.Now()
	}
	client.requestTimeout = 200 * time.Millisecond
	serverPeer := client.getPeer(server.self)
	require.NotNil(t, serverPeer)
	serverPeer.SetConsensusPeerState(&MockPeerStateEditor{height: storedTestHeight + 10})

	psh := ps.Header()
	client.AddCommitment(storedTestHeight, 0, &psh)
	require.Eventually(t, func() bool {
		requests, has := serverPeer.GetRequests(storedTestHeight, 0)
		return has && !requests.IsEmpty()
	}, 2*time.Second, 10*time.Millisecond, "client sends a catchup request")

	// Wait until the server has processed the request and dropped it.
	require.Eventually(t, func() bool {
		return budgetChecks.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)
	_, parts, _ := client.GetProposal(storedTestHeight, 0)
	require.True(t, parts.BitArray().IsEmpty(), "server sent no parts")

	// The server can now serve parts again.
	b := server.storedPartsBudget
	b.mtx.Lock()
	b.burst, b.tokens = 10_000, 10_000
	b.mtx.Unlock()

	require.Eventually(t, func() bool {
		client.retryWants()
		_, parts, has := client.GetProposal(storedTestHeight, 0)
		return has && parts.IsComplete()
	}, 3*time.Second, 100*time.Millisecond)
}

func TestPeerStateExpireRequests(t *testing.T) {
	ps := newPeerState(context.Background(), mock.NewPeer(net.IP{127, 0, 0, 1}), log.NewNopLogger())
	reqs := bits.NewBitArray(4)
	reqs.SetIndex(1, true)
	ps.AddRequests(1, 0, reqs)

	ps.ExpireRequests(1, 0, time.Hour)
	got, has := ps.GetRequests(1, 0)
	require.True(t, has)
	require.True(t, got.GetIndex(1), "recent requests are kept")

	ps.ExpireRequests(1, 0, 0)
	got, has = ps.GetRequests(1, 0)
	require.True(t, has)
	require.True(t, got.IsEmpty(), "old requests are cleared")

	ps.ExpireRequests(2, 0, 0) // unknown height is a no-op
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
	// the store, not p.height, decides whether a height is committed.
	r.pmtx.Lock()
	r.height = storedTestHeight + 1
	r.pmtx.Unlock()

	want := storedWant(ps.Total(), 1, 0)
	want.Height = storedTestHeight + 1
	r.handleWants(rp.ID(), want)
	require.Empty(t, rp.sentParts())
}

// An incomplete entry for the committed block, created after Prune filled the
// entries it saw, must not park a catchup want: nothing would ever fill it.
func TestIncompleteCommittedEntryWantIsServedFromStore(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	psh := ps.Header()
	// Prune's background pass fills this entry; once it is complete, the pass's
	// snapshot is taken and cannot include the late entry.
	r.AddCommitment(storedTestHeight, 1, &psh)
	r.Prune(storedTestHeight)
	require.Eventually(t, func() bool {
		_, parts, has, _ := r.getCachedState(storedTestHeight, 1, true)
		return has && parts.IsComplete()
	}, 5*time.Second, 5*time.Millisecond)
	r.AddCommitment(storedTestHeight, 0, &psh)

	r.handleWants(rp.ID(), storedWant(ps.Total(), 2, 0, 1))
	require.Len(t, rp.sentParts(), 2)
	_, parts, has, _ := r.getCachedState(storedTestHeight, 0, true)
	require.True(t, has)
	require.False(t, parts.IsComplete(), "the parts came from the store, not the cache")
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
