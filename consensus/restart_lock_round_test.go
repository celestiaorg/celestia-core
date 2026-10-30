package consensus

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
)

// laterRoundsFilter is missedBlockFilter that also drops every vote after round 0
// at the target height, so the fleet parks in round 1.
type laterRoundsFilter struct{ *missedBlockFilter }

func (f laterRoundsFilter) QueueUnprocessedEnvelope(e p2p.UnprocessedEnvelope) {
	if e.ChannelID == VoteChannel {
		m := &cmtcons.Message{}
		if proto.Unmarshal(e.Message, m) == nil {
			if v := m.GetVote(); v != nil && v.Vote != nil && v.Vote.Height == f.target && v.Vote.Round >= 1 {
				return
			}
		}
	}
	f.missedBlockFilter.QueueUnprocessedEnvelope(e)
}

// Three validators lock on X at round 0 without a precommit quorum and the round-1 proposer
// proposes Y. After restarting all four in round 1 the chain must still commit X, also when
// the round-0 proposer, the only node with X's bytes, starts consensus late.
func TestRestartAllAfterLockInEarlierRound(t *testing.T) {
	t.Run("restart_all", func(t *testing.T) { testRestartAllAfterLock(t, 0) })
	t.Run("p0_late", func(t *testing.T) { testRestartAllAfterLock(t, 10*time.Second) })
}

func testRestartAllAfterLock(t *testing.T, p0Delay time.Duration) {
	const n = 4
	const target = int64(3)
	ti := abci.TimeoutInfo{
		TimeoutPropose: 2 * time.Second, TimeoutProposeDelta: 500 * time.Millisecond,
		TimeoutPrevote: 500 * time.Millisecond, TimeoutPrevoteDelta: 100 * time.Millisecond,
		TimeoutPrecommit: 500 * time.Millisecond, TimeoutPrecommitDelta: 100 * time.Millisecond,
		TimeoutCommit: 200 * time.Millisecond,
	}
	css, restartNode := newRestartFleet(t, n, ti)

	// roles at the target height: proposer priorities advance once per height.
	vals := css[0].GetState().Validators.CopyIncrementProposerPriority(int32(target - 1))
	p0Addr := vals.GetProposer().Address
	p1Addr := vals.CopyIncrementProposerPriority(1).GetProposer().Address
	valIdx := map[int32]int{}
	p0, d, b, c := -1, -1, -1, -1
	for i, cs := range css {
		addr := cs.privValidatorPubKey.Address()
		idx, _ := vals.GetByAddress(addr)
		valIdx[idx] = i
		switch {
		case bytes.Equal(addr, p0Addr):
			p0 = i
		case bytes.Equal(addr, p1Addr):
			d = i
		case b < 0:
			b = i
		default:
			c = i
		}
	}

	reactors, blocksSubs, eventBuses := startPropagationNet(t, css, func(i int, r p2p.Reactor) p2p.Reactor {
		return laterRoundsFilter{&missedBlockFilter{Reactor: r, node: i, d: d, b: b, c: c, target: target, valIdx: valIdx}}
	})
	stopRun1 := sync.OnceFunc(func() { stopConsensusNet(css[0].Logger, reactors, eventBuses) })
	t.Cleanup(stopRun1)
	for h := int64(1); h < target; h++ {
		timeoutWaitGroup(n, func(j int) { <-blocksSubs[j].Out() })
	}
	require.Eventually(t, func() bool {
		for i, cs := range css {
			rs := cs.GetRoundState()
			if rs.Height != target || rs.Round != 1 || rs.Step != cstypes.RoundStepPrevote ||
				rs.Proposal == nil || rs.Proposal.Round != 1 || (i != d && rs.LockedRound != 0) {
				return false
			}
		}
		return true
	}, 30*time.Second, 10*time.Millisecond, "the fleet did not park in round 1 with a round-0 lock")
	x := css[p0].GetRoundState().LockedBlock.Hash()
	require.NotEqual(t, x, css[d].GetRoundState().Proposal.BlockID.Hash)
	stopRun1()

	css2 := make([]*State, n)
	for i := range css {
		css2[i] = restartNode(i)
	}
	reactors2, _, eventBuses2 := startPropagationNet(t, css2, nil, p0)
	defer stopConsensusNet(css2[0].Logger, reactors2, eventBuses2)
	time.Sleep(p0Delay)
	reactors2[p0].SwitchToConsensus(css2[p0].GetState(), false)
	require.Eventually(t, func() bool {
		for _, cs := range css2 {
			if cs.GetRoundState().Height <= target {
				return false
			}
		}
		return true
	}, 30*time.Second, 50*time.Millisecond, "not every node committed height %d after the restart", target)
	for i, cs := range css2 {
		require.Equal(t, x, cs.blockStore.LoadBlock(target).Hash(), "node %d", i)
	}
}
