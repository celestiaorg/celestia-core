package state_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/log"
	mpmocks "github.com/cometbft/cometbft/mempool/mocks"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/state/mocks"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

func TestRollback(t *testing.T) {
	var (
		height     int64 = 100
		nextHeight int64 = 101
	)
	blockStore := &mocks.BlockStore{}
	stateStore := setupStateStore(t, height)
	initialState, err := stateStore.Load()
	require.NoError(t, err)

	// perform the rollback over a version bump
	newParams := types.DefaultConsensusParams()
	newParams.Version.App = 11
	newParams.Block.MaxBytes = 1000
	nextState := initialState.Copy()
	nextState.LastBlockHeight = nextHeight
	nextState.Version.Consensus.App = 11
	nextState.LastBlockID = makeBlockIDRandom()
	nextState.AppHash = tmhash.Sum([]byte("app_hash"))
	nextState.LastValidators = initialState.Validators
	nextState.Validators = initialState.NextValidators
	nextState.NextValidators = initialState.NextValidators.CopyIncrementProposerPriority(1)
	nextState.ConsensusParams = *newParams
	nextState.LastHeightConsensusParamsChanged = nextHeight + 1
	nextState.LastHeightValidatorsChanged = nextHeight + 1

	// update the state
	require.NoError(t, stateStore.Save(nextState))

	block := &types.BlockMeta{
		BlockID: initialState.LastBlockID,
		Header: types.Header{
			Height:          initialState.LastBlockHeight,
			Time:            initialState.LastBlockTime,
			AppHash:         crypto.CRandBytes(tmhash.Size),
			LastBlockID:     makeBlockIDRandom(),
			LastResultsHash: initialState.LastResultsHash,
		},
	}
	nextBlock := &types.BlockMeta{
		BlockID: initialState.LastBlockID,
		Header: types.Header{
			Height:          nextState.LastBlockHeight,
			AppHash:         initialState.AppHash,
			LastBlockID:     block.BlockID,
			Time:            nextState.LastBlockTime,
			LastResultsHash: nextState.LastResultsHash,
		},
	}
	blockStore.On("LoadBlockMeta", height).Return(block)
	blockStore.On("LoadBlockMeta", nextHeight).Return(nextBlock)
	blockStore.On("Height").Return(nextHeight)

	// rollback the state
	rollbackHeight, rollbackHash, err := state.Rollback(blockStore, stateStore, false)
	require.NoError(t, err)
	require.EqualValues(t, height, rollbackHeight)
	require.EqualValues(t, initialState.AppHash, rollbackHash)
	blockStore.AssertExpectations(t)

	// assert that we've recovered the prior state
	loadedState, err := stateStore.Load()
	require.NoError(t, err)
	require.EqualValues(t, initialState, loadedState)
}

func TestRollbackHard(t *testing.T) {
	const height int64 = 100
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	stateStore := state.NewStore(dbm.NewMemDB(), state.StoreOptions{DiscardABCIResponses: false})

	valSet, _ := types.RandValidatorSet(5, 10)

	params := types.DefaultConsensusParams()
	params.Version.App = 10
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	block := &types.Block{
		Header: types.Header{
			Version:            cmtversion.Consensus{Block: version.BlockProtocol, App: 1},
			ChainID:            "test-chain",
			Time:               now,
			Height:             height,
			AppHash:            crypto.CRandBytes(tmhash.Size),
			LastBlockID:        makeBlockIDRandom(),
			LastCommitHash:     crypto.CRandBytes(tmhash.Size),
			DataHash:           crypto.CRandBytes(tmhash.Size),
			ValidatorsHash:     valSet.Hash(),
			NextValidatorsHash: valSet.CopyIncrementProposerPriority(1).Hash(),
			ConsensusHash:      params.Hash(),
			LastResultsHash:    crypto.CRandBytes(tmhash.Size),
			EvidenceHash:       crypto.CRandBytes(tmhash.Size),
			ProposerAddress:    crypto.CRandBytes(crypto.AddressSize),
		},
		LastCommit: &types.Commit{Height: height - 1},
	}

	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	blockStore.SaveBlock(block, partSet, &types.Commit{Height: block.Height})

	currState := state.State{
		Version: cmtstate.Version{
			Consensus: block.Header.Version, //nolint:staticcheck
			Software:  version.TMCoreSemVer,
		},
		LastBlockHeight:                  block.Height,
		LastBlockTime:                    block.Time,
		AppHash:                          crypto.CRandBytes(tmhash.Size),
		LastValidators:                   valSet,
		Validators:                       valSet.CopyIncrementProposerPriority(1),
		NextValidators:                   valSet.CopyIncrementProposerPriority(2),
		ConsensusParams:                  *params,
		LastHeightConsensusParamsChanged: height + 1,
		LastHeightValidatorsChanged:      height + 1,
		LastResultsHash:                  crypto.CRandBytes(tmhash.Size),
	}
	require.NoError(t, stateStore.Bootstrap(currState))

	nextBlock := &types.Block{
		Header: types.Header{
			Version:            cmtversion.Consensus{Block: version.BlockProtocol, App: 1},
			ChainID:            block.ChainID,
			Time:               block.Time,
			Height:             currState.LastBlockHeight + 1,
			AppHash:            currState.AppHash,
			LastBlockID:        types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()},
			LastCommitHash:     crypto.CRandBytes(tmhash.Size),
			DataHash:           crypto.CRandBytes(tmhash.Size),
			ValidatorsHash:     valSet.CopyIncrementProposerPriority(1).Hash(),
			NextValidatorsHash: valSet.CopyIncrementProposerPriority(2).Hash(),
			ConsensusHash:      params.Hash(),
			LastResultsHash:    currState.LastResultsHash,
			EvidenceHash:       crypto.CRandBytes(tmhash.Size),
			ProposerAddress:    crypto.CRandBytes(crypto.AddressSize),
		},
		LastCommit: &types.Commit{Height: currState.LastBlockHeight},
	}

	nextPartSet, err := nextBlock.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	blockStore.SaveBlock(nextBlock, nextPartSet, &types.Commit{Height: nextBlock.Height})

	rollbackHeight, rollbackHash, err := state.Rollback(blockStore, stateStore, true)
	require.NoError(t, err)
	require.Equal(t, rollbackHeight, currState.LastBlockHeight)
	require.Equal(t, rollbackHash, currState.AppHash)

	// state should not have been changed
	loadedState, err := stateStore.Load()
	require.NoError(t, err)
	require.Equal(t, currState, loadedState)

	// resave the same block
	blockStore.SaveBlock(nextBlock, nextPartSet, &types.Commit{Height: nextBlock.Height})

	params.Version.App = 11

	nextState := state.State{
		Version: cmtstate.Version{
			Consensus: block.Header.Version, //nolint:staticcheck
			Software:  version.TMCoreSemVer,
		},
		LastBlockHeight:                  nextBlock.Height,
		LastBlockTime:                    nextBlock.Time,
		AppHash:                          crypto.CRandBytes(tmhash.Size),
		LastValidators:                   valSet.CopyIncrementProposerPriority(1),
		Validators:                       valSet.CopyIncrementProposerPriority(2),
		NextValidators:                   valSet.CopyIncrementProposerPriority(3),
		ConsensusParams:                  *params,
		LastHeightConsensusParamsChanged: nextBlock.Height + 1,
		LastHeightValidatorsChanged:      nextBlock.Height + 1,
		LastResultsHash:                  crypto.CRandBytes(tmhash.Size),
	}
	require.NoError(t, stateStore.Save(nextState))

	rollbackHeight, rollbackHash, err = state.Rollback(blockStore, stateStore, true)
	require.NoError(t, err)
	require.Equal(t, rollbackHeight, currState.LastBlockHeight)
	require.Equal(t, rollbackHash, currState.AppHash)
}

func TestRollbackNoState(t *testing.T) {
	stateStore := state.NewStore(dbm.NewMemDB(),
		state.StoreOptions{
			DiscardABCIResponses: false,
		})
	blockStore := &mocks.BlockStore{}

	_, _, err := state.Rollback(blockStore, stateStore, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no state found")
}

func TestRollbackNoBlocks(t *testing.T) {
	const height = int64(100)
	stateStore := setupStateStore(t, height)
	blockStore := &mocks.BlockStore{}
	blockStore.On("Height").Return(height)
	blockStore.On("LoadBlockMeta", height).Return(nil)
	blockStore.On("LoadBlockMeta", height-1).Return(nil)

	_, _, err := state.Rollback(blockStore, stateStore, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "block at height 99 not found")
}

func TestRollbackDifferentStateHeight(t *testing.T) {
	const height = int64(100)
	stateStore := setupStateStore(t, height)
	blockStore := &mocks.BlockStore{}
	blockStore.On("Height").Return(height + 2)

	_, _, err := state.Rollback(blockStore, stateStore, false)
	require.Error(t, err)
	require.Equal(t, err.Error(), "statestore height (100) is not one below or equal to blockstore height (102)")
}

func setupStateStore(t *testing.T, height int64) state.Store {
	stateStore := state.NewStore(dbm.NewMemDB(), state.StoreOptions{DiscardABCIResponses: false})
	valSet, _ := types.RandValidatorSet(5, 10)

	params := types.DefaultConsensusParams()
	params.Version.App = 10

	initialState := state.State{
		Version: cmtstate.Version{
			Consensus: cmtversion.Consensus{
				Block: version.BlockProtocol,
				App:   10,
			},
			Software: version.TMCoreSemVer,
		},
		ChainID:                          "test-chain",
		InitialHeight:                    10,
		LastBlockID:                      makeBlockIDRandom(),
		AppHash:                          tmhash.Sum([]byte("app_hash")),
		LastResultsHash:                  tmhash.Sum([]byte("last_results_hash")),
		LastBlockHeight:                  height,
		LastBlockTime:                    time.Now(),
		LastValidators:                   valSet,
		Validators:                       valSet.CopyIncrementProposerPriority(1),
		NextValidators:                   valSet.CopyIncrementProposerPriority(2),
		LastHeightValidatorsChanged:      height + 1 + 1,
		ConsensusParams:                  *params,
		LastHeightConsensusParamsChanged: height + 1,
	}
	require.NoError(t, stateStore.Bootstrap(initialState))
	return stateStore
}

func makeBlockIDRandom() types.BlockID {
	var (
		blockHash   = make([]byte, tmhash.Size)
		partSetHash = make([]byte, tmhash.Size)
	)
	rand.Read(blockHash)   //nolint: errcheck // ignore errcheck for read
	rand.Read(partSetHash) //nolint: errcheck // ignore errcheck for read
	return types.BlockID{
		Hash: blockHash,
		PartSetHeader: types.PartSetHeader{
			Total: 123,
			Hash:  partSetHash,
		},
	}
}

func TestRollbackToHeightMatchesHistory(t *testing.T) {
	const (
		n           = 12
		valChangeAt = 5
	)
	for _, target := range []int64{11, 9, 3} {
		t.Run(fmt.Sprintf("target %d", target), func(t *testing.T) {
			bs, ss, states := makeRollbackChain(t, n, valChangeAt)
			if target < valChangeAt {
				require.NotEqual(t, states[target].Validators.Hash(), states[n].Validators.Hash())
			}

			height, appHash, err := state.RollbackToHeight(bs, ss, target, false)
			require.NoError(t, err)
			require.Equal(t, target, height)
			require.Equal(t, states[target].AppHash, appHash)
			requireRolledBackState(t, states[target], loadState(t, ss))
			require.Equal(t, target+1, bs.Height())
		})
	}
}

func TestRollbackToHeightHard(t *testing.T) {
	const n = 12
	bs, ss, states := makeRollbackChain(t, n, 5)

	height, appHash, err := state.RollbackToHeight(bs, ss, n-1, true)
	require.NoError(t, err)
	require.Equal(t, int64(n-1), height)
	require.Equal(t, states[n-1].AppHash, appHash)
	require.Equal(t, int64(n-1), bs.Height())
	requireRolledBackState(t, states[n-1], loadState(t, ss))
}

func TestRollbackToHeightRefusesHardMultiHeight(t *testing.T) {
	const n = 12
	bs, ss, _ := makeRollbackChain(t, n, 5)
	before := loadState(t, ss).Bytes()

	_, _, err := state.RollbackToHeight(bs, ss, n-2, true)
	require.ErrorContains(t, err, "at most one height")
	require.Equal(t, int64(n), bs.Height())
	require.Equal(t, before, loadState(t, ss).Bytes())
}

func TestRollbackToHeightBlockstoreAhead(t *testing.T) {
	const n = 12
	newChain := func(t *testing.T) (*store.BlockStore, state.Store, map[int64]state.State) {
		bs, ss, states := makeRollbackChain(t, n, 5)
		saveUnexecutedBlock(t, bs, states[n])
		return bs, ss, states
	}

	t.Run("roll back executed state", func(t *testing.T) {
		bs, ss, states := newChain(t)
		_, _, err := state.RollbackToHeight(bs, ss, n-1, false)
		require.NoError(t, err)
		require.Equal(t, int64(n), bs.Height())
		requireRolledBackState(t, states[n-1], loadState(t, ss))
	})

	t.Run("keep unexecuted block", func(t *testing.T) {
		bs, ss, _ := newChain(t)
		before := loadState(t, ss).Bytes()
		height, _, err := state.RollbackToHeight(bs, ss, n, false)
		require.NoError(t, err)
		require.Equal(t, int64(n), height)
		require.Equal(t, int64(n+1), bs.Height())
		require.Equal(t, before, loadState(t, ss).Bytes())
	})

	t.Run("remove unexecuted block", func(t *testing.T) {
		bs, ss, _ := newChain(t)
		before := loadState(t, ss).Bytes()
		height, _, err := state.RollbackToHeight(bs, ss, n, true)
		require.NoError(t, err)
		require.Equal(t, int64(n), height)
		require.Equal(t, int64(n), bs.Height())
		require.Equal(t, before, loadState(t, ss).Bytes())
	})
}

func TestRollbackToHeightResumes(t *testing.T) {
	const n = 12

	t.Run("between steps", func(t *testing.T) {
		bs, ss, states := makeRollbackChain(t, n, 5)

		// Interrupted run: two heights already rolled back.
		for i := 0; i < 2; i++ {
			_, _, err := state.Rollback(bs, ss, true)
			require.NoError(t, err)
		}

		// The second call is a no-op.
		for i := 0; i < 2; i++ {
			height, appHash, err := state.RollbackToHeight(bs, ss, n-5, false)
			require.NoError(t, err)
			require.Equal(t, int64(n-5), height)
			require.Equal(t, states[n-5].AppHash, appHash)
			requireRolledBackState(t, states[n-5], loadState(t, ss))
			require.Equal(t, int64(n-4), bs.Height())
		}
	})

	t.Run("inside a step", func(t *testing.T) {
		bs, ss, states := makeRollbackChain(t, n, 5)

		// Crash after saving state, before deleting the top block.
		_, _, err := state.Rollback(bs, ss, true)
		require.NoError(t, err)
		_, _, err = state.Rollback(bs, ss, false)
		require.NoError(t, err)
		require.Equal(t, loadState(t, ss).LastBlockHeight+1, bs.Height())

		height, appHash, err := state.RollbackToHeight(bs, ss, n-5, false)
		require.NoError(t, err)
		require.Equal(t, int64(n-5), height)
		require.Equal(t, states[n-5].AppHash, appHash)
		requireRolledBackState(t, states[n-5], loadState(t, ss))
		require.Equal(t, int64(n-4), bs.Height())
	})
}

func TestRollbackToHeightBlockstoreTooFarAhead(t *testing.T) {
	const n = 12
	bs, ss, states := makeRollbackChain(t, n, 5)
	saveUnexecutedBlock(t, bs, states[n])
	saveUnexecutedBlock(t, bs, states[n])
	before := loadState(t, ss).Bytes()

	_, _, err := state.RollbackToHeight(bs, ss, n-1, false)
	require.ErrorContains(t, err, "is not one below or equal to blockstore height")
	require.Equal(t, int64(n+2), bs.Height())
	require.Equal(t, before, loadState(t, ss).Bytes())
}

func TestCheckRollbackToHeightPrunedHistory(t *testing.T) {
	const (
		n          = 12
		pruneBelow = 8
	)

	t.Run("blocks pruned", func(t *testing.T) {
		bs, ss, states := makeRollbackChain(t, n, 5)
		_, _, err := bs.PruneBlocks(pruneBelow, states[n])
		require.NoError(t, err)
		before := loadState(t, ss).Bytes()

		_, err = state.CheckRollbackToHeight(bs, ss, pruneBelow-1, false)
		require.ErrorContains(t, err, fmt.Sprintf("target height %d", pruneBelow-1))
		require.Equal(t, int64(pruneBelow), bs.Base())
		require.Equal(t, int64(n), bs.Height())
		require.Equal(t, before, loadState(t, ss).Bytes())
	})

	t.Run("states pruned", func(t *testing.T) {
		bs, ss, _ := makeRollbackChain(t, n, 5)
		_, err := ss.PruneStates(1, pruneBelow, pruneBelow, 0)
		require.NoError(t, err)
		before := loadState(t, ss).Bytes()

		_, err = state.CheckRollbackToHeight(bs, ss, 3, false)
		require.ErrorContains(t, err, "validator set at height 3")
		require.Equal(t, int64(n), bs.Height())
		require.Equal(t, before, loadState(t, ss).Bytes())
	})
}

func TestCheckRollbackToHeightMissingBlockMeta(t *testing.T) {
	// A node at genesis has state but no blocks.
	_, stateDB, _ := makeState(3, 1)
	ss := state.NewStore(stateDB, state.StoreOptions{DiscardABCIResponses: false})
	bs := store.NewBlockStore(dbm.NewMemDB())

	_, err := state.CheckRollbackToHeight(bs, ss, 0, false)
	require.ErrorContains(t, err, "block meta at height 0 not found")
}

func TestCheckRollbackToHeightBlockMetaHeightMismatch(t *testing.T) {
	const height = int64(100)
	stateStore := setupStateStore(t, height)
	blockStore := &mocks.BlockStore{}
	blockStore.On("Base").Return(int64(1))
	blockStore.On("Height").Return(height)
	blockStore.On("LoadBlockMeta", height-1).Return(&types.BlockMeta{Header: types.Header{Height: height - 2}})

	_, err := state.CheckRollbackToHeight(blockStore, stateStore, height-1, false)
	require.ErrorContains(t, err, "block meta at height 99 has height 98")
}

func TestCheckRollbackToHeightOutOfRange(t *testing.T) {
	const n = 12
	bs, ss, _ := makeRollbackChain(t, n, 5)

	for _, target := range []int64{0, n + 1} {
		_, err := state.CheckRollbackToHeight(bs, ss, target, false)
		require.ErrorContains(t, err, "out of range")
	}
}

func TestCheckRollbackToHeightInfo(t *testing.T) {
	const (
		n      = 12
		target = 9
	)
	bs, ss, states := makeRollbackChain(t, n, 5)

	info, err := state.CheckRollbackToHeight(bs, ss, target, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), info.BlockBase)
	require.Equal(t, int64(n), info.BlockHeight)
	require.Equal(t, int64(n), info.StateHeight)
	require.Equal(t, int64(target), info.TargetHeight)
	require.Equal(t, []byte(bs.LoadBlockMeta(target+1).Header.AppHash), info.TargetAppHash)
	require.Equal(t, states[target].AppHash, info.TargetAppHash)
	params, err := ss.LoadConsensusParams(target + 1)
	require.NoError(t, err)
	require.Equal(t, params.Version.App, info.TargetAppVersion)
	require.Equal(t, states[n].Version.Consensus.App, info.CurrentAppVersion)

	// Target before the app version change.
	info, err = state.CheckRollbackToHeight(bs, ss, 3, false)
	require.NoError(t, err)
	require.Equal(t, states[3].Version.Consensus.App, info.TargetAppVersion)
	require.Equal(t, states[n].Version.Consensus.App, info.CurrentAppVersion)
	require.NotEqual(t, info.TargetAppVersion, info.CurrentAppVersion)

	info, err = state.CheckRollbackToHeight(bs, ss, n, false)
	require.NoError(t, err)
	require.Equal(t, states[n].AppHash, info.TargetAppHash)
	require.Equal(t, states[n].Version.Consensus.App, info.TargetAppVersion)
}

func TestDefaultRollbackHeight(t *testing.T) {
	const n = 12
	bs, ss, states := makeRollbackChain(t, n, 5)

	height, err := state.DefaultRollbackHeight(bs, ss)
	require.NoError(t, err)
	require.Equal(t, int64(n-1), height)

	saveUnexecutedBlock(t, bs, states[n])
	_, err = state.DefaultRollbackHeight(bs, ss)
	require.ErrorIs(t, err, state.ErrBlockNotExecuted)
}

// makeRollbackChain executes n blocks, changing one validator's power at valChangeAt
// and the app version from 1 to 2 at height 8.
func makeRollbackChain(t *testing.T, n int, valChangeAt int64) (*store.BlockStore, state.Store, map[int64]state.State) {
	t.Helper()
	app := &versionedApp{testApp: &testApp{}}
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	t.Cleanup(func() { _ = proxyApp.Stop() })

	st, stateDB, privVals := makeState(3, 1)
	stateStore := state.NewStore(stateDB, state.StoreOptions{DiscardABCIResponses: false})
	blockStore := store.NewBlockStore(dbm.NewMemDB())

	mp := &mpmocks.Mempool{}
	mp.On("Lock").Return()
	mp.On("Unlock").Return()
	mp.On("FlushAppConn", mock.Anything).Return(nil)
	mp.On("Update",
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything).Return(nil)
	blockExec := state.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mp, state.EmptyEvidencePool{}, blockStore)

	states := make(map[int64]state.State, n)
	lastCommit := &types.Commit{}
	for h := int64(1); h <= int64(n); h++ {
		app.AppHash = tmhash.Sum([]byte{byte(h)})
		app.appVersion = 1
		if h >= 8 {
			app.appVersion = 2
		}
		app.ValidatorUpdates = nil
		if h == valChangeAt {
			_, val := st.NextValidators.GetByIndex(0)
			app.ValidatorUpdates = []abci.ValidatorUpdate{
				types.TM2PB.NewValidatorUpdate(val.PubKey, val.VotingPower+1),
			}
		}

		block, parts, err := makeBlock(st, h, lastCommit)
		require.NoError(t, err)
		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		commit, _, err := makeValidCommit(h, blockID, st.Validators, privVals)
		require.NoError(t, err)
		blockStore.SaveBlock(block, parts, commit.ToCommit())

		st, err = blockExec.ApplyBlock(st, blockID, block, nil)
		require.NoError(t, err)
		states[h] = st
		lastCommit = commit.ToCommit()
	}
	return blockStore, stateStore, states
}

// versionedApp is a testApp that returns appVersion in its params updates.
type versionedApp struct {
	*testApp
	appVersion uint64
}

func (app *versionedApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	res, err := app.testApp.FinalizeBlock(ctx, req)
	if err != nil {
		return nil, err
	}
	res.ConsensusParamUpdates.Version.App = app.appVersion
	return res, nil
}

// saveUnexecutedBlock saves the next block to the blockstore without executing it.
func saveUnexecutedBlock(t *testing.T, bs *store.BlockStore, st state.State) {
	t.Helper()
	h := bs.Height() + 1
	block, parts, err := makeBlock(st, h, bs.LoadSeenCommit(st.LastBlockHeight))
	require.NoError(t, err)
	bs.SaveBlock(block, parts, &types.Commit{Height: h})
}

func loadState(t *testing.T, ss state.Store) state.State {
	t.Helper()
	st, err := ss.Load()
	require.NoError(t, err)
	return st
}

// requireRolledBackState compares states, ignoring fields rollback clamps or does not restore.
func requireRolledBackState(t *testing.T, expected, actual state.State) {
	t.Helper()
	actual.LastHeightValidatorsChanged = expected.LastHeightValidatorsChanged
	actual.LastHeightConsensusParamsChanged = expected.LastHeightConsensusParamsChanged
	actual.Timeouts = expected.Timeouts
	require.Equal(t, expected.Bytes(), actual.Bytes())
}
