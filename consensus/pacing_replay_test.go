package consensus

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/abci/types/mocks"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/internal/test"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestPacingWALReplayRestoresRoundBeforeStartupDeadline(t *testing.T) {
	state, privVals := randGenesisState(4, false, 10, test.ConsensusParams())
	// NewState establishes a new local deadline on restart. Make it long
	// enough that the test does not depend on how quickly replay executes.
	state.Timeouts.DynamicTimeoutCommit = time.Hour
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

func TestPacingWALReplayEntersRoundZeroBeforeStartupDeadline(t *testing.T) {
	state, privVals := randGenesisState(4, false, 10, test.ConsensusParams())
	state.Timeouts.DynamicTimeoutCommit = time.Hour
	cs := newState(state, privVals[0], kvstore.NewInMemoryApplication())
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.SetPrivValidator(nil)
	require.True(t, time.Now().Before(cs.rs.StartTime))

	wal, err := NewWAL(filepath.Join(t.TempDir(), "wal"))
	require.NoError(t, err)
	require.NoError(t, wal.Start())
	t.Cleanup(func() {
		require.NoError(t, wal.Stop())
		wal.Wait()
	})
	cs.wal = wal
	// The height had started before the crash, so replay must not wait again.
	require.NoError(t, wal.Write(timeoutInfo{Height: cs.rs.Height, Round: 0, Step: cstypes.RoundStepNewHeight}))
	require.NoError(t, wal.FlushAndSync())

	require.NoError(t, cs.catchupReplay(cs.rs.Height))
	require.Equal(t, cstypes.RoundStepPropose, cs.rs.Step)
}

func TestPacingWALReplayRestoresLegacyEarlyProposalLock(t *testing.T) {
	state, privVals := randGenesisState(4, false, 10, test.ConsensusParams())
	state.Timeouts.DynamicTimeoutCommit = time.Hour
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

func TestHandshakeStoresDynamicTimeoutCommit(t *testing.T) {
	app := &mocks.Application{}
	app.On("Info", mock.Anything, mock.Anything).Return(&abci.ResponseInfo{}, nil)
	app.On("InitChain", mock.Anything, mock.Anything).Return(&abci.ResponseInitChain{
		TimeoutInfo: abci.TimeoutInfo{TimeoutCommit: time.Nanosecond, DynamicTimeoutCommit: 800 * time.Millisecond},
	}, nil)
	config := ResetConfig("handshake_dynamic_test_")
	t.Cleanup(func() { os.RemoveAll(config.RootDir) })
	stateDB, state, store := stateAndStore(t, config, 0x0)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{})
	genDoc, err := sm.MakeGenesisDocFromFile(config.GenesisFile())
	require.NoError(t, err)
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	t.Cleanup(func() { require.NoError(t, proxyApp.Stop()) })

	_, err = NewHandshaker(stateStore, state, store, genDoc).Handshake(proxyApp)
	require.NoError(t, err)

	state, err = stateStore.Load()
	require.NoError(t, err)
	require.Equal(t, 800*time.Millisecond, state.Timeouts.DynamicTimeoutCommit)
	require.Equal(t, time.Nanosecond, state.Timeouts.TimeoutCommit)
}
