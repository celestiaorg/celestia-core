package consensus

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// slowExecApp sleeps in FinalizeBlock and returns the v11 timeouts.
type slowExecApp struct {
	abci.Application
	finalizeDelay time.Duration
	dynamic       time.Duration
}

func (a *slowExecApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	time.Sleep(a.finalizeDelay)
	res, err := a.Application.FinalizeBlock(ctx, req)
	if res != nil {
		res.TimeoutInfo = abci.TimeoutInfo{
			TimeoutPropose:          3000 * time.Millisecond,
			TimeoutProposeDelta:     500 * time.Millisecond,
			TimeoutPrevote:          2000 * time.Millisecond,
			TimeoutPrevoteDelta:     500 * time.Millisecond,
			TimeoutPrecommit:        2000 * time.Millisecond,
			TimeoutPrecommitDelta:   500 * time.Millisecond,
			TimeoutCommit:           100 * time.Millisecond,
			DelayedPrecommitTimeout: 0,
			DynamicTimeoutCommit:    a.dynamic,
		}
	}
	return res, err
}

// latePrecommitPV signs precommits after a delay, so they reach peers after the other 3.
type latePrecommitPV struct {
	types.PrivValidator
	delay time.Duration
}

func (pv *latePrecommitPV) SignVote(chainID string, vote *cmtproto.Vote) error {
	if vote.Type == cmtproto.PrecommitType {
		time.Sleep(pv.delay)
	}
	return pv.PrivValidator.SignVote(chainID, vote)
}

type lateResult struct {
	heights, absent, proposed, absentProposed int
}

// runLatePrecommit runs 4 validators where node 3 precommits late, and counts
// the heights in [from, to] whose LastCommit lacks node 3.
func runLatePrecommit(t *testing.T, finalizeDelay, precommitDelay, dynamic time.Duration, from, to int64) lateResult {
	t.Helper()
	css, cleanup := randConsensusNet(t, 4, "consensus_late_precommit_test", NewTimeoutTicker,
		func() abci.Application {
			return &slowExecApp{Application: newKVStore(), finalizeDelay: finalizeDelay, dynamic: dynamic}
		},
		func(c *cfg.Config) {
			c.Consensus.SkipTimeoutCommit = false
			c.Consensus.TimeoutCommit = 100 * time.Millisecond
			c.Consensus.TimeoutPropose = 3000 * time.Millisecond
			c.Consensus.TimeoutProposeDelta = 500 * time.Millisecond
			c.Consensus.TimeoutPrevote = 2000 * time.Millisecond
			c.Consensus.TimeoutPrevoteDelta = 500 * time.Millisecond
			c.Consensus.TimeoutPrecommit = 2000 * time.Millisecond
			c.Consensus.TimeoutPrecommitDelta = 500 * time.Millisecond
		})
	t.Cleanup(cleanup)
	for _, cs := range css {
		cs.SetLogger(log.NewNopLogger())
	}
	css[3].SetPrivValidator(&latePrecommitPV{PrivValidator: css[3].privValidator, delay: precommitDelay})
	lateAddr := css[3].privValidatorPubKey.Address()
	lateIdx, _ := css[0].state.Validators.GetByAddress(lateAddr)

	_, subs := startPacedNet(t, css, []bool{true, true, true, true})
	var res lateResult
	deadline := time.After(time.Duration(to+5) * (finalizeDelay + 2*time.Second))
	for {
		select {
		case msg := <-subs[0].Out():
			b := msg.Data().(types.EventDataNewBlock).Block
			if b.Height < from {
				continue
			}
			res.heights++
			absent := b.LastCommit.Signatures[lateIdx].BlockIDFlag == types.BlockIDFlagAbsent
			if absent {
				res.absent++
			}
			if string(b.ProposerAddress) == string(lateAddr) {
				res.proposed++
				if absent {
					res.absentProposed++
				}
			}
			if b.Height >= to {
				return res
			}
		case <-deadline:
			t.Fatalf("height %d not reached", to)
		}
	}
}

// TestLatePrecommitMatrix reports how often a validator whose precommit lands
// 10/50 ms after quorum is missing from the next LastCommit. Opt-in: LATE_PRECOMMIT_MATRIX=1.
func TestLatePrecommitMatrix(t *testing.T) {
	if os.Getenv("LATE_PRECOMMIT_MATRIX") == "" {
		t.Skip("set LATE_PRECOMMIT_MATRIX=1")
	}
	const from, to = 4, 35
	for _, dynamic := range []time.Duration{800 * time.Millisecond, 0} {
		for _, fd := range []time.Duration{0, 300 * time.Millisecond, 900 * time.Millisecond} {
			if dynamic == 0 && fd == 0 {
				continue
			}
			for _, pd := range []time.Duration{10 * time.Millisecond, 50 * time.Millisecond} {
				name := fmt.Sprintf("dyn%d_exec%d_late%d", dynamic.Milliseconds(), fd.Milliseconds(), pd.Milliseconds())
				t.Run(name, func(t *testing.T) {
					r := runLatePrecommit(t, fd, pd, dynamic, from, to)
					t.Logf("RESULT %s absent=%d/%d (as proposer %d/%d)", name, r.absent, r.heights, r.absentProposed, r.proposed)
				})
			}
		}
	}
}

// A precommit that lands after quorum must reach the next LastCommit even when
// the block executes past the dynamic deadline (zero-length NewHeight step).
func TestLatePrecommitIncludedAfterSlowExecution(t *testing.T) {
	r := runLatePrecommit(t, 900*time.Millisecond, 50*time.Millisecond, 800*time.Millisecond, 4, 12)
	require.Zero(t, r.absent, "late validator absent from %d of %d LastCommits", r.absent, r.heights)
}
