package consensus

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// missedBlockFilter drops, at the receiving node, the messages that keep node d
// from ever receiving the round-0 block and keep round 0 from committing.
type missedBlockFilter struct {
	p2p.Reactor
	node, d, b, c int
	target        int64
	valIdx        map[int32]int
}

func (f *missedBlockFilter) QueueUnprocessedEnvelope(e p2p.UnprocessedEnvelope) {
	if !f.drop(e) {
		f.Reactor.QueueUnprocessedEnvelope(e)
	}
}

func (f *missedBlockFilter) drop(e p2p.UnprocessedEnvelope) bool {
	switch e.ChannelID {
	case VoteChannel, DataChannel:
		m := &cmtcons.Message{}
		if proto.Unmarshal(e.Message, m) != nil {
			return false
		}
		// every node sees three round-0 precommits but only two for the block.
		if v := m.GetVote(); v != nil && v.Vote != nil && v.Vote.Height == f.target &&
			v.Vote.Round == 0 && v.Vote.Type == cmtproto.PrecommitType {
			val := f.valIdx[v.Vote.ValidatorIndex]
			return (val == f.c && f.node != f.c) || (val == f.b && f.node == f.c)
		}
		if f.node != f.d {
			return false
		}
		if p := m.GetProposal(); p != nil && p.Proposal.Height == f.target && p.Proposal.Round == 0 {
			return true
		}
		if bp := m.GetBlockPart(); bp != nil && bp.Height == f.target && bp.Round == 0 {
			return true
		}
	case propagation.DataChannel, propagation.WantChannel:
		if f.node != f.d {
			return false
		}
		m := &propproto.Message{}
		if proto.Unmarshal(e.Message, m) != nil {
			return false
		}
		if cb := m.GetCompactBlock(); cb != nil && cb.Proposal != nil && cb.Proposal.Height == f.target && cb.Proposal.Round == 0 {
			return true
		}
		if hp := m.GetHaveParts(); hp != nil && hp.Height == f.target && hp.Round == 0 {
			return true
		}
		if rp := m.GetRecoveryPart(); rp != nil && rp.Height == f.target && rp.Round == 0 {
			return true
		}
	}
	return false
}

// configTimeoutsApp leaves the round timeouts to the consensus config.
type configTimeoutsApp struct{ *kvstore.Application }

func (a configTimeoutsApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	res, err := a.Application.FinalizeBlock(ctx, req)
	if res != nil {
		res.TimeoutInfo = abci.TimeoutInfo{}
	}
	return res, err
}

// TestReactorCommitsBlockNeverReceived checks that a validator that never receives
// the block the others lock on at round 0 and commit at round 1 still reaches the
// next height.
func TestReactorCommitsBlockNeverReceived(t *testing.T) {
	const n = 4
	const target = int64(3)
	css, cleanup := randConsensusNet(t, n, "consensus_missed_block_test", newMockTickerFunc(false),
		func() abci.Application { return configTimeoutsApp{kvstore.NewInMemoryApplication()} },
		func(c *cfg.Config) {
			c.Consensus.TimeoutPropose = 2 * time.Second
			c.Consensus.TimeoutProposeDelta = 500 * time.Millisecond
			c.Consensus.TimeoutPrevote = 500 * time.Millisecond
			c.Consensus.TimeoutPrecommit = 500 * time.Millisecond
			c.Consensus.TimeoutCommit = 200 * time.Millisecond
			c.Consensus.SkipTimeoutCommit = false
		})
	t.Cleanup(cleanup)
	for i := range css {
		css[i].SetTimeoutTicker(NewTimeoutTicker())
	}

	// roles at the target height: proposer priorities advance once per height.
	vals := css[0].GetState().Validators.CopyIncrementProposerPriority(int32(target - 1))
	p0 := vals.GetProposer().Address
	p1 := vals.CopyIncrementProposerPriority(1).GetProposer().Address
	valIdx := map[int32]int{}
	d, b, c := -1, -1, -1
	for i, cs := range css {
		addr := cs.privValidatorPubKey.Address()
		idx, _ := vals.GetByAddress(addr)
		valIdx[idx] = i
		switch {
		case bytes.Equal(addr, p0):
		case bytes.Equal(addr, p1):
			d = i
		case b < 0:
			b = i
		default:
			c = i
		}
	}

	reactors := make([]*Reactor, n)
	buses := make([]*types.EventBus, n)
	for i := range css {
		reactors[i] = NewReactor(css[i], css[i].propagator, true, WithGossipDataEnabled(false))
		reactors[i].SetLogger(css[i].Logger)
		buses[i] = css[i].eventBus
		reactors[i].SetEventBus(buses[i])
		require.NoError(t, css[i].blockExec.Store().Save(css[i].state))
	}
	p2p.MakeConnectedSwitches(config.P2P, n, func(i int, s *p2p.Switch) *p2p.Switch {
		prop := css[i].propagator.(*propagation.Reactor)
		prop.SetLogger(css[i].Logger.With("module", "propagation"))
		filter := func(r p2p.Reactor) p2p.Reactor {
			return &missedBlockFilter{Reactor: r, node: i, d: d, b: b, c: c, target: target, valIdx: valIdx}
		}
		s.AddReactor("CONSENSUS", filter(reactors[i]))
		s.AddReactor("RECOVERY", filter(prop))
		s.SetLogger(css[i].Logger.With("module", "p2p"))
		return s
	}, p2p.Connect2Switches)
	t.Cleanup(func() { stopConsensusNet(css[0].Logger, reactors, buses) })
	for i := range reactors {
		reactors[i].SwitchToConsensus(reactors[i].conS.GetState(), false)
	}

	require.Eventually(t, func() bool {
		for _, cs := range css {
			if cs.GetRoundState().Height <= target {
				return false
			}
		}
		return true
	}, 30*time.Second, 50*time.Millisecond, "node %d never received the block and stalled at height %d", d, target)

	// the scenario must have played out: the block was committed at round 1.
	commit := css[d].blockStore.LoadSeenCommit(target)
	require.NotNil(t, commit)
	require.EqualValues(t, 1, commit.Round)
}
