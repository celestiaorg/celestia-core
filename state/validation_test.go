package state_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	mpmocks "github.com/cometbft/cometbft/mempool/mocks"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/state/mocks"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

const validationTestsStopHeight int64 = 10

func TestValidateBlockHeader(t *testing.T) {
	proxyApp := newTestApp()
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(3, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
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

	blockStore := store.NewBlockStore(dbm.NewMemDB())

	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		sm.EmptyEvidencePool{},
		blockStore,
	)
	lastCommit := &types.Commit{}
	var lastExtCommit *types.ExtendedCommit

	// some bad values
	wrongHash := tmhash.Sum([]byte("this hash is wrong"))
	wrongVersion1 := state.Version.Consensus
	wrongVersion1.Block += 2
	wrongVersion2 := state.Version.Consensus
	wrongVersion2.App += 2

	// Manipulation of any header field causes failure.
	testCases := []struct {
		name          string
		malleateBlock func(block *types.Block)
	}{
		{"Version wrong1", func(block *types.Block) { block.Version = wrongVersion1 }},
		{"Version wrong2", func(block *types.Block) { block.Version = wrongVersion2 }},
		{"ChainID wrong", func(block *types.Block) { block.ChainID = "not-the-real-one" }},
		{"Height wrong", func(block *types.Block) { block.Height += 10 }},
		{"Time wrong", func(block *types.Block) { block.Time = block.Time.Add(-time.Second * 1) }},

		{"LastBlockID wrong", func(block *types.Block) { block.LastBlockID.PartSetHeader.Total += 10 }},
		{"LastCommitHash wrong", func(block *types.Block) { block.LastCommitHash = wrongHash }},
		{"DataHash wrong", func(block *types.Block) { block.DataHash = wrongHash }},

		{"ValidatorsHash wrong", func(block *types.Block) { block.ValidatorsHash = wrongHash }},
		{"NextValidatorsHash wrong", func(block *types.Block) { block.NextValidatorsHash = wrongHash }},
		{"ConsensusHash wrong", func(block *types.Block) { block.ConsensusHash = wrongHash }},
		{"AppHash wrong", func(block *types.Block) { block.AppHash = wrongHash }},
		{"LastResultsHash wrong", func(block *types.Block) { block.LastResultsHash = wrongHash }},

		{"EvidenceHash wrong", func(block *types.Block) { block.EvidenceHash = wrongHash }},
		{"Proposer wrong", func(block *types.Block) { block.ProposerAddress = ed25519.GenPrivKey().PubKey().Address() }},
		{"Proposer invalid", func(block *types.Block) { block.ProposerAddress = []byte("wrong size") }},
	}

	// Build up state for multiple heights
	for height := int64(1); height < validationTestsStopHeight; height++ {
		/*
			Invalid blocks don't pass
		*/
		for _, tc := range testCases {
			block, _, err := makeBlock(state, height, lastCommit)
			require.NoError(t, err)
			tc.malleateBlock(block)
			err = blockExec.ValidateBlock(state, block)
			require.Error(t, err, tc.name)
		}

		/*
			A good block passes
		*/
		var err error
		state, _, lastExtCommit, err = makeAndCommitGoodBlock(
			state, height, lastCommit, state.Validators.GetProposer().Address, blockExec, privVals, nil)
		require.NoError(t, err, "height %d", height)
		lastCommit = lastExtCommit.ToCommit()
	}

	nextHeight := validationTestsStopHeight
	block, _, err := makeBlock(state, nextHeight, lastCommit)
	require.NoError(t, err)
	state.InitialHeight = nextHeight + 1
	err = blockExec.ValidateBlock(state, block)
	require.Error(t, err, "expected an error when state is ahead of block")
	assert.Contains(t, err.Error(), "lower than initial height")
}

func TestValidateBlockCommit(t *testing.T) {
	proxyApp := newTestApp()
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
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

	blockStore := store.NewBlockStore(dbm.NewMemDB())

	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		sm.EmptyEvidencePool{},
		blockStore,
	)
	lastCommit := &types.Commit{}
	var lastExtCommit *types.ExtendedCommit
	wrongSigsCommit := &types.Commit{Height: 1}
	badPrivVal := types.NewMockPV()

	for height := int64(1); height < validationTestsStopHeight; height++ {
		proposerAddr := state.Validators.GetProposer().Address
		if height > 1 {
			/*
				#2589: ensure state.LastValidators.VerifyCommit fails here
			*/
			// should be height-1 instead of height
			idx, _ := state.Validators.GetByAddress(proposerAddr)
			wrongHeightVote := types.MakeVoteNoError(
				t,
				privVals[proposerAddr.String()],
				chainID,
				idx,
				height,
				0,
				2,
				state.LastBlockID,
				time.Now(),
			)
			wrongHeightCommit := &types.Commit{
				Height:     wrongHeightVote.Height,
				Round:      wrongHeightVote.Round,
				BlockID:    state.LastBlockID,
				Signatures: []types.CommitSig{wrongHeightVote.CommitSig()},
			}
			block, _, err := makeBlock(state, height, wrongHeightCommit)
			require.NoError(t, err)
			err = blockExec.ValidateBlock(state, block)
			_, isErrInvalidCommitHeight := err.(types.ErrInvalidCommitHeight)
			require.True(t, isErrInvalidCommitHeight, "expected ErrInvalidCommitHeight at height %d but got: %v", height, err)

			/*
				#2589: test len(block.LastCommit.Signatures) == state.LastValidators.Size()
			*/
			_, _, err = makeBlock(state, height, wrongSigsCommit)
			require.Error(t, err)
			require.ErrorContains(t, err, "error making block")
		}

		/*
			A good block passes
		*/
		var err error
		var blockID types.BlockID
		state, blockID, lastExtCommit, err = makeAndCommitGoodBlock(
			state,
			height,
			lastCommit,
			proposerAddr,
			blockExec,
			privVals,
			nil,
		)
		require.NoError(t, err, "height %d", height)
		lastCommit = lastExtCommit.ToCommit()

		/*
			wrongSigsCommit is fine except for the extra bad precommit
		*/
		idx, _ := state.Validators.GetByAddress(proposerAddr)
		goodVote := types.MakeVoteNoError(
			t,
			privVals[proposerAddr.String()],
			chainID,
			idx,
			height,
			0,
			cmtproto.PrecommitType,
			blockID,
			time.Now(),
		)

		bpvPubKey, err := badPrivVal.GetPubKey()
		require.NoError(t, err)

		badVote := &types.Vote{
			ValidatorAddress: bpvPubKey.Address(),
			ValidatorIndex:   0,
			Height:           height,
			Round:            0,
			Timestamp:        cmttime.Now(),
			Type:             cmtproto.PrecommitType,
			BlockID:          blockID,
		}

		g := goodVote.ToProto()
		b := badVote.ToProto()

		err = badPrivVal.SignVote(chainID, g)
		require.NoError(t, err, "height %d", height)
		err = badPrivVal.SignVote(chainID, b)
		require.NoError(t, err, "height %d", height)

		goodVote.Signature, badVote.Signature = g.Signature, b.Signature

		wrongSigsCommit = &types.Commit{
			Height:     goodVote.Height,
			Round:      goodVote.Round,
			BlockID:    blockID,
			Signatures: []types.CommitSig{goodVote.CommitSig(), badVote.CommitSig()},
		}
	}
}

// TestValidateBlockSkipLastCommit checks that ValidateBlock rejects a block
// whose LastCommit has an invalid signature, while ValidateBlockSkipLastCommit
// accepts the same block because it skips LastCommit verification (added when
// porting cometbft/cometbft#5753).
func TestValidateBlockSkipLastCommit(t *testing.T) {
	proxyApp := newTestApp()
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(4, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
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

	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		sm.EmptyEvidencePool{},
		blockStore,
	)

	// Commit height 1 to obtain a valid commit for it.
	proposerAddr := state.Validators.GetProposer().Address
	state, _, lastExtCommit, err := makeAndCommitGoodBlock(
		state, 1, &types.Commit{}, proposerAddr, blockExec, privVals, nil,
	)
	require.NoError(t, err)

	// Corrupt 4th signature (index 3), past the +2/3 threshold of 4 equal-power validators:
	// the commit keeps its valid majority (passes VerifyCommitLight) but must
	// fail the full VerifyCommit.
	badCommit := lastExtCommit.ToCommit()
	require.Len(t, badCommit.Signatures, 4)
	bad := make([]byte, len(badCommit.Signatures[3].Signature))
	copy(bad, badCommit.Signatures[3].Signature)
	bad[0] ^= 0xFF
	badCommit.Signatures[3].Signature = bad

	// Build height 2 referencing that commit as its LastCommit.
	block, _, err := makeBlock(state, 2, badCommit)
	require.NoError(t, err)

	// Assert on the specific error so the test can't pass for an unrelated
	// reason: the rejection must come from the corrupted signature #3.
	err = blockExec.ValidateBlock(state, block)
	require.ErrorContains(t, err, "wrong signature (#3)",
		"ValidateBlock must reject the block because LastCommit signature #3 is invalid")

	// Skipping LastCommit verification must accept the same block.
	require.NoError(t, blockExec.ValidateBlockSkipLastCommit(state, block),
		"ValidateBlockSkipLastCommit must not verify LastCommit")
}

func TestValidateBlockEvidence(t *testing.T) {
	proxyApp := newTestApp()
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(4, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	defaultEvidenceTime := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)

	evpool := &mocks.EvidencePool{}
	evpool.On("CheckEvidence", mock.AnythingOfType("types.EvidenceList")).Return(nil)
	evpool.On("Update", mock.AnythingOfType("state.State"), mock.AnythingOfType("types.EvidenceList")).Return()
	evpool.On("ABCIEvidence", mock.AnythingOfType("int64"), mock.AnythingOfType("[]types.Evidence")).Return(
		[]abci.Misbehavior{})

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
	state.ConsensusParams.Evidence.MaxBytes = 1000
	blockStore := store.NewBlockStore(dbm.NewMemDB())

	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		evpool,
		blockStore,
	)
	lastCommit := &types.Commit{}
	var lastExtCommit *types.ExtendedCommit

	for height := int64(1); height < validationTestsStopHeight; height++ {
		proposerAddr := state.Validators.GetProposer().Address
		maxBytesEvidence := state.ConsensusParams.Evidence.MaxBytes
		if height > 1 {
			/*
				A block with too much evidence fails
			*/
			evidence := make([]types.Evidence, 0)
			var currentBytes int64
			// more bytes than the maximum allowed for evidence
			for currentBytes <= maxBytesEvidence {
				newEv, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, time.Now(),
					privVals[proposerAddr.String()], chainID)
				require.NoError(t, err)
				evidence = append(evidence, newEv)
				currentBytes += int64(len(newEv.Bytes()))
			}
			block, _, err := state.MakeBlock(height, types.MakeData(test.MakeNTxs(height, 10)), lastCommit, evidence, proposerAddr)
			require.NoError(t, err)

			err = blockExec.ValidateBlock(state, block)
			if assert.Error(t, err) {
				_, ok := err.(*types.ErrEvidenceOverflow)
				require.True(t, ok, "expected error to be of type ErrEvidenceOverflow at height %d but got %v", height, err)
			}
		}

		/*
			A good block with several pieces of good evidence passes
		*/
		evidence := make([]types.Evidence, 0)
		var currentBytes int64
		// precisely the amount of allowed evidence
		for {
			newEv, err := types.NewMockDuplicateVoteEvidenceWithValidator(height, defaultEvidenceTime,
				privVals[proposerAddr.String()], chainID)
			require.NoError(t, err)
			currentBytes += int64(len(newEv.Bytes()))
			if currentBytes >= maxBytesEvidence {
				break
			}
			evidence = append(evidence, newEv)
		}

		var err error
		state, _, lastExtCommit, err = makeAndCommitGoodBlock(
			state,
			height,
			lastCommit,
			proposerAddr,
			blockExec,
			privVals,
			evidence,
		)
		require.NoError(t, err, "height %d", height)
		lastCommit = lastExtCommit.ToCommit()

	}
}

func TestValidateBlockTime(t *testing.T) {
	proxyApp := newTestApp()
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(3, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
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

	blockStore := store.NewBlockStore(dbm.NewMemDB())

	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		sm.EmptyEvidencePool{},
		blockStore,
	)
	lastCommit := &types.Commit{}
	var lastExtCommit *types.ExtendedCommit

	// Build up state for test
	for height := int64(1); height < 3; height++ {
		var err error
		state, _, lastExtCommit, err = makeAndCommitGoodBlock(
			state, height, lastCommit, state.Validators.GetProposer().Address, blockExec, privVals, nil)
		require.NoError(t, err, "height %d", height)
		lastCommit = lastExtCommit.ToCommit()
	}

	t.Run("block time before last block time", func(t *testing.T) {
		height := int64(3)
		block, _, err := makeBlock(state, height, lastCommit)
		require.NoError(t, err)

		// Set time to before last block time
		block.Time = block.Time.Add(-time.Millisecond * 10)
		err = blockExec.ValidateBlock(state, block)

		require.ErrorContains(t, err, "not greater than last block time")
	})

	t.Run("block time after last block time, different than median time", func(t *testing.T) {
		height := int64(3)
		block, _, err := makeBlock(state, height, lastCommit)
		require.NoError(t, err)
		// Set time to after the median time
		block.Time = block.Time.Add(time.Second)
		err = blockExec.ValidateBlock(state, block)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid block time")
	})

	t.Run("block time after last block time, same as median time", func(t *testing.T) {
		height := int64(3)
		block, _, err := makeBlock(state, height, lastCommit)
		require.NoError(t, err)
		err = blockExec.ValidateBlock(state, block)
		require.NoError(t, err)
	})

	t.Run("block time too far in the future", func(t *testing.T) {
		height := int64(3)
		block, _, err := makeBlock(state, height, lastCommit)
		require.NoError(t, err)
		// A block time well beyond maxBlockTimeTolerance must be rejected,
		// regardless of the median, to guard against malicious timestamps.
		block.Time = time.Now().Add(1000 * time.Hour)
		err = blockExec.ValidateBlock(state, block)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too far in the future")
	})
}

// TestValidateBlockMedianTimeRuleByAppVersion checks that ValidateBlock uses
// the median rule of the app version in state. Each commit has 4 equal-power
// signers, so the legacy rule picks the 2nd timestamp and the strict rule
// picks the 3rd. The test also covers the first block after the app upgrades
// to StrictMedianTimeAppVersion.
func TestValidateBlockMedianTimeRuleByAppVersion(t *testing.T) {
	app := &testApp{AppVersion: sm.StrictMedianTimeAppVersion - 1}
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(4, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
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
	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		sm.EmptyEvidencePool{},
		store.NewBlockStore(dbm.NewMemDB()),
	)

	// Height 1 moves state to the legacy app version.
	state, blockID, err := makeAndApplyGoodBlock(
		state, 1, &types.Commit{}, state.Validators.GetProposer().Address, blockExec, nil)
	require.NoError(t, err)
	require.Equal(t, sm.StrictMedianTimeAppVersion-1, state.Version.Consensus.App)

	commit, legacyTime, strictTime := makeSpreadCommit(t, state, 1, blockID, privVals)
	requireMedianTimeRule(t, blockExec, state, 2, commit, legacyTime, strictTime)

	// Height 2 is made with the legacy rule. The app upgrades in this block,
	// so height 3 is the first block that uses the strict rule.
	app.AppVersion = sm.StrictMedianTimeAppVersion
	state, blockID, err = makeAndApplyGoodBlock(
		state, 2, commit, state.Validators.GetProposer().Address, blockExec, nil)
	require.NoError(t, err)
	require.Equal(t, sm.StrictMedianTimeAppVersion, state.Version.Consensus.App)

	commit, legacyTime, strictTime = makeSpreadCommit(t, state, 2, blockID, privVals)
	requireMedianTimeRule(t, blockExec, state, 3, commit, strictTime, legacyTime)
}

// makeSpreadCommit signs a commit for height in which validator i votes at
// LastBlockTime + (i+1) seconds. It returns the commit and the times that the
// legacy and strict median rules select for 4 equal-power signers.
func makeSpreadCommit(
	t *testing.T,
	state sm.State,
	height int64,
	blockID types.BlockID,
	privVals map[string]types.PrivValidator,
) (*types.Commit, time.Time, time.Time) {
	t.Helper()
	require.Equal(t, 4, state.Validators.Size())

	times := make([]time.Time, state.Validators.Size())
	sigs := make([]types.ExtendedCommitSig, state.Validators.Size())
	for i := range sigs {
		_, val := state.Validators.GetByIndex(int32(i))
		require.EqualValues(t, 1000, val.VotingPower)
		times[i] = state.LastBlockTime.Add(time.Duration(i+1) * time.Second)
		vote, err := types.MakeVote(
			privVals[val.Address.String()],
			chainID,
			int32(i),
			height,
			0,
			cmtproto.PrecommitType,
			blockID,
			times[i],
		)
		require.NoError(t, err)
		sigs[i] = vote.ExtendedCommitSig()
	}
	extCommit := &types.ExtendedCommit{
		Height:             height,
		BlockID:            blockID,
		ExtendedSignatures: sigs,
	}
	return extCommit.ToCommit(), times[1], times[2]
}

// requireMedianTimeRule checks that a block at height built on commit gets
// want as its time, that ValidateBlock accepts it, and that ValidateBlock
// rejects the same block with the other rule's time.
func requireMedianTimeRule(
	t *testing.T,
	blockExec *sm.BlockExecutor,
	state sm.State,
	height int64,
	commit *types.Commit,
	want time.Time,
	other time.Time,
) {
	t.Helper()
	require.False(t, want.Equal(other))

	block, _, err := makeBlock(state, height, commit)
	require.NoError(t, err)
	require.True(t, want.Equal(block.Time), "app version %d: got %v, want %v",
		state.Version.Consensus.App, block.Time, want)
	require.NoError(t, blockExec.ValidateBlock(state, block))

	block.Time = other
	require.ErrorContains(t, blockExec.ValidateBlock(state, block), "invalid block time")
}

func TestValidateBlockInvalidCommit(t *testing.T) {
	proxyApp := newTestApp()
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	state, stateDB, privVals := makeState(3, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
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

	blockStore := store.NewBlockStore(dbm.NewMemDB())

	blockExec := sm.NewBlockExecutor(
		stateStore,
		log.TestingLogger(),
		proxyApp.Consensus(),
		mp,
		sm.EmptyEvidencePool{},
		blockStore,
	)
	lastCommit := &types.Commit{}
	var lastExtCommit *types.ExtendedCommit

	// Build up state for test
	for height := int64(1); height < 3; height++ {
		var err error
		state, _, lastExtCommit, err = makeAndCommitGoodBlock(
			state, height, lastCommit, state.Validators.GetProposer().Address, blockExec, privVals, nil)
		require.NoError(t, err, "height %d", height)
		lastCommit = lastExtCommit.ToCommit()
	}

	t.Run("commit with unknown validator flagged as commit", func(t *testing.T) {
		height := int64(3)

		// Create a commit where only unknown validators have BlockIDFlagCommit
		unknownVal := ed25519.GenPrivKey()
		now := time.Now()

		invalidCommit := &types.Commit{
			Height:  height - 1,
			Round:   0,
			BlockID: state.LastBlockID,
			Signatures: []types.CommitSig{
				{
					BlockIDFlag:      types.BlockIDFlagCommit,
					ValidatorAddress: unknownVal.PubKey().Address(),
					Timestamp:        now,
					Signature:        []byte("dummy"),
				},
			},
		}

		_, _, err := makeBlock(state, height, invalidCommit)
		require.Error(t, err)
		require.Contains(t, err.Error(), "commit validator not found in validator set")
	})
}
