package propagation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestUnrequestedParts(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	cb, ps, _, _ := testCompactBlock(t, sm, pv, 10, 0)
	// the parts the sender may push, and one it may not
	pushable := uncoveredParts(cb.Blobs, ps.Total(), types.BlockPartSizeBytes, cb.LastLen)
	require.NotEmpty(t, pushable)
	const coveredPart = 1
	require.NotContains(t, pushable, uint32(coveredPart))

	partMsg := func(i int) *proptypes.RecoveryPart {
		part := ps.GetPart(i)
		return &proptypes.RecoveryPart{Height: 10, Round: 0, Index: part.Index, Data: part.Bytes, Proof: &part.Proof}
	}

	// receiver gets the compact block from sender, other never sent it.
	setup := func(t *testing.T) (receiver, sender, other *Reactor) {
		reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
		receiver, sender, other = reactors[0], reactors[1], reactors[2]
		receiver.handleCompactBlock(cb, sender.self, true)
		return receiver, sender, other
	}

	t.Run("sender may push only the uncovered parts", func(t *testing.T) {
		receiver, sender, _ := setup(t)
		for _, i := range pushable {
			receiver.handleRecoveryPart(sender.self, partMsg(int(i)))
		}
		require.NotNil(t, receiver.getPeer(sender.self))
		_, parts, _, _ := receiver.getAllState(10, 0, false)
		require.Equal(t, len(pushable), len(parts.BitArray().GetTrueIndices()))

		// any other part is dropped and the sender stays connected
		receiver.handleRecoveryPart(sender.self, partMsg(coveredPart))
		require.NotNil(t, receiver.getPeer(sender.self))
		require.False(t, parts.HasPart(coveredPart))
	})

	t.Run("a part we already have does not use up the allowance", func(t *testing.T) {
		receiver, sender, other := setup(t)
		wants := createBitArray(int(ps.Total())*2, []int{0})
		receiver.getPeer(other.self).AddRequests(10, 0, wants)
		receiver.handleRecoveryPart(other.self, partMsg(0))

		receiver.handleRecoveryPart(sender.self, partMsg(0))
		require.True(t, receiver.getPeer(sender.self).ConsumePushAllowance(10, 0, 0))
	})

	t.Run("accepted pushed part is announced to other peers", func(t *testing.T) {
		receiver, sender, other := setup(t)
		require.True(t, other.AddProposal(cb))

		receiver.handleRecoveryPart(sender.self, partMsg(0))

		require.Eventually(t, func() bool {
			haves, has := other.getPeer(receiver.self).GetHaves(10, 0)
			return has && haves.GetIndex(0)
		}, 5*time.Second, 20*time.Millisecond)
	})

	t.Run("resending the compact block does not refill the allowance", func(t *testing.T) {
		receiver, sender, _ := setup(t)
		p := receiver.getPeer(sender.self)
		require.True(t, p.ConsumePushAllowance(10, 0, 0))
		receiver.handleCompactBlock(cb, sender.self, true)
		require.False(t, p.ConsumePushAllowance(10, 0, 0))
	})

	t.Run("unrequested part from a peer that sent no compact block is dropped", func(t *testing.T) {
		receiver, _, other := setup(t)
		receiver.handleRecoveryPart(other.self, partMsg(0))
		require.NotNil(t, receiver.getPeer(other.self))
		_, parts, _, _ := receiver.getAllState(10, 0, false)
		require.False(t, parts.HasPart(0))
	})

	t.Run("requested part is accepted from any peer", func(t *testing.T) {
		receiver, _, other := setup(t)
		wants := createBitArray(int(ps.Total())*2, []int{0})
		receiver.getPeer(other.self).AddRequests(10, 0, wants)

		receiver.handleRecoveryPart(other.self, partMsg(0))
		require.NotNil(t, receiver.getPeer(other.self))
		_, parts, _, _ := receiver.getAllState(10, 0, false)
		require.True(t, parts.HasPart(0))
	})

	t.Run("unrequested part during catchup is dropped", func(t *testing.T) {
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		receiver, other := reactors[0], reactors[1]
		psh := ps.Header()
		receiver.AddCommitment(10, 0, &psh)

		receiver.handleRecoveryPart(other.self, partMsg(0))
		require.NotNil(t, receiver.getPeer(other.self))
		_, parts, _, _ := receiver.getAllState(10, 0, true)
		require.False(t, parts.HasPart(0))
	})
}

// TestProposePushesUncoveredParts checks every peer completes the pushed parts
// without anyone being disconnected.
func TestProposePushesUncoveredParts(t *testing.T) {
	reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	prop, partSet, _, metaData := createTestProposal(t, sm, pv, 1, 0, 100, 1000)
	pushed := uncoveredParts(metaData, partSet.Total(), types.BlockPartSizeBytes, uint32(len(partSet.GetPart(int(partSet.Total()-1)).Bytes)))
	require.NotEmpty(t, pushed)

	require.NoError(t, reactors[0].ProposeBlock(prop, partSet, metaData))

	for _, r := range reactors[1:] {
		require.Eventually(t, func() bool {
			_, parts, _, has := r.getAllState(prop.Height, prop.Round, false)
			if !has {
				return false
			}
			for _, i := range pushed {
				if !parts.HasPart(int(i)) {
					return false
				}
			}
			return true
		}, 5*time.Second, 20*time.Millisecond)
	}
	for _, r := range reactors {
		require.Len(t, r.getPeers(), 2)
	}

	// the pushed parts arrive before the haves, so no peer asks the proposer
	// for them again
	proposer := reactors[0]
	for _, r := range reactors[1:] {
		for _, index := range pushed {
			require.False(t, r.getPeer(proposer.self).Requested(prop.Height, prop.Round, index))
		}
	}
}

// TestProposeAdvertisesFirstAndLastWhenNothingIsPushed checks a peer still
// learns the proposer has the first and last part when it pushes nothing, and
// requests them.
func TestProposeAdvertisesFirstAndLastWhenNothingIsPushed(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	prop, partSet, _, metaData := createTestProposal(t, sm, pv, 1, 0, 100, 1000)
	// a gap between txs means nothing is pushed
	metaData = append(metaData[:1], metaData[2:]...)
	lastIndex := partSet.Total() - 1
	require.Nil(t, uncoveredParts(metaData, partSet.Total(), types.BlockPartSizeBytes, uint32(len(partSet.GetPart(int(lastIndex)).Bytes))))

	proposer, receiver := reactors[0], reactors[1]
	require.NoError(t, proposer.ProposeBlock(prop, partSet, metaData))

	require.Eventually(t, func() bool {
		p := receiver.getPeer(proposer.self)
		return p.Requested(prop.Height, prop.Round, 0) && p.Requested(prop.Height, prop.Round, lastIndex)
	}, 5*time.Second, 20*time.Millisecond)
}

func TestPushAllowancePruned(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	p := reactors[0].getPeer(reactors[1].self)
	for h := int64(1); h <= 5; h++ {
		blockID := types.BlockID{PartSetHeader: types.PartSetHeader{Total: 1}}
		p.GrantPushAllowance(&proptypes.CompactBlock{Proposal: types.Proposal{Height: h, BlockID: blockID}})
	}
	p.prune(4)
	require.False(t, p.ConsumePushAllowance(3, 0, 0))
	require.True(t, p.ConsumePushAllowance(4, 0, 0))
}

func TestRequestPartsRollsBackFailedSend(t *testing.T) {
	peer := &mocks.Peer{}
	peer.On("TrySend", mock.Anything).Return(false).Once()
	peer.On("TrySend", mock.Anything).Return(true)
	p := newPeerState(context.Background(), peer, log.NewNopLogger())
	p.AddRequests(1, 0, createBitArray(4, []int{0}))
	requestedAt := p.state[1][0].requestedAt

	// a failed send rolls back only the parts this call added
	require.False(t, p.RequestParts(1, 0, createBitArray(4, []int{0, 1}), p2p.Envelope{}))
	require.Equal(t, requestedAt, p.state[1][0].requestedAt)
	requests, _ := p.GetRequests(1, 0)
	require.Equal(t, []int{0}, requests.GetTrueIndices())
	require.True(t, p.Requested(1, 0, 0))
	require.False(t, p.Requested(1, 0, 1), "a part whose only request failed must not count as requested")

	require.True(t, p.RequestParts(1, 0, createBitArray(4, []int{1}), p2p.Envelope{}))
	requests, _ = p.GetRequests(1, 0)
	require.Equal(t, []int{0, 1}, requests.GetTrueIndices())
}

func TestLateReplyAfterRequestExpiredIsAccepted(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	cb, ps, _, _ := testCompactBlock(t, sm, pv, 10, 0)

	reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
	receiver, sender, other := reactors[0], reactors[1], reactors[2]
	receiver.handleCompactBlock(cb, sender.self, true)

	// we asked other for part 0, and the request expired before it replied
	otherPeer := receiver.getPeer(other.self)
	otherPeer.AddRequests(10, 0, createBitArray(int(ps.Total())*2, []int{0}))
	otherPeer.ExpireRequests(10, 0, 0, 0)

	part := ps.GetPart(0)
	receiver.handleRecoveryPart(other.self, &proptypes.RecoveryPart{Height: 10, Round: 0, Index: part.Index, Data: part.Bytes, Proof: &part.Proof})

	require.NotNil(t, receiver.getPeer(other.self))
	_, parts, _, _ := receiver.getAllState(10, 0, false)
	require.True(t, parts.HasPart(0))
}

func TestCachedCompactBlockGrantsPushAllowance(t *testing.T) {
	reactors, _ := createTestReactors(3, defaultTestP2PConf(), false, "")
	receiver, sender, forger := reactors[0], reactors[1], reactors[2]
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	receiver.SetConsensusState(1, 0, mockPubKey)
	cb, _, _, _ := testCompactBlock(t, sm, pv, 2, 0)

	// same block ID, but the signature doesn't verify
	forged := &proptypes.CompactBlock{
		Proposal:    cb.Proposal,
		LastLen:     cb.LastLen,
		BpHash:      cb.BpHash,
		Blobs:       cb.Blobs,
		PartsHashes: cb.PartsHashes,
		Signature:   cmtrand.Bytes(64),
	}

	// both cached while behind, then replayed on reaching height 2
	receiver.handleCompactBlock(cb, sender.self, false)
	receiver.handleCompactBlock(forged, forger.self, false)
	require.NotNil(t, receiver.GetUnverifiedProposal(2))
	receiver.Prune(1)
	receiver.SetConsensusState(2, 0, mockPubKey)

	_, _, has := receiver.GetProposal(2, 0)
	require.True(t, has)
	require.True(t, receiver.getPeer(sender.self).ConsumePushAllowance(2, 0, 0))
	require.False(t, receiver.getPeer(forger.self).ConsumePushAllowance(2, 0, 0))
}
