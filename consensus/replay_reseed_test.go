package consensus

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// proposeRecorder records SetConsensusState ("set") and ProposeBlock ("propose") calls in order.
type proposeRecorder struct {
	propagation.Propagator

	mtx   cmtsync.Mutex
	calls []string
	props []*types.Proposal
	parts []*types.PartSet
}

func (r *proposeRecorder) SetConsensusState(height int64, round int32, proposer crypto.PubKey) {
	r.mtx.Lock()
	r.calls = append(r.calls, "set")
	r.mtx.Unlock()
	r.Propagator.SetConsensusState(height, round, proposer)
}

func (r *proposeRecorder) ProposeBlock(prop *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData) error {
	r.mtx.Lock()
	r.calls = append(r.calls, "propose")
	r.props = append(r.props, prop)
	r.parts = append(r.parts, parts)
	r.mtx.Unlock()
	return r.Propagator.ProposeBlock(prop, parts, txs)
}

// The round 0 proposer restarts with its proposal and parts replayed and hands
// them back to the propagator, after SetConsensusState.
func TestRestartedProposerReseedsPropagator(t *testing.T) {
	f := newLockFixture(t, true, false)
	// Our node is the round 0 proposer.
	privval.NewFilePV(f.vss[f.p0].PrivValidator.(types.MockPV).PrivKey,
		f.config.PrivValidatorKeyFile(), f.config.PrivValidatorStateFile()).Save()

	cs1 := f.newState()
	require.NoError(t, cs1.Start())
	voteCh := subscribeToVoter(cs1, cs1.privValidatorPubKey.Address())
	nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	prop := cs1.GetRoundState().Proposal
	require.NotNil(t, prop)
	require.NoError(t, cs1.Stop())
	cs1.Wait()

	cs := f.newState()
	rec := &proposeRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	startRestarted(t, cs)

	rec.mtx.Lock()
	defer rec.mtx.Unlock()
	require.Len(t, rec.props, 1)
	require.Equal(t, []string{"set", "propose"}, rec.calls[len(rec.calls)-2:])
	require.Equal(t, prop.BlockID, rec.props[0].BlockID)
	require.Equal(t, prop.Signature, rec.props[0].Signature)
	require.True(t, rec.parts[0].HasHeader(prop.BlockID.PartSetHeader))
	_, held, has := rec.GetProposal(1, 0)
	require.True(t, has)
	require.True(t, held.IsComplete())
}

func TestReseedPropagatorGuards(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round
	ourIdx, _ := cs.rs.Validators.GetByAddress(cs.privValidatorPubKey.Address())
	require.True(t, cs.isProposer(cs.privValidatorPubKey.Address()))
	prop, block := decideProposal(context.Background(), t, cs, vss[ourIdx], height, round)
	parts, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	run := func(edit func(rs *proposeRecorder)) int {
		rec := &proposeRecorder{Propagator: propagation.NewNoOpPropagator()}
		cs.propagator = rec
		cs.rs.Proposal, cs.rs.ProposalBlock, cs.rs.ProposalBlockParts = prop, block, parts
		edit(rec)
		cs.reseedPropagator()
		return len(rec.calls)
	}
	require.Equal(t, 1, run(func(*proposeRecorder) {}))
	require.Zero(t, run(func(*proposeRecorder) { cs.rs.ProposalBlock = nil }), "no block")
	require.Zero(t, run(func(*proposeRecorder) { cs.rs.Proposal = nil }), "no proposal")
	require.Zero(t, run(func(*proposeRecorder) {
		p := *prop
		p.Round = round + 1
		cs.rs.Proposal = &p
	}), "proposal of another round")
	require.Zero(t, run(func(*proposeRecorder) {
		p := *prop
		p.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)
		cs.rs.Proposal = &p
	}), "part set does not match the proposal")
	require.Zero(t, run(func(rec *proposeRecorder) { rec.Propagator = heldPropagator{} }), "already held")

	pk := cs.privValidatorPubKey
	cs.privValidatorPubKey = vss[(ourIdx+1)%4].PrivValidator.(types.MockPV).PrivKey.PubKey()
	require.Zero(t, run(func(*proposeRecorder) {}), "not the round's proposer")
	cs.privValidatorPubKey = pk
}

type heldPropagator struct{ *propagation.NoOpPropagator }

func (heldPropagator) GetProposal(int64, int32) (*types.Proposal, *types.PartSet, bool) {
	return nil, nil, true
}

// timeoutsApp reports fixed timeouts so the test controls the precommit delay.
type timeoutsApp struct {
	*kvstore.Application
	ti abci.TimeoutInfo
}

func (a *timeoutsApp) Info(ctx context.Context, req *abci.RequestInfo) (*abci.ResponseInfo, error) {
	res, err := a.Application.Info(ctx, req)
	if err != nil {
		return nil, err
	}
	res.TimeoutInfo = a.ti
	return res, nil
}

func (a *timeoutsApp) InitChain(ctx context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	res, err := a.Application.InitChain(ctx, req)
	if err != nil {
		return nil, err
	}
	res.TimeoutInfo = a.ti
	return res, nil
}

func (a *timeoutsApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	res, err := a.Application.FinalizeBlock(ctx, req)
	if err != nil {
		return nil, err
	}
	res.TimeoutInfo = a.ti
	return res, nil
}

// startPropagationNet is startConsensusNet with legacy block gossip off: block
// data moves only through each state's propagation reactor, as in production.
func startPropagationNet(t *testing.T, css []*State) ([]*Reactor, []types.Subscription, []*types.EventBus) {
	n := len(css)
	reactors := make([]*Reactor, n)
	blocksSubs := make([]types.Subscription, n)
	eventBuses := make([]*types.EventBus, n)
	for i := 0; i < n; i++ {
		reactors[i] = NewReactor(css[i], css[i].propagator, true, WithGossipDataEnabled(false))
		reactors[i].SetLogger(css[i].Logger)
		eventBuses[i] = css[i].eventBus
		reactors[i].SetEventBus(eventBuses[i])
		sub, err := eventBuses[i].Subscribe(context.Background(), testSubscriber, types.EventQueryNewBlock)
		require.NoError(t, err)
		blocksSubs[i] = sub
	}
	p2p.MakeConnectedSwitches(config.P2P, n, func(i int, s *p2p.Switch) *p2p.Switch {
		prop := css[i].propagator.(*propagation.Reactor)
		prop.SetLogger(css[i].Logger.With("module", "propagation"))
		s.AddReactor("CONSENSUS", reactors[i])
		s.AddReactor("RECOVERY", prop)
		s.SetLogger(css[i].Logger.With("module", "p2p"))
		return s
	}, p2p.Connect2Switches)
	for i := 0; i < n; i++ {
		reactors[i].SwitchToConsensus(reactors[i].conS.GetState(), false)
	}
	return reactors, blocksSubs, eventBuses
}

// Four validators with only_internal_wal are stopped together while sleeping in
// the precommit delay of height 3 (all prevoted the proposal X, nobody locked).
// Restarting the three non-proposers must recover; restarting all four must
// commit X, which only the proposer's replayed WAL holds.
func TestCorrelatedRestartDuringPrecommitDelay(t *testing.T) {
	for _, tc := range []struct {
		restartProposer, legacyGossip bool
	}{
		{false, true}, {false, false}, {true, true}, {true, false},
	} {
		t.Run(fmt.Sprintf("restart_proposer=%v/legacy_gossip=%v", tc.restartProposer, tc.legacyGossip), func(t *testing.T) {
			testCorrelatedRestart(t, tc.restartProposer, tc.legacyGossip)
		})
	}
}

func testCorrelatedRestart(t *testing.T, restartProposer, legacyGossip bool) {
	const n = 4
	const target = int64(3)
	ti := abci.TimeoutInfo{
		TimeoutPropose: 3 * time.Second, TimeoutProposeDelta: 500 * time.Millisecond,
		TimeoutPrevote: 500 * time.Millisecond, TimeoutPrevoteDelta: 100 * time.Millisecond,
		TimeoutPrecommit: 500 * time.Millisecond, TimeoutPrecommitDelta: 100 * time.Millisecond,
		TimeoutCommit:           200 * time.Millisecond,
		DelayedPrecommitTimeout: 2 * time.Second,
	}
	genDoc, mockPVs := randGenesisDoc(n, false, 30, nil)
	logger := consensusLogger()
	configs := make([]*cfg.Config, n)
	apps := make([]abci.Application, n)
	dbs := make([]dbm.DB, n)
	css := make([]*State, n)
	newState := func(i int, state sm.State, run int) *State {
		pv := privval.LoadFilePV(configs[i].PrivValidatorKeyFile(), configs[i].PrivValidatorStateFile())
		cs := newStateWithConfigAndBlockStore(configs[i], state, pv, apps[i], dbs[i])
		cs.SetTimeoutTicker(NewTimeoutTicker())
		cs.SetLogger(logger.With("validator", i, "run", run))
		return cs
	}
	for i := 0; i < n; i++ {
		dbs[i] = dbm.NewMemDB()
		stateStore := sm.NewStore(dbs[i], sm.StoreOptions{DiscardABCIResponses: false})
		state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
		require.NoError(t, err)
		state.Timeouts.TimeoutPropose, state.Timeouts.TimeoutProposeDelta = ti.TimeoutPropose, ti.TimeoutProposeDelta
		state.Timeouts.TimeoutPrevote, state.Timeouts.TimeoutPrevoteDelta = ti.TimeoutPrevote, ti.TimeoutPrevoteDelta
		state.Timeouts.TimeoutPrecommit, state.Timeouts.TimeoutPrecommitDelta = ti.TimeoutPrecommit, ti.TimeoutPrecommitDelta
		state.Timeouts.TimeoutCommit, state.Timeouts.DelayedPrecommitTimeout = ti.TimeoutCommit, ti.DelayedPrecommitTimeout
		configs[i] = ResetConfig(fmt.Sprintf("%s_%d", strings.ReplaceAll(t.Name(), "/", "_"), i))
		t.Cleanup(func() { os.RemoveAll(configs[i].RootDir) })
		configs[i].Consensus.OnlyInternalWal = true
		ensureDir(filepath.Dir(configs[i].Consensus.WalFile()), 0o700)
		apps[i] = &timeoutsApp{Application: kvstore.NewInMemoryApplication(), ti: ti}
		_, err = apps[i].InitChain(context.Background(), &abci.RequestInitChain{Validators: types.TM2PB.ValidatorUpdates(state.Validators)})
		require.NoError(t, err)
		privval.NewFilePV(mockPVs[i].(types.MockPV).PrivKey,
			configs[i].PrivValidatorKeyFile(), configs[i].PrivValidatorStateFile()).Save()
		css[i] = newState(i, state, 1)
	}
	start := func(css []*State) ([]*Reactor, []types.Subscription, []*types.EventBus) {
		if legacyGossip {
			return startConsensusNet(t, css, len(css))
		}
		return startPropagationNet(t, css)
	}

	reactors, blocksSubs, eventBuses := start(css)
	for h := int64(1); h < target; h++ {
		timeoutWaitGroup(n, func(j int) { <-blocksSubs[j].Out() })
	}
	require.Eventually(t, func() bool {
		for _, cs := range css {
			rs := cs.GetRoundState()
			if rs.Height != target || rs.Round != 0 || !cs.StartedPrecommitSleep.Load() {
				return false
			}
		}
		return true
	}, 20*time.Second, 2*time.Millisecond)
	stopConsensusNet(logger, reactors, eventBuses)

	rs := css[0].GetRoundState()
	proposer := rs.Validators.GetProposer().Address
	restart := make([]int, 0, n)
	var x []byte
	for i, cs := range css {
		if bytes.Equal(cs.privValidatorPubKey.Address(), proposer) {
			x = cs.GetRoundState().ProposalBlock.Hash()
			if !restartProposer {
				continue
			}
		}
		restart = append(restart, i)
	}
	require.NotNil(t, x)

	css2 := make([]*State, len(restart))
	for k, i := range restart {
		css2[k] = newState(i, css[i].GetState(), 2)
	}
	reactors2, blocksSubs2, eventBuses2 := start(css2)
	defer stopConsensusNet(logger, reactors2, eventBuses2)

	blocks := make(chan *types.Block, len(css2))
	for _, sub := range blocksSubs2 {
		go func() {
			select {
			case msg := <-sub.Out():
				blocks <- msg.Data().(types.EventDataNewBlock).Block
			case <-time.After(30 * time.Second):
				blocks <- nil
			}
		}()
	}
	for range css2 {
		b := <-blocks
		require.NotNil(t, b, "no block at height %d within 30s of the restart", target)
		require.Equal(t, target, b.Height)
		if restartProposer {
			require.Equal(t, x, b.Hash().Bytes(), "the replayed proposal was not committed")
		}
	}
}
