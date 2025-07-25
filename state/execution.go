package state

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"

	abci "github.com/cometbft/cometbft/abci/types"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/fail"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

//-----------------------------------------------------------------------------
// BlockExecutor handles block execution and state updates.
// It exposes ApplyBlock(), which validates & executes the block, updates state w/ ABCI responses,
// then commits and updates the mempool atomically, then saves state.

// BlockExecutor provides the context and accessories for properly executing a block.
type BlockExecutor struct {
	// save state, validators, consensus params, abci responses here
	store Store

	// use blockstore for the pruning functions.
	blockStore BlockStore

	// execute the app against this
	proxyApp proxy.AppConnConsensus

	// events
	eventBus types.BlockEventPublisher

	// manage the mempool lock during commit
	// and update both with block results after commit.
	mempool mempool.Mempool
	evpool  EvidencePool

	logger log.Logger

	metrics *Metrics

	// root directory for debug file saving
	rootDir string

	// tracer optional tracer
	tracer trace.Tracer
}

type BlockExecutorOption func(executor *BlockExecutor)

func BlockExecutorWithMetrics(metrics *Metrics) BlockExecutorOption {
	return func(blockExec *BlockExecutor) {
		blockExec.metrics = metrics
	}
}

func BlockExecutorWithRootDir(rootDir string) BlockExecutorOption {
	return func(blockExec *BlockExecutor) {
		blockExec.rootDir = rootDir
	}
}

func BlockExecutorWithTracer(tracer trace.Tracer) BlockExecutorOption {
	return func(blockExec *BlockExecutor) {
		blockExec.tracer = tracer
	}
}

// NewBlockExecutor returns a new BlockExecutor with a NopEventBus.
// Call SetEventBus to provide one.
func NewBlockExecutor(
	stateStore Store,
	logger log.Logger,
	proxyApp proxy.AppConnConsensus,
	mempool mempool.Mempool,
	evpool EvidencePool,
	blockStore BlockStore,
	options ...BlockExecutorOption,
) *BlockExecutor {
	res := &BlockExecutor{
		store:      stateStore,
		proxyApp:   proxyApp,
		eventBus:   types.NopEventBus{},
		mempool:    mempool,
		evpool:     evpool,
		logger:     logger,
		metrics:    NopMetrics(),
		blockStore: blockStore,
		tracer:     trace.NoOpTracer(),
	}

	for _, option := range options {
		option(res)
	}

	return res
}

func (blockExec *BlockExecutor) Store() Store {
	return blockExec.store
}

// SetEventBus - sets the event bus for publishing block related events.
// If not called, it defaults to types.NopEventBus.
func (blockExec *BlockExecutor) SetEventBus(eventBus types.BlockEventPublisher) {
	blockExec.eventBus = eventBus
}

// CreateProposalBlock calls state.MakeBlock with evidence from the evpool
// and txs from the mempool. The max bytes must be big enough to fit the commit.
// The block space is first allocated to outstanding evidence.
// The rest is given to txs, up to the max gas.
//
// Contract: application will not return more bytes than are sent over the wire.
func (blockExec *BlockExecutor) CreateProposalBlock(
	ctx context.Context,
	height int64,
	state State,
	lastExtCommit *types.ExtendedCommit,
	proposerAddr []byte,
) (*types.Block, *types.PartSet, error) {

	maxBytes := state.ConsensusParams.Block.MaxBytes
	emptyMaxBytes := maxBytes == -1
	if emptyMaxBytes {
		maxBytes = int64(types.MaxBlockSizeBytes)
	}

	maxGas := state.ConsensusParams.Block.MaxGas

	evidence, evSize := blockExec.evpool.PendingEvidence(state.ConsensusParams.Evidence.MaxBytes)

	// Fetch a limited amount of valid txs
	maxDataBytes := types.MaxDataBytes(maxBytes, evSize, state.Validators.Size())
	var maxReapBytes int64
	if emptyMaxBytes {
		maxReapBytes = -1
	} else {
		// Reap 25% more transactions than can fit in the block to provide
		// buffer for PrepareProposal to have sufficient transactions to work with
		maxReapBytes = maxDataBytes + maxDataBytes/4
	}

	txs := blockExec.mempool.ReapMaxBytesMaxGas(maxReapBytes, maxGas)
	commit := lastExtCommit.ToCommit()
	block, _, err := state.MakeBlock(height, types.MakeData(types.TxsFromCachedTxs(txs)), commit, evidence, proposerAddr)
	if err != nil {
		return nil, nil, err
	}
	req := &abci.RequestPrepareProposal{
		MaxTxBytes:         maxDataBytes,
		Txs:                block.Txs.ToSliceOfBytes(),
		LocalLastCommit:    buildExtendedCommitInfoFromStore(lastExtCommit, blockExec.store, state.InitialHeight, state.ConsensusParams.ABCI),
		Misbehavior:        block.Evidence.Evidence.ToABCI(),
		Height:             block.Height,
		Time:               block.Time,
		NextValidatorsHash: block.NextValidatorsHash,
		ProposerAddress:    block.ProposerAddress,
	}

	var rpp *abci.ResponsePrepareProposal

	func() {
		defer func() {
			if r := recover(); r != nil {
				blockExec.saveFailedProposalBlock(state, block, "prepare_proposal_panic")
				err = fmt.Errorf("PrepareProposal panicked: %v", r)
			}
		}()

		schema.WriteABCI(blockExec.tracer, schema.PrepareProposalStart, block.Height, -1)
		rpp, err = blockExec.proxyApp.PrepareProposal(ctx, req)
		schema.WriteABCI(blockExec.tracer, schema.PrepareProposalEnd, block.Height, -1)
	}()
	if err != nil {
		// For non-panic errors, also save the failed proposal block
		if rpp == nil {
			blockExec.saveFailedProposalBlock(state, block, "prepare_proposal_error")
		}
		// The App MUST ensure that only valid (and hence 'processable') transactions
		// enter the mempool. Hence, at this point, we can't have any non-processable
		// transaction causing an error.
		//
		// Also, the App can simply skip any transaction that could cause any kind of trouble.
		// Either way, we cannot recover in a meaningful way, unless we skip proposing
		// this block, repair what caused the error and try again. Hence, we return an
		// error for now (the production code calling this function is expected to panic).
		return nil, nil, err
	}

	rawNewData := rpp.GetTxs()

	rejectedTxs := len(rawNewData) - len(txs)
	if rejectedTxs > 0 {
		blockExec.metrics.RejectedTransactions.Add(float64(rejectedTxs))
		blockExec.logger.Debug("rejected txs while creating a block", "tx count", rejectedTxs)
	}

	txl := types.ToTxs(rpp.Txs)
	if err := txl.Validate(maxDataBytes); err != nil {
		return nil, nil, err
	}

	newData := types.NewData(txl, rpp.SquareSize, rpp.DataRootHash)
	block, partset, err := state.MakeBlock(height, newData, commit, evidence, proposerAddr)
	if err != nil {
		return nil, nil, err
	}

	// get the cached hashes
	// TODO: make sure that the hashes are correct here
	// via also removing hashes that the application removed!
	hashes := make([][]byte, len(newData.Txs))
	for i := 0; i < len(newData.Txs); i++ {
		hashes[i] = newData.Txs[i].Hash()
	}

	block.SetCachedHashes(hashes)

	return block, partset, nil
}

func (blockExec *BlockExecutor) ProcessProposal(
	block *types.Block,
	state State,
) (bool, error) {
	pbHeader := block.Header.ToProto()
	resp, err := blockExec.proxyApp.ProcessProposal(context.TODO(), &abci.RequestProcessProposal{
		Hash:               block.Header.Hash(),
		Height:             block.Header.Height,             //nolint:staticcheck
		Time:               block.Header.Time,               //nolint:staticcheck
		Txs:                block.Data.Txs.ToSliceOfBytes(), //nolint:staticcheck
		SquareSize:         block.Data.SquareSize,           //nolint:staticcheck
		DataRootHash:       block.Data.Hash(),
		ProposedLastCommit: buildLastCommitInfoFromStore(block, blockExec.store, state.InitialHeight),
		Misbehavior:        block.Evidence.Evidence.ToABCI(),
		ProposerAddress:    block.ProposerAddress,
		NextValidatorsHash: block.NextValidatorsHash,

		// needed for v3 to sync with multiplexer as the header is stored in state
		Header: pbHeader,
	})
	if err != nil {
		return false, err
	}
	if resp.IsStatusUnknown() {
		panic(fmt.Sprintf("ProcessProposal responded with status %s", resp.Status.String()))
	}

	return resp.IsAccepted(), nil
}

// ValidateBlock validates the given block against the given state.
// If the block is invalid, it returns an error.
// Validation does not mutate state, but does require historical information from the stateDB,
// ie. to verify evidence from a validator at an old height.
func (blockExec *BlockExecutor) ValidateBlock(state State, block *types.Block) error {
	err := validateBlock(state, block)
	if err != nil {
		return err
	}
	return blockExec.evpool.CheckEvidence(block.Evidence.Evidence)
}

// ApplyVerifiedBlock does the same as `ApplyBlock`, but skips verification.
func (blockExec *BlockExecutor) ApplyVerifiedBlock(
	state State, blockID types.BlockID, block *types.Block, lastCommit *types.Commit,
) (State, error) {
	return blockExec.applyBlock(state, blockID, block, lastCommit)
}

// ApplyBlock validates the block against the state, executes it against the app,
// fires the relevant events, commits the app, and saves the new state and responses.
// It returns the new state.
// It's the only function that needs to be called
// from outside this package to process and commit an entire block.
// It takes a blockID to avoid recomputing the parts hash.
func (blockExec *BlockExecutor) ApplyBlock(
	state State, blockID types.BlockID, block *types.Block, lastCommit *types.Commit,
) (State, error) {

	if err := validateBlock(state, block); err != nil {
		return state, ErrInvalidBlock(err)
	}

	return blockExec.applyBlock(state, blockID, block, lastCommit)
}

func (blockExec *BlockExecutor) applyBlock(state State, blockID types.BlockID, block *types.Block, lastCommit *types.Commit) (State, error) {
	startTime := time.Now().UnixNano()

	// Unmarshal blob txs
	txs := make([][]byte, len(block.Txs))
	for i, tx := range block.Txs {
		blobTx, isBlobTx := types.UnmarshalBlobTx(tx)
		if isBlobTx {
			tx = blobTx.Tx
		}
		txs[i] = tx
	}
	pbHeader := block.Header.ToProto()

	abciResponse, err := blockExec.proxyApp.FinalizeBlock(context.TODO(), &abci.RequestFinalizeBlock{
		Hash:               block.Hash(),
		NextValidatorsHash: block.NextValidatorsHash,
		ProposerAddress:    block.ProposerAddress,
		Height:             block.Height,
		Time:               block.Time,
		DecidedLastCommit:  buildLastCommitInfoFromStore(block, blockExec.store, state.InitialHeight),
		Misbehavior:        block.Evidence.Evidence.ToABCI(),
		Txs:                txs,

		// needed for v3 to sync with multiplexer as the header is stored in state
		Header: pbHeader,
	})
	endTime := time.Now().UnixNano()
	blockExec.metrics.BlockProcessingTime.Observe(float64(endTime-startTime) / 1000000)
	if err != nil {
		blockExec.logger.Error("error in proxyAppConn.FinalizeBlock", "err", err)
		return state, err
	}

	blockExec.logger.Info(
		"finalized block",
		"height", block.Height,
		"num_txs_res", len(abciResponse.TxResults),
		"num_val_updates", len(abciResponse.ValidatorUpdates),
		"block_app_hash", fmt.Sprintf("%X", abciResponse.AppHash),
	)

	// Assert that the application correctly returned tx results for each of the transactions provided in the block
	if len(block.Data.Txs) != len(abciResponse.TxResults) { //nolint:staticcheck
		return state, fmt.Errorf("expected tx results length to match size of transactions in block. Expected %d, got %d", len(block.Data.Txs), len(abciResponse.TxResults)) //nolint:staticcheck
	}

	blockExec.logger.Info("executed block", "height", block.Height, "app_hash", fmt.Sprintf("%X", abciResponse.AppHash))
	// Save indexing info of the transaction.
	// This needs to be done prior to saving state
	// for correct crash recovery
	if blockExec.blockStore != nil {
		respCodes := getResponseCodes(abciResponse.TxResults)
		logs := getLogs(abciResponse.TxResults)
		if err := blockExec.blockStore.SaveTxInfo(block, respCodes, logs); err != nil {
			return state, err
		}
	}

	fail.Fail() // XXX

	// Save the results before we commit.
	if err := blockExec.store.SaveFinalizeBlockResponse(block.Height, abciResponse); err != nil {
		return state, err
	}

	fail.Fail() // XXX

	// validate the validator updates and convert to CometBFT types
	err = validateValidatorUpdates(abciResponse.ValidatorUpdates, state.ConsensusParams.Validator)
	if err != nil {
		return state, fmt.Errorf("error in validator updates: %v", err)
	}

	validatorUpdates, err := types.PB2TM.ValidatorUpdates(abciResponse.ValidatorUpdates)
	if err != nil {
		return state, err
	}
	if len(validatorUpdates) > 0 {
		blockExec.logger.Info("updates to validators", "updates", types.ValidatorListString(validatorUpdates))
		blockExec.metrics.ValidatorSetUpdates.Add(1)
	}
	if abciResponse.ConsensusParamUpdates != nil {
		blockExec.metrics.ConsensusParamUpdates.Add(1)
	}

	// Update the state with the block and responses.
	state, err = updateState(state, blockID, &block.Header, abciResponse, validatorUpdates)
	if err != nil {
		return state, fmt.Errorf("commit failed for application: %v", err)
	}

	// Lock mempool, commit app state, update mempoool.
	retainHeight, err := blockExec.Commit(state, block, abciResponse)
	if err != nil {
		return state, fmt.Errorf("commit failed for application: %v", err)
	}

	// Update evpool with the latest state.
	blockExec.evpool.Update(state, block.Evidence.Evidence)

	fail.Fail() // XXX

	// Update the app hash and save the state.
	state.AppHash = abciResponse.AppHash
	if err := blockExec.store.Save(state); err != nil {
		return state, err
	}

	fail.Fail() // XXX

	// Prune old heights, if requested by ABCI app.
	if retainHeight > 0 {
		pruned, err := blockExec.pruneBlocks(retainHeight, state)
		if err != nil {
			blockExec.logger.Error("failed to prune blocks", "retain_height", retainHeight, "err", err)
		} else {
			blockExec.logger.Debug("pruned blocks", "pruned", pruned, "retain_height", retainHeight)
		}
	}

	// Events are fired after everything else.
	// NOTE: if we crash between Commit and Save, events wont be fired during replay
	fireEvents(blockExec.logger, blockExec.eventBus, block, blockID, abciResponse, validatorUpdates, state.Validators, lastCommit)

	return state, nil
}

func (blockExec *BlockExecutor) ExtendVote(
	ctx context.Context,
	vote *types.Vote,
	block *types.Block,
	state State,
) ([]byte, error) {
	if !block.HashesTo(vote.BlockID.Hash) {
		panic(fmt.Sprintf("vote's hash does not match the block it is referring to %X!=%X", block.Hash(), vote.BlockID.Hash))
	}
	if vote.Height != block.Height {
		panic(fmt.Sprintf("vote's and block's heights do not match %d!=%d", block.Height, vote.Height))
	}

	req := abci.RequestExtendVote{
		Hash:               vote.BlockID.Hash,
		Height:             vote.Height,
		Time:               block.Time,
		Txs:                block.Txs.ToSliceOfBytes(),
		ProposedLastCommit: buildLastCommitInfoFromStore(block, blockExec.store, state.InitialHeight),
		Misbehavior:        block.Evidence.Evidence.ToABCI(),
		NextValidatorsHash: block.NextValidatorsHash,
		ProposerAddress:    block.ProposerAddress,
	}

	resp, err := blockExec.proxyApp.ExtendVote(ctx, &req)
	if err != nil {
		panic(fmt.Errorf("ExtendVote call failed: %w", err))
	}
	return resp.VoteExtension, nil
}

func (blockExec *BlockExecutor) VerifyVoteExtension(ctx context.Context, vote *types.Vote) error {
	req := abci.RequestVerifyVoteExtension{
		Hash:             vote.BlockID.Hash,
		ValidatorAddress: vote.ValidatorAddress,
		Height:           vote.Height,
		VoteExtension:    vote.Extension,
	}

	resp, err := blockExec.proxyApp.VerifyVoteExtension(ctx, &req)
	if err != nil {
		panic(fmt.Errorf("VerifyVoteExtension call failed: %w", err))
	}
	if resp.IsStatusUnknown() {
		panic(fmt.Sprintf("VerifyVoteExtension responded with status %s", resp.Status.String()))
	}

	if !resp.IsAccepted() {
		return types.ErrInvalidVoteExtension
	}
	return nil
}

// Commit locks the mempool, runs the ABCI Commit message, and updates the
// mempool.
// It returns the result of calling abci.Commit which is the height to retain (if any)).
// The application is expected to have persisted its state (if any) before returning
// from the ABCI Commit call. This is the only place where the application should
// persist its state.
// The Mempool must be locked during commit and update because state is
// typically reset on Commit and old txs must be replayed against committed
// state before new txs are run in the mempool, lest they be invalid.
func (blockExec *BlockExecutor) Commit(
	state State,
	block *types.Block,
	abciResponse *abci.ResponseFinalizeBlock,
) (int64, error) {
	blockExec.mempool.Lock()
	defer blockExec.mempool.Unlock()

	// while mempool is Locked, flush to ensure all async requests have completed
	// in the ABCI app before Commit.
	err := blockExec.mempool.FlushAppConn()
	if err != nil {
		blockExec.logger.Error("client error during mempool.FlushAppConn", "err", err)
		return 0, err
	}

	// Commit block, get hash back
	res, err := blockExec.proxyApp.Commit(context.TODO())
	if err != nil {
		blockExec.logger.Error("client error during proxyAppConn.CommitSync", "err", err)
		return 0, err
	}

	// ResponseCommit has no error code - just data
	blockExec.logger.Info(
		"committed state",
		"height", block.Height,
		"block_app_hash", fmt.Sprintf("%X", block.AppHash),
	)

	// Update mempool.
	err = blockExec.mempool.Update(
		block.Height,
		types.CachedTxFromTxs(block.Txs),
		abciResponse.TxResults,
		TxPreCheck(state),
		TxPostCheck(state),
	)

	return res.RetainHeight, err
}

//---------------------------------------------------------
// Helper functions for executing blocks and updating state

func buildLastCommitInfoFromStore(block *types.Block, store Store, initialHeight int64) abci.CommitInfo {
	if block.Height == initialHeight { // check for initial height before loading validators
		// there is no last commit for the initial height.
		// return an empty value.
		return abci.CommitInfo{}
	}

	lastValSet, err := store.LoadValidators(block.Height - 1)
	if err != nil {
		panic(fmt.Errorf("failed to load validator set at height %d: %w", block.Height-1, err))
	}

	return BuildLastCommitInfo(block, lastValSet, initialHeight)
}

// BuildLastCommitInfo builds a CommitInfo from the given block and validator set.
// If you want to load the validator set from the store instead of providing it,
// use buildLastCommitInfoFromStore.
func BuildLastCommitInfo(block *types.Block, lastValSet *types.ValidatorSet, initialHeight int64) abci.CommitInfo {
	if block.Height == initialHeight {
		// there is no last commit for the initial height.
		// return an empty value.
		return abci.CommitInfo{}
	}

	var (
		commitSize = block.LastCommit.Size()
		valSetLen  = len(lastValSet.Validators)
	)

	// ensure that the size of the validator set in the last commit matches
	// the size of the validator set in the state store.
	if commitSize != valSetLen {
		panic(fmt.Sprintf(
			"commit size (%d) doesn't match validator set length (%d) at height %d\n\n%v\n\n%v",
			commitSize, valSetLen, block.Height, block.LastCommit.Signatures, lastValSet.Validators,
		))
	}

	votes := make([]abci.VoteInfo, block.LastCommit.Size())
	for i, val := range lastValSet.Validators {
		commitSig := block.LastCommit.Signatures[i]
		votes[i] = abci.VoteInfo{
			Validator:   types.TM2PB.Validator(val),
			BlockIdFlag: cmtproto.BlockIDFlag(commitSig.BlockIDFlag),
		}
	}

	return abci.CommitInfo{
		Round: block.LastCommit.Round,
		Votes: votes,
	}
}

// buildExtendedCommitInfoFromStore populates an ABCI extended commit from the
// corresponding CometBFT extended commit ec, using the stored validator set
// from ec.  It requires ec to include the original precommit votes along with
// the vote extensions from the last commit.
//
// For heights below the initial height, for which we do not have the required
// data, it returns an empty record.
//
// Assumes that the commit signatures are sorted according to validator index.
func buildExtendedCommitInfoFromStore(ec *types.ExtendedCommit, store Store, initialHeight int64, ap types.ABCIParams) abci.ExtendedCommitInfo {
	if ec.Height < initialHeight {
		// There are no extended commits for heights below the initial height.
		return abci.ExtendedCommitInfo{}
	}

	valSet, err := store.LoadValidators(ec.Height)
	if err != nil {
		panic(fmt.Errorf("failed to load validator set at height %d, initial height %d: %w", ec.Height, initialHeight, err))
	}

	return BuildExtendedCommitInfo(ec, valSet, initialHeight, ap)
}

// BuildExtendedCommitInfo builds an ExtendedCommitInfo from the given block and validator set.
// If you want to load the validator set from the store instead of providing it,
// use buildExtendedCommitInfoFromStore.
func BuildExtendedCommitInfo(ec *types.ExtendedCommit, valSet *types.ValidatorSet, initialHeight int64, ap types.ABCIParams) abci.ExtendedCommitInfo {
	if ec.Height < initialHeight {
		// There are no extended commits for heights below the initial height.
		return abci.ExtendedCommitInfo{}
	}

	var (
		ecSize    = ec.Size()
		valSetLen = len(valSet.Validators)
	)

	// Ensure that the size of the validator set in the extended commit matches
	// the size of the validator set in the state store.
	if ecSize != valSetLen {
		panic(fmt.Errorf(
			"extended commit size (%d) does not match validator set length (%d) at height %d\n\n%v\n\n%v",
			ecSize, valSetLen, ec.Height, ec.ExtendedSignatures, valSet.Validators,
		))
	}

	votes := make([]abci.ExtendedVoteInfo, ecSize)
	for i, val := range valSet.Validators {
		ecs := ec.ExtendedSignatures[i]

		// Absent signatures have empty validator addresses, but otherwise we
		// expect the validator addresses to be the same.
		if ecs.BlockIDFlag != types.BlockIDFlagAbsent && !bytes.Equal(ecs.ValidatorAddress, val.Address) {
			panic(fmt.Errorf("validator address of extended commit signature in position %d (%s) does not match the corresponding validator's at height %d (%s)",
				i, ecs.ValidatorAddress, ec.Height, val.Address,
			))
		}

		// Check if vote extensions were enabled during the commit's height: ec.Height.
		// ec is the commit from the previous height, so if extensions were enabled
		// during that height, we ensure they are present and deliver the data to
		// the proposer. If they were not enabled during this previous height, we
		// will not deliver extension data.
		if err := ecs.EnsureExtension(ap.VoteExtensionsEnabled(ec.Height)); err != nil {
			panic(fmt.Errorf("commit at height %d has problems with vote extension data; err %w", ec.Height, err))
		}

		votes[i] = abci.ExtendedVoteInfo{
			Validator:          types.TM2PB.Validator(val),
			BlockIdFlag:        cmtproto.BlockIDFlag(ecs.BlockIDFlag),
			VoteExtension:      ecs.Extension,
			ExtensionSignature: ecs.ExtensionSignature,
		}
	}

	return abci.ExtendedCommitInfo{
		Round: ec.Round,
		Votes: votes,
	}
}

func validateValidatorUpdates(abciUpdates []abci.ValidatorUpdate,
	params types.ValidatorParams) error {
	for _, valUpdate := range abciUpdates {
		if valUpdate.GetPower() < 0 {
			return fmt.Errorf("voting power can't be negative %v", valUpdate)
		} else if valUpdate.GetPower() == 0 {
			// continue, since this is deleting the validator, and thus there is no
			// pubkey to check
			continue
		}

		// Check if validator's pubkey matches an ABCI type in the consensus params
		pk, err := cryptoenc.PubKeyFromProto(valUpdate.PubKey)
		if err != nil {
			return err
		}

		if !types.IsValidPubkeyType(params, pk.Type()) {
			return fmt.Errorf("validator %v is using pubkey %s, which is unsupported for consensus",
				valUpdate, pk.Type())
		}
	}
	return nil
}

// updateState returns a new State updated according to the header and responses.
func updateState(
	state State,
	blockID types.BlockID,
	header *types.Header,
	abciResponse *abci.ResponseFinalizeBlock,
	validatorUpdates []*types.Validator,
) (State, error) {

	// Copy the valset so we can apply changes from EndBlock
	// and update s.LastValidators and s.Validators.
	nValSet := state.NextValidators.Copy()

	// Update the validator set with the latest abciResponse.
	lastHeightValsChanged := state.LastHeightValidatorsChanged
	if len(validatorUpdates) > 0 {
		err := nValSet.UpdateWithChangeSet(validatorUpdates)
		if err != nil {
			return state, fmt.Errorf("changing validator set: %w", err)
		}
		// Change results from this height but only applies to the next next height.
		lastHeightValsChanged = header.Height + 1 + 1
	}

	// Update validator proposer priority and set state variables.
	nValSet.IncrementProposerPriority(1)

	// Update the params with the latest abciResponse.
	nextParams := state.ConsensusParams
	lastHeightParamsChanged := state.LastHeightConsensusParamsChanged
	if abciResponse.ConsensusParamUpdates != nil {
		// NOTE: must not mutate state.ConsensusParams
		nextParams = state.ConsensusParams.Update(abciResponse.ConsensusParamUpdates)
		err := nextParams.ValidateBasic()
		if err != nil {
			return state, fmt.Errorf("validating new consensus params: %w", err)
		}

		err = state.ConsensusParams.ValidateUpdate(abciResponse.ConsensusParamUpdates, header.Height)
		if err != nil {
			return state, fmt.Errorf("updating consensus params: %w", err)
		}

		state.Version.Consensus.App = nextParams.Version.App

		// Change results from this height but only applies to the next height.
		lastHeightParamsChanged = header.Height + 1
	}

	nextVersion := state.Version

	// NOTE: the AppHash and the VoteExtension has not been populated.
	// It will be filled on state.Save.
	return State{
		Version:                          nextVersion,
		ChainID:                          state.ChainID,
		InitialHeight:                    state.InitialHeight,
		LastBlockHeight:                  header.Height,
		LastBlockID:                      blockID,
		LastBlockTime:                    header.Time,
		NextValidators:                   nValSet,
		Validators:                       state.NextValidators.Copy(),
		LastValidators:                   state.Validators.Copy(),
		LastHeightValidatorsChanged:      lastHeightValsChanged,
		ConsensusParams:                  nextParams,
		LastHeightConsensusParamsChanged: lastHeightParamsChanged,
		LastResultsHash:                  TxResultsHash(abciResponse.TxResults),
		AppHash:                          nil,
		TimeoutCommit:                    abciResponse.TimeoutInfo.TimeoutCommit,
		TimeoutPropose:                   abciResponse.TimeoutInfo.TimeoutPropose,
	}, nil
}

// Fire NewBlock, NewBlockHeader.
// Fire TxEvent for every tx.
// NOTE: if CometBFT crashes before commit, some or all of these events may be published again.
func fireEvents(
	logger log.Logger,
	eventBus types.BlockEventPublisher,
	block *types.Block,
	blockID types.BlockID,
	abciResponse *abci.ResponseFinalizeBlock,
	validatorUpdates []*types.Validator,
	currentValidators *types.ValidatorSet,
	lastCommit *types.Commit,
) {
	if err := eventBus.PublishEventNewBlock(types.EventDataNewBlock{
		Block:               block,
		BlockID:             blockID,
		ResultFinalizeBlock: *abciResponse,
	}); err != nil {
		logger.Error("failed publishing new block", "err", err)
	}

	if lastCommit != nil {
		err := eventBus.PublishEventSignedBlock(types.EventDataSignedBlock{
			Header:       block.Header,
			Commit:       *lastCommit,
			ValidatorSet: *currentValidators,
			Data:         block.Data,
		})
		if err != nil {
			logger.Error("failed publishing new signed block", "err", err)
		}
	}

	if err := eventBus.PublishEventNewBlockHeader(types.EventDataNewBlockHeader{
		Header: block.Header,
	}); err != nil {
		logger.Error("failed publishing new block header", "err", err)
	}

	if err := eventBus.PublishEventNewBlockEvents(types.EventDataNewBlockEvents{
		Height: block.Height,
		Events: abciResponse.Events,
		NumTxs: int64(len(block.Txs)),
	}); err != nil {
		logger.Error("failed publishing new block events", "err", err)
	}

	if len(block.Evidence.Evidence) != 0 {
		for _, ev := range block.Evidence.Evidence {
			if err := eventBus.PublishEventNewEvidence(types.EventDataNewEvidence{
				Evidence: ev,
				Height:   block.Height,
			}); err != nil {
				logger.Error("failed publishing new evidence", "err", err)
			}
		}
	}

	for i, tx := range block.Data.Txs { //nolint:staticcheck
		blobTx, isBlobTx := types.UnmarshalBlobTx(tx)
		if isBlobTx {
			tx = blobTx.Tx
		}
		if err := eventBus.PublishEventTx(types.EventDataTx{TxResult: abci.TxResult{
			Height: block.Height,
			Index:  uint32(i),
			Tx:     tx,
			Result: *(abciResponse.TxResults[i]),
		}}); err != nil {
			logger.Error("failed publishing event TX", "err", err)
		}
	}

	if len(validatorUpdates) > 0 {
		if err := eventBus.PublishEventValidatorSetUpdates(
			types.EventDataValidatorSetUpdates{ValidatorUpdates: validatorUpdates}); err != nil {
			logger.Error("failed publishing event", "err", err)
		}
	}
}

//----------------------------------------------------------------------------------------------------
// Execute block without state. TODO: eliminate

// ExecCommitBlock executes and commits a block on the proxyApp without validating or mutating the state.
// It returns the application root hash (result of abci.Commit).
func ExecCommitBlock(
	appConnConsensus proxy.AppConnConsensus,
	block *types.Block,
	logger log.Logger,
	store Store,
	initialHeight int64,
) ([]byte, error) {
	commitInfo := buildLastCommitInfoFromStore(block, store, initialHeight)
	pbHeader := block.Header.ToProto()

	resp, err := appConnConsensus.FinalizeBlock(context.TODO(), &abci.RequestFinalizeBlock{
		Hash:               block.Hash(),
		NextValidatorsHash: block.NextValidatorsHash,
		ProposerAddress:    block.ProposerAddress,
		Height:             block.Height,
		Time:               block.Time,
		DecidedLastCommit:  commitInfo,
		Misbehavior:        block.Evidence.Evidence.ToABCI(),
		Txs:                block.Txs.ToSliceOfBytes(),

		// needed for v3 to sync with multiplexer as the header is stored in state
		Header: pbHeader,
	})
	if err != nil {
		logger.Error("error in proxyAppConn.FinalizeBlock", "err", err)
		return nil, err
	}

	// Assert that the application correctly returned tx results for each of the transactions provided in the block
	if len(block.Data.Txs) != len(resp.TxResults) { //nolint:staticcheck
		return nil, fmt.Errorf("expected tx results length to match size of transactions in block. Expected %d, got %d", len(block.Data.Txs), len(resp.TxResults)) //nolint:staticcheck
	}

	logger.Info("executed block", "height", block.Height, "app_hash", fmt.Sprintf("%X", resp.AppHash))

	// Commit block
	_, err = appConnConsensus.Commit(context.TODO())
	if err != nil {
		logger.Error("client error during proxyAppConn.Commit", "err", err)
		return nil, err
	}

	// ResponseCommit has no error or log
	return resp.AppHash, nil
}

func (blockExec *BlockExecutor) pruneBlocks(retainHeight int64, state State) (uint64, error) {
	base := blockExec.blockStore.Base()
	if retainHeight <= base {
		return 0, nil
	}

	amountPruned, prunedHeaderHeight, err := blockExec.blockStore.PruneBlocks(retainHeight, state)
	if err != nil {
		return 0, fmt.Errorf("failed to prune block store: %w", err)
	}

	err = blockExec.Store().PruneStates(base, retainHeight, prunedHeaderHeight)
	if err != nil {
		return 0, fmt.Errorf("failed to prune state store: %w", err)
	}
	return amountPruned, nil
}

// getResponseCodes gets response codes from a list of ResponseDeliverTx.
func getResponseCodes(responses []*abci.ExecTxResult) []uint32 {
	responseCodes := make([]uint32, len(responses))
	for i, response := range responses {
		responseCodes[i] = response.Code
	}
	return responseCodes
}

// getLogs gets logs from a list of ResponseDeliverTx.
func getLogs(responses []*abci.ExecTxResult) []string {
	logs := make([]string, len(responses))
	for i, response := range responses {
		logs[i] = response.Log
	}
	return logs
}

// saveFailedProposalBlock saves a failed proposal block to the debug directory
func (blockExec *BlockExecutor) saveFailedProposalBlock(state State, block *types.Block, reason string) {
	if blockExec.rootDir == "" {
		blockExec.logger.Debug("no root directory configured, skipping failed proposal block save")
		return
	}

	debugDir := filepath.Join(blockExec.rootDir, "data", "debug")
	timestamp := time.Now().Format("20060102-150405.000")
	filename := fmt.Sprintf("%s-%d-%s-%s_failed_proposal.pb",
		state.ChainID,
		block.Height,
		timestamp,
		reason,
	)

	if err := types.SaveBlockToFile(debugDir, filename, block); err != nil {
		blockExec.logger.Error("failed to save failed proposal block", "err", err.Error(), "reason", reason)
	} else {
		blockExec.logger.Info("saved failed proposal block", "file", filepath.Join(debugDir, filename), "reason", reason)
	}
}
