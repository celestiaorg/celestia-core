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

	ps.ExpireRequests(1, 0, time.Hour, time.Hour)
	got, has := ps.GetRequests(1, 0)
	require.True(t, has)
	require.True(t, got.GetIndex(1), "recent requests are kept")

	ps.ExpireRequests(1, 0, 0, time.Hour)
	got, has = ps.GetRequests(1, 0)
	require.True(t, has)
	require.True(t, got.IsEmpty(), "old requests are cleared")

	ps.ExpireRequests(2, 0, 0, 0) // unknown height is a no-op
}

// A request to a peer that has sent nothing for idleTimeout expires, so a
// want the peer refused is sent again on the next retry tick. A peer that is
// still sending parts keeps its requests.
func TestPeerStateExpireIdleRequests(t *testing.T) {
	ps := newPeerState(context.Background(), mock.NewPeer(net.IP{127, 0, 0, 1}), log.NewNopLogger())
	request := func() {
		reqs := bits.NewBitArray(4)
		reqs.SetIndex(1, true)
		ps.AddRequests(1, 0, reqs)
	}
	backdate := func(requested, received time.Duration) {
		ps.mtx.Lock()
		defer ps.mtx.Unlock()
		ps.state[1][0].requestedAt = time.Now().Add(-requested)
		if received > 0 {
			ps.state[1][0].receivedAt = time.Now().Add(-received)
		}
	}
	requested := func() bool {
		got, has := ps.GetRequests(1, 0)
		require.True(t, has)
		return got.GetIndex(1)
	}

	request()
	backdate(2*time.Second, 0)
	ps.ExpireRequests(1, 0, time.Hour, time.Second)
	require.False(t, requested(), "nothing received for idleTimeout: expired")

	request()
	backdate(2*time.Second, 100*time.Millisecond)
	ps.ExpireRequests(1, 0, time.Hour, time.Second)
	require.True(t, requested(), "a part arrived recently: kept")

	request()
	ps.ExpireRequests(1, 0, time.Hour, time.Second)
	require.True(t, requested(), "a new request is kept")

	ps.MarkPartReceived(1, 0)
	ps.MarkPartReceived(2, 0) // unknown height is a no-op
}

// A want that the server refuses for lack of budget is sent again once the
// peer goes quiet, long before the request would expire by age.
func TestRefusedStoredWantIsRetriedWhenPeerGoesQuiet(t *testing.T) {
	reactors, switches := testBlockPropReactors(2, defaultTestP2PConf())
	stopSwitchesOnCleanup(t, switches)
	server, client := reactors[0], reactors[1]
	ps := storeTestBlock(t, server)

	clientOnServer := server.getPeer(client.self)
	require.NotNil(t, clientOnServer)
	var budgetChecks atomic.Int64
	b := clientOnServer.storedPartsBudget
	b.mtx.Lock()
	b.rate, b.tokens = 0, 0
	b.now = func() time.Time {
		budgetChecks.Add(1)
		return time.Now()
	}
	b.mtx.Unlock()
	// Another peer is asking, so the server applies the client's own budget.
	other := addRecordingPeer(t, server, 1_000_000)
	server.handleWants(other.ID(), storedWant(ps.Total(), 1, 0))

	// Only idleness can expire the request in this test.
	client.requestTimeout = time.Hour
	client.requestIdleTimeout = 100 * time.Millisecond
	serverPeer := client.getPeer(server.self)
	require.NotNil(t, serverPeer)
	serverPeer.SetConsensusPeerState(&MockPeerStateEditor{height: storedTestHeight + 10})

	psh := ps.Header()
	client.AddCommitment(storedTestHeight, 0, &psh)
	require.Eventually(t, func() bool {
		return budgetChecks.Load() > 0
	}, 2*time.Second, 10*time.Millisecond, "server refuses the first want")
	_, parts, _ := client.GetProposal(storedTestHeight, 0)
	require.True(t, parts.BitArray().IsEmpty(), "the refused want sent no parts")

	b.mtx.Lock()
	b.rate, b.burst, b.tokens = 10_000, 10_000, 10_000
	b.mtx.Unlock()

	require.Eventually(t, func() bool {
		client.retryWants()
		_, parts, has := client.GetProposal(storedTestHeight, 0)
		return has && parts.IsComplete()
	}, 3*time.Second, 100*time.Millisecond)
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

// An incomplete entry for the committed block must not park a catchup want:
// nothing would ever fill it.
func TestIncompleteCommittedEntryWantIsServedFromStore(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	psh := ps.Header()
	// A late compact block leaves a header-only entry at the committed height.
	r.AddCommitment(storedTestHeight, 0, &psh)
	r.pmtx.Lock()
	r.height = storedTestHeight + 1
	r.pmtx.Unlock()

	r.handleWants(rp.ID(), storedWant(ps.Total(), 2, 0, 1))
	require.Len(t, rp.sentParts(), 2)
	parts, has, _ := r.getCachedState(storedTestHeight, 0, true)
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

// One peer that floods stored-height wants must not use up the budget that
// all peers share, or honest catchup peers get no parts.
func TestFloodingPeerCannotStarveStoredCatchup(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	r.storedPartsBudget = newTokenBucket(0, float64(StoredPartsPerPeerBurst)+float64(ps.Total())+1)
	attacker := addRecordingPeer(t, r, 1_000_000)
	// No refill, so the test does not depend on how fast the loop runs.
	r.getPeer(attacker.ID()).storedPartsBudget = newTokenBucket(0, float64(StoredPartsPerPeerBurst))
	honest := addRecordingPeer(t, r, 1_000_000)
	// The honest peer is catching up: it has asked for a part already.
	r.handleWants(honest.ID(), storedWant(ps.Total(), 1, 0))
	require.Len(t, honest.sentParts(), 1)

	for i := 0; i < 2*StoredPartsPerPeerBurst/int(ps.Total()); i++ {
		r.handleWants(attacker.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	}
	require.Len(t, attacker.sentParts(), StoredPartsPerPeerBurst, "one peer is capped below the shared budget")

	r.handleWants(honest.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, honest.sentParts(), 1+int(ps.Total()))
}

// A peer that does not read its socket makes every send fail. Each part is
// still loaded from the store, so it must count against the peer's budget.
// The peer's budget then keeps it from draining the shared budget.
func TestFullQueuePeerCannotDrainSharedBudget(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	r.storedPartsBudget = newTokenBucket(0, float64(StoredPartsPerPeerBurst)+float64(ps.Total())+1)
	full := addRecordingPeer(t, r, 0)
	r.getPeer(full.ID()).storedPartsBudget = newTokenBucket(0, float64(StoredPartsPerPeerBurst))
	honest := addRecordingPeer(t, r, 1_000_000)
	// The honest peer is catching up: it has asked for a part already.
	r.handleWants(honest.ID(), storedWant(ps.Total(), 1, 0))
	require.Len(t, honest.sentParts(), 1)

	for i := 0; i < 2*StoredPartsPerPeerBurst; i++ {
		r.handleWants(full.ID(), storedWant(ps.Total(), 1, 0))
	}
	require.Empty(t, full.sentParts())
	full.mtx.Lock()
	attempts := full.attempts
	full.mtx.Unlock()
	require.Equal(t, StoredPartsPerPeerBurst, attempts, "store reads are bounded by the peer's budget")

	r.handleWants(honest.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, honest.sentParts(), 1+int(ps.Total()))
}

// A new peer starts with a small budget, below the burst: enough to catch up
// a typical block at once, but reconnecting gives less than a full burst.
func TestNewPeerStartsWithSmallStoredPartsBudget(t *testing.T) {
	require.Less(t, StoredPartsPerPeerInitial, StoredPartsPerPeerBurst)
	r, ps := newStoredBlockReactor(t)
	r.storedPartsBudget = newTokenBucket(0, 10_000)

	rp := newRecordingPeer(1_000_000)
	peer := newPeerState(context.Background(), rp, log.NewNopLogger())
	r.setPeer(rp.ID(), peer)
	r.handleWants(rp.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, rp.sentParts(), int(ps.Total()), "a new peer catches up a typical block at once")

	b := peer.storedPartsBudget
	b.mtx.Lock()
	tokens := b.tokens
	b.mtx.Unlock()
	require.Less(t, tokens, float64(StoredPartsPerPeerInitial), "the parts came from the initial balance")
}

// With the real limits, one peer that saved up its whole burst cannot use up
// the shared burst, so other peers are still served.
func TestFullPeerBurstLeavesSharedBudgetForOthers(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	r.storedPartsBudget = newTokenBucket(0, float64(types.MaxBlockPartsCount))
	attacker := addRecordingPeer(t, r, 1_000_000)
	r.getPeer(attacker.ID()).storedPartsBudget = newTokenBucket(0, float64(StoredPartsPerPeerBurst))
	honest := addRecordingPeer(t, r, 1_000_000)
	// The honest peer is catching up: it has asked for a part already.
	r.handleWants(honest.ID(), storedWant(ps.Total(), 1, 0))
	require.Len(t, honest.sentParts(), 1)

	for i := 0; i < 2*int(types.MaxBlockPartsCount)/int(ps.Total()); i++ {
		r.handleWants(attacker.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	}
	r.handleWants(honest.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, honest.sentParts(), 1+int(ps.Total()))
}

// A peer that is the only one asking for stored parts has no one to be fair
// to, so its own budget does not cap it: catchup is as fast as with the
// shared budget alone.
func TestLonePeerIsNotCappedByItsStoredPartsBudget(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	rp := addRecordingPeer(t, r, 1_000_000)
	r.getPeer(rp.ID()).storedPartsBudget = newTokenBucket(0, 10)

	r.handleWants(rp.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, rp.sentParts(), int(ps.Total()))
}

// A peer that floods while alone cannot use the reserve that the shared
// budget keeps for other peers, so an honest peer that starts asking is
// served at once. The flooder is then capped by its own budget.
func TestHonestPeerIsServedAtOnceAfterJoiningAFlood(t *testing.T) {
	r, ps := newStoredBlockReactor(t)
	// No refill, so only the reserve can serve the honest peer.
	r.storedPartsBudget = newTokenBucket(0, float64(types.MaxBlockPartsCount))
	attacker := addRecordingPeer(t, r, 1_000_000)
	r.getPeer(attacker.ID()).storedPartsBudget = newTokenBucketWithTokens(0, float64(StoredPartsPerPeerBurst), StoredPartsPerPeerInitial)
	honest := addRecordingPeer(t, r, 1_000_000)
	flood := func() {
		for i := 0; i < 4*int(types.MaxBlockPartsCount)/int(ps.Total()); i++ {
			r.handleWants(attacker.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
		}
	}

	flood()
	require.Equal(t, int(types.MaxBlockPartsCount)-StoredPartsPerPeerInitial, len(attacker.sentParts()), "a lone peer leaves the reserve")
	r.handleWants(honest.ID(), storedWant(ps.Total(), int32(ps.Total()), allOriginal(ps.Total())...))
	require.Len(t, honest.sentParts(), int(ps.Total()), "the honest peer is served from the reserve")

	before := len(attacker.sentParts())
	flood()
	require.Equal(t, before, len(attacker.sentParts()), "the flooder is capped once another peer asks")
}

// Under contention a block larger than the peer's budget is served in part.
// The rest comes through retries, with the real per-peer limits.
func TestPartlyServedStoredWantCompletesOnRetry(t *testing.T) {
	reactors, switches := testBlockPropReactors(2, defaultTestP2PConf())
	stopSwitchesOnCleanup(t, switches)
	server, client := reactors[0], reactors[1]

	txs := make(types.Txs, 300) // 300 x 125 KB is about 572 parts, over StoredPartsPerPeerInitial
	for i := range txs {
		txs[i] = make([]byte, 125_000)
		txs[i][0], txs[i][1] = byte(i), byte(i>>8)
	}
	block := types.MakeBlock(storedTestHeight, types.Data{Txs: txs}, &types.Commit{Height: storedTestHeight - 1}, nil)
	block.ProposerAddress = mockPubKey.Address()
	ps, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.Greater(t, int(ps.Total()), StoredPartsPerPeerInitial)
	server.store.SaveBlock(block, ps, &types.Commit{Height: storedTestHeight})
	server.pmtx.Lock()
	server.height = storedTestHeight + 10
	server.pmtx.Unlock()

	// Another peer keeps asking, so the server applies per-peer budgets.
	other := addRecordingPeer(t, server, 1_000_000)
	server.handleWants(other.ID(), storedWant(ps.Total(), 1, 0))

	serverPeer := client.getPeer(server.self)
	require.NotNil(t, serverPeer)
	serverPeer.SetConsensusPeerState(&MockPeerStateEditor{height: storedTestHeight + 10})
	psh := ps.Header()
	client.AddCommitment(storedTestHeight, 0, &psh)

	require.Eventually(t, func() bool {
		_, parts, has := client.GetProposal(storedTestHeight, 0)
		return has && !parts.BitArray().IsEmpty() && !parts.IsComplete()
	}, 2*time.Second, 5*time.Millisecond, "the first want is served in part")
	// The rest comes once the request goes idle and is sent again. The
	// timing of that retry is covered by TestPeerStateExpireIdleRequests and
	// TestRefusedStoredWantIsRetriedWhenPeerGoesQuiet; the limit here only has
	// to absorb a slow -race run.
	require.Eventually(t, func() bool {
		server.handleWants(other.ID(), storedWant(ps.Total(), 1, 0)) // still contended
		client.retryWants()
		_, parts, has := client.GetProposal(storedTestHeight, 0)
		return has && parts.IsComplete()
	}, 15*time.Second, 100*time.Millisecond)
}

func TestRecentPeers(t *testing.T) {
	now := time.Unix(0, 0)
	r := newRecentPeers(5 * time.Second)
	r.now = func() time.Time { return now }

	require.False(t, r.touch("a"), "one peer alone")
	require.False(t, r.touch("a"), "the same peer again")
	now = now.Add(4 * time.Second)
	require.True(t, r.touch("b"), "a asked within the window")
	now = now.Add(4 * time.Second)
	require.False(t, r.touch("b"), "a asked 8 s ago and expired; b itself does not count")
	require.True(t, r.touch("a"), "b asked now")
	now = now.Add(6 * time.Second)
	require.False(t, r.touch("a"), "b expired")
	require.Len(t, r.seen, 1, "expired peers are dropped")
}

func TestTokenBucketAllowAbove(t *testing.T) {
	b := newTokenBucket(0, 5)
	require.True(t, b.allowAbove(3))
	require.True(t, b.allowAbove(3))
	require.False(t, b.allowAbove(3), "3 tokens left: taking one would go below the reserve")
	require.True(t, b.allow(), "allow may use the reserve")
}
