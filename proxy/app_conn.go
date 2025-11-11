package proxy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/kit/metrics"

	abcicli "github.com/cometbft/cometbft/abci/client"
	"github.com/cometbft/cometbft/abci/types"
)

//go:generate ../scripts/mockery_generate.sh AppConnConsensus|AppConnMempool|AppConnQuery|AppConnSnapshot

// CommitMode represents the commit execution mode for the consensus connection.
type CommitMode int

const (
	// CommitModeImmediate executes commits immediately (default, used during consensus)
	CommitModeImmediate CommitMode = iota
	// CommitModeBatched queues commits and executes them together (used during blocksync)
	CommitModeBatched
)

//----------------------------------------------------------------------------------------
// Enforce which abci msgs can be sent on a connection at the type level

type AppConnConsensus interface {
	Error() error
	InitChain(context.Context, *types.RequestInitChain) (*types.ResponseInitChain, error)
	PrepareProposal(context.Context, *types.RequestPrepareProposal) (*types.ResponsePrepareProposal, error)
	ProcessProposal(context.Context, *types.RequestProcessProposal) (*types.ResponseProcessProposal, error)
	ExtendVote(context.Context, *types.RequestExtendVote) (*types.ResponseExtendVote, error)
	VerifyVoteExtension(context.Context, *types.RequestVerifyVoteExtension) (*types.ResponseVerifyVoteExtension, error)
	FinalizeBlock(context.Context, *types.RequestFinalizeBlock) (*types.ResponseFinalizeBlock, error)
	Commit(context.Context) (*types.ResponseCommit, error)

	// Batched commit mode for blocksync optimization
	SetCommitMode(mode CommitMode) error
	FlushCommitBatch(context.Context) error
}

type AppConnMempool interface {
	SetResponseCallback(abcicli.Callback)
	Error() error

	CheckTx(context.Context, *types.RequestCheckTx) (*types.ResponseCheckTx, error)
	CheckTxAsync(context.Context, *types.RequestCheckTx) (*abcicli.ReqRes, error)
	Flush(context.Context) error
	QuerySequence(context.Context, *types.RequestQuerySequence) (*types.ResponseQuerySequence, error)
}

type AppConnQuery interface {
	Error() error

	Echo(context.Context, string) (*types.ResponseEcho, error)
	Info(context.Context, *types.RequestInfo) (*types.ResponseInfo, error)
	Query(context.Context, *types.RequestQuery) (*types.ResponseQuery, error)
}

type AppConnSnapshot interface {
	Error() error

	ListSnapshots(context.Context, *types.RequestListSnapshots) (*types.ResponseListSnapshots, error)
	OfferSnapshot(context.Context, *types.RequestOfferSnapshot) (*types.ResponseOfferSnapshot, error)
	LoadSnapshotChunk(context.Context, *types.RequestLoadSnapshotChunk) (*types.ResponseLoadSnapshotChunk, error)
	ApplySnapshotChunk(context.Context, *types.RequestApplySnapshotChunk) (*types.ResponseApplySnapshotChunk, error)
}

//-----------------------------------------------------------------------------------------
// Implements AppConnConsensus (subset of abcicli.Client)

type appConnConsensus struct {
	metrics *Metrics
	appConn abcicli.Client

	// Batched commit mode for blocksync optimization
	commitMode       CommitMode // CommitModeImmediate or CommitModeBatched
	hasPendingCommit bool       // true if at least one Commit() was called in batched mode
	commitMu         sync.Mutex
}

var _ AppConnConsensus = (*appConnConsensus)(nil)

func NewAppConnConsensus(appConn abcicli.Client, metrics *Metrics) AppConnConsensus {
	return &appConnConsensus{
		metrics:    metrics,
		appConn:    appConn,
		commitMode: CommitModeImmediate, // Default to immediate commits
	}
}

func (app *appConnConsensus) Error() error {
	return app.appConn.Error()
}

func (app *appConnConsensus) InitChain(ctx context.Context, req *types.RequestInitChain) (*types.ResponseInitChain, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "init_chain", "type", "sync"))()
	return app.appConn.InitChain(ctx, req)
}

func (app *appConnConsensus) PrepareProposal(ctx context.Context,
	req *types.RequestPrepareProposal) (*types.ResponsePrepareProposal, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "prepare_proposal", "type", "sync"))()
	return app.appConn.PrepareProposal(ctx, req)
}

func (app *appConnConsensus) ProcessProposal(ctx context.Context, req *types.RequestProcessProposal) (*types.ResponseProcessProposal, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "process_proposal", "type", "sync"))()
	return app.appConn.ProcessProposal(ctx, req)
}

func (app *appConnConsensus) ExtendVote(ctx context.Context, req *types.RequestExtendVote) (*types.ResponseExtendVote, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "extend_vote", "type", "sync"))()
	return app.appConn.ExtendVote(ctx, req)
}

func (app *appConnConsensus) VerifyVoteExtension(ctx context.Context, req *types.RequestVerifyVoteExtension) (*types.ResponseVerifyVoteExtension, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "verify_vote_extension", "type", "sync"))()
	return app.appConn.VerifyVoteExtension(ctx, req)
}

func (app *appConnConsensus) FinalizeBlock(ctx context.Context, req *types.RequestFinalizeBlock) (*types.ResponseFinalizeBlock, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "finalize_block", "type", "sync"))()
	return app.appConn.FinalizeBlock(ctx, req)
}

func (app *appConnConsensus) Commit(ctx context.Context) (*types.ResponseCommit, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "commit", "type", "sync"))()

	app.commitMu.Lock()
	mode := app.commitMode
	app.commitMu.Unlock()

	if mode == CommitModeBatched {
		// In batched mode, just mark that we have a pending commit
		// We only commit ONCE per batch, not once per block
		// The app's finalizeBlockState accumulates changes from all FinalizeBlock calls
		app.commitMu.Lock()
		app.hasPendingCommit = true
		app.commitMu.Unlock()

		// Return empty response immediately - actual commit happens in FlushCommitBatch
		return &types.ResponseCommit{}, nil
	}

	// Immediate mode - execute commit right away
	return app.appConn.Commit(ctx, &types.RequestCommit{})
}

// SetCommitMode sets the commit mode to either CommitModeImmediate or CommitModeBatched.
// In batched mode, Commit() calls are queued and executed together when FlushCommitBatch() is called.
func (app *appConnConsensus) SetCommitMode(mode CommitMode) error {
	if mode != CommitModeImmediate && mode != CommitModeBatched {
		return fmt.Errorf("invalid commit mode: %d (must be CommitModeImmediate or CommitModeBatched)", mode)
	}

	app.commitMu.Lock()
	defer app.commitMu.Unlock()

	app.commitMode = mode
	return nil
}

// FlushCommitBatch executes the batched commit.
// In batched mode, all FinalizeBlock calls accumulate changes in the app's finalizeBlockState.
// This method commits that accumulated state once.
func (app *appConnConsensus) FlushCommitBatch(ctx context.Context) error {
	app.commitMu.Lock()
	hasPending := app.hasPendingCommit
	app.hasPendingCommit = false
	app.commitMu.Unlock()

	if !hasPending {
		// No commits were queued, nothing to do
		return nil
	}

	// Execute the single commit for the entire batch
	// This commits the app's finalizeBlockState which has accumulated changes from all FinalizeBlock calls
	_, err := app.appConn.Commit(ctx, &types.RequestCommit{})
	if err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	return nil
}

//------------------------------------------------
// Implements AppConnMempool (subset of abcicli.Client)

type appConnMempool struct {
	metrics *Metrics
	appConn abcicli.Client
}

func NewAppConnMempool(appConn abcicli.Client, metrics *Metrics) AppConnMempool {
	return &appConnMempool{
		metrics: metrics,
		appConn: appConn,
	}
}

func (app *appConnMempool) SetResponseCallback(cb abcicli.Callback) {
	app.appConn.SetResponseCallback(cb)
}

func (app *appConnMempool) Error() error {
	return app.appConn.Error()
}

func (app *appConnMempool) Flush(ctx context.Context) error {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "flush", "type", "sync"))()
	return app.appConn.Flush(ctx)
}

func (app *appConnMempool) CheckTx(ctx context.Context, req *types.RequestCheckTx) (*types.ResponseCheckTx, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "check_tx", "type", "sync"))()
	return app.appConn.CheckTx(ctx, req)
}

func (app *appConnMempool) CheckTxAsync(ctx context.Context, req *types.RequestCheckTx) (*abcicli.ReqRes, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "check_tx", "type", "async"))()
	return app.appConn.CheckTxAsync(ctx, req)
}

func (app *appConnMempool) QuerySequence(ctx context.Context, req *types.RequestQuerySequence) (*types.ResponseQuerySequence, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "query_sequence", "type", "sync"))()
	return app.appConn.QuerySequence(ctx, req)
}

//------------------------------------------------
// Implements AppConnQuery (subset of abcicli.Client)

type appConnQuery struct {
	metrics *Metrics
	appConn abcicli.Client
}

func NewAppConnQuery(appConn abcicli.Client, metrics *Metrics) AppConnQuery {
	return &appConnQuery{
		metrics: metrics,
		appConn: appConn,
	}
}

func (app *appConnQuery) Error() error {
	return app.appConn.Error()
}

func (app *appConnQuery) Echo(ctx context.Context, msg string) (*types.ResponseEcho, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "echo", "type", "sync"))()
	return app.appConn.Echo(ctx, msg)
}

func (app *appConnQuery) Info(ctx context.Context, req *types.RequestInfo) (*types.ResponseInfo, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "info", "type", "sync"))()
	return app.appConn.Info(ctx, req)
}

func (app *appConnQuery) Query(ctx context.Context, req *types.RequestQuery) (*types.ResponseQuery, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "query", "type", "sync"))()
	return app.appConn.Query(ctx, req)
}

//------------------------------------------------
// Implements AppConnSnapshot (subset of abcicli.Client)

type appConnSnapshot struct {
	metrics *Metrics
	appConn abcicli.Client
}

func NewAppConnSnapshot(appConn abcicli.Client, metrics *Metrics) AppConnSnapshot {
	return &appConnSnapshot{
		metrics: metrics,
		appConn: appConn,
	}
}

func (app *appConnSnapshot) Error() error {
	return app.appConn.Error()
}

func (app *appConnSnapshot) ListSnapshots(ctx context.Context, req *types.RequestListSnapshots) (*types.ResponseListSnapshots, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "list_snapshots", "type", "sync"))()
	return app.appConn.ListSnapshots(ctx, req)
}

func (app *appConnSnapshot) OfferSnapshot(ctx context.Context, req *types.RequestOfferSnapshot) (*types.ResponseOfferSnapshot, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "offer_snapshot", "type", "sync"))()
	return app.appConn.OfferSnapshot(ctx, req)
}

func (app *appConnSnapshot) LoadSnapshotChunk(ctx context.Context, req *types.RequestLoadSnapshotChunk) (*types.ResponseLoadSnapshotChunk, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "load_snapshot_chunk", "type", "sync"))()
	return app.appConn.LoadSnapshotChunk(ctx, req)
}

func (app *appConnSnapshot) ApplySnapshotChunk(ctx context.Context, req *types.RequestApplySnapshotChunk) (*types.ResponseApplySnapshotChunk, error) {
	defer addTimeSample(app.metrics.MethodTimingSeconds.With("method", "apply_snapshot_chunk", "type", "sync"))()
	return app.appConn.ApplySnapshotChunk(ctx, req)
}

// addTimeSample returns a function that, when called, adds an observation to m.
// The observation added to m is the number of seconds ellapsed since addTimeSample
// was initially called. addTimeSample is meant to be called in a defer to calculate
// the amount of time a function takes to complete.
func addTimeSample(m metrics.Histogram) func() {
	start := time.Now()
	return func() { m.Observe(time.Since(start).Seconds()) }
}
