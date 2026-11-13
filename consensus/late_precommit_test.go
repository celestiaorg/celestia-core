package consensus

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cstypes "github.com/cometbft/cometbft/consensus/types"

	cmtpubsub "github.com/cometbft/cometbft/libs/pubsub"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

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

// A precommit that lands after quorum must reach the next LastCommit even when
// the block executes past the dynamic deadline (zero-length NewHeight step).
func TestLatePrecommitIncludedAfterSlowExecution(t *testing.T) {
	const from, to = 4, 21
	css := newPacedNet(t, 4, func() *pacedApp {
		return &pacedApp{Application: newKVStore(), dynamic: 800 * time.Millisecond,
			timeoutPropose: 3 * time.Second, execDelay: 900 * time.Millisecond}
	})
	css[3].SetPrivValidator(&latePrecommitPV{PrivValidator: css[3].privValidator, delay: 50 * time.Millisecond})
	lateIdx, _ := css[0].state.Validators.GetByAddress(css[3].privValidatorPubKey.Address())
	_, subs := startPacedNet(t, css, []bool{true, true, true, true})

	deadline := time.After(2 * time.Minute)
	for absent := 0; ; {
		select {
		case msg := <-subs[0].Out():
			b := msg.Data().(types.EventDataNewBlock).Block
			if b.Height >= from && b.LastCommit.Signatures[lateIdx].BlockIDFlag == types.BlockIDFlagAbsent {
				absent++
			}
			if b.Height >= to {
				// One miss in 18 heights is allowed: a precommit still in flight when round 0
				// starts is lost by design. Without either mechanism the late validator misses ~2-6 of 9.
				require.LessOrEqual(t, absent, 1, "late validator absent from %d LastCommits", absent)
				return
			}
		case <-deadline:
			t.Fatalf("height %d not reached", to)
		}
	}
}

// receiveRoutine must handle our own queued vote before queued peer votes.
// Under random select order this fails about half the time per iteration.
func TestReceiveRoutineHandlesOwnMessagesFirst(t *testing.T) {
	for i := 0; i < 20; i++ {
		cs, vss := randState(4)
		voteCh := subscribeBuffered(cs.eventBus, types.EventQueryVote, 10)
		ownAddr := cs.privValidatorPubKey.Address()

		// A nil prevote quorum moves the state to a new step, which ends
		// receiveRoutine(1) without a goroutine.
		cs.updateRoundStep(0, cstypes.RoundStepPrevote)
		cs.nSteps = 0
		for _, vs := range vss {
			vs.Height, vs.Round = cs.rs.Height, 0
			vote := signVote(vs, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false)
			if bytes.Equal(vote.ValidatorAddress, ownAddr) {
				cs.internalMsgQueue <- msgInfo{&VoteMessage{vote}, ""}
			} else {
				cs.peerMsgQueue <- msgInfo{&VoteMessage{vote}, "peer"}
			}
		}
		cs.receiveRoutine(1)

		var msg cmtpubsub.Message
		select {
		case msg = <-voteCh:
		case <-time.After(5 * time.Second):
			t.Fatal("no vote event")
		}
		first := msg.Data().(types.EventDataVote).Vote
		require.Equal(t, ownAddr, first.ValidatorAddress, "iteration %d", i)
	}
}
