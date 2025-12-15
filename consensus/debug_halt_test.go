package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// TestDebugHaltUnlock tests the debug halt and unlock functionality.
// It verifies that:
// 1. DebugHalt returns the current height
// 2. IsDebugHalted returns true after halt
// 3. DebugUnlock returns correct halt duration and heights
// 4. IsDebugHalted returns false after unlock
func TestDebugHaltUnlock(t *testing.T) {
	cs1, _ := randState(4)
	height, round := cs1.rs.Height, cs1.rs.Round

	// Start the consensus state
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)

	startTestRound(cs1, height, round)

	// Wait for first round
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	// Verify not halted initially
	require.False(t, cs1.IsDebugHalted(), "should not be halted initially")

	// Halt consensus
	haltHeight := cs1.DebugHalt()
	require.Equal(t, height, haltHeight, "halt height should match current height")
	require.True(t, cs1.IsDebugHalted(), "should be halted after DebugHalt")

	// Calling halt again should be idempotent and return the original halt height
	haltHeight2 := cs1.DebugHalt()
	require.Equal(t, haltHeight, haltHeight2, "second halt should return same height")

	// Wait a bit to simulate time passing
	time.Sleep(50 * time.Millisecond)

	// Unlock consensus
	duration, unlockHaltHeight, currentHeight := cs1.DebugUnlock()
	require.Equal(t, haltHeight, unlockHaltHeight, "unlock should return original halt height")
	require.GreaterOrEqual(t, duration.Milliseconds(), int64(50), "duration should be at least 50ms")
	require.Equal(t, height, currentHeight, "current height should still be the same")
	require.False(t, cs1.IsDebugHalted(), "should not be halted after unlock")

	// Calling unlock again when not halted should return zeros
	duration2, haltHeight3, currentHeight2 := cs1.DebugUnlock()
	require.Equal(t, time.Duration(0), duration2, "duration should be 0 when not halted")
	require.Equal(t, int64(0), haltHeight3, "halt height should be 0 when not halted")
	require.Equal(t, height, currentHeight2, "current height should still be returned")
}

// TestDebugHaltBlocksCommit tests that debug halt actually blocks block commits
// while allowing message processing to continue.
func TestDebugHaltBlocksCommit(t *testing.T) {
	cs1, vss := randState(4)
	height, round := cs1.rs.Height, cs1.rs.Round

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	newBlockCh := subscribe(cs1.eventBus, types.EventQueryNewBlock)

	startTestRound(cs1, height, round)

	// Wait for first round and proposal
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	rs := cs1.GetRoundState()

	// Halt before precommits
	haltHeight := cs1.DebugHalt()
	require.Equal(t, height, haltHeight)
	require.True(t, cs1.IsDebugHalted())

	// Sign precommit votes - this should trigger commit attempt which will block
	signAddVotes(cs1, cmtproto.PrecommitType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), true, vss[1:]...)

	// Give time for the commit to be attempted (it should block)
	time.Sleep(100 * time.Millisecond)

	// Verify we're still at the same height (commit is blocked)
	rs = cs1.GetRoundState()
	require.Equal(t, height, rs.Height, "height should not have advanced while halted")

	// Now unlock
	cs1.DebugUnlock()

	// The commit should now proceed - wait for new block
	ensureNewBlock(newBlockCh, height)

	// Verify we advanced to the next height
	ensureNewRound(newRoundCh, height+1, 0)
	rs = cs1.GetRoundState()
	require.Equal(t, height+1, rs.Height, "height should advance after unlock")
}
