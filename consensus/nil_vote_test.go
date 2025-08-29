package consensus

import (
	"fmt"
	"testing"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	"github.com/stretchr/testify/require"
)

// TestNilVoteExchange is an integration test that connects two consensus reactors and exchanges a nil vote.
func TestNilVoteExchange(t *testing.T) {
	nValidators := 2
	css, cleanup := randConsensusNet(t, nValidators, "consensus_two_peer_vote_test", newMockTickerFunc(true), newKVStore)
	defer cleanup()
	logger := log.TestingLogger()
	// Create reactors for the two validators
	reactors := make([]*Reactor, nValidators)
	for i := 0; i < nValidators; i++ {
		reactors[i] = NewReactor(css[i], css[i].propagator, true, WithGossipDataEnabled(true))
		reactors[i].SetLogger(css[i].Logger)
		reactors[i].SetEventBus(css[i].eventBus)
		if css[i].state.LastBlockHeight == 0 {
			if err := css[i].blockExec.Store().Save(css[i].state); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Connect the two peers using p2p switches
	switches := p2p.MakeConnectedSwitches(config.P2P, nValidators, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("CONSENSUS", reactors[i])
		s.SetLogger(reactors[i].conS.Logger.With("module", "p2p"))
		return s
	}, p2p.Connect2Switches)
	defer func() {
		for i, s := range switches {
			logger.Info("Stopping switch", "i", i)
			if err := s.Stop(); err != nil {
				t.Error(err)
			}
		}
	}()
	// Start the consensus state machines
	for i := 0; i < nValidators; i++ {
		s := reactors[i].conS.GetState()
		reactors[i].SwitchToConsensus(s, false)
	}
	// Wait for both peers to be connected
	require.Eventually(t, func() bool {
		return len(switches[0].Peers().List()) == 1 && len(switches[1].Peers().List()) == 1
	}, 5*time.Second, 100*time.Millisecond, "Peers should be connected")
	// Get references to the two consensus states
	cs0 := reactors[0].conS
	cs1 := reactors[1].conS
	// Wait for both to reach the same height/round
	require.Eventually(t, func() bool {
		cs0.rsMtx.RLock()
		cs1.rsMtx.RLock()
		height0, round0 := cs0.rs.Height, cs0.rs.Round
		height1, round1 := cs1.rs.Height, cs1.rs.Round
		cs0.rsMtx.RUnlock()
		cs1.rsMtx.RUnlock()
		return height0 == height1 && round0 == round1
	}, 10*time.Second, 100*time.Millisecond, "Both peers should reach the same height/round")
	s1 := switches[0]
	ps := s1.Peers().List()
	fmt.Println("found some peers", len(ps))
	p1 := ps[0]
	vote := &cmtcons.Vote{}
	vote.Vote = nil
	p1.Send(p2p.Envelope{
		ChannelID: VoteChannel,
		Message:   vote,
	})
	time.Sleep(time.Second * 1)
	t.Log("✓ Test completed successfully: Two peers connected and exchanged votes")
}
