package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// pacedApp returns v11-style timeouts (1ns TimeoutCommit plus a dynamic floor),
// delays every ProcessProposal and can make one height slow to execute.
type pacedApp struct {
	abci.Application
	dynamic        time.Duration
	timeoutPropose time.Duration
	processDelay   time.Duration
	slowHeight     int64
	slowFor        time.Duration
}

func (a *pacedApp) ProcessProposal(ctx context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	time.Sleep(a.processDelay)
	return a.Application.ProcessProposal(ctx, req)
}

func (a *pacedApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	if req.Height == a.slowHeight {
		time.Sleep(a.slowFor)
	}
	res, err := a.Application.FinalizeBlock(ctx, req)
	if res != nil {
		res.TimeoutInfo.TimeoutCommit = time.Nanosecond
		res.TimeoutInfo.DynamicTimeoutCommit = a.dynamic
		if a.timeoutPropose > 0 {
			res.TimeoutInfo.TimeoutPropose = a.timeoutPropose
		}
	}
	return res, err
}

// startPacedNet connects every node with the consensus and propagation
// reactors, then starts consensus on the nodes listed in start.
func startPacedNet(t *testing.T, css []*State, start []bool) ([]*Reactor, []types.Subscription) {
	t.Helper()
	n := len(css)
	reactors := make([]*Reactor, n)
	subs := make([]types.Subscription, n)
	for i, cs := range css {
		reactors[i] = NewReactor(cs, cs.propagator, true)
		reactors[i].SetLogger(cs.Logger)
		reactors[i].SetEventBus(cs.eventBus)
		sub, err := cs.eventBus.Subscribe(context.Background(), testSubscriber, types.EventQueryNewBlock, 100)
		require.NoError(t, err)
		subs[i] = sub
		require.NoError(t, cs.blockExec.Store().Save(cs.state))
	}
	p2p.MakeConnectedSwitches(config.P2P, n, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("CONSENSUS", reactors[i])
		s.AddReactor("RECOVERY", css[i].propagator.(*propagation.Reactor))
		s.SetLogger(css[i].Logger.With("module", "p2p"))
		return s
	}, p2p.Connect2Switches)
	t.Cleanup(func() {
		for i, r := range reactors {
			if err := r.Switch.Stop(); err != nil {
				t.Log(err)
			}
			if err := css[i].eventBus.Stop(); err != nil {
				t.Log(err)
			}
		}
	})
	for i, r := range reactors {
		if start[i] {
			r.SwitchToConsensus(css[i].GetState(), false)
		}
	}
	return reactors, subs
}

func newPacedNet(t *testing.T, n int, app func() *pacedApp) []*State {
	t.Helper()
	a := app()
	css, cleanup := randConsensusNet(t, n, "consensus_pacing_test", NewTimeoutTicker,
		func() abci.Application { return app() },
		func(c *cfg.Config) {
			c.Consensus.SkipTimeoutCommit = false
			if a.timeoutPropose > 0 {
				c.Consensus.TimeoutPropose = a.timeoutPropose
			}
		})
	t.Cleanup(cleanup)
	for _, cs := range css {
		cs.SetLogger(log.NewNopLogger())
	}
	return css
}

// waitHeight returns the local time each height up to target was committed.
func waitHeight(t *testing.T, sub types.Subscription, target int64) map[int64]time.Time {
	t.Helper()
	seen := make(map[int64]time.Time)
	deadline := time.After(30 * time.Second)
	for {
		select {
		case msg := <-sub.Out():
			h := msg.Data().(types.EventDataNewBlock).Block.Height
			seen[h] = time.Now()
			if h >= target {
				return seen
			}
		case <-sub.Canceled():
			t.Fatal("subscription canceled")
		case <-deadline:
			t.Fatalf("height %d not reached, saw %d heights", target, len(seen))
		}
	}
}

func TestPacingIntervalIncludesRoundAndSlowBlockStartsNextImmediately(t *testing.T) {
	const (
		tc    = 500 * time.Millisecond
		delay = 400 * time.Millisecond
	)
	css := newPacedNet(t, 4, func() *pacedApp {
		// The long propose timeout keeps round 0 alive while ProcessProposal sleeps.
		return &pacedApp{Application: newKVStore(), dynamic: tc, timeoutPropose: 5 * time.Second,
			processDelay: delay, slowHeight: 5, slowFor: 2 * tc}
	})
	_, subs := startPacedNet(t, css, []bool{true, true, true, true})

	seen := waitHeight(t, subs[0], 7)

	for h := int64(3); h <= 4; h++ {
		interval := seen[h].Sub(seen[h-1])
		t.Logf("height %d->%d %v", h-1, h, interval)
		require.Less(t, interval, tc+3*delay/4, "proposal processing must count towards the floor, not add to it")
	}
	afterSlow, next := seen[6].Sub(seen[5]), seen[7].Sub(seen[6])
	t.Logf("height 5->6 %v, 6->7 %v", afterSlow, next)
	require.Less(t, afterSlow, tc+3*delay/4, "a block slower than the floor must not wait the floor again")
	require.Greater(t, next, tc/2, "the floor must apply again once heights are fast")
}

func TestPacingLaggingNodeCatchesUpWithoutFloor(t *testing.T) {
	const (
		tc     = 500 * time.Millisecond
		target = 8
	)
	css := newPacedNet(t, 4, func() *pacedApp {
		return &pacedApp{Application: newKVStore(), dynamic: tc}
	})
	reactors, subs := startPacedNet(t, css, []bool{true, true, true, false})
	waitHeight(t, subs[0], target)

	start := time.Now()
	reactors[3].SwitchToConsensus(css[3].GetState(), false)
	waitHeight(t, subs[3], target)

	elapsed := time.Since(start)
	t.Logf("caught up %d heights in %v", target, elapsed)
	require.Less(t, elapsed, target*tc/2,
		"a lagging node must commit on +2/3 precommits, not at one height per DynamicTimeoutCommit")
}
