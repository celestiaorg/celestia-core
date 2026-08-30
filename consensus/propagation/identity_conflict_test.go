package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// signedTestCompactBlock is testCompactBlock with a caller-provided signer so
// tests can use a proposer other than the shared mock validator.
func signedTestCompactBlock(t *testing.T, sm state.State, pv types.PrivValidator, signer types.PrivValidator, height int64, round int32) *proptypes.CompactBlock {
	t.Helper()
	prop, ps, _, metaData := createTestProposal(t, sm, pv, height, round, 2, 1000000)

	protoProp := prop.ToProto()
	protoProp.Signature = nil
	require.NoError(t, signer.SignProposal(TestChainID, protoProp))
	prop.Signature = protoProp.Signature

	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)

	cb := &proptypes.CompactBlock{
		BpHash:      pse.Header().Hash,
		LastLen:     uint32(lastLen),
		Blobs:       metaData,
		PartsHashes: extractHashes(ps, pse),
		Proposal:    *prop,
	}
	cb.SetProofCache(extractProofs(ps, pse))
	signBytes, err := cb.SignBytes()
	require.NoError(t, err)
	sig, err := signer.SignRawBytes(TestChainID, CompactBlockUID, signBytes)
	require.NoError(t, err)
	cb.Signature = sig
	return cb
}

// TestSetConsensusStateReplaysCachedProposalOnce asserts that installing the
// height, round, and proposer atomically replays a cached proposal exactly
// once, against the complete state.
func TestSetConsensusStateReplaysCachedProposalOnce(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	n1, n2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// a round-1 proposal from a proposer different from the round-0 proposer.
	roundOneProposer := types.NewMockPV()
	cb := signedTestCompactBlock(t, sm, pv, roundOneProposer, 1, 1)

	// while propagation is at round 0, the proposal fails validation and is
	// cached.
	n1.handleCompactBlock(cb, n2.self, false)
	require.NotNil(t, n1.GetUnverifiedProposal(1), "round-1 proposal should be cached")
	_, _, has := n1.GetProposal(1, 1)
	require.False(t, has)

	logger := &captureLogger{}
	n1.SetLogger(logger)

	// transition to round 1 with the round-1 proposer.
	pub, err := roundOneProposer.GetPubKey()
	require.NoError(t, err)
	n1.SetConsensusState(1, 1, pub)

	// the cached proposal was validated once against the complete state and
	// accepted.
	_, _, has = n1.GetProposal(1, 1)
	require.True(t, has, "cached proposal should be applied")
	_, failed := logger.levelOf("cached proposal failed validation")
	require.False(t, failed, "the cached proposal must not be validated against a partially updated state: %+v", logger.entries)
	applied := 0
	logger.mtx.Lock()
	for _, e := range logger.entries {
		if e.msg == "applying cached proposal from catchup" {
			applied++
		}
	}
	logger.mtx.Unlock()
	require.Equal(t, 1, applied, "cached proposal should be applied exactly once")
}

// TestPropagationStateDoesNotMixProposalIdentities asserts that a commitment
// placeholder and a conflicting compact block at the same height and round
// are never combined.
func TestPropagationStateDoesNotMixProposalIdentities(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	n1, n2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// identity A: a committed part-set header delivered before B's proposal.
	_, psA, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	pshA := psA.Header()
	n1.AddCommitment(1, 0, &pshA)

	// identity B: a valid signed proposal for the same height and round with
	// a different block hash and part-set header.
	cbB, psB, _, proofsB := testCompactBlock(t, sm, pv, 1, 0)
	require.False(t, pshA.Equals(psB.Header()))

	n1.handleCompactBlock(cbB, n2.self, false)

	// B must not be forwarded to consensus.
	select {
	case prop := <-n1.GetProposalChan():
		t.Fatalf("conflicting proposal forwarded to consensus: %+v", prop)
	default:
	}

	// the stored state is still bound to A's part-set header.
	_, parts, _, has := n1.getAllState(1, 0, true)
	require.True(t, has)
	require.True(t, pshA.Equals(parts.Original().Header()), "stored part state must keep A's identity")

	// B's parts must not be added to A's part state.
	partB := psB.GetPart(0)
	n1.handleRecoveryPart(n2.self, &proptypes.RecoveryPart{
		Height: 1,
		Round:  0,
		Index:  0,
		Data:   partB.Bytes,
		Proof:  proofsB[0],
	})
	require.True(t, parts.BitArray().IsEmpty(), "B's parts must not be combined with A's state")
}

// TestHandleCachedCompactBlockRejectsConflictBeforeForwarding asserts that a
// cached proposal conflicting with the stored identity is rejected before it
// is forwarded to consensus.
func TestHandleCachedCompactBlockRejectsConflictBeforeForwarding(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	n1 := reactors[0]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	cbA, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	cbB, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	require.False(t, cbA.Proposal.BlockID.Equals(cbB.Proposal.BlockID))

	added, conflict := n1.AddProposal(cbA)
	require.True(t, added)
	require.False(t, conflict)

	applied, conflict := n1.handleCachedCompactBlock(cbB)
	require.False(t, applied)
	require.True(t, conflict)

	// B must not be sent to the consensus proposal channel.
	select {
	case prop := <-n1.GetProposalChan():
		t.Fatalf("conflicting cached proposal forwarded to consensus: %+v", prop)
	default:
	}

	// the stored proposal is still A.
	storedCb, _, has := n1.GetCurrentCompactBlock()
	require.True(t, has)
	require.True(t, storedCb.Proposal.BlockID.Equals(cbA.Proposal.BlockID))
}

// TestEvictProposalAllowsReplacement asserts that an evicted proposal is
// quarantined, its per-peer request state is cleared, and a proposal with a
// different identity can then be accepted.
func TestEvictProposalAllowsReplacement(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	n1, n2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	cbA, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	added, _ := n1.AddProposal(cbA)
	require.True(t, added)

	// record request state bound to A on the peer.
	peer := n1.getPeer(n2.self)
	require.NotNil(t, peer)
	reqs := bits.NewBitArray(4)
	reqs.SetIndex(0, true)
	peer.AddRequests(1, 0, reqs)

	n1.EvictProposal(1, 0)

	_, _, has := n1.GetProposal(1, 0)
	require.False(t, has, "evicted proposal should be gone")
	_, hasReqs := peer.GetRequests(1, 0)
	require.False(t, hasReqs, "per-peer request state should be cleared")

	// the evicted identity cannot be re-added.
	added, conflict := n1.AddProposal(cbA)
	require.False(t, added)
	require.True(t, conflict)

	// a different identity for the same height and round is accepted.
	cbB, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	added, conflict = n1.AddProposal(cbB)
	require.True(t, added)
	require.False(t, conflict)
}

// TestInvalidHaveConflictRecovers asserts that when haves from several
// distinct peers conflict with the pinned proposal identity, the pinned
// candidate is evicted, request state is cleared, and a replacement proposal
// is accepted without disconnecting the peers.
func TestInvalidHaveConflictRecovers(t *testing.T) {
	reactors, _ := testBlockPropReactors(4, cfg.DefaultP2PConfig())
	n1 := reactors[0]
	peers := reactors[1:]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	cbA, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)
	cbB, psB, parityB, _ := testCompactBlock(t, sm, pv, 1, 0)
	added, _ := n1.AddProposal(cbA)
	require.True(t, added)

	// haves that consistently belong to proposal B arrive from three peers.
	hashesB := extractHashes(psB, parityB)
	for _, p := range peers {
		n1.handleHaves(p.self, &proptypes.HaveParts{
			Height: 1,
			Round:  0,
			Parts:  []proptypes.PartMetaData{{Index: 0, Hash: hashesB[0]}},
		})
	}

	// the pinned candidate is evicted and the peers are not disconnected.
	_, _, has := n1.GetProposal(1, 0)
	require.False(t, has, "conflicting candidate should be evicted")
	for _, p := range peers {
		require.NotNil(t, n1.getPeer(p.self), "peers must not be disconnected")
	}

	// proposal B can now be accepted and forwarded.
	n1.handleCompactBlock(cbB, peers[0].self, false)
	_, _, has = n1.GetProposal(1, 0)
	require.True(t, has, "replacement proposal should be accepted")
	select {
	case prop := <-n1.GetProposalChan():
		require.Equal(t, cbB.Proposal.BlockID, prop.Proposal.BlockID)
	case <-time.After(time.Second):
		t.Fatal("replacement proposal was not forwarded to consensus")
	}
}

// TestWantBeforeCompactBlockIsServed asserts that a prove=true Want arriving
// before the compact block is retained and serviced exactly once when the
// compact block arrives.
func TestWantBeforeCompactBlockIsServed(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	n1, n2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	cb, ps, parity, proofs := testCompactBlock(t, sm, pv, 1, 0)
	total := int(ps.Total() + parity.Total())

	// the Want arrives before any proposal state exists.
	wantParts := bits.NewBitArray(total)
	wantParts.SetIndex(0, true)
	n1.handleWants(n2.self, &proptypes.WantParts{
		Height:            1,
		Round:             0,
		Parts:             wantParts,
		Prove:             true,
		MissingPartsCount: 1,
	})

	peer := n1.getPeer(n2.self)
	require.NotNil(t, peer)
	_, hasWants := peer.GetWants(1, 0)
	require.False(t, hasWants, "want should be retained, not registered yet")

	// the compact block arrives and the retained Want is serviced.
	n1.handleCompactBlock(cb, n2.self, false)

	wants, hasWants := peer.GetWants(1, 0)
	require.True(t, hasWants, "retained want should be serviced when the compact block arrives")
	require.True(t, wants.GetIndex(0))
	require.Nil(t, peer.TakePendingWant(1, 0), "retained want must be serviced exactly once")

	// once the part is available it is sent to the peer.
	part := ps.GetPart(0)
	n1.handleRecoveryPart(n2.self, &proptypes.RecoveryPart{
		Height: 1,
		Round:  0,
		Index:  0,
		Data:   part.Bytes,
		Proof:  proofs[0],
	})
	require.Eventually(t, func() bool {
		return !peer.WantsPart(1, 0, 0)
	}, 2*time.Second, 50*time.Millisecond, "the retained want should be cleared once the part is served")
}

// TestRetryWantsSkipsPeerWithoutTargetProposal asserts that catch-up requests
// to a peer that reports a height above the target but never serves the
// proposal are bounded, its pending requests are released, and an available
// peer keeps being used.
func TestRetryWantsSkipsPeerWithoutTargetProposal(t *testing.T) {
	reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
	n1, n2, n3 := reactors[0], reactors[1], reactors[2]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// an unfinished committed height that needs catch-up. The block is small
	// so the per-part request limit allows asking several peers.
	_, ps, _, _ := createTestProposal(t, sm, pv, 5, 0, 1, 100)
	parity, _, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	proofs := extractProofs(ps, parity)
	psh := ps.Header()
	n1.AddCommitment(5, 0, &psh)

	unavailable := n1.getPeer(n2.self)
	available := n1.getPeer(n3.self)
	require.NotNil(t, unavailable)
	require.NotNil(t, available)

	// both peers report a height above the target.
	unavailableEditor := &MockPeerStateEditor{}
	unavailableEditor.SetHeight(7)
	unavailable.SetConsensusPeerState(unavailableEditor)
	availableEditor := &MockPeerStateEditor{}
	availableEditor.SetHeight(7)
	available.SetConsensusPeerState(availableEditor)

	// the unavailable peer never answers: after MaxCatchupAttempts its
	// pending requests are released and it is no longer asked.
	for i := 0; i < MaxCatchupAttempts+2; i++ {
		n1.retryWants()
	}
	require.Equal(t, MaxCatchupAttempts, unavailable.CatchupAttempts(5),
		"attempts to an unavailable peer must be bounded")
	reqs, has := unavailable.GetRequests(5, 0)
	require.True(t, !has || reqs.IsEmpty(),
		"pending requests to the unavailable peer must be released")

	// the available peer answers with a part: its attempt counter resets and
	// it keeps being used for the remaining parts.
	part := ps.GetPart(0)
	n1.handleRecoveryPart(n3.self, &proptypes.RecoveryPart{
		Height: 5,
		Round:  0,
		Index:  0,
		Data:   part.Bytes,
		Proof:  proofs[0],
	})
	require.Zero(t, available.CatchupAttempts(5))

	n1.retryWants()
	require.Less(t, available.CatchupAttempts(5), MaxCatchupAttempts,
		"an answering peer keeps being selected")
	require.Equal(t, MaxCatchupAttempts, unavailable.CatchupAttempts(5),
		"the unavailable peer is not asked again")
}
