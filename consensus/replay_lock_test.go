package consensus

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	cfg "github.com/cometbft/cometbft/config"
	cmtpubsub "github.com/cometbft/cometbft/libs/pubsub"
	mempl "github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// lockFixture is a 4-validator height 1. Unless asProposer, our node is neither
// the round 0 nor the round 1 proposer, so its WAL never holds the block's bytes.
type lockFixture struct {
	config  *cfg.Config
	state   sm.State
	app     *kvstore.Application
	blockDB dbm.DB
	vss     []*validatorStub
	others  []*validatorStub
	p0, p1  int32
	// lazyDelay is how long a lock stays open before its block is copied; long by default.
	lazyDelay time.Duration
}

func newLockFixture(t *testing.T, onlyInternalWal, asProposer bool) *lockFixture {
	t.Helper()
	config := ResetConfig(strings.ReplaceAll(t.Name(), "/", "_"))
	t.Cleanup(func() { os.RemoveAll(config.RootDir) })
	config.Consensus.OnlyInternalWal = onlyInternalWal

	// Vote extensions off, as in celestia-app.
	state, privVals := randGenesisState(4, false, testMinPower, types.DefaultConsensusParams())
	state.Timeouts.TimeoutPropose = 3 * time.Second

	f := &lockFixture{config: config, state: state, app: kvstore.NewInMemoryApplication(), blockDB: dbm.NewMemDB(), lazyDelay: time.Hour}
	for i, pv := range privVals {
		f.vss = append(f.vss, newValidatorStub(pv, int32(i)))
		f.vss[i].Height = 1
	}
	f.p0, _ = state.Validators.GetByAddress(state.Validators.GetProposer().Address)
	vals := state.Validators.Copy()
	vals.IncrementProposerPriority(1)
	f.p1, _ = state.Validators.GetByAddress(vals.GetProposer().Address)
	ours := int32(-1)
	if asProposer {
		ours = f.p0
	}
	for i := range f.vss {
		if ours < 0 && int32(i) != f.p0 && int32(i) != f.p1 {
			ours = int32(i)
		} else if int32(i) != ours {
			f.others = append(f.others, f.vss[i])
		}
	}
	privval.NewFilePV(privVals[ours].(types.MockPV).PrivKey, config.PrivValidatorKeyFile(), config.PrivValidatorStateFile()).Save()
	return f
}

// newState builds our node from the files on disk, as a restart would.
func (f *lockFixture) newState() *State {
	pv := privval.LoadFilePV(f.config.PrivValidatorKeyFile(), f.config.PrivValidatorStateFile())
	cs := newStateWithConfigAndBlockStore(f.config, f.state, pv, f.app, f.blockDB)
	cs.lockedBlockDelay = func() time.Duration { return f.lazyDelay }
	return cs
}

// lockOnX drives a started cs to prevote the round 0 proposal X and receive a
// polka for it, which makes it lock and precommit X.
func (f *lockFixture) lockOnX(t *testing.T, cs *State) (*types.Proposal, *types.Block, *types.PartSet) {
	t.Helper()
	prop, block := decideProposal(context.Background(), t, cs, f.vss[f.p0], 1, 0)
	parts, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NoError(t, cs.SetProposalAndBlock(prop, block, parts, "peer"))
	signAddVotes(cs, cmtproto.PrevoteType, block.Hash(), parts.Header(), false, f.others...)
	return prop, block, parts
}

// lockAndStop locks a fresh node on X at 1/0, waits for its precommit and stops it.
func (f *lockFixture) lockAndStop(t *testing.T) (*types.Proposal, *types.Block, *types.PartSet) {
	t.Helper()
	cs := f.newState()
	require.NoError(t, cs.Start())
	voteCh := subscribeToVoter(cs, cs.privValidatorPubKey.Address())
	prop, block, parts := f.lockOnX(t, cs)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.EqualValues(t, 0, cs.GetRoundState().LockedRound)
	require.NoError(t, cs.Stop())
	cs.Wait()
	return prop, block, parts
}

// proposeFreshY makes the round 1 proposer propose a block other than X, with no POL.
func (f *lockFixture) proposeFreshY(t *testing.T, cs *State) (*types.Block, *types.PartSet) {
	t.Helper()
	require.NoError(t, assertMempool(cs.txNotifier).CheckTx(kvstore.NewTx("lock", "y"), nil, mempl.TxInfo{}))
	cs.mtx.Lock()
	blockY, _, err := cs.createProposalBlock(context.Background())
	cs.mtx.Unlock()
	require.NoError(t, err)
	partsY, err := blockY.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	propY := types.NewProposal(1, 1, -1, types.BlockID{Hash: blockY.Hash(), PartSetHeader: partsY.Header()})
	p := propY.ToProto()
	require.NoError(t, f.vss[f.p1].SignProposal(cs.state.ChainID, p))
	propY.Signature = p.Signature
	require.NoError(t, cs.SetProposalAndBlock(propY, blockY, partsY, "peer"))
	return blockY, partsY
}

// moveToRound1 has the other validators precommit nil at round 0.
func (f *lockFixture) moveToRound1(t *testing.T, cs *State, newRoundCh <-chan cmtpubsub.Message) {
	t.Helper()
	signAddVotes(cs, cmtproto.PrecommitType, nil, types.PartSetHeader{}, false, f.others...)
	waitRound(t, newRoundCh, 1, 1)
	incrementRound(f.vss...)
}

func nextVote(t *testing.T, voteCh <-chan cmtpubsub.Message, height int64, round int32, vt cmtproto.SignedMsgType) *types.Vote {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("no %v at %d/%d", vt, height, round)
		case msg := <-voteCh:
			v := msg.Data().(types.EventDataVote).Vote
			if v.Height == height && v.Round == round && v.Type == vt {
				return v
			}
		}
	}
}

func waitRound(t *testing.T, ch <-chan cmtpubsub.Message, height int64, round int32) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("no NewRound %d/%d", height, round)
		case msg := <-ch:
			ev := msg.Data().(types.EventDataNewRound)
			if ev.Height == height && ev.Round == round {
				return
			}
		}
	}
}

func startRestarted(t *testing.T, cs *State) (newRoundCh, voteCh <-chan cmtpubsub.Message) {
	t.Helper()
	sub, err := cs.eventBus.Subscribe(context.Background(), testSubscriber, types.EventQueryNewRound, 10)
	require.NoError(t, err)
	voteCh = subscribeToVoter(cs, cs.privValidatorPubKey.Address())
	require.NoError(t, cs.Start())
	t.Cleanup(func() { _ = cs.Stop(); cs.Wait() })
	return sub.Out(), voteCh
}

// A node locked on X at round 0 restarts; in round 1 a fresh block Y is
// proposed with no POL. The node must still prevote X.
func TestLockSurvivesRestart(t *testing.T) {
	for _, onlyInternal := range []bool{true, false} {
		t.Run(fmt.Sprintf("only_internal_wal=%v", onlyInternal), func(t *testing.T) {
			f := newLockFixture(t, onlyInternal, false)
			_, block, parts := f.lockAndStop(t)
			xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

			cs := f.newState()
			newRoundCh, voteCh := startRestarted(t, cs)
			rs := cs.GetRoundState()
			require.EqualValues(t, 0, rs.LockedRound, "lock lost across the restart")
			require.Equal(t, xID, rs.LockedBlockID)
			if onlyInternal {
				require.Nil(t, rs.LockedBlock, "a peer's block is not in the internal WAL")
			}

			f.moveToRound1(t, cs, newRoundCh)
			blockY, _ := f.proposeFreshY(t, cs)
			require.NotEqual(t, block.Hash(), blockY.Hash())
			v := nextVote(t, voteCh, 1, 1, cmtproto.PrevoteType)
			require.Equal(t, xID, v.BlockID, "must prevote the locked block, not the fresh proposal")
		})
	}
}

// A lock restored without bytes still commits once the parts arrive.
func TestLockByIDFinalizesWhenBytesArrive(t *testing.T) {
	f := newLockFixture(t, true, false)
	prop, block, parts := f.lockAndStop(t)

	cs := f.newState()
	blockCh := subscribe(cs.eventBus, types.EventQueryNewBlockHeader)
	startRestarted(t, cs)
	require.Nil(t, cs.GetRoundState().LockedBlock)

	signAddVotes(cs, cmtproto.PrecommitType, block.Hash(), parts.Header(), false, f.others...)
	ensureNoNewEvent(blockCh, 500*time.Millisecond, "committed without the block")
	require.NoError(t, cs.SetProposalAndBlock(prop, block, parts, "peer"))
	ensureNewBlockHeader(blockCh, 1, block.Hash())
}

// A restored lock is released by a polka for another block at a later round.
func TestLockByIDReleasedByLaterPolka(t *testing.T) {
	f := newLockFixture(t, true, false)
	_, block, _ := f.lockAndStop(t)

	cs := f.newState()
	newRoundCh, voteCh := startRestarted(t, cs)
	f.moveToRound1(t, cs, newRoundCh)
	blockY, partsY := f.proposeFreshY(t, cs)
	v := nextVote(t, voteCh, 1, 1, cmtproto.PrevoteType)
	require.Equal(t, block.Hash(), v.BlockID.Hash)

	signAddVotes(cs, cmtproto.PrevoteType, blockY.Hash(), partsY.Header(), false, f.others...)
	v = nextVote(t, voteCh, 1, 1, cmtproto.PrecommitType)
	yID := types.BlockID{Hash: blockY.Hash(), PartSetHeader: partsY.Header()}
	require.Equal(t, yID, v.BlockID)
	rs := cs.GetRoundState()
	require.EqualValues(t, 1, rs.LockedRound)
	require.Equal(t, yID, rs.LockedBlockID)
}

// A later nil polka releases a lock held by ID only, and the release survives a restart.
func TestUnlockSurvivesRestart(t *testing.T) {
	f := newLockFixture(t, true, false)
	f.lockAndStop(t)

	cs := f.newState()
	newRoundCh, voteCh := startRestarted(t, cs)
	f.moveToRound1(t, cs, newRoundCh)
	signAddVotes(cs, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, f.others...)
	blockY, partsY := f.proposeFreshY(t, cs)
	v := nextVote(t, voteCh, 1, 1, cmtproto.PrevoteType)
	require.Equal(t, types.BlockID{Hash: blockY.Hash(), PartSetHeader: partsY.Header()}, v.BlockID,
		"the nil polka must release the lock before we prevote")
	nextVote(t, voteCh, 1, 1, cmtproto.PrecommitType)
	require.NoError(t, cs.Stop())
	cs.Wait()

	cs = f.newState()
	startRestarted(t, cs)
	require.False(t, cs.GetRoundState().IsLocked(), "unlock lost across the restart")
}

// A proposer locked on its own block gets the bytes back from its WAL.
func TestLockRestoresOwnProposalBytes(t *testing.T) {
	f := newLockFixture(t, true, true)
	cs := f.newState()
	voteCh := subscribeToVoter(cs, cs.privValidatorPubKey.Address())
	require.NoError(t, cs.Start())
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	signAddVotes(cs, cmtproto.PrevoteType, v.BlockID.Hash, v.BlockID.PartSetHeader, false, f.others...)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.NoError(t, cs.Stop())
	cs.Wait()

	cs = f.newState()
	startRestarted(t, cs)
	rs := cs.GetRoundState()
	require.Equal(t, v.BlockID, rs.LockedBlockID)
	require.True(t, blockMatchesBlockID(rs.LockedBlock, rs.LockedBlockParts, v.BlockID))
}

// crashBeforeSignWAL crashes at the first flush that follows a LockInfo write,
// i.e. after the lock is journaled and before the precommit is signed.
type crashBeforeSignWAL struct {
	WAL
	panicCh chan error
	locked  bool
}

func (w *crashBeforeSignWAL) Write(m WALMessage) error {
	if _, ok := m.(LockInfo); ok {
		w.locked = true
	}
	return w.WAL.Write(m)
}

func (w *crashBeforeSignWAL) FlushAndSync() error {
	err := w.WAL.FlushAndSync()
	if w.locked {
		w.panicCh <- err
		runtime.Goexit()
	}
	return err
}

// A crash after the lock record and before the signature leaves a locked node
// that has not precommitted; once the polka is back it precommits X by ID.
func TestLockRecordCrashBeforePrecommit(t *testing.T) {
	f := newLockFixture(t, true, false)
	cs := f.newState()
	csWal, err := cs.OpenWAL(f.config.Consensus.WalFile())
	require.NoError(t, err)
	walPanicked := make(chan error, 1)
	cs.wal = &crashBeforeSignWAL{WAL: csWal, panicCh: walPanicked}
	require.NoError(t, cs.Start())
	_, block, parts := f.lockOnX(t, cs)
	select {
	case err := <-walPanicked:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("did not crash before signing the precommit")
	}
	_ = cs.Stop()
	stopCrashedWAL(t, csWal)

	pv := privval.LoadFilePV(f.config.PrivValidatorKeyFile(), f.config.PrivValidatorStateFile())
	require.Equal(t, int8(2), pv.LastSignState.Step, "the precommit must not have been signed")

	cs2 := f.newState()
	_, voteCh := startRestarted(t, cs2)
	rs := cs2.GetRoundState()
	xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	require.EqualValues(t, 0, rs.LockedRound)
	require.Equal(t, xID, rs.LockedBlockID)
	require.Nil(t, rs.LockedBlock)

	signAddVotes(cs2, cmtproto.PrevoteType, block.Hash(), parts.Header(), false, f.others...)
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.Equal(t, xID, v.BlockID)
}

// A WAL written before lock records existed replays as before: no lock.
func TestReplayWALWithoutLockRecords(t *testing.T) {
	f := newLockFixture(t, true, false)
	f.lockAndStop(t)
	walFile := f.config.Consensus.WalFile()
	require.Positive(t, dropLockRecords(t, walFile))

	cs := f.newState()
	newRoundCh, _ := startRestarted(t, cs)
	require.False(t, cs.GetRoundState().IsLocked())
	f.moveToRound1(t, cs, newRoundCh)
}

// dropLockRecords rewrites the WAL head file without its LockInfo records.
func dropLockRecords(t *testing.T, walFile string) int {
	t.Helper()
	in, err := os.Open(walFile)
	require.NoError(t, err)
	dec := NewWALDecoder(in)
	var msgs []*TimedWALMessage
	dropped := 0
	for {
		msg, err := dec.Decode()
		if err != nil {
			break
		}
		if _, ok := msg.Msg.(LockInfo); ok {
			dropped++
			continue
		}
		msgs = append(msgs, msg)
	}
	require.NoError(t, in.Close())

	out, err := os.Create(walFile)
	require.NoError(t, err)
	enc := NewWALEncoder(out)
	for _, msg := range msgs {
		require.NoError(t, enc.Encode(msg))
	}
	require.NoError(t, out.Close())
	return dropped
}
