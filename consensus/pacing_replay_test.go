package consensus

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/internal/test"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestPacingWALReplayRestoresRoundBeforeStartupDeadline(t *testing.T) {
	state, privVals := randGenesisState(4, false, 10, test.ConsensusParams())
	// NewState establishes a new local deadline on restart. Make it long
	// enough that the test does not depend on how quickly replay executes.
	state.Timeouts.TimeoutCommit = time.Hour
	cs := newState(state, privVals[0], kvstore.NewInMemoryApplication())
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	require.True(t, time.Now().Before(cs.rs.StartTime))

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	proposal := types.NewProposal(cs.rs.Height, 0, -1,
		types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()})
	p := proposal.ToProto()
	require.NoError(t, cs.privValidator.SignProposal(cs.state.ChainID, p))
	proposal.Signature = p.Signature
	cs.SetPrivValidator(nil)

	wal, err := NewWAL(filepath.Join(t.TempDir(), "wal"))
	require.NoError(t, err)
	require.NoError(t, wal.Start())
	t.Cleanup(func() {
		require.NoError(t, wal.Stop())
		wal.Wait()
	})
	cs.wal = wal
	// This sequence was received before the crash: the height began and
	// then a complete signed proposal arrived, taking consensus to Prevote.
	require.NoError(t, wal.Write(timeoutInfo{
		Height: cs.rs.Height,
		Round:  0,
		Step:   cstypes.RoundStepNewHeight,
	}))
	require.NoError(t, wal.Write(msgInfo{Msg: &ProposalMessage{Proposal: proposal}}))
	for i := uint32(0); i < parts.Total(); i++ {
		require.NoError(t, wal.Write(msgInfo{Msg: &BlockPartMessage{
			Height: cs.rs.Height,
			Round:  0,
			Part:   parts.GetPart(int(i)),
		}}))
	}
	require.NoError(t, wal.FlushAndSync())

	require.NoError(t, cs.catchupReplay(cs.rs.Height))
	require.False(t, cs.replayMode)
	require.True(t, cs.isProposalComplete(), "the signed proposal must have been restored")
	require.Equal(t, cstypes.RoundStepPrevote, cs.rs.Step,
		"historical WAL events must restore the prior step without waiting for the new startup deadline")
}

func TestPacingWALReplayRestoresLegacyEarlyProposalLock(t *testing.T) {
	state, privVals := randGenesisState(4, false, 10, test.ConsensusParams())
	state.Timeouts.TimeoutCommit = time.Hour
	cs := newState(state, privVals[0], kvstore.NewInMemoryApplication())
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	require.True(t, time.Now().Before(cs.rs.StartTime))

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	proposal := types.NewProposal(cs.rs.Height, 0, -1,
		types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()})
	p := proposal.ToProto()
	require.NoError(t, cs.privValidator.SignProposal(cs.state.ChainID, p))
	proposal.Signature = p.Signature
	cs.SetPrivValidator(nil)

	wal, err := NewWAL(filepath.Join(t.TempDir(), "wal"))
	require.NoError(t, err)
	require.NoError(t, wal.Start())
	t.Cleanup(func() {
		require.NoError(t, wal.Stop())
		wal.Wait()
	})
	cs.wal = wal
	// Before the pacing fix, a complete proposal arriving during NewHeight
	// entered Prevote without a NewHeight timeout. A following prevote quorum
	// locked this block, and the node could crash after its own precommit.
	require.NoError(t, wal.Write(msgInfo{Msg: &ProposalMessage{Proposal: proposal}}))
	for i := uint32(0); i < parts.Total(); i++ {
		require.NoError(t, wal.Write(msgInfo{Msg: &BlockPartMessage{
			Height: cs.rs.Height,
			Round:  0,
			Part:   parts.GetPart(int(i)),
		}}))
	}
	validators := make([]*validatorStub, len(privVals))
	for i, pv := range privVals {
		validators[i] = newValidatorStub(pv, int32(i))
		validators[i].Height = cs.rs.Height
	}
	for _, vote := range signVotes(cmtproto.PrevoteType, block.Hash(), parts.Header(), false, validators[:3]...) {
		require.NoError(t, wal.Write(msgInfo{Msg: &VoteMessage{Vote: vote}}))
	}
	precommit, err := validators[0].signVote(cmtproto.PrecommitType, block.Hash(), parts.Header(), nil,
		cs.state.ConsensusParams.ABCI.VoteExtensionsEnabled(cs.rs.Height))
	require.NoError(t, err)
	require.NoError(t, wal.Write(msgInfo{Msg: &VoteMessage{Vote: precommit}}))
	require.NoError(t, wal.FlushAndSync())

	require.NoError(t, cs.catchupReplay(cs.rs.Height))
	require.True(t, cs.rs.Votes.Prevotes(0).HasTwoThirdsMajority(), "the signed prevote quorum must be restored")
	require.NotNil(t, cs.rs.Votes.Precommits(0).GetByIndex(0), "the node's signed precommit must be restored")
	require.False(t, cs.rs.Votes.Precommits(0).HasTwoThirdsMajority(), "the WAL ends before a committing quorum")
	require.Equal(t, int32(0), cs.rs.LockedRound,
		"replay must preserve the lock established before the pacing fix")
	require.True(t, cs.rs.LockedBlock.HashesTo(block.Hash()), "replay must restore the originally locked block")
	require.Equal(t, cstypes.RoundStepPrecommit, cs.rs.Step)
}
