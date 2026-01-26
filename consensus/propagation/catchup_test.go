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
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(t, sm, pv, 1, 0, 2, 1000000)
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
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(t, sm, pv, 1, 0, 2, 1000000)
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

// TestCacheCapacityEviction tests that the cache evicts highest heights when full.
func TestCacheCapacityEviction(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// n2 at height 1
	n2.SetHeightAndRound(1, 0)

	// Fill cache with proposals for heights 2 through MaxUnverifiedProposals+1
	for h := int64(2); h <= int64(MaxUnverifiedProposals+1); h++ {
		n1.SetHeightAndRound(h, 0)
		prop, ps, _, metaData := createTestProposal(t, sm, pv, h, 0, 2, 100000)
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
	propHigh, psHigh, _, metaDataHigh := createTestProposal(t, sm, pv, int64(MaxUnverifiedProposals+2), 0, 2, 100000)
	cbHigh, _ := createCompactBlock(t, propHigh, psHigh, metaDataHigh)
	n2.handleCompactBlock(cbHigh, n1.self, false)

	// Height MaxUnverifiedProposals+2 should NOT be cached (rejected)
	cached := n2.GetUnverifiedProposal(int64(MaxUnverifiedProposals + 2))
	require.Nil(t, cached, "highest height should be rejected when cache is full")

	// Height 2 should still be cached
	cached = n2.GetUnverifiedProposal(2)
	require.NotNil(t, cached, "lowest heights should still be cached")
}

// TestHandleCompactBlock_CachesCurrentHeightWrongRound tests that proposals for the
// current height but a different round are cached (not discarded).
func TestHandleCompactBlock_CachesCurrentHeightWrongRound(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// n2 at height 2, round 0
	n2.SetHeightAndRound(2, 0)

	// Create proposal for height 2, round 1 (different round)
	prop, ps, _, metaData := createTestProposal(t, sm, pv, 2, 1, 2, 1000000)
	cb, _ := createCompactBlock(t, prop, ps, metaData)

	// n1 sends the proposal for round 1 to n2 (which is at round 0)
	n2.handleCompactBlock(cb, n1.self, false)

	// Proposal should be cached (not in proposal cache, since it failed validation)
	_, _, has := n2.GetProposal(2, 1)
	require.False(t, has, "proposal should not be in proposal cache (failed validation)")

	// But it should be in the unverified cache
	cached := n2.GetUnverifiedProposal(2)
	require.NotNil(t, cached, "proposal should be cached for later retry")
	require.Equal(t, int32(1), cached.Proposal.Round, "cached proposal should be for round 1")
}

// TestApplyCachedProposalIfAvailable tests the positive path of caching a future-height
// proposal and automatically applying it when we catch up to that height/round.
func TestApplyCachedProposalIfAvailable(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 3
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]
	n3 := reactors[2] // This node will "halt" and catch up

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// All nodes start at height 1
	for _, r := range reactors {
		r.SetHeightAndRound(1, 0)
		r.SetProposer(mockPubKey)
	}

	// Setup height 1 for all nodes - use testCompactBlock for proper signing
	cb1, ps1, parityBlock1, _ := testCompactBlock(t, sm, pv, 1, 0)
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

	// Create proposal for height 2, round 0 - use testCompactBlock for proper signing
	cb2, ps2, parityBlock2, _ := testCompactBlock(t, sm, pv, 2, 0)

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

	// Proposal should NOT be in proposal cache yet
	_, _, has := n3.GetProposal(2, 0)
	require.False(t, has, "proposal should not be in proposal cache yet")

	// Now n3 catches up to height 2 - SetHeightAndRound triggers applyCachedProposalIfAvailable
	n3.Prune(1)
	n3.SetHeightAndRound(2, 0)

	// Verify proposal is now in the proposal cache (automatically applied)
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

// TestApplyCachedProposalIfAvailable_MultiPeer tests that applyCachedProposalIfAvailable
// iterates through all peers and applies a valid proposal even if earlier peers have invalid ones.
// It verifies:
// 1. First peer has an invalid proposal (bad signature)
// 2. Second peer has a valid proposal
// 3. The function skips the invalid one and applies the valid one
// 4. Invalid peer's cache entry remains, valid peer's entry is deleted
func TestApplyCachedProposalIfAvailable_MultiPeer(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 3
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0] // Will have invalid proposal
	n2 := reactors[1] // Will have valid proposal
	n3 := reactors[2] // The node applying cached proposals

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// All nodes start at height 1
	for _, r := range reactors {
		r.SetHeightAndRound(1, 0)
		r.SetProposer(mockPubKey)
	}

	// n1, n2 advance to height 2
	n1.SetHeightAndRound(2, 0)
	n2.SetHeightAndRound(2, 0)

	// Create an INVALID proposal (random signature) for peer n1
	prop2Invalid, ps2Invalid, _, metaData2Invalid := createTestProposal(t, sm, pv, 2, 0, 2, 1000000)
	cbInvalid, _ := createCompactBlock(t, prop2Invalid, ps2Invalid, metaData2Invalid)

	// Create a VALID proposal (properly signed) for peer n2
	cbValid, _, _, _ := testCompactBlock(t, sm, pv, 2, 0)

	// n3 receives invalid proposal from n1 first, then valid from n2
	// Note: peer iteration order in getPeers() is not guaranteed, but we cache
	// from both peers and verify the end state
	n3.handleCompactBlock(cbInvalid, n1.self, false)
	n3.handleCompactBlock(cbValid, n2.self, false)

	// Verify both peers cached the proposal
	peer1 := n3.getPeer(n1.self)
	peer2 := n3.getPeer(n2.self)
	require.NotNil(t, peer1.GetUnverifiedProposal(2), "peer1 should have cached proposal")
	require.NotNil(t, peer2.GetUnverifiedProposal(2), "peer2 should have cached proposal")

	// n3 catches up to height 2 - this triggers applyCachedProposalIfAvailable
	n3.Prune(1)
	n3.SetHeightAndRound(2, 0)

	// Proposal should now be in the proposal cache (applied from valid peer)
	_, propData, has := n3.GetProposal(2, 0)
	require.True(t, has, "proposal should be in proposal cache")
	require.NotNil(t, propData)

	// The invalid peer's cache entry should REMAIN (validation failed, entry not deleted)
	peer1Cache := peer1.GetUnverifiedProposal(2)
	require.NotNil(t, peer1Cache, "invalid peer's cache entry should remain after failed validation")

	// The valid peer's cache entry should be DELETED (successful apply)
	peer2Cache := peer2.GetUnverifiedProposal(2)
	require.Nil(t, peer2Cache, "valid peer's cache entry should be deleted after successful apply")
}

// TestApplyCachedProposalIfAvailable_KeepOnFailure ensures we don't delete a cached proposal
// if handleCachedCompactBlock fails to apply it.
func TestApplyCachedProposalIfAvailable_KeepOnFailure(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0] // Will have a proposal that fails in handleCachedCompactBlock
	n2 := reactors[1] // The node applying cached proposals

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// n2 at height 1, round 0, with proposer set
	n2.SetHeightAndRound(1, 0)
	n2.SetProposer(mockPubKey)

	// Create a compact block with valid signatures but invalid parts hashes.
	cbBad, _, _, _ := testCompactBlock(t, sm, pv, 2, 0)
	badHash := make([]byte, len(cbBad.PartsHashes[0]))
	copy(badHash, cbBad.PartsHashes[0])
	badHash[0] ^= 0xFF
	cbBad.PartsHashes[0] = badHash
	cbBad.SetProofCache(nil)
	signBytes, err := cbBad.SignBytes()
	require.NoError(t, err)
	sig, err := mockPrivVal.SignRawBytes(TestChainID, CompactBlockUID, signBytes)
	require.NoError(t, err)
	cbBad.Signature = sig

	// n2 receives proposal for height 2 while still at height 1 - should cache it.
	n2.handleCompactBlock(cbBad, n1.self, false)

	peer1 := n2.getPeer(n1.self)
	require.NotNil(t, peer1.GetUnverifiedProposal(2), "peer1 should have cached proposal")

	// Now n2 catches up to height 2 - applyCachedProposalIfAvailable runs and should fail to apply cbBad.
	n2.Prune(1)
	n2.SetHeightAndRound(2, 0)

	_, _, has := n2.GetProposal(2, 0)
	require.False(t, has, "proposal should not be applied when proofs are invalid")

	// Cached proposal should remain since handleCachedCompactBlock failed.
	require.NotNil(t, peer1.GetUnverifiedProposal(2), "cached proposal should remain after failed apply")
}

// TestApplyCachedProposalIfAvailable_WrongRound tests that cached proposals for
// a different round are kept cached until we advance to that round.
func TestApplyCachedProposalIfAvailable_WrongRound(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// n2 at height 1, round 0
	n2.SetHeightAndRound(1, 0)

	// Create proposal for height 2, round 1
	prop, ps, _, metaData := createTestProposal(t, sm, pv, 2, 1, 2, 1000000)
	cb, _ := createCompactBlock(t, prop, ps, metaData)

	// n1 sends proposal for height 2 round 1 - n2 caches it (future height)
	n2.handleCompactBlock(cb, n1.self, false)
	require.NotNil(t, n2.GetUnverifiedProposal(2), "proposal should be cached")

	// n2 advances to height 2, round 0 - proposal should stay cached (wrong round)
	n2.Prune(1)
	n2.SetHeightAndRound(2, 0)

	// Proposal should still be cached (we're at round 0, proposal is for round 1)
	cached := n2.GetUnverifiedProposal(2)
	require.NotNil(t, cached, "proposal should stay cached when at different round")

	// Proposal should NOT be in proposal cache (applyCachedProposalIfAvailable skipped it)
	_, _, has := n2.GetProposal(2, 1)
	require.False(t, has, "proposal should not be applied when at different round")
}

func TestAddCommitment_ReplaceProposalData(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/tmp/test/add_commitment_replace_proposal_data")
	r1 := reactors[0]
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	firstProposal, firstPartset, _, _ := createTestProposal(t, sm, pv, 1, 0, 2, 1000000)
	firstPsh := firstPartset.Header()

	// set the first partset header
	r1.AddCommitment(firstProposal.Height, firstProposal.Round, &firstPsh)
	actualFirstPsh := r1.proposals[firstProposal.Height][firstProposal.Round].block.Original().Header()
	require.Equal(t, firstPartset.Total(), actualFirstPsh.Total)
	require.Equal(t, firstPsh.Hash, actualFirstPsh.Hash)

	// replace the existing partset header with a new one
	secondProposal, secondPartset, _, _ := createTestProposal(t, sm, pv, 1, 0, 10, 1000000)
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
