package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestUnrequestedParts(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	cb, ps, _, _ := testCompactBlock(t, sm, pv, 10, 0)
	require.Greater(t, int(ps.Total()), maxPushedParts+2)

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

	t.Run("sender may push up to the cap", func(t *testing.T) {
		receiver, sender, _ := setup(t)
		for i := range maxPushedParts {
			receiver.handleRecoveryPart(sender.self, partMsg(i))
		}
		require.NotNil(t, receiver.getPeer(sender.self))
		_, parts, _, _ := receiver.getAllState(10, 0, false)
		require.Equal(t, maxPushedParts, len(parts.BitArray().GetTrueIndices()))

		receiver.handleRecoveryPart(sender.self, partMsg(maxPushedParts))
		require.Nil(t, receiver.getPeer(sender.self))
	})

	t.Run("resending the compact block does not refill the allowance", func(t *testing.T) {
		receiver, sender, _ := setup(t)
		for i := range maxPushedParts {
			receiver.handleRecoveryPart(sender.self, partMsg(i))
		}
		receiver.handleCompactBlock(cb, sender.self, true)
		receiver.handleRecoveryPart(sender.self, partMsg(maxPushedParts))
		require.Nil(t, receiver.getPeer(sender.self))
	})

	t.Run("unrequested part from a peer that sent no compact block disconnects it", func(t *testing.T) {
		receiver, _, other := setup(t)
		receiver.handleRecoveryPart(other.self, partMsg(0))
		require.Nil(t, receiver.getPeer(other.self))
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
// without anyone being disconnected, so relays don't forward them unasked.
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
}

func TestPushAllowancePruned(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	p := reactors[0].getPeer(reactors[1].self)
	for h := int64(1); h <= 5; h++ {
		p.GrantPushAllowance(h, 0, maxPushedParts)
	}
	p.prune(4)
	require.False(t, p.ConsumePushAllowance(3, 0))
	require.True(t, p.ConsumePushAllowance(4, 0))
}

func TestRemoveRequestsKeepsEarlierRequests(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	p := reactors[0].getPeer(reactors[1].self)
	p.AddRequests(1, 0, createBitArray(4, []int{0}))

	added := p.AddRequests(1, 0, createBitArray(4, []int{0, 1}))
	require.Equal(t, []int{1}, added.GetTrueIndices())

	p.RemoveRequests(1, 0, added)
	require.True(t, p.Requested(1, 0, 0))
	require.False(t, p.Requested(1, 0, 1))
}

func TestCachedCompactBlockGrantsPushAllowance(t *testing.T) {
	reactors, _ := createTestReactors(2, defaultTestP2PConf(), false, "")
	receiver, sender := reactors[0], reactors[1]
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	receiver.SetConsensusState(1, 0, mockPubKey)
	cb, _, _, _ := testCompactBlock(t, sm, pv, 2, 0)

	// cached while behind, then replayed on reaching height 2
	receiver.handleCompactBlock(cb, sender.self, false)
	require.NotNil(t, receiver.GetUnverifiedProposal(2))
	receiver.Prune(1)
	receiver.SetConsensusState(2, 0, mockPubKey)

	_, _, has := receiver.GetProposal(2, 0)
	require.True(t, has)
	require.True(t, receiver.getPeer(sender.self).ConsumePushAllowance(2, 0))
}
