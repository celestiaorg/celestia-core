package evidence_test

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/evidence"
	"github.com/cometbft/cometbft/evidence/mocks"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	smmocks "github.com/cometbft/cometbft/state/mocks"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

const evidenceChainID = "test_chain"

var (
	defaultEvidenceTime           = time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	defaultEvidenceMaxBytes int64 = 1000
)

func TestEvidencePoolBasic(t *testing.T) {
	var (
		height     = int64(1)
		stateStore = &smmocks.Store{}
		evidenceDB = dbm.NewMemDB()
		blockStore = &mocks.BlockStore{}
	)

	valSet, privVals := types.RandValidatorSet(1, 10)

	blockStore.On("LoadBlockMeta", mock.AnythingOfType("int64")).Return(
		&types.BlockMeta{Header: types.Header{Time: defaultEvidenceTime}},
	)
	stateStore.On("LoadValidators", mock.AnythingOfType("int64")).Return(valSet, nil)
	stateStore.On("Load").Return(createState(height+1, valSet), nil)

	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	require.NoError(t, err)
	pool.SetLogger(log.TestingLogger())

	// evidence not seen yet:
	evs, size := pool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Equal(t, 0, len(evs))
	assert.Zero(t, size)

	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, defaultEvidenceTime, privVals[0], evidenceChainID)
	require.NoError(t, err)

	// good evidence
	evAdded := make(chan struct{})
	go func() {
		<-pool.EvidenceWaitChan()
		close(evAdded)
	}()

	// evidence seen but not yet committed:
	assert.NoError(t, pool.AddEvidence(ev))

	select {
	case <-evAdded:
	case <-time.After(5 * time.Second):
		t.Fatal("evidence was not added to list after 5s")
	}

	next := pool.EvidenceFront()
	assert.Equal(t, ev, next.Value.(types.Evidence))

	const evidenceBytes int64 = 372
	evs, size = pool.PendingEvidence(evidenceBytes)
	assert.Equal(t, 1, len(evs))
	assert.Equal(t, evidenceBytes, size) // check that the size of the single evidence in bytes is correct

	// shouldn't be able to add evidence twice
	assert.NoError(t, pool.AddEvidence(ev))
	evs, _ = pool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Equal(t, 1, len(evs))
}

// Tests inbound evidence for the right time and height
func TestAddExpiredEvidence(t *testing.T) {
	var (
		val                 = types.NewMockPV()
		height              = int64(30)
		stateStore          = initializeValidatorState(val, height)
		evidenceDB          = dbm.NewMemDB()
		blockStore          = &mocks.BlockStore{}
		expiredEvidenceTime = time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)
		expiredHeight       = int64(2)
	)

	blockStore.On("LoadBlockMeta", mock.AnythingOfType("int64")).Return(func(h int64) *types.BlockMeta {
		if h == height || h == expiredHeight {
			return &types.BlockMeta{Header: types.Header{Time: defaultEvidenceTime}}
		}
		return &types.BlockMeta{Header: types.Header{Time: expiredEvidenceTime}}
	})

	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	require.NoError(t, err)

	testCases := []struct {
		evHeight      int64
		evTime        time.Time
		expErr        bool
		evDescription string
	}{
		{height, defaultEvidenceTime, false, "valid evidence"},
		{expiredHeight, defaultEvidenceTime, false, "valid evidence (despite old height)"},
		{height - 1, expiredEvidenceTime, false, "valid evidence (despite old time)"},
		{
			expiredHeight - 1, expiredEvidenceTime, true,
			"evidence from height 1 (created at: 2019-01-01 00:00:00 +0000 UTC) is too old",
		},
		{height - 2, defaultEvidenceTime, true, "evidence time and block time is different"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.evDescription, func(t *testing.T) {
			ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(tc.evHeight, tc.evTime, val, evidenceChainID)
			require.NoError(t, err)
			err = pool.AddEvidence(ev)
			if tc.expErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReportConflictingVotes(t *testing.T) {
	var height int64 = 10

	pool, pv := defaultTestPool(t, height)
	val := types.NewValidator(pv.PrivKey.PubKey(), 10)
	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height+1, defaultEvidenceTime, pv, evidenceChainID)
	require.NoError(t, err)

	pool.ReportConflictingVotes(ev.VoteA, ev.VoteB)

	// shouldn't be able to submit the same evidence twice
	pool.ReportConflictingVotes(ev.VoteA, ev.VoteB)

	// evidence from consensus should not be added immediately but reside in the consensus buffer
	evList, evSize := pool.PendingEvidence(defaultEvidenceMaxBytes)
	require.Empty(t, evList)
	require.Zero(t, evSize)

	next := pool.EvidenceFront()
	require.Nil(t, next)

	// move to next height and update state and evidence pool
	state := pool.State()
	state.LastBlockHeight++
	state.LastBlockTime = ev.Time()
	state.LastValidators = types.NewValidatorSet([]*types.Validator{val})
	pool.Update(state, []types.Evidence{})

	// should be able to retrieve evidence from pool
	evList, _ = pool.PendingEvidence(defaultEvidenceMaxBytes)
	require.Equal(t, []types.Evidence{ev}, evList)

	next = pool.EvidenceFront()
	require.NotNil(t, next)
}

func TestEvidencePoolUpdate(t *testing.T) {
	height := int64(21)
	pool, val := defaultTestPool(t, height)
	state := pool.State()

	// create new block (no need to save it to blockStore)
	prunedEv, err := types.NewMockDuplicateVoteEvidenceWithValidator(1, defaultEvidenceTime.Add(1*time.Minute),
		val, evidenceChainID)
	require.NoError(t, err)
	err = pool.AddEvidence(prunedEv)
	require.NoError(t, err)
	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, defaultEvidenceTime.Add(21*time.Minute),
		val, evidenceChainID)
	require.NoError(t, err)
	lastExtCommit := makeExtCommit(height, val.PrivKey.PubKey().Address())
	block := types.MakeBlock(height+1, types.MakeData([]types.Tx{}), lastExtCommit.ToCommit(), []types.Evidence{ev})
	// update state (partially)
	state.LastBlockHeight = height + 1
	state.LastBlockTime = defaultEvidenceTime.Add(22 * time.Minute)
	err = pool.CheckEvidence(types.EvidenceList{ev})
	require.NoError(t, err)

	pool.Update(state, block.Evidence.Evidence)
	// a) Update marks evidence as committed so pending evidence should be empty
	evList, evSize := pool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Empty(t, evList)
	assert.Zero(t, evSize)

	// b) If we try to check this evidence again it should fail because it has already been committed
	err = pool.CheckEvidence(types.EvidenceList{ev})
	if assert.Error(t, err) {
		assert.Equal(t, "evidence was already committed", err.(*types.ErrInvalidEvidence).Reason.Error())
	}
}

func TestVerifyPendingEvidencePasses(t *testing.T) {
	var height int64 = 1
	pool, val := defaultTestPool(t, height)
	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, defaultEvidenceTime.Add(1*time.Minute),
		val, evidenceChainID)
	require.NoError(t, err)
	err = pool.AddEvidence(ev)
	require.NoError(t, err)

	err = pool.CheckEvidence(types.EvidenceList{ev})
	assert.NoError(t, err)
}

func TestVerifyDuplicatedEvidenceFails(t *testing.T) {
	var height int64 = 1
	pool, val := defaultTestPool(t, height)
	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, defaultEvidenceTime.Add(1*time.Minute),
		val, evidenceChainID)
	require.NoError(t, err)
	err = pool.CheckEvidence(types.EvidenceList{ev, ev})
	if assert.Error(t, err) {
		assert.Equal(t, "duplicate evidence", err.(*types.ErrInvalidEvidence).Reason.Error())
	}
}

// check that valid light client evidence is correctly validated and stored in
// evidence pool
func TestLightClientAttackEvidenceLifecycle(t *testing.T) {
	var (
		height       int64 = 100
		commonHeight int64 = 90
	)

	ev, trusted, common := makeLunaticEvidence(t, height, commonHeight,
		10, 5, 5, defaultEvidenceTime, defaultEvidenceTime.Add(1*time.Hour))

	state := sm.State{
		LastBlockTime:   defaultEvidenceTime.Add(2 * time.Hour),
		LastBlockHeight: 110,
		ConsensusParams: *types.DefaultConsensusParams(),
	}
	stateStore := &smmocks.Store{}
	stateStore.On("LoadValidators", height).Return(trusted.ValidatorSet, nil)
	stateStore.On("LoadValidators", commonHeight).Return(common.ValidatorSet, nil)
	stateStore.On("Load").Return(state, nil)
	blockStore := &mocks.BlockStore{}
	blockStore.On("LoadBlockMeta", height).Return(&types.BlockMeta{Header: *trusted.Header})
	blockStore.On("LoadBlockMeta", commonHeight).Return(&types.BlockMeta{Header: *common.Header})
	blockStore.On("LoadBlockCommit", height).Return(trusted.Commit)
	blockStore.On("LoadBlockCommit", commonHeight).Return(common.Commit)

	pool, err := evidence.NewPool(dbm.NewMemDB(), stateStore, blockStore)
	require.NoError(t, err)
	pool.SetLogger(log.TestingLogger())

	err = pool.AddEvidence(ev)
	assert.NoError(t, err)

	hash := ev.Hash()

	require.NoError(t, pool.AddEvidence(ev))
	require.NoError(t, pool.AddEvidence(ev))

	pendingEv, _ := pool.PendingEvidence(state.ConsensusParams.Evidence.MaxBytes)
	require.Equal(t, 1, len(pendingEv))
	require.Equal(t, ev, pendingEv[0])

	require.NoError(t, pool.CheckEvidence(pendingEv))
	require.Equal(t, ev, pendingEv[0])

	state.LastBlockHeight++
	state.LastBlockTime = state.LastBlockTime.Add(1 * time.Minute)
	pool.Update(state, pendingEv)
	require.Equal(t, hash, pendingEv[0].Hash())

	remaindingEv, _ := pool.PendingEvidence(state.ConsensusParams.Evidence.MaxBytes)
	require.Empty(t, remaindingEv)

	// evidence is already committed so it shouldn't pass
	require.Error(t, pool.CheckEvidence(types.EvidenceList{ev}))
	require.NoError(t, pool.AddEvidence(ev))

	remaindingEv, _ = pool.PendingEvidence(state.ConsensusParams.Evidence.MaxBytes)
	require.Empty(t, remaindingEv)
}

// Tests that restarting the evidence pool after a potential failure will recover the
// pending evidence and continue to gossip it
func TestRecoverPendingEvidence(t *testing.T) {
	height := int64(10)
	val := types.NewMockPV()
	valAddress := val.PrivKey.PubKey().Address()
	evidenceDB := dbm.NewMemDB()
	stateStore := initializeValidatorState(val, height)
	state, err := stateStore.Load()
	require.NoError(t, err)
	blockStore, err := initializeBlockStore(dbm.NewMemDB(), state, valAddress)
	require.NoError(t, err)
	// create previous pool and populate it
	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	require.NoError(t, err)
	pool.SetLogger(log.TestingLogger())
	goodEvidence, err := types.NewMockDuplicateVoteEvidenceWithValidator(height,
		defaultEvidenceTime.Add(10*time.Minute), val, evidenceChainID)
	require.NoError(t, err)
	expiredEvidence, err := types.NewMockDuplicateVoteEvidenceWithValidator(int64(1),
		defaultEvidenceTime.Add(1*time.Minute), val, evidenceChainID)
	require.NoError(t, err)
	err = pool.AddEvidence(goodEvidence)
	require.NoError(t, err)
	err = pool.AddEvidence(expiredEvidence)
	require.NoError(t, err)

	// now recover from the previous pool at a different time
	newStateStore := &smmocks.Store{}
	newStateStore.On("Load").Return(sm.State{
		LastBlockTime:   defaultEvidenceTime.Add(25 * time.Minute),
		LastBlockHeight: height + 15,
		ConsensusParams: types.ConsensusParams{
			Block: types.BlockParams{
				MaxBytes: 22020096,
				MaxGas:   -1,
			},
			Evidence: types.EvidenceParams{
				MaxAgeNumBlocks: 20,
				MaxAgeDuration:  20 * time.Minute,
				MaxBytes:        defaultEvidenceMaxBytes,
			},
		},
	}, nil)
	newPool, err := evidence.NewPool(evidenceDB, newStateStore, blockStore)
	assert.NoError(t, err)
	evList, _ := newPool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Equal(t, 1, len(evList))
	next := newPool.EvidenceFront()
	assert.Equal(t, goodEvidence, next.Value.(types.Evidence))
}

func initializeStateFromValidatorSet(valSet *types.ValidatorSet, height int64) sm.Store {
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	state := sm.State{
		ChainID:                     evidenceChainID,
		InitialHeight:               1,
		LastBlockHeight:             height,
		LastBlockTime:               defaultEvidenceTime,
		Validators:                  valSet,
		NextValidators:              valSet.CopyIncrementProposerPriority(1),
		LastValidators:              valSet,
		LastHeightValidatorsChanged: 1,
		ConsensusParams: types.ConsensusParams{
			Block: types.BlockParams{
				MaxBytes: 22020096,
				MaxGas:   -1,
			},
			Evidence: types.EvidenceParams{
				MaxAgeNumBlocks: 20,
				MaxAgeDuration:  20 * time.Minute,
				MaxBytes:        1000,
			},
		},
	}

	// save all states up to height
	for i := int64(0); i <= height; i++ {
		state.LastBlockHeight = i
		if err := stateStore.Save(state); err != nil {
			panic(err)
		}
	}

	return stateStore
}

func initializeValidatorState(privVal types.PrivValidator, height int64) sm.Store {
	pubKey, _ := privVal.GetPubKey()
	validator := &types.Validator{Address: pubKey.Address(), VotingPower: 10, PubKey: pubKey}

	// create validator set and state
	valSet := &types.ValidatorSet{
		Validators: []*types.Validator{validator},
		Proposer:   validator,
	}

	return initializeStateFromValidatorSet(valSet, height)
}

// initializeBlockStore creates a block storage and populates it w/ a dummy
// block at +height+.
func initializeBlockStore(db dbm.DB, state sm.State, valAddr []byte) (*store.BlockStore, error) {
	blockStore := store.NewBlockStore(db)

	state.Version.Consensus = cmtversion.Consensus{Block: version.BlockProtocol, App: 1}

	for i := int64(1); i <= state.LastBlockHeight; i++ {
		lastCommit := makeExtCommit(i-1, valAddr)
		block, err := state.MakeBlockWithoutPartset(i, types.MakeData(test.MakeNTxs(i, 1)), lastCommit.ToCommit(), nil, state.Validators.Proposer.Address)
		if err != nil {
			return nil, err
		}
		block.Header.Time = defaultEvidenceTime.Add(time.Duration(i) * time.Minute) //nolint:staticcheck

		partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
		if err != nil {
			return nil, err
		}

		seenCommit := makeExtCommit(i, valAddr)
		blockStore.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
	}

	return blockStore, nil
}

func makeExtCommit(height int64, valAddr []byte) *types.ExtendedCommit {
	commitSig := types.ExtendedCommitSig{
		CommitSig: types.CommitSig{
			BlockIDFlag:      types.BlockIDFlagCommit,
			ValidatorAddress: valAddr,
			Timestamp:        defaultEvidenceTime,
			Signature:        []byte("Signature"),
		},
		ExtensionSignature: []byte("Extended Signature"),
	}
	ec := &types.ExtendedCommit{
		Height:             height,
		ExtendedSignatures: []types.ExtendedCommitSig{commitSig},
	}
	if height >= 1 {
		ec.BlockID = types.BlockID{
			Hash:          crypto.CRandBytes(32),
			PartSetHeader: types.PartSetHeader{Hash: crypto.CRandBytes(32), Total: 1},
		}
	}
	return ec
}

func defaultTestPool(t *testing.T, height int64) (*evidence.Pool, types.MockPV) {
	t.Helper()
	val := types.NewMockPV()
	valAddress := val.PrivKey.PubKey().Address()
	evidenceDB := dbm.NewMemDB()
	stateStore := initializeValidatorState(val, height)
	state, _ := stateStore.Load()
	blockStore, err := initializeBlockStore(dbm.NewMemDB(), state, valAddress)
	require.NoError(t, err)
	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	if err != nil {
		panic("test evidence pool could not be created")
	}
	pool.SetLogger(log.TestingLogger())
	return pool, val
}

func TestEvidencePoolWithPrunedBlocks(t *testing.T) {
	// Setup: create 50 blocks with a real block store and state store.
	const numBlocks = 50
	val := types.NewMockPV()
	valAddress := val.PrivKey.PubKey().Address()
	stateStore := initializeValidatorState(val, numBlocks)
	state, err := stateStore.Load()
	require.NoError(t, err)

	blockStore, err := initializeBlockStore(dbm.NewMemDB(), state, valAddress)
	require.NoError(t, err)

	// Prune blocks below height 40. The prune state uses a LastBlockTime far
	// enough in the future that evidence for the earliest blocks is expired.
	// With MaxAgeNumBlocks=20, MaxAgeDuration=20m, and LastBlockHeight=50,
	// evidence is expired when BOTH age > 20 blocks AND duration > 20min.
	// Block times are defaultEvidenceTime + h*min, so the evidence retain
	// height will be around 30 (the first non-expired height).
	pruneState := state
	pruneState.LastBlockHeight = numBlocks
	pruneState.LastBlockTime = defaultEvidenceTime.Add(numBlocks * time.Minute)
	_, evidenceRetainHeight, err := blockStore.PruneBlocks(40, pruneState)
	require.NoError(t, err)
	t.Logf("evidenceRetainHeight=%d", evidenceRetainHeight)

	// After pruning:
	//   heights 1..(evidenceRetainHeight-1): fully pruned (no meta, no commit, no block)
	//   heights evidenceRetainHeight..39:    partially pruned (meta+commit kept, block parts deleted)
	//   heights 40..50:                      unpruned

	// Sanity-check the pruning tiers.
	require.Nil(t, blockStore.LoadBlockMeta(evidenceRetainHeight-1), "expected fully pruned")
	require.NotNil(t, blockStore.LoadBlockMeta(evidenceRetainHeight), "expected meta retained for evidence")
	require.Nil(t, blockStore.LoadBlock(evidenceRetainHeight), "expected block parts deleted")
	require.NotNil(t, blockStore.LoadBlockMeta(45), "expected unpruned")
	require.NotNil(t, blockStore.LoadBlock(45), "expected full block available")

	pool, err := evidence.NewPool(dbm.NewMemDB(), stateStore, blockStore)
	require.NoError(t, err)
	pool.SetLogger(log.TestingLogger())

	t.Run("fully pruned block - AddEvidence returns error", func(t *testing.T) {
		h := int64(15)
		require.Nil(t, blockStore.LoadBlockMeta(h), "precondition: block meta must be nil")
		ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(
			h, defaultEvidenceTime.Add(time.Duration(h)*time.Minute), val, evidenceChainID)
		require.NoError(t, err)
		err = pool.AddEvidence(ev)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "don't have header")
	})

	t.Run("fully pruned block - CheckEvidence returns error", func(t *testing.T) {
		h := int64(10)
		require.Nil(t, blockStore.LoadBlockMeta(h), "precondition: block meta must be nil")
		ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(
			h, defaultEvidenceTime.Add(time.Duration(h)*time.Minute), val, evidenceChainID)
		require.NoError(t, err)
		err = pool.CheckEvidence(types.EvidenceList{ev})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "don't have header")
	})

	t.Run("partially pruned block - evidence is verifiable", func(t *testing.T) {
		h := evidenceRetainHeight + 2 // well inside the partially-pruned tier
		require.NotNil(t, blockStore.LoadBlockMeta(h), "precondition: block meta must exist")
		ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(
			h, defaultEvidenceTime.Add(time.Duration(h)*time.Minute), val, evidenceChainID)
		require.NoError(t, err)
		require.NoError(t, pool.AddEvidence(ev))
		require.NoError(t, pool.CheckEvidence(types.EvidenceList{ev}))
	})

	t.Run("unpruned block - evidence is verifiable", func(t *testing.T) {
		h := int64(45)
		require.NotNil(t, blockStore.LoadBlockMeta(h), "precondition: block meta must exist")
		ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(
			h, defaultEvidenceTime.Add(time.Duration(h)*time.Minute), val, evidenceChainID)
		require.NoError(t, err)
		require.NoError(t, pool.AddEvidence(ev))
	})
}

func createState(height int64, valSet *types.ValidatorSet) sm.State {
	return sm.State{
		ChainID:         evidenceChainID,
		LastBlockHeight: height,
		LastBlockTime:   defaultEvidenceTime,
		Validators:      valSet,
		ConsensusParams: *types.DefaultConsensusParams(),
	}
}

// Duplicate vote evidence for a validator at a height should only be admitted
// to the pool once, whatever its round, vote type or encoding.
func TestAddEvidenceIgnoresDuplicateVoteVariants(t *testing.T) {
	height := int64(10)
	pool, val := defaultTestPool(t, height)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)

	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(ev))

	const numVariants = 20
	var wg sync.WaitGroup
	for i := 0; i < numVariants; i++ {
		// A freshly signed equivocation with different block IDs.
		resigned, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
		require.NoError(t, err)
		// The same evidence with fields not covered by the vote signatures changed.
		mutated := *ev
		voteA := *ev.VoteA
		voteA.ValidatorIndex = int32(i + 1)
		voteA.Extension = []byte{byte(i)}
		voteA.ExtensionSignature = []byte{byte(i)}
		mutated.VoteA = &voteA

		for _, variant := range []types.Evidence{resigned, &mutated} {
			wg.Add(1)
			go func(variant types.Evidence) {
				defer wg.Done()
				assert.NoError(t, pool.AddEvidence(variant))
			}(variant)
		}
	}
	// Freshly signed equivocations at other rounds of the same height.
	for round := int32(1); round <= numVariants; round++ {
		wg.Add(1)
		go func(variant types.Evidence) {
			defer wg.Done()
			assert.NoError(t, pool.AddEvidence(variant))
		}(newDuplicateVoteAtRound(t, height, round, evTime, val))
	}
	wg.Wait()

	evs, _ := pool.PendingEvidence(defaultEvidenceMaxBytes)
	require.Len(t, evs, 1)
	assert.Equal(t, ev.Hash(), evs[0].Hash())
	assert.EqualValues(t, 1, pool.Size())

	// Evidence for a different height is still admitted.
	other, err := types.NewMockDuplicateVoteEvidenceWithValidator(height-1, evTime.Add(-time.Minute), val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(other))
	evs, _ = pool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Len(t, evs, 2)
}

// Invalid duplicate vote evidence is rejected as invalid even when evidence for
// the same validator and height is already pending or committed, so that peers
// sending it are disconnected as they were before deduplication.
func TestAddEvidenceRejectsInvalidVariantOfKnownDuplicateVote(t *testing.T) {
	height := int64(10)
	pool, val := defaultTestPool(t, height)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)

	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(ev))

	badSignature := *ev
	voteB := *ev.VoteB
	voteB.Signature = make([]byte, len(voteB.Signature))
	badSignature.VoteB = &voteB

	otherRound := newDuplicateVoteAtRound(t, height, 1, evTime, val)
	mismatchedRound := *ev
	mismatchedRound.VoteB = otherRound.VoteB

	requireInvalid := func() {
		t.Helper()
		for _, invalid := range []*types.DuplicateVoteEvidence{&badSignature, &mismatchedRound} {
			var errInvalid *types.ErrInvalidEvidence
			require.ErrorAs(t, pool.AddEvidence(invalid), &errInvalid)
		}
	}
	requireInvalid()

	// Also after the evidence is committed.
	state := pool.State()
	state.LastBlockHeight++
	state.LastBlockTime = state.LastBlockTime.Add(time.Minute)
	pool.Update(state, types.EvidenceList{ev})
	require.Zero(t, pool.Size())
	requireInvalid()
}

// Duplicate vote evidence whose unsigned fields are not in canonical form is
// admitted without error, and the pool keeps a canonical copy of it. An error
// would make the reactor disconnect honest peers that relay such evidence.
func TestAddEvidenceStoresCanonicalDuplicateVote(t *testing.T) {
	height := int64(10)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)

	testCases := []struct {
		name     string
		malleate func(ev *types.DuplicateVoteEvidence)
	}{
		{"wrong validator index on vote A", func(ev *types.DuplicateVoteEvidence) { ev.VoteA.ValidatorIndex = 1 }},
		{"wrong validator index on vote B", func(ev *types.DuplicateVoteEvidence) { ev.VoteB.ValidatorIndex = 7 }},
		{"unverified vote extension", func(ev *types.DuplicateVoteEvidence) {
			ev.VoteB.Extension = []byte("extension")
			ev.VoteB.ExtensionSignature = []byte("signature")
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pool, val := defaultTestPool(t, height)
			ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
			require.NoError(t, err)
			canonicalHash := ev.Hash()
			tc.malleate(ev)
			require.NoError(t, ev.ValidateBasic())
			malleatedHash := ev.Hash()
			require.NotEqual(t, canonicalHash, malleatedHash)

			require.NoError(t, pool.AddEvidence(ev))
			assert.Equal(t, malleatedHash, ev.Hash(), "AddEvidence must not modify its argument")

			evs, _ := pool.PendingEvidence(defaultEvidenceMaxBytes)
			require.Len(t, evs, 1)
			assert.Equal(t, canonicalHash, evs[0].Hash())
			require.NotNil(t, pool.EvidenceFront())
			assert.Equal(t, canonicalHash, pool.EvidenceFront().Value.(types.Evidence).Hash())

			// Evidence in a block is validated as before.
			require.NoError(t, pool.CheckEvidence(types.EvidenceList{ev}))
		})
	}
}

// Once duplicate vote evidence for a validator at a height is committed,
// pending and future evidence for that validator and height is dropped, at
// any round and under any hash.
func TestCommittedDuplicateVoteVariantIsDropped(t *testing.T) {
	height := int64(21)
	pool, val := defaultTestPool(t, height)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)

	pending, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(pending))

	committed, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NotEqual(t, pending.Hash(), committed.Hash())
	require.NoError(t, pool.CheckEvidence(types.EvidenceList{committed}))

	state := pool.State()
	state.LastBlockHeight = height + 1
	state.LastBlockTime = evTime.Add(time.Minute)
	pool.Update(state, types.EvidenceList{committed})

	evs, _ := pool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Empty(t, evs)
	assert.Zero(t, pool.Size())
	assert.Nil(t, pool.EvidenceFront())

	require.NoError(t, pool.AddEvidence(pending))
	for round := int32(1); round <= 10; round++ {
		require.NoError(t, pool.AddEvidence(newDuplicateVoteAtRound(t, height, round, evTime, val)))
	}
	evs, _ = pool.PendingEvidence(defaultEvidenceMaxBytes)
	assert.Empty(t, evs)
	assert.Zero(t, pool.Size())
}

// A pool restored from a store that holds several pieces of duplicate vote
// evidence for one equivocation keeps only one of them.
func TestNewPoolDeduplicatesRestoredDuplicateVotes(t *testing.T) {
	height := int64(10)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)
	val := types.NewMockPV()
	evidenceDB := dbm.NewMemDB()
	stateStore := initializeValidatorState(val, height)
	state, err := stateStore.Load()
	require.NoError(t, err)
	blockStore, err := initializeBlockStore(dbm.NewMemDB(), state, val.PrivKey.PubKey().Address())
	require.NoError(t, err)

	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
		require.NoError(t, err)
		require.NoError(t, pool.AddPendingEvidenceUnchecked(ev))
	}
	other, err := types.NewMockDuplicateVoteEvidenceWithValidator(height-1, evTime.Add(-time.Minute), val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddPendingEvidenceUnchecked(other))

	for restart := 0; restart < 2; restart++ {
		pool, err = evidence.NewPool(evidenceDB, stateStore, blockStore)
		require.NoError(t, err)
		evs, _ := pool.PendingEvidence(-1)
		assert.Len(t, evs, 2)
		assert.EqualValues(t, 2, pool.Size())
		listed := 0
		for e := pool.EvidenceFront(); e != nil; e = e.Next() {
			listed++
		}
		assert.Equal(t, 2, listed)
	}

	variant, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(variant))
	assert.EqualValues(t, 2, pool.Size())
}

// Duplicate vote evidence admitted while the same equivocation is being
// committed must not stay pending.
func TestCommitDuplicateVoteConcurrentWithAdmission(t *testing.T) {
	height := int64(21)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)
	for i := 0; i < 50; i++ {
		pool, val := defaultTestPool(t, height)
		pool.SetLogger(log.NewNopLogger())
		pending, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
		require.NoError(t, err)
		require.NoError(t, pool.AddEvidence(pending))
		committed, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
		require.NoError(t, err)
		variants := make([]types.Evidence, 50)
		for j := range variants {
			variants[j], err = types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
			require.NoError(t, err)
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start
				for j := w; j < len(variants); j += 4 {
					assert.NoError(t, pool.AddEvidence(variants[j]))
				}
			}(w)
		}
		state := pool.State()
		state.LastBlockHeight = height + 1
		state.LastBlockTime = evTime.Add(time.Minute)
		close(start)
		pool.Update(state, types.EvidenceList{committed})
		wg.Wait()

		evs, _ := pool.PendingEvidence(-1)
		require.Empty(t, evs, "iteration %d", i)
		require.Zero(t, pool.Size(), "iteration %d", i)
	}
}

// Pending duplicate vote evidence for a validator and height that is already
// committed is dropped when the pool restarts. This covers a crash between
// writing the commit marker and deleting the pending entry.
func TestNewPoolDropsCommittedDuplicateVotes(t *testing.T) {
	height := int64(21)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)
	val := types.NewMockPV()
	evidenceDB := dbm.NewMemDB()
	stateStore := initializeValidatorState(val, height)
	state, err := stateStore.Load()
	require.NoError(t, err)
	blockStore, err := initializeBlockStore(dbm.NewMemDB(), state, val.PrivKey.PubKey().Address())
	require.NoError(t, err)

	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	require.NoError(t, err)
	committed, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(committed))
	state = pool.State()
	state.LastBlockHeight = height + 1
	state.LastBlockTime = evTime.Add(time.Minute)
	pool.Update(state, types.EvidenceList{committed})
	require.Zero(t, pool.Size())

	// Leftover pending entries: the committed evidence itself, and evidence
	// for the same validator and height at another round.
	require.NoError(t, pool.AddPendingEvidenceUnchecked(committed))
	require.NoError(t, pool.AddPendingEvidenceUnchecked(newDuplicateVoteAtRound(t, height, 3, evTime, val)))
	stillPending, err := types.NewMockDuplicateVoteEvidenceWithValidator(height-1, evTime.Add(-time.Minute), val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddPendingEvidenceUnchecked(stillPending))

	for restart := 0; restart < 2; restart++ {
		pool, err = evidence.NewPool(evidenceDB, stateStore, blockStore)
		require.NoError(t, err)
		evs, _ := pool.PendingEvidence(-1)
		require.Len(t, evs, 1)
		assert.Equal(t, stillPending.Hash(), evs[0].Hash())
		assert.EqualValues(t, 1, pool.Size())
	}
}

// Commit markers for duplicate vote evidence are pruned once evidence at that
// height has expired.
func TestCommittedDuplicateVoteMarkerIsPruned(t *testing.T) {
	height := int64(21)
	pool, val := defaultTestPool(t, height)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)

	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(ev))
	state := pool.State()
	state.LastBlockHeight = height + 1
	state.LastBlockTime = evTime.Add(time.Minute)
	pool.Update(state, types.EvidenceList{ev})
	require.True(t, pool.HasCommittedDuplicateVote(ev))

	params := state.ConsensusParams.Evidence
	state.LastBlockHeight = height + params.MaxAgeNumBlocks + 1
	state.LastBlockTime = evTime.Add(params.MaxAgeDuration).Add(time.Minute)
	pool.Update(state, nil)
	assert.False(t, pool.HasCommittedDuplicateVote(ev))
}

// newDuplicateVoteAtRound returns duplicate vote evidence of precommits signed
// by pv at the given height and round.
func newDuplicateVoteAtRound(
	t *testing.T, height int64, round int32, evTime time.Time, pv types.MockPV,
) *types.DuplicateVoteEvidence {
	t.Helper()
	return newDuplicateVote(t, height, round, cmtproto.PrecommitType, evTime, pv)
}

// newDuplicateVote returns duplicate vote evidence of votes of the given type
// signed by pv at the given height and round.
func newDuplicateVote(
	t *testing.T, height int64, round int32, voteType cmtproto.SignedMsgType, evTime time.Time, pv types.MockPV,
) *types.DuplicateVoteEvidence {
	t.Helper()
	pubKey := pv.PrivKey.PubKey()
	valSet := types.NewValidatorSet([]*types.Validator{types.NewValidator(pubKey, 10)})
	makeVote := func() *types.Vote {
		vote := &types.Vote{
			Type:   voteType,
			Height: height,
			Round:  round,
			BlockID: types.BlockID{
				Hash:          crypto.CRandBytes(32),
				PartSetHeader: types.PartSetHeader{Total: 1, Hash: crypto.CRandBytes(32)},
			},
			Timestamp:        evTime,
			ValidatorAddress: pubKey.Address(),
			ValidatorIndex:   0,
		}
		pb := vote.ToProto()
		require.NoError(t, pv.SignVote(evidenceChainID, pb))
		vote.Signature = pb.Signature
		return vote
	}
	ev, err := types.NewDuplicateVoteEvidence(makeVote(), makeVote(), evTime, valSet)
	require.NoError(t, err)
	return ev
}

// Evidence committed before commit markers existed must not become pending
// again when a variant of it is submitted whose canonical form is the
// committed evidence.
func TestNonCanonicalVariantOfCommittedEvidenceIsNotPending(t *testing.T) {
	height := int64(10)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)
	pool, val := defaultTestPool(t, height)
	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.MarkCommittedWithoutDuplicateVoteMarker(ev))

	mutated := *ev
	voteA := *ev.VoteA
	voteA.ValidatorIndex = 5
	mutated.VoteA = &voteA
	require.NotEqual(t, ev.Hash(), mutated.Hash())

	require.NoError(t, pool.AddEvidence(&mutated))
	assert.Zero(t, pool.Size())

	require.NoError(t, pool.CheckEvidence(types.EvidenceList{&mutated}))
	assert.Zero(t, pool.Size())
}

// A prevote equivocation and a precommit equivocation by the same validator at
// the same height are one offense: one piece of evidence is enough to punish
// the validator, so only one is admitted.
func TestDuplicateVoteTypesShareOneEntryPerValidatorAndHeight(t *testing.T) {
	height := int64(21)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)
	pool, val := defaultTestPool(t, height)

	prevotes := newDuplicateVote(t, height, 0, cmtproto.PrevoteType, evTime, val)
	precommits := newDuplicateVote(t, height, 0, cmtproto.PrecommitType, evTime, val)
	require.NoError(t, pool.AddEvidence(prevotes))
	require.NoError(t, pool.AddEvidence(precommits))
	evs, _ := pool.PendingEvidence(-1)
	require.Len(t, evs, 1)
	assert.Equal(t, prevotes.Hash(), evs[0].Hash())
	assert.EqualValues(t, 1, pool.Size())

	// Once the precommit equivocation is committed, the pending prevote
	// equivocation is dropped and new prevote equivocations are ignored.
	require.NoError(t, pool.CheckEvidence(types.EvidenceList{precommits}))
	state := pool.State()
	state.LastBlockHeight = height + 1
	state.LastBlockTime = evTime.Add(time.Minute)
	pool.Update(state, types.EvidenceList{precommits})
	assert.Zero(t, pool.Size())
	require.NoError(t, pool.AddEvidence(newDuplicateVote(t, height, 1, cmtproto.PrevoteType, evTime, val)))
	evs, _ = pool.PendingEvidence(-1)
	assert.Empty(t, evs)
	assert.Zero(t, pool.Size())
}

// Conflicting votes reported by consensus are deduplicated per validator and
// height, both against each other and against evidence admitted from peers.
func TestReportConflictingVotesDeduplicatesDuplicateVotes(t *testing.T) {
	height := int64(10)
	evHeight := int64(5)
	// The time of the block at evHeight, see initializeBlockStore.
	evTime := defaultEvidenceTime.Add(time.Duration(evHeight) * time.Minute)
	pool, val := defaultTestPool(t, height)

	gossiped := newDuplicateVoteAtRound(t, evHeight, 0, evTime, val)
	require.NoError(t, pool.AddEvidence(gossiped))
	require.EqualValues(t, 1, pool.Size())

	for round := int32(1); round <= 2; round++ {
		reported := newDuplicateVoteAtRound(t, evHeight, round, evTime, val)
		pool.ReportConflictingVotes(reported.VoteA, reported.VoteB)
	}
	// Another validator and height is still admitted from consensus.
	otherHeight := evHeight - 1
	other := newDuplicateVoteAtRound(t, otherHeight, 0,
		defaultEvidenceTime.Add(time.Duration(otherHeight)*time.Minute), val)
	pool.ReportConflictingVotes(other.VoteA, other.VoteB)
	// Two reports for one validator and height in the same flush.
	for round := int32(1); round <= 2; round++ {
		reported := newDuplicateVoteAtRound(t, otherHeight, round,
			defaultEvidenceTime.Add(time.Duration(otherHeight)*time.Minute), val)
		pool.ReportConflictingVotes(reported.VoteA, reported.VoteB)
	}

	state := pool.State()
	state.LastBlockHeight++
	state.LastBlockTime = state.LastBlockTime.Add(time.Minute)
	pool.Update(state, nil)

	evs, _ := pool.PendingEvidence(-1)
	require.Len(t, evs, 2)
	assert.EqualValues(t, 2, pool.Size())
	hashes := map[string]bool{string(evs[0].Hash()): true, string(evs[1].Hash()): true}
	assert.True(t, hashes[string(gossiped.Hash())])
	assert.True(t, hashes[string(other.Hash())])
	listed := 0
	for e := pool.EvidenceFront(); e != nil; e = e.Next() {
		listed++
	}
	assert.Equal(t, 2, listed)
}

// Pending evidence that is committed under its own hash, but has no duplicate
// vote commit marker, is dropped when the pool restarts. Released versions
// record committed evidence only by hash, and evidence other than duplicate
// vote evidence never has a marker.
func TestNewPoolDropsEvidenceCommittedByHash(t *testing.T) {
	var (
		height       int64 = 100
		commonHeight int64 = 90
	)
	lunatic, trusted, common := makeLunaticEvidence(t, height, commonHeight,
		10, 5, 5, defaultEvidenceTime, defaultEvidenceTime.Add(1*time.Hour))
	state := sm.State{
		LastBlockTime:   defaultEvidenceTime.Add(2 * time.Hour),
		LastBlockHeight: 110,
		ConsensusParams: *types.DefaultConsensusParams(),
	}
	stateStore := &smmocks.Store{}
	stateStore.On("LoadValidators", height).Return(trusted.ValidatorSet, nil)
	stateStore.On("LoadValidators", commonHeight).Return(common.ValidatorSet, nil)
	stateStore.On("Load").Return(state, nil)
	blockStore := &mocks.BlockStore{}

	evidenceDB := dbm.NewMemDB()
	pool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
	require.NoError(t, err)

	val := types.NewMockPV()
	committedDVE := newDuplicateVoteAtRound(t, height, 0, defaultEvidenceTime, val)
	stillPending := newDuplicateVoteAtRound(t, height-1, 0, defaultEvidenceTime, val)
	for _, ev := range []types.Evidence{lunatic, committedDVE, stillPending} {
		require.NoError(t, pool.AddPendingEvidenceUnchecked(ev))
	}
	for _, ev := range []types.Evidence{lunatic, committedDVE} {
		require.NoError(t, pool.MarkCommittedWithoutDuplicateVoteMarker(ev))
	}
	require.False(t, pool.HasCommittedDuplicateVote(committedDVE))

	for restart := 0; restart < 2; restart++ {
		pool, err = evidence.NewPool(evidenceDB, stateStore, blockStore)
		require.NoError(t, err)
		evs, _ := pool.PendingEvidence(-1)
		require.Len(t, evs, 1)
		assert.Equal(t, stillPending.Hash(), evs[0].Hash())
		assert.EqualValues(t, 1, pool.Size())
		require.NotNil(t, pool.EvidenceFront())
		assert.Nil(t, pool.EvidenceFront().Next())
	}
}

// When pending duplicate vote evidence expires, its entry in the in-memory
// index of pending duplicate votes is removed too, so the index does not grow
// with every validator and height that ever had pending evidence.
func TestExpiredDuplicateVoteIsRemovedFromIndex(t *testing.T) {
	height := int64(21)
	pool, val := defaultTestPool(t, height)
	evTime := defaultEvidenceTime.Add(time.Duration(height) * time.Minute)

	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, evTime, val, evidenceChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(ev))
	require.True(t, pool.HasPendingDuplicateVoteIndexEntry(ev))

	state := pool.State()
	params := state.ConsensusParams.Evidence
	state.LastBlockHeight = height + params.MaxAgeNumBlocks + 1
	state.LastBlockTime = evTime.Add(params.MaxAgeDuration).Add(time.Minute)
	pool.Update(state, nil)
	require.Zero(t, pool.Size())
	assert.False(t, pool.HasPendingDuplicateVoteIndexEntry(ev))
}
