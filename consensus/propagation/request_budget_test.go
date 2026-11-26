package propagation

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// TestRequestsBoundedAfterMempoolRecovery checks that a node which rebuilt
// most of a block from its mempool only asks for about the parts it still
// needs, not every part its peers advertise.
func TestRequestsBoundedAfterMempoolRecovery(t *testing.T) {
	const (
		nodeCount = 10
		txCount   = 250
		height    = int64(1)
		round     = int32(0)
		// the receivers don't have txs [gapStart, gapEnd) in their mempools.
		gapStart = 100
		gapEnd   = 130
	)
	reactors, switches := createTestReactors(nodeCount, defaultTestP2PConf(), false, "")
	stopSwitchesOnCleanup(t, switches)
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// small PFF-sized txs, so several txs fit in each part.
	txs := make([]types.Tx, txCount)
	for i := range txs {
		txs[i] = randomBytes(4900 + i*8)
	}
	commit := state.MakeTestCommit(t, sm, pv, time.Now())
	block, partSet, err := sm.MakeBlock(height, types.Data{Txs: txs}, commit, nil, cmtrand.Bytes(20))
	require.NoError(t, err)

	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{Start: pos.Start, End: pos.End, Hash: block.Txs[i].Hash()}
	}

	proposal := types.NewProposal(height, round, -1, types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()})
	protoProposal := proposal.ToProto()
	require.NoError(t, mockPrivVal.SignProposal(TestChainID, protoProposal))
	proposal.Signature = protoProposal.Signature

	// the parts a receiver can't rebuild: the first and last part, plus every
	// part that holds a tx from the gap.
	lastPart := partSet.Total() - 1
	cantRebuild := map[uint32]bool{0: true, lastPart: true}

	proposer, receivers := reactors[0], reactors[1:]
	for txIndex, tx := range block.Txs {
		if txIndex >= gapStart && txIndex < gapEnd {
			firstPart := metaData[txIndex].Start / types.BlockPartSizeBytes
			endPart := (metaData[txIndex].End - 1) / types.BlockPartSizeBytes
			for part := firstPart; part <= endPart; part++ {
				cantRebuild[part] = true
			}
			continue
		}
		for _, receiver := range receivers {
			receiver.mempool.(*mockMempool).AddTx(tx)
		}
	}

	require.NoError(t, proposer.ProposeBlock(proposal, partSet, metaData))

	// nodes stop sending wants once the block is complete, so the request
	// counts are final after this.
	require.Eventually(t, func() bool {
		for _, reactor := range reactors {
			_, parts, _, has := reactor.getAllState(height, round, false)
			if !has || !parts.IsComplete() {
				return false
			}
		}
		return true
	}, 10*time.Second, 5*time.Millisecond)

	// a receiver needs at most the parts it can't rebuild, and the budget
	// lets it request about 2x that. Without the budget each one requests ~23
	// parts. We check the total, since a slow node can be missing a bit more
	// when it sends its first want.
	missing := len(cantRebuild)
	_, maxPerReceiver := requestLimits(missing, int(partSet.Total())*2)
	require.Less(t, maxPerReceiver, int(partSet.Total()))

	totalRequested := 0
	for _, receiver := range receivers {
		totalRequested += countRequestedParts(receiver, height, round)
	}
	require.LessOrEqual(t, totalRequested, maxPerReceiver*len(receivers))
}

// TestSendWantsWithinBudget checks that peers asking at the same time share
// one budget, that no part is requested twice, and that no peer gets more
// than its share.
func TestSendWantsWithinBudget(t *testing.T) {
	const (
		height    = int64(10)
		round     = int32(0)
		heldCount = 10
	)
	receiver, parts, partSet := receiverWithCompactBlock(t, 6, height, round)
	holdFirstParts(t, parts, partSet, heldCount)

	missing := int(partSet.Total()) - heldCount
	maxPerPeer, budget := requestLimits(missing, int(parts.Total()))
	totalParts := int(parts.Total())
	peers := receiver.getPeers()
	// there are more peers and more parts than the budget can use.
	require.Greater(t, len(peers)*maxPerPeer, budget)
	require.Less(t, budget, totalParts-heldCount)

	// every peer is asked for every part, all at once.
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(peer *PeerState) {
			defer wg.Done()
			wants := wantAllParts(height, round, totalParts, missing)
			require.NoError(t, receiver.sendWantsWithinBudget(peer, wants, parts))
		}(peer)
	}
	wg.Wait()

	distinctParts := len(receiver.requestedParts(height, round, totalParts).GetTrueIndices())
	require.Equal(t, budget, distinctParts)
	require.Equal(t, budget, countRequestedParts(receiver, height, round))
	for _, peer := range peers {
		require.LessOrEqual(t, countPeerRequests(peer, height, round), maxPerPeer)
	}
}

// TestSendWantsSkipsHeldParts checks that parts we got after the wants were
// picked aren't requested and don't use up the budget.
func TestSendWantsSkipsHeldParts(t *testing.T) {
	const (
		height    = int64(10)
		round     = int32(0)
		heldCount = 10
	)
	receiver, parts, partSet := receiverWithCompactBlock(t, 2, height, round)

	// the wants are picked while the receiver has no parts.
	totalParts := int(parts.Total())
	wants := wantAllParts(height, round, totalParts, int(partSet.Total()))

	// then the receiver gets the first parts some other way.
	holdFirstParts(t, parts, partSet, heldCount)

	peer := receiver.getPeers()[0]
	require.NoError(t, receiver.sendWantsWithinBudget(peer, wants, parts))

	requested := receiver.requestedParts(height, round, totalParts)
	for i := 0; i < heldCount; i++ {
		require.False(t, requested.GetIndex(i), "requested part %d that we already have", i)
	}

	// the peer's whole share goes to parts we still need.
	missing := int(partSet.Total()) - heldCount
	maxPerPeer, _ := requestLimits(missing, totalParts)
	require.Equal(t, maxPerPeer, len(requested.GetTrueIndices()))
	require.Equal(t, int32(missing), wants.MissingPartsCount)
}

// TestRequestLimits checks the numbers of the request budget.
func TestRequestLimits(t *testing.T) {
	type testCase struct {
		name               string
		missing            int
		totalParts         int
		expectedMaxPerPeer int
		expectedBudget     int
	}
	testCases := []testCase{
		{name: "most of the block rebuilt from the mempool", missing: 3, totalParts: 46, expectedMaxPerPeer: 2, expectedBudget: 7},
		{name: "some parts held", missing: 21, totalParts: 62, expectedMaxPerPeer: 11, expectedBudget: 43},
		{name: "no parts held, even count", missing: 30, totalParts: 60, expectedMaxPerPeer: 15, expectedBudget: 60},
		// 31 / 2 rounded up is 16, but two silent peers would then hold 32
		// parts and leave only 30 for the others.
		{name: "no parts held, odd count", missing: 31, totalParts: 62, expectedMaxPerPeer: 15, expectedBudget: 61},
		{name: "one part block", missing: 1, totalParts: 2, expectedMaxPerPeer: 1, expectedBudget: 3},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			maxPerPeer, budget := requestLimits(tc.missing, tc.totalParts)
			require.Equal(t, tc.expectedMaxPerPeer, maxPerPeer)
			require.Equal(t, tc.expectedBudget, budget)
		})
	}
}

// TestSilentPeersLeaveEnoughBudget checks that peers which are asked first
// and never send anything can't use up the budget: the other peers are still
// asked for every part we need.
func TestSilentPeersLeaveEnoughBudget(t *testing.T) {
	const (
		height = int64(10)
		round  = int32(0)
	)
	type testCase struct {
		name      string
		heldCount int
	}
	testCases := []testCase{
		{name: "receiver has some parts", heldCount: 10},
		// here the silent peers can hold as many parts as we can spare.
		{name: "receiver has no parts", heldCount: 0},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			receiver, parts, partSet := receiverWithCompactBlock(t, 6, height, round)
			holdFirstParts(t, parts, partSet, tc.heldCount)

			missing := int(partSet.Total()) - tc.heldCount
			totalParts := int(parts.Total())
			peers := receiver.getPeers()
			silentPeers, honestPeers := peers[:silentPeersTolerated], peers[silentPeersTolerated:]

			// the silent peers are asked first. They never send a part, so
			// their requests stay open.
			for _, peer := range silentPeers {
				wants := wantAllParts(height, round, totalParts, missing)
				require.NoError(t, receiver.sendWantsWithinBudget(peer, wants, parts))
			}
			for _, peer := range honestPeers {
				wants := wantAllParts(height, round, totalParts, missing)
				require.NoError(t, receiver.sendWantsWithinBudget(peer, wants, parts))
			}

			askedFromHonest := 0
			for _, peer := range honestPeers {
				askedFromHonest += countPeerRequests(peer, height, round)
			}
			require.GreaterOrEqual(t, askedFromHonest, missing)
		})
	}
}

// receiverWithCompactBlock starts nodeCount connected nodes and gives the
// first one a compact block, without any of its parts.
func receiverWithCompactBlock(t *testing.T, nodeCount int, height int64, round int32) (*Reactor, *proptypes.CombinedPartSet, *types.PartSet) {
	t.Helper()
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	compactBlock, partSet, _, _ := testCompactBlock(t, sm, pv, height, round)

	reactors, switches := testBlockPropReactors(nodeCount, cfg.DefaultP2PConfig())
	stopSwitchesOnCleanup(t, switches)
	receiver, sender := reactors[0], reactors[1]
	receiver.handleCompactBlock(compactBlock, sender.self, true)
	_, parts, _, has := receiver.getAllState(height, round, false)
	require.True(t, has)
	require.Len(t, receiver.getPeers(), nodeCount-1)
	// each part can only be requested from one peer in a block this big.
	require.Equal(t, 1, ReqLimit(int(parts.Total())))
	return receiver, parts, partSet
}

// holdFirstParts gives the receiver the first count original parts.
func holdFirstParts(t *testing.T, parts *proptypes.CombinedPartSet, partSet *types.PartSet, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		added, err := parts.AddOriginalPart(partSet.GetPart(i))
		require.NoError(t, err)
		require.True(t, added)
	}
}

// wantAllParts builds a want for every part of the block.
func wantAllParts(height int64, round int32, totalParts, missing int) *proptypes.WantParts {
	allParts := bits.NewBitArray(totalParts)
	allParts.Fill()
	return &proptypes.WantParts{
		Height:            height,
		Round:             round,
		Parts:             allParts,
		MissingPartsCount: int32(missing),
	}
}

// countPeerRequests counts the parts a node requested from one peer.
func countPeerRequests(peer *PeerState, height int64, round int32) int {
	reqs, has := peer.GetRequests(height, round)
	if !has {
		return 0
	}
	return len(reqs.GetTrueIndices())
}

// countRequestedParts adds up the parts a node requested from each peer.
func countRequestedParts(reactor *Reactor, height int64, round int32) int {
	requested := 0
	for _, peer := range reactor.getPeers() {
		reqs, has := peer.GetRequests(height, round)
		if has {
			requested += len(reqs.GetTrueIndices())
		}
	}
	return requested
}
