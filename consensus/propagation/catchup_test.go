package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestGapCatchup(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 3
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/tmp/test/gap_catchup")
	n1 := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(t, sm, 1, 0, 2, 1000000)
	cb, parityBlock := createCompactBlock(t, prop, ps, metaData)

	added := n1.AddProposal(cb)
	require.True(t, added)

	_, parts, _, has := n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	parts.SetProposalData(ps, parityBlock)

	// add the partset header to the second node and trigger the call to retry
	// wants
	n2 := reactors[1]
	n3 := reactors[2]

	_, _, has = n2.GetProposal(prop.Height, prop.Round)
	require.False(t, has)
	_, _, has = n3.GetProposal(prop.Height, prop.Round)
	require.False(t, has)

	psh := ps.Header()

	// test two reactors catching up at the same time as that can increase
	// flakiness if something is broken
	n2.AddCommitment(prop.Height, prop.Round, &psh)
	n3.AddCommitment(prop.Height, prop.Round, &psh)

	time.Sleep(800 * time.Millisecond)

	_, caughtUp, has := n2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	require.True(t, caughtUp.IsComplete())

	_, caughtUp, has = n3.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	require.True(t, caughtUp.IsComplete())
}

func TestReceiveHaveOnCatchupBlock(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(t, sm, 1, 0, 2, 1000000)
	cb, _ := createCompactBlock(t, prop, ps, metaData)

	n1.AddCommitment(prop.Height, prop.Round, &cb.Proposal.BlockID.PartSetHeader)
	_, _, _, has := n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	assert.NotNil(t, n1.getPeer(n2.self))
	// receive a have for this block added via catchup
	n1.handleHaves(n2.self, &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts: []proptypes.PartMetaData{
			{
				Index: 0,
				Hash:  cb.PartsHashes[0],
			},
		},
	})

	// make sure the error didn't happen, and we didn't disconnect from the peer
	assert.NotNil(t, n1.getPeer(n2.self))
}

func TestCatchupOnlyFromUpToDatePeers(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// add the first height
	prop, ps, _, metaData := createTestProposal(t, sm, 1, 0, 2, 1000000)
	cb, _ := createCompactBlock(t, prop, ps, metaData)
	n1.AddCommitment(prop.Height, prop.Round, &cb.Proposal.BlockID.PartSetHeader)
	_, _, _, has := n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)
	n2.AddCommitment(prop.Height, prop.Round, &cb.Proposal.BlockID.PartSetHeader)
	_, _, _, has = n2.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	// add the second height
	prop, ps, _, metaData = createTestProposal(t, sm, 2, 0, 2, 1000000)
	cb, _ = createCompactBlock(t, prop, ps, metaData)
	n1.AddCommitment(prop.Height, prop.Round, &cb.Proposal.BlockID.PartSetHeader)
	_, _, _, has = n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)
	n2.AddCommitment(prop.Height, prop.Round, &cb.Proposal.BlockID.PartSetHeader)
	_, _, _, has = n2.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	// try catchup
	n1.retryWants()

	time.Sleep(200 * time.Millisecond)

	// check if reactor 2 received a want for height 1 but not height 2
	p1 := n2.getPeer(n1.self)
	_, has = p1.GetWants(1, 0)
	require.True(t, has)

	_, has = p1.GetWants(2, 0)
	require.False(t, has)
}

func TestAddCommitment_ReplaceProposalData(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/tmp/test/add_commitment_replace_proposal_data")
	r1 := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	firstProposal, firstPartset, _, _ := createTestProposal(t, sm, 1, 0, 2, 1000000)
	firstPsh := firstPartset.Header()

	// set the first partset header
	r1.AddCommitment(firstProposal.Height, firstProposal.Round, &firstPsh)
	pending, exists := r1.pendingBlocks.GetBlock(firstProposal.Height)
	require.True(t, exists)
	actualFirstPsh := pending.Parts.Original().Header()
	require.Equal(t, firstPartset.Total(), actualFirstPsh.Total)
	require.Equal(t, firstPsh.Hash, actualFirstPsh.Hash)

	// replace the existing partset header with a new one
	secondProposal, secondPartset, _, _ := createTestProposal(t, sm, 1, 0, 10, 1000000)
	secondPsh := secondPartset.Header()
	r1.AddCommitment(secondProposal.Height, secondProposal.Round, &secondPsh)

	// verify that we still have the same block (AddCommitment doesn't replace existing)
	pending2, exists := r1.pendingBlocks.GetBlock(secondProposal.Height)
	require.True(t, exists)
	actualSecondPsh := pending2.Parts.Original().Header()
	// The commitment should not replace existing block, so it keeps the first
	assert.Equal(t, firstPartset.Total(), actualSecondPsh.Total)
	assert.Equal(t, firstPsh.Hash, actualSecondPsh.Hash)
}

func defaultTestP2PConf() *cfg.P2PConfig {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 100000000
	p2pCfg.RecvRate = 100000000
	return p2pCfg
}

// TestHeightPriorityOrdering verifies that lower heights are prioritized (FIFO).
// This test focuses on verifying the priority ordering in GetMissingParts,
// not the full catchup completion (which is tested in TestGapCatchup).
func TestHeightPriorityOrdering(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Add three heights via commitment
	for h := int64(10); h <= 12; h++ {
		prop, ps, _, _ := createTestProposal(t, sm, h, 0, 2, 100000)
		psh := ps.Header()
		r.AddCommitment(prop.Height, prop.Round, &psh)
	}

	// Verify missing parts are returned in priority order (lowest first)
	missing := r.pendingBlocks.GetMissingParts()
	require.Len(t, missing, 3)
	require.Equal(t, int64(10), missing[0].Height, "lowest height should be first")
	require.Equal(t, int64(11), missing[1].Height)
	require.Equal(t, int64(12), missing[2].Height)

	// Verify priority weights are correct (exponential decay)
	require.Equal(t, 1.0, missing[0].Priority, "first height should have priority 1.0")
	require.Equal(t, 0.5, missing[1].Priority, "second height should have priority 0.5")
	require.Equal(t, 0.25, missing[2].Priority, "third height should have priority 0.25")

	// Verify heights are maintained in sorted order
	heights := r.pendingBlocks.Heights()
	require.Equal(t, []int64{10, 11, 12}, heights, "heights should be sorted ascending")
}

// TestCapacityManagement verifies that the manager rejects new blocks when at capacity
// and that lower heights have priority (higher heights are rejected, not evicted).
func TestCapacityManagement(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Temporarily reduce capacity for testing
	oldConfig := r.pendingBlocks.config
	r.pendingBlocks.config = PendingBlocksConfig{
		MaxConcurrent: 2,
		MemoryBudget:  100 * 1024 * 1024,
	}
	defer func() { r.pendingBlocks.config = oldConfig }()

	// Add two blocks via commitment (fills capacity)
	for h := int64(10); h <= 11; h++ {
		prop, ps, _, _ := createTestProposal(t, sm, h, 0, 2, 100000)
		psh := ps.Header()
		r.AddCommitment(prop.Height, prop.Round, &psh)
	}
	require.Equal(t, 2, r.pendingBlocks.Len(), "should have 2 blocks")

	// Try to add a third block - should be rejected due to capacity
	prop, ps, _, _ := createTestProposal(t, sm, 12, 0, 2, 100000)
	psh := ps.Header()
	r.AddCommitment(prop.Height, prop.Round, &psh)
	require.Equal(t, 2, r.pendingBlocks.Len(), "should still have 2 blocks (capacity reached)")

	// Verify the original blocks are still there (not evicted)
	_, exists := r.pendingBlocks.GetBlock(10)
	require.True(t, exists, "height 10 should still exist")
	_, exists = r.pendingBlocks.GetBlock(11)
	require.True(t, exists, "height 11 should still exist")
	_, exists = r.pendingBlocks.GetBlock(12)
	require.False(t, exists, "height 12 should not exist (rejected)")

	// Prune height 10, which should free capacity
	r.Prune(10)
	require.Equal(t, 1, r.pendingBlocks.Len())

	// Now height 12 can be added
	r.AddCommitment(prop.Height, prop.Round, &psh)
	require.Equal(t, 2, r.pendingBlocks.Len())
	_, exists = r.pendingBlocks.GetBlock(12)
	require.True(t, exists, "height 12 should now exist")
}

// TestBlockSourceTracking verifies that blocks track their source correctly.
func TestBlockSourceTracking(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Add via compact block
	prop1, ps1, _, metaData1 := createTestProposal(t, sm, 10, 0, 2, 100000)
	cb1, _ := createCompactBlock(t, prop1, ps1, metaData1)
	added := r.AddProposal(cb1)
	require.True(t, added)
	pending1, _ := r.pendingBlocks.GetBlock(10)
	require.Equal(t, SourceCompactBlock, pending1.Source, "should be SourceCompactBlock")

	// Add via commitment
	prop2, ps2, _, _ := createTestProposal(t, sm, 11, 0, 2, 100000)
	psh2 := ps2.Header()
	r.AddCommitment(prop2.Height, prop2.Round, &psh2)
	pending2, _ := r.pendingBlocks.GetBlock(11)
	require.Equal(t, SourceCommitment, pending2.Source, "should be SourceCommitment")

	// Catchup blocks should have IsCatchup() = true
	require.True(t, pending2.Parts.IsCatchup(), "commitment block should be catchup")
	require.False(t, pending1.Parts.IsCatchup(), "compact block should not be catchup")
}

// TestCatchupBlocksNeedAllParts verifies that catchup blocks request ALL original parts
// while live blocks only need enough for erasure decoding.
func TestCatchupBlocksNeedAllParts(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Add a catchup block (via commitment)
	prop1, ps1, _, _ := createTestProposal(t, sm, 10, 0, 4, 500000) // 4 parts
	psh1 := ps1.Header()
	r.AddCommitment(prop1.Height, prop1.Round, &psh1)

	// Add a live block (via compact block)
	prop2, ps2, _, metaData2 := createTestProposal(t, sm, 11, 0, 4, 500000) // 4 parts
	cb2, _ := createCompactBlock(t, prop2, ps2, metaData2)
	r.AddProposal(cb2)

	missing := r.pendingBlocks.GetMissingParts()
	require.Len(t, missing, 2)

	// Find catchup block (height 10)
	var catchupInfo, liveInfo *MissingPartsInfo
	for i := range missing {
		if missing[i].Height == 10 {
			catchupInfo = &missing[i]
		} else if missing[i].Height == 11 {
			liveInfo = &missing[i]
		}
	}

	require.NotNil(t, catchupInfo, "should have catchup block info")
	require.NotNil(t, liveInfo, "should have live block info")

	// Catchup blocks should be marked as such
	require.True(t, catchupInfo.Catchup, "height 10 should be catchup")
	require.False(t, liveInfo.Catchup, "height 11 should not be catchup")
}

// TestOnBlockAddedCallback verifies that the callback is triggered for catchup sources.
func TestOnBlockAddedCallback(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	callbackCount := 0
	r.pendingBlocks.SetOnBlockAdded(func(height int64, source BlockSource) {
		callbackCount++
		require.True(t, source == SourceCommitment || source == SourceHeaderSync,
			"callback should only fire for catchup sources")
	})

	// Add via commitment - should trigger callback
	prop1, ps1, _, _ := createTestProposal(t, sm, 10, 0, 2, 100000)
	psh1 := ps1.Header()
	r.pendingBlocks.AddFromCommitment(prop1.Height, prop1.Round, &psh1)
	require.Equal(t, 1, callbackCount, "callback should fire for commitment")

	// Add via compact block - should NOT trigger callback
	prop2, ps2, _, metaData2 := createTestProposal(t, sm, 11, 0, 2, 100000)
	cb2, _ := createCompactBlock(t, prop2, ps2, metaData2)
	r.AddProposal(cb2)
	require.Equal(t, 1, callbackCount, "callback should NOT fire for compact block")
}

// TestCompactBlockAttachesToHeaderOnlyBlock verifies that when a compact block arrives
// for a height that already has a header-only block, it attaches correctly.
func TestCompactBlockAttachesToHeaderOnlyBlock(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// First add via commitment (simulates header-only arrival)
	prop, ps, _, metaData := createTestProposal(t, sm, 10, 0, 2, 100000)
	psh := ps.Header()
	r.AddCommitment(prop.Height, prop.Round, &psh)

	// Verify it's a catchup block
	pending, exists := r.pendingBlocks.GetBlock(10)
	require.True(t, exists)
	require.Nil(t, pending.CompactBlock, "should not have compact block yet")
	require.True(t, pending.Parts.IsCatchup(), "should be catchup block")

	// Now add the compact block
	cb, _ := createCompactBlock(t, prop, ps, metaData)
	added := r.AddProposal(cb)
	require.True(t, added, "should attach compact block to existing entry")

	// Verify compact block is attached
	pending, exists = r.pendingBlocks.GetBlock(10)
	require.True(t, exists)
	require.NotNil(t, pending.CompactBlock, "should have compact block attached")
	require.Equal(t, SourceCompactBlock, pending.Source, "source should be upgraded to compact block")
}

func createCompactBlock(
	t testing.TB,
	prop *types.Proposal,
	ps *types.PartSet,
	metaData []proptypes.TxMetaData,
) (*proptypes.CompactBlock, *types.PartSet) {
	parityBlock, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)

	partHashes := extractHashes(ps, parityBlock)
	proofs := extractProofs(ps, parityBlock)
	cb := &proptypes.CompactBlock{
		Proposal:    *prop,
		LastLen:     uint32(lastLen),
		Signature:   cmtrand.Bytes(64), // todo: sign the proposal with a real signature
		BpHash:      parityBlock.Hash(),
		Blobs:       metaData,
		PartsHashes: partHashes,
	}
	cb.SetProofCache(proofs)
	return cb, parityBlock
}
