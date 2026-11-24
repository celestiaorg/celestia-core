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
	// lets it request 1.5x that. Without the budget each one requests ~23
	// parts. We check the total, since a slow node can be missing a bit more
	// when it sends its first want.
	missing := len(cantRebuild)
	maxPerReceiver := missing + (missing+1)/2
	require.Less(t, maxPerReceiver, int(partSet.Total())/2)

	totalRequested := 0
	for _, receiver := range receivers {
		totalRequested += countRequestedParts(receiver, height, round)
	}
	require.LessOrEqual(t, totalRequested, maxPerReceiver*len(receivers))
}

// TestSendWantsWithinBudget checks that peers asking at the same time share
// one budget, and that no part is requested twice.
func TestSendWantsWithinBudget(t *testing.T) {
	const (
		height = int64(10)
		round  = int32(0)
	)
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	compactBlock, partSet, _, _ := testCompactBlock(t, sm, pv, height, round)

	reactors, switches := testBlockPropReactors(5, cfg.DefaultP2PConfig())
	stopSwitchesOnCleanup(t, switches)
	receiver, sender := reactors[0], reactors[1]
	receiver.handleCompactBlock(compactBlock, sender.self, true)
	_, parts, _, has := receiver.getAllState(height, round, false)
	require.True(t, has)

	// the receiver has no parts yet, so it's missing all the original ones.
	missing := int(partSet.Total())
	budget := missing + (missing+1)/2
	totalParts := int(parts.Total())
	require.Less(t, budget, totalParts)

	// every peer is asked for every part, all at once.
	var wg sync.WaitGroup
	for _, peer := range receiver.getPeers() {
		wg.Add(1)
		go func(peer *PeerState) {
			defer wg.Done()
			allParts := bits.NewBitArray(totalParts)
			allParts.Fill()
			wants := &proptypes.WantParts{
				Height:            height,
				Round:             round,
				Parts:             allParts,
				MissingPartsCount: int32(missing),
			}
			require.NoError(t, receiver.sendWantsWithinBudget(peer, wants, parts))
		}(peer)
	}
	wg.Wait()

	distinctParts := len(receiver.requestedParts(height, round, totalParts).GetTrueIndices())
	require.Equal(t, budget, distinctParts)
	require.Equal(t, budget, countRequestedParts(receiver, height, round))
}

// TestSendWantsSkipsHeldParts checks that parts we got after the wants were
// picked aren't requested and don't use up the budget.
func TestSendWantsSkipsHeldParts(t *testing.T) {
	const (
		height    = int64(10)
		round     = int32(0)
		heldCount = 10
	)
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	compactBlock, partSet, _, _ := testCompactBlock(t, sm, pv, height, round)

	reactors, switches := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	stopSwitchesOnCleanup(t, switches)
	receiver, sender := reactors[0], reactors[1]
	receiver.handleCompactBlock(compactBlock, sender.self, true)
	_, parts, _, has := receiver.getAllState(height, round, false)
	require.True(t, has)

	// the wants are picked while the receiver has no parts.
	totalParts := int(parts.Total())
	allParts := bits.NewBitArray(totalParts)
	allParts.Fill()
	wants := &proptypes.WantParts{
		Height:            height,
		Round:             round,
		Parts:             allParts,
		MissingPartsCount: int32(partSet.Total()),
	}

	// then the receiver gets the first parts some other way.
	for i := 0; i < heldCount; i++ {
		added, err := parts.AddOriginalPart(partSet.GetPart(i))
		require.NoError(t, err)
		require.True(t, added)
	}

	peer := receiver.getPeer(sender.self)
	require.NotNil(t, peer)
	require.NoError(t, receiver.sendWantsWithinBudget(peer, wants, parts))

	requested := receiver.requestedParts(height, round, totalParts)
	for i := 0; i < heldCount; i++ {
		require.False(t, requested.GetIndex(i), "requested part %d that we already have", i)
	}

	// the whole budget goes to parts we still need.
	missing := int(partSet.Total()) - heldCount
	budget := missing + (missing+1)/2
	require.Equal(t, budget, len(requested.GetTrueIndices()))
	require.Equal(t, int32(missing), wants.MissingPartsCount)
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
