package propagation

import (
	"testing"
	"time"

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

	pshA := cbA.Proposal.BlockID.PartSetHeader
	n1.AddCommitment(1, 0, &pshA)

	n1.handleCompactBlock(cbB, n2.self, false)

	// B must not be forwarded to consensus.
	select {
	case prop := <-n1.GetProposalChan():
		t.Fatalf("conflicting proposal forwarded to consensus: %+v", prop)
	default:
	}

	// the stored part state is still bound to A's identity.
	_, parts, _, has := n1.getAllState(1, 0, true)
	require.True(t, has)
	require.True(t, parts.Original().Header().Equals(pshA))

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
	// consensus.
	n1.handleCompactBlock(cbA, n2.self, false)
	select {
	case prop := <-n1.GetProposalChan():
		require.True(t, prop.Proposal.BlockID.Equals(cbA.Proposal.BlockID))
	case <-time.After(time.Second):
		t.Fatal("matching proposal was not forwarded to consensus")
	}
}

// TestConflictsWith covers the identity comparison against stored entries.
func TestConflictsWith(t *testing.T) {
	pc := NewProposalCache(makeTestBlockStore(t))

	cbA := makeCompactBlock(10, 0, 3)
	cbA.Proposal.BlockID.Hash = cmtrand.Bytes(32)
	cbA.Proposal.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)
	require.True(t, pc.AddProposal(cbA))

	// the same identity is not a conflict.
	require.False(t, pc.conflictsWith(cbA))

	// a different identity at the same height and round is a conflict.
	cbB := makeCompactBlock(10, 0, 3)
	cbB.Proposal.BlockID.Hash = cmtrand.Bytes(32)
	cbB.Proposal.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)
	require.True(t, pc.conflictsWith(cbB))
	require.False(t, pc.AddProposal(cbB))

	// an empty slot is not a conflict.
	require.False(t, pc.conflictsWith(makeCompactBlock(11, 0, 3)))
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
	pshA := cbA.Proposal.BlockID.PartSetHeader
	n1.AddCommitment(3, 0, &pshA)

	_, parts, _, has = n1.getAllState(3, 0, true)
	require.True(t, has)
	require.True(t, parts.Original().Header().Equals(pshA))

	_, has = peer.GetHaves(3, 0)
	require.False(t, has, "peer have state for the replaced identity must be purged")
	_, has = peer.GetRequests(3, 0)
	require.False(t, has, "peer request state for the replaced identity must be purged")
	require.Zero(t, peer.GetRemainingRequests(3, 0))
}
