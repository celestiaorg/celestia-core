package propagation

import (
	"errors"
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

// TestApplyCachedProposal tests the full flow of caching a future-height proposal
// and then applying it after catching up.
func TestApplyCachedProposal(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 3
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]
	n3 := reactors[2] // This node will "halt"

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// All nodes start at height 1
	for _, r := range reactors {
		r.SetHeightAndRound(1, 0)
	}

	// Setup height 1 for all nodes
	prop1, ps1, _, metaData1 := createTestProposal(t, sm, 1, 0, 2, 1000000)
	cb1, parityBlock1 := createCompactBlock(t, prop1, ps1, metaData1)
	for _, r := range reactors {
		added := r.AddProposal(cb1)
		require.True(t, added)
	}
	_, parts1, _, _ := n1.getAllState(1, 0, true)
	parts1.SetProposalData(ps1, parityBlock1)

	// n1 and n2 advance to height 2, n3 stays at height 1 (halted)
	n1.Prune(1)
	n1.SetHeightAndRound(2, 0)
	n2.Prune(1)
	n2.SetHeightAndRound(2, 0)

	// Create proposal for height 2
	prop2, ps2, _, metaData2 := createTestProposal(t, sm, 2, 0, 2, 1000000)
	cb2, parityBlock2 := createCompactBlock(t, prop2, ps2, metaData2)

	// n1 and n2 process height 2
	added := n1.AddProposal(cb2)
	require.True(t, added)
	_, parts2, _, _ := n1.getAllState(2, 0, true)
	parts2.SetProposalData(ps2, parityBlock2)
	added = n2.AddProposal(cb2)
	require.True(t, added)

	// n3 receives height 2 proposal while still at height 1 - should cache it
	n3.handleCompactBlock(cb2, n1.self, false)

	// Verify it's cached
	cachedCb := n3.GetUnverifiedProposal(2)
	require.NotNil(t, cachedCb, "proposal should be cached")

	// Now n3 catches up to height 2
	n3.Prune(1)
	n3.SetHeightAndRound(2, 0)

	// Apply the cached proposal with a mock verifier that accepts
	applied := n3.ApplyCachedProposal(2, func(cb *proptypes.CompactBlock) error {
		// In real usage, this would verify proposal signature and compact block signature
		return nil // Accept for testing
	})
	require.True(t, applied, "cached proposal should be applied")

	// Verify proposal is now in the proposal cache
	_, propData, has := n3.GetProposal(2, 0)
	require.True(t, has, "proposal should now be in proposal cache")
	require.NotNil(t, propData)

	// Wait for catchup to start (retryWants should be triggered)
	time.Sleep(300 * time.Millisecond)

	// The proposal should be marked as catchup
	n3.pmtx.Lock()
	pd := n3.proposals[2][0]
	n3.pmtx.Unlock()
	require.True(t, pd.catchup, "proposal should be marked as catchup")
}

// TestApplyCachedProposal_VerificationFails tests that invalid cached proposals are rejected.
func TestApplyCachedProposal_VerificationFails(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// n1 at height 2, n2 at height 1
	n1.SetHeightAndRound(2, 0)
	n2.SetHeightAndRound(1, 0)

	// Create and send proposal for height 2
	prop2, ps2, _, metaData2 := createTestProposal(t, sm, 2, 0, 2, 1000000)
	cb2, _ := createCompactBlock(t, prop2, ps2, metaData2)

	// n2 receives it - should cache
	n2.handleCompactBlock(cb2, n1.self, false)
	require.NotNil(t, n2.GetUnverifiedProposal(2))

	// n2 catches up
	n2.SetHeightAndRound(2, 0)

	// Try to apply with a verifier that rejects
	applied := n2.ApplyCachedProposal(2, func(cb *proptypes.CompactBlock) error {
		return errors.New("invalid signature")
	})
	require.False(t, applied, "should not apply when verification fails")

	// Proposal should NOT be in proposal cache
	_, _, has := n2.GetProposal(2, 0)
	require.False(t, has, "proposal should not be in cache when verification fails")
}

// TestCacheCapacityEviction tests that the cache evicts highest heights when full.
func TestCacheCapacityEviction(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// n2 at height 1
	n2.SetHeightAndRound(1, 0)

	// Fill cache with proposals for heights 2 through MaxUnverifiedProposals+1
	for h := int64(2); h <= int64(MaxUnverifiedProposals+1); h++ {
		n1.SetHeightAndRound(h, 0)
		prop, ps, _, metaData := createTestProposal(t, sm, h, 0, 2, 100000)
		cb, _ := createCompactBlock(t, prop, ps, metaData)
		n2.handleCompactBlock(cb, n1.self, false)
	}

	// All heights 2 to MaxUnverifiedProposals+1 should be cached
	for h := int64(2); h <= int64(MaxUnverifiedProposals+1); h++ {
		cached := n2.GetUnverifiedProposal(h)
		require.NotNil(t, cached, "height %d should be cached", h)
	}

	// Now add a proposal for height MaxUnverifiedProposals+2 (should be rejected - cache full with lower heights)
	n1.SetHeightAndRound(int64(MaxUnverifiedProposals+2), 0)
	propHigh, psHigh, _, metaDataHigh := createTestProposal(t, sm, int64(MaxUnverifiedProposals+2), 0, 2, 100000)
	cbHigh, _ := createCompactBlock(t, propHigh, psHigh, metaDataHigh)
	n2.handleCompactBlock(cbHigh, n1.self, false)

	// Height MaxUnverifiedProposals+2 should NOT be cached (rejected)
	cached := n2.GetUnverifiedProposal(int64(MaxUnverifiedProposals + 2))
	require.Nil(t, cached, "highest height should be rejected when cache is full")

	// Height 2 should still be cached
	cached = n2.GetUnverifiedProposal(2)
	require.NotNil(t, cached, "lowest heights should still be cached")
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
	actualFirstPsh := r1.proposals[firstProposal.Height][firstProposal.Round].block.Original().Header()
	require.Equal(t, firstPartset.Total(), actualFirstPsh.Total)
	require.Equal(t, firstPsh.Hash, actualFirstPsh.Hash)

	// replace the existing partset header with a new one
	secondProposal, secondPartset, _, _ := createTestProposal(t, sm, 1, 0, 10, 1000000)
	secondPsh := secondPartset.Header()
	r1.AddCommitment(secondProposal.Height, secondProposal.Round, &secondPsh)

	// verify if the partset header got updated
	actualSecondPsh := r1.proposals[secondProposal.Height][secondProposal.Round].block.Original().Header()
	assert.Equal(t, secondPartset.Total(), actualSecondPsh.Total)
	assert.Equal(t, secondPsh.Hash, actualSecondPsh.Hash)
}

func defaultTestP2PConf() *cfg.P2PConfig {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 100000000
	p2pCfg.RecvRate = 100000000
	return p2pCfg
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
