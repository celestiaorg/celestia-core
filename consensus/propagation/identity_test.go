package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/state"
)

// TestPropagationStateDoesNotMixProposalIdentities delivers A's commitment
// before B's proposal and parts at the same height and round and asserts that
// no stored state or message forwarded to consensus combines fields from A
// and B.
func TestPropagationStateDoesNotMixProposalIdentities(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, defaultTestP2PConf())
	n1, n2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// identity A: the committed identity, delivered before B's proposal.
	cbA, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	// identity B: a validly signed compact block for the same height and
	// round with a different block hash and part-set header.
	cbB, psB, _, proofsB := testCompactBlock(t, sm, pv, 1, 0)
	require.False(t, cbA.Proposal.BlockID.Equals(cbB.Proposal.BlockID))

	n1.AddCommitment(1, 0, cbA.Proposal.BlockID)

	n1.pmtx.Lock()
	entry := n1.proposals[1][0]
	n1.pmtx.Unlock()
	require.NotNil(t, entry)
	require.True(t, entry.commitmentBacked)

	n1.handleCompactBlock(cbB, n2.self, false)

	// B must not be forwarded to consensus.
	select {
	case prop := <-n1.GetProposalChan():
		t.Fatalf("conflicting proposal forwarded to consensus: %+v", prop)
	default:
	}

	// the stored state is still bound to A's identity.
	cb, parts, _, has := n1.getAllState(1, 0, true)
	require.True(t, has)
	require.True(t, cb.Proposal.BlockID.Equals(cbA.Proposal.BlockID))
	require.True(t, parts.Original().Header().Equals(cbA.Proposal.BlockID.PartSetHeader))

	// B's parts must not be added to A's part state or forwarded to consensus.
	partB := psB.GetPart(0)
	n1.handleRecoveryPart(n2.self, &proptypes.RecoveryPart{
		Height: 1,
		Round:  0,
		Index:  0,
		Data:   partB.Bytes,
		Proof:  proofsB[0],
	})
	require.True(t, parts.BitArray().IsEmpty(), "B's parts must not be combined with A's state")
	select {
	case part := <-n1.GetPartChan():
		t.Fatalf("conflicting part forwarded to consensus: %+v", part)
	default:
	}

	// a compact block matching the committed identity is forwarded to
	// consensus without replacing the commitment-backed entry.
	n1.handleCompactBlock(cbA, n2.self, false)
	select {
	case prop := <-n1.GetProposalChan():
		require.True(t, prop.Proposal.BlockID.Equals(cbA.Proposal.BlockID))
	case <-time.After(time.Second):
		t.Fatal("matching proposal was not forwarded to consensus")
	}
	n1.pmtx.Lock()
	assert.Same(t, entry, n1.proposals[1][0])
	n1.pmtx.Unlock()
}

// TestAddProposalIdentityConflict covers the (added, conflict) contract of
// AddProposal.
func TestAddProposalIdentityConflict(t *testing.T) {
	pc := NewProposalCache(makeTestBlockStore(t))

	cbA := makeCompactBlock(10, 0, 3)
	cbA.Proposal.BlockID.Hash = cmtrand.Bytes(32)
	cbA.Proposal.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)

	added, conflict := pc.AddProposal(cbA)
	require.True(t, added)
	require.False(t, conflict)

	// the same identity again is a duplicate, not a conflict.
	added, conflict = pc.AddProposal(cbA)
	require.False(t, added)
	require.False(t, conflict)

	// a different identity at the same height and round is a conflict and is
	// not stored.
	cbB := makeCompactBlock(10, 0, 3)
	cbB.Proposal.BlockID.Hash = cmtrand.Bytes(32)
	cbB.Proposal.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)
	added, conflict = pc.AddProposal(cbB)
	require.False(t, added)
	require.True(t, conflict)

	stored, _, has := pc.GetCurrentCompactBlock()
	require.True(t, has)
	require.True(t, stored.Proposal.BlockID.Equals(cbA.Proposal.BlockID))

	// an irrelevant height is neither added nor a conflict.
	cbC := makeCompactBlock(9, 0, 3)
	added, conflict = pc.AddProposal(cbC)
	require.False(t, added)
	require.False(t, conflict)
}

// TestAddCommitmentReplacementPurgesPeerState asserts that when a commitment
// replaces an entry bound to a different identity, the per-peer part state
// recorded for that height and round is purged with it.
func TestAddCommitmentReplacementPurgesPeerState(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, defaultTestP2PConf())
	n1, n2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// height 3 keeps retryWants from re-requesting through the mock peers,
	// whose consensus peer state reports height 0.
	cbA, _, _, _ := testCompactBlock(t, sm, pv, 3, 0)
	cbB, _, _, _ := testCompactBlock(t, sm, pv, 3, 0)
	require.False(t, cbA.Proposal.BlockID.Equals(cbB.Proposal.BlockID))

	n1.SetHeightAndRound(3, 0)
	n1.handleCompactBlock(cbB, n2.self, false)
	_, parts, _, has := n1.getAllState(3, 0, true)
	require.True(t, has)
	require.True(t, parts.Original().Header().Equals(cbB.Proposal.BlockID.PartSetHeader))

	// record have and request state for B on n2's peer state.
	n1.handleHaves(n2.self, &proptypes.HaveParts{
		Height: 3,
		Round:  0,
		Parts: []proptypes.PartMetaData{
			{Index: 0, Hash: cbB.PartsHashes[0]},
			{Index: 1, Hash: cbB.PartsHashes[1]},
		},
	})
	peer := n1.getPeer(n2.self)
	require.NotNil(t, peer)
	haves, has := peer.GetHaves(3, 0)
	require.True(t, has)
	require.False(t, haves.IsEmpty())
	// let the queued haves drain into sent requests before the replacement.
	time.Sleep(300 * time.Millisecond)

	// a commitment for A replaces B and purges B's per-peer state.
	n1.AddCommitment(3, 0, cbA.Proposal.BlockID)

	n1.pmtx.Lock()
	entry := n1.proposals[3][0]
	n1.pmtx.Unlock()
	require.NotNil(t, entry)
	require.True(t, entry.commitmentBacked)
	require.True(t, entry.blockID().Equals(cbA.Proposal.BlockID))
	require.True(t, entry.block.Original().Header().Equals(cbA.Proposal.BlockID.PartSetHeader))

	_, has = peer.GetHaves(3, 0)
	require.False(t, has, "peer have state for the replaced identity must be purged")
	_, has = peer.GetRequests(3, 0)
	require.False(t, has, "peer request state for the replaced identity must be purged")
	require.Zero(t, peer.GetRemainingRequests(3, 0))
}

// TestHandleCachedCompactBlockRejectsConflict asserts that a cached compact
// block conflicting with the stored identity is rejected before it is
// forwarded to consensus, while a matching one is forwarded.
func TestHandleCachedCompactBlockRejectsConflict(t *testing.T) {
	reactors, _ := testBlockPropReactors(1, defaultTestP2PConf())
	n1 := reactors[0]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	cbA, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	cbB, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	require.False(t, cbA.Proposal.BlockID.Equals(cbB.Proposal.BlockID))

	n1.AddCommitment(1, 0, cbA.Proposal.BlockID)

	applied, conflict := n1.handleCachedCompactBlock(cbB)
	require.False(t, applied)
	require.True(t, conflict)
	select {
	case prop := <-n1.GetProposalChan():
		t.Fatalf("conflicting cached proposal forwarded to consensus: %+v", prop)
	default:
	}

	applied, conflict = n1.handleCachedCompactBlock(cbA)
	require.True(t, applied)
	require.False(t, conflict)
	select {
	case prop := <-n1.GetProposalChan():
		require.True(t, prop.Proposal.BlockID.Equals(cbA.Proposal.BlockID))
	case <-time.After(time.Second):
		t.Fatal("matching cached proposal was not forwarded to consensus")
	}
}
