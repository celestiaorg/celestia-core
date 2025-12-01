package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	"github.com/cometbft/cometbft/proto/tendermint/libs/bits"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestLegacyAndNewReactorCompatibility(t *testing.T) {
	N := 2
	css, cleanup := randConsensusNet(t, N, "consensus_compat_test", newMockTickerFunc(true), newKVStore)
	defer cleanup()

	reactors := make([]*Reactor, N)
	eventBuses := make([]*types.EventBus, N)

	// Node 0: Legacy Propagation Enabled (Has DataChannel)
	// Node 1: Legacy Propagation Disabled (No DataChannel)

	for i := 0; i < N; i++ {
		var opts []ReactorOption
		if i == 0 {
			opts = append(opts, WithGossipDataEnabled(true))
		} else {
			opts = append(opts, WithGossipDataEnabled(false))
		}

		r := NewReactor(css[i], css[i].propagator, true, opts...)
		r.SetLogger(css[i].Logger)

		eventBuses[i] = css[i].eventBus
		r.SetEventBus(eventBuses[i])

		_, err := eventBuses[i].Subscribe(context.Background(), testSubscriber, types.EventQueryNewBlock)
		require.NoError(t, err)

		reactors[i] = r

		if css[i].state.LastBlockHeight == 0 {
			if err := css[i].blockExec.Store().Save(css[i].state); err != nil {
				t.Error(err)
			}
		}
	}

	switches := p2p.MakeConnectedSwitches(config.P2P, N, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("CONSENSUS", reactors[i])
		s.SetLogger(reactors[i].conS.Logger.With("module", "p2p"))
		return s
	}, p2p.Connect2Switches)

	// Start the state machines
	for i := 0; i < N; i++ {
		s := reactors[i].conS.GetState()
		reactors[i].SwitchToConsensus(s, false)
	}
	defer stopConsensusNet(log.TestingLogger(), reactors, eventBuses)

	t.Log("Reactors started. Waiting to observe connectivity...")

	// Allow some time for handshake and initial gossip
	time.Sleep(2 * time.Second)

	// Check connectivity
	peers0 := switches[0].Peers().List()
	peers1 := switches[1].Peers().List()

	t.Logf("Node 0 peers: %d", len(peers0))
	t.Logf("Node 1 peers: %d", len(peers1))

	// Ensure connected
	assert.Equal(t, 1, len(peers0))
	assert.Equal(t, 1, len(peers1))

	// Manually send a NewRoundStep message from Node 1 to Node 0 to ensure StateChannel is working.
	// This verifies that despite mismatched DataChannel support, the nodes remain connected
	// and can exchange consensus control messages.
	peer0 := switches[1].Peers().List()[0]
	msg := &cmtcons.NewValidBlock{
		Height: 1,
		Round:  0,
		BlockPartSetHeader: cmtproto.PartSetHeader{
			Total: 1,
			Hash:  cmtrand.Bytes(32),
		},
		BlockParts: &bits.BitArray{Elems: []uint64{1}, Bits: int64(1)},
		IsCommit:   false,
	}
	peer0.Send(p2p.Envelope{
		ChannelID: StateChannel,
		Message:   msg,
	})

	// Let's wait and see if they stay connected and process the message.
	time.Sleep(2 * time.Second)

	peers0After := switches[0].Peers().List()
	t.Logf("Node 0 peers after 2s: %d", len(peers0After))
	assert.Equal(t, 1, len(peers0After), "Nodes should remain connected")

	// Check if Node 0 received the state update from Node 1
	peer1From0 := switches[0].Peers().List()[0]
	ps0 := peer1From0.Get(types.PeerStateKey).(*PeerState)

	// Verify the state matches what we sent
	// Note: ps.GetRoundState() returns a copy, so it's safe to read.
	rs := ps0.GetRoundState()
	t.Logf("Node 0 view of Node 1: Hash=%v", rs.ProposalBlockPartSetHeader.Hash)
	assert.Equal(t, msg.BlockPartSetHeader.Hash, rs.ProposalBlockPartSetHeader.Hash.Bytes(), "Node 0 should have updated peer state for Node 1")
}
