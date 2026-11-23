package blocksync

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	mpmocks "github.com/cometbft/cometbft/mempool/mocks"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

// valChangeApp returns a fixed, height-keyed schedule of validator updates and
// records the DecidedLastCommit it receives for every height.
type valChangeApp struct {
	*abci.BaseApplication
	schedule map[int64][]abci.ValidatorUpdate

	mtx     sync.Mutex
	decided map[int64]string
}

func (a *valChangeApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	resp, err := a.BaseApplication.FinalizeBlock(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.ValidatorUpdates = a.schedule[req.Height]
	a.mtx.Lock()
	a.decided[req.Height] = fmt.Sprintf("%v", req.DecidedLastCommit)
	a.mtx.Unlock()
	return resp, nil
}

type pipeNode struct {
	reactor      *ByzantineReactor
	app          *valChangeApp
	appConns     proxy.AppConns
	stateStore   sm.Store
	blockStore   *store.BlockStore
	state        sm.State
	blockExec    *sm.BlockExecutor
	invalidBlock *types.Block
}

// newReactor (re)creates the node's blocksync reactor from its stores so the
// same generated chain can be served in several runs.
func (n *pipeNode) newReactor() {
	bcReactor := NewReactor(n.state.Copy(), n.blockExec, n.blockStore, true, NopMetrics(), 0, ReactorVerifyData(false))
	bcReactor.SetLogger(log.NewNopLogger())
	byz := NewByzantineReactor(0, bcReactor)
	byz.invalidBlock = n.invalidBlock
	n.reactor = byz
}

func newPipeNode(
	t *testing.T,
	genDoc *types.GenesisDoc,
	privValsByAddr map[string]types.PrivValidator,
	schedule map[int64][]abci.ValidatorUpdate,
	maxBlockHeight, invalidCommitHeight int64,
) *pipeNode {
	t.Helper()
	app := &valChangeApp{BaseApplication: abci.NewBaseApplication(), schedule: schedule, decided: map[int64]string{}}
	appConns := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
	require.NoError(t, appConns.Start())

	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{DiscardABCIResponses: false})
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)

	mp := &mpmocks.Mempool{}
	mp.On("Lock").Return()
	mp.On("Unlock").Return()
	mp.On("FlushAppConn", mock.Anything).Return(nil)
	mp.On("Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	blockExec := sm.NewBlockExecutor(stateStore, log.NewNopLogger(), appConns.Consensus(), mp, sm.EmptyEvidencePool{}, blockStore)
	require.NoError(t, stateStore.Save(state))

	lastCommit := &types.Commit{}
	var invalidBlock *types.Block
	for h := int64(1); h <= maxBlockHeight; h++ {
		block, parts, err := state.MakeBlock(h, types.MakeData([]types.Tx{types.Tx(fmt.Sprintf("tx-%d", h))}), lastCommit, nil, state.Validators.GetProposer().Address)
		require.NoError(t, err)
		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		commit := makeMultiValCommit(t, h, blockID, state.Validators, privValsByAddr, state.ChainID)
		if h == invalidCommitHeight {
			invalidBlock, _, err = state.MakeBlock(h, types.MakeData([]types.Tx{types.Tx(fmt.Sprintf("tx-%d", h))}), corruptLastSig(t, lastCommit), nil, state.Validators.GetProposer().Address)
			require.NoError(t, err)
		}
		state, err = blockExec.ApplyBlock(state, blockID, block, lastCommit)
		require.NoError(t, err)
		blockStore.SaveBlock(block, parts, commit)
		lastCommit = commit
	}

	n := &pipeNode{app: app, appConns: appConns, stateStore: stateStore, blockStore: blockStore,
		state: state, blockExec: blockExec, invalidBlock: invalidBlock}
	n.newReactor()
	return n
}

type syncOutcome struct {
	storeHeight int64
	peersLeft   int
	blockHashes []string
	commits     []string
	decided     map[int64]string
	stateBytes  []byte
	stats       [5]int64
	misses      [4]int64
}

func runPipelineSync(
	t *testing.T,
	enabled bool,
	genDoc *types.GenesisDoc,
	privValsByAddr map[string]types.PrivValidator,
	schedule map[int64][]abci.ValidatorUpdate,
	source *pipeNode,
	waitHeight int64,
) syncOutcome {
	t.Helper()

	source.newReactor()
	receiver := newPipeNode(t, genDoc, privValsByAddr, schedule, 0, 0)
	receiver.reactor.pipeline = enabled
	source.reactor.pipeline = enabled
	nodes := []*pipeNode{source, receiver}

	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", nodes[i].reactor)
		return s
	}, p2p.Connect2Switches)

	start := time.Now()
	for receiver.blockStore.Height() < waitHeight && receiver.reactor.Switch.Peers().Size() != 0 {
		if time.Since(start) > 90*time.Second {
			t.Fatalf("timeout: receiver height %d", receiver.blockStore.Height())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// let any in-flight processing settle
	time.Sleep(300 * time.Millisecond)
	for _, n := range nodes {
		_ = n.reactor.Stop()
	}
	time.Sleep(100 * time.Millisecond)
	_ = receiver.appConns.Stop()

	out := syncOutcome{
		storeHeight: receiver.blockStore.Height(),
		peersLeft:   receiver.reactor.Switch.Peers().Size(),
		decided:     map[int64]string{},
	}
	for h := int64(1); h <= out.storeHeight; h++ {
		out.blockHashes = append(out.blockHashes, receiver.blockStore.LoadBlockMeta(h).BlockID.String())
		out.commits = append(out.commits, fmt.Sprintf("%X", receiver.blockStore.LoadBlockCommit(h).Hash()))
	}
	receiver.app.mtx.Lock()
	for h, d := range receiver.app.decided {
		out.decided[h] = d
	}
	receiver.app.mtx.Unlock()
	st, err := receiver.stateStore.Load()
	require.NoError(t, err)
	out.stateBytes = st.Bytes()
	ps := &receiver.reactor.pipelineStats
	out.stats = [5]int64{ps.started.Load(), ps.partsUsed.Load(), ps.commitUsed.Load(), ps.commitMissed.Load(), ps.partsDiscarded.Load()}
	out.misses = [4]int64{ps.missNoNext.Load(), ps.missBlockChanged.Load(), ps.missSpecFailed.Load(), ps.missValsMismatch.Load()}
	return out
}

func pipelineFixture(t *testing.T) (*types.GenesisDoc, map[string]types.PrivValidator, map[int64][]abci.ValidatorUpdate) {
	genDoc, privVals := randGenesisDoc(6, false, 10)
	genDoc.ConsensusParams.ABCI.VoteExtensionsEnableHeight = 0
	byAddr := map[string]types.PrivValidator{}
	pubs := make([]types.GenesisValidator, 0, len(genDoc.Validators))
	for _, pv := range privVals {
		pk, err := pv.GetPubKey()
		require.NoError(t, err)
		byAddr[pk.Address().String()] = pv
	}
	pubs = append(pubs, genDoc.Validators...)
	extra := make([]types.PrivValidator, 3)
	for i := range extra {
		_, pv := types.RandValidator(false, 1)
		extra[i] = pv
		pk, err := pv.GetPubKey()
		require.NoError(t, err)
		byAddr[pk.Address().String()] = pv
	}
	upd := func(pv types.PrivValidator, power int64) abci.ValidatorUpdate {
		pk, err := pv.GetPubKey()
		require.NoError(t, err)
		return types.TM2PB.NewValidatorUpdate(pk, power)
	}
	genUpd := func(i int, power int64) abci.ValidatorUpdate {
		return types.TM2PB.NewValidatorUpdate(pubs[i].PubKey, power)
	}
	schedule := map[int64][]abci.ValidatorUpdate{
		3:  {genUpd(0, 50)},
		5:  {upd(extra[0], 20)},
		6:  {upd(extra[1], 15), genUpd(1, 30)},
		9:  {genUpd(2, 0)}, // remove
		12: {upd(extra[0], 5)},
		13: {upd(extra[2], 40)},
		17: {genUpd(3, 0), upd(extra[1], 0)},
		21: {genUpd(0, 11)},
	}
	return genDoc, byAddr, schedule
}

// TestPipelineEquivalence syncs a chain with validator set changes with the
// pipeline + parallel commit verification + LastValidators shortcut off and on
// and checks identical stored blocks, commits, app inputs and final state.
func TestPipelineEquivalence(t *testing.T) {
	config = test.ResetTestRoot("blocksync_pipeline_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, byAddr, schedule := pipelineFixture(t)

	const maxHeight = int64(30)
	source := newPipeNode(t, genDoc, byAddr, schedule, maxHeight, 0)
	defer source.appConns.Stop() //nolint:errcheck
	off := runPipelineSync(t, false, genDoc, byAddr, schedule, source, maxHeight-1)
	on := runPipelineSync(t, true, genDoc, byAddr, schedule, source, maxHeight-1)

	require.Equal(t, maxHeight-1, off.storeHeight)
	require.Equal(t, off.storeHeight, on.storeHeight)
	require.Equal(t, off.blockHashes, on.blockHashes)
	require.Equal(t, off.commits, on.commits)
	require.Equal(t, off.decided, on.decided)
	require.Equal(t, off.stateBytes, on.stateBytes)
	require.Len(t, on.decided, int(maxHeight-1))

	t.Logf("stats off=%v on=%v (started, partsUsed, commitUsed, commitMissed, partsDiscarded) misses=%v (noNext, blockChanged, specFailed, valsMismatch)", off.stats, on.stats, on.misses)
	require.Zero(t, on.misses[3], "speculated validator set must match state.Validators")
	require.Zero(t, on.misses[2])
	require.Equal(t, [5]int64{}, off.stats)
	require.Greater(t, on.stats[2], maxHeight/2, "pipelined commit verification should be used for most blocks")
	require.Zero(t, on.stats[4])
}

// TestPipelineBadCommitMidChain: a peer serves a block whose LastCommit has an
// invalid signature past +2/3 in the middle of a chain with validator set
// changes. Both modes must stop at the same height and drop the peer.
func TestPipelineBadCommitMidChain(t *testing.T) {
	config = test.ResetTestRoot("blocksync_pipeline_bad_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, byAddr, schedule := pipelineFixture(t)

	const maxHeight, badHeight = int64(30), int64(15)
	source := newPipeNode(t, genDoc, byAddr, schedule, maxHeight, badHeight)
	defer source.appConns.Stop() //nolint:errcheck
	off := runPipelineSync(t, false, genDoc, byAddr, schedule, source, maxHeight-1)
	on := runPipelineSync(t, true, genDoc, byAddr, schedule, source, maxHeight-1)

	require.Equal(t, badHeight-2, off.storeHeight)
	require.Equal(t, off.storeHeight, on.storeHeight)
	require.Equal(t, 0, off.peersLeft)
	require.Equal(t, 0, on.peersLeft)
	require.Equal(t, off.blockHashes, on.blockHashes)
	require.Equal(t, off.decided, on.decided)
	require.Equal(t, off.stateBytes, on.stateBytes)
	t.Logf("stats on=%v misses=%v", on.stats, on.misses)
	require.Zero(t, on.misses[3])
	require.GreaterOrEqual(t, on.stats[3], int64(1), "speculative failure must fall back to synchronous verification")
}

// TestBlockSpecGuards checks the consumption guards of a speculative result.
func TestBlockSpecGuards(t *testing.T) {
	vals, privVals := types.RandValidatorSet(20, 10)
	byAddr := map[string]types.PrivValidator{}
	for _, pv := range privVals {
		pk, _ := pv.GetPubKey()
		byAddr[pk.Address().String()] = pv
	}
	state := sm.State{Version: sm.InitStateVersion(1), ChainID: "spec-chain", Validators: vals, NextValidators: vals, LastValidators: vals,
		ConsensusParams: *types.DefaultConsensusParams(), InitialHeight: 1, LastBlockHeight: 4}
	prevCommit := makeMultiValCommit(t, 4, makeRandomBlockID(), vals, byAddr, "spec-chain")
	block, parts, err := state.MakeBlock(5, types.MakeData(nil), prevCommit, nil, vals.GetProposer().Address)
	require.NoError(t, err)
	bid := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	nextCommit := makeMultiValCommit(t, 5, bid, vals, byAddr, "spec-chain")
	state.LastBlockHeight = 5
	next, _, err := state.MakeBlock(6, types.MakeData(nil), nextCommit, nil, vals.GetProposer().Address)
	require.NoError(t, err)

	// fresh copies to mimic blocks received from the network
	clone := func(b *types.Block) *types.Block {
		pb, err := b.ToProto()
		require.NoError(t, err)
		c, err := types.BlockFromProto(pb)
		require.NoError(t, err)
		return c
	}
	b5, b6 := clone(block), clone(next)
	require.True(t, canSpeculate(b5))

	s := startBlockSpec("spec-chain", b5, b6, vals.Copy())
	<-s.done
	p, id, ok := s.partsFor(b5)
	require.True(t, ok)
	require.Equal(t, bid, id)
	require.Equal(t, parts.Header(), p.Header())
	require.True(t, s.commitVerifiedFor(b5, b6, vals))

	// different block objects with identical content: not used
	_, _, ok = s.partsFor(clone(b5))
	require.False(t, ok)
	require.False(t, s.commitVerifiedFor(b5, clone(b6), vals))

	// validator set differs (power change): not used
	changed := vals.Copy()
	require.NoError(t, changed.UpdateWithChangeSet([]*types.Validator{types.NewValidator(changed.Validators[0].PubKey, 99)}))
	require.False(t, s.commitVerifiedFor(b5, b6, changed))

	// verification failure is never surfaced as success
	bad := clone(next)
	bad.LastCommit = corruptLastSig(t, nextCommit)
	s2 := startBlockSpec("spec-chain", b5, bad, vals.Copy())
	<-s2.done
	require.False(t, s2.commitVerifiedFor(b5, bad, vals))
	_, _, ok = s2.partsFor(b5)
	require.True(t, ok)

	// wrong (speculated) validator set: commit result not used
	other, _ := types.RandValidatorSet(20, 10)
	s3 := startBlockSpec("spec-chain", b5, b6, other)
	<-s3.done
	require.False(t, s3.commitVerifiedFor(b5, b6, vals))

	// missing next block: parts only
	s4 := startBlockSpec("spec-chain", b5, nil, vals.Copy())
	<-s4.done
	_, _, ok = s4.partsFor(b5)
	require.True(t, ok)
	require.False(t, s4.commitVerifiedFor(b5, b6, vals))

	// nil spec
	var ns *blockSpec
	_, _, ok = ns.partsFor(b5)
	require.False(t, ok)
	require.False(t, ns.commitVerifiedFor(b5, b6, vals))

	// blocks with nil memoized header hashes are not speculated on
	b5.LastCommitHash = nil
	require.False(t, canSpeculate(b5))
}
