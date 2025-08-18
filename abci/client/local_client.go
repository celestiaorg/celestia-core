package abcicli

import (
	"context"

	types "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/service"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
)

type localClient struct {
	service.BaseService

	types.Application
	Callback
}

var _ Client = (*localClient)(nil)

// NewLocalClient creates a local client, which wraps the application interface that
// Tendermint as the client will call to the application as the server.
func NewLocalClient(mtx *cmtsync.Mutex, app types.Application) Client {
	cli := &localClient{
		Application: app,
	}
	cli.BaseService = *service.NewBaseService(nil, "localClient", cli)
	return cli
}

func (app *localClient) SetResponseCallback(cb Callback) {
	app.Callback = cb
}

func (app *localClient) CheckTxAsync(ctx context.Context, req *types.RequestCheckTx) (*ReqRes, error) {
	res, err := app.Application.CheckTx(ctx, req)
	if err != nil {
		return nil, err
	}
	return app.callback(
		types.ToRequestCheckTx(req),
		types.ToResponseCheckTx(res),
	), nil
}

func (app *localClient) callback(req *types.Request, res *types.Response) *ReqRes {
	app.Callback(req, res)
	rr := newLocalReqRes(req, res)
	rr.callbackInvoked = true
	return rr
}

func newLocalReqRes(req *types.Request, res *types.Response) *ReqRes {
	reqRes := NewReqRes(req)
	reqRes.Response = res
	return reqRes
}

//-------------------------------------------------------

func (app *localClient) Error() error {
	return nil
}

func (app *localClient) Flush(context.Context) error {
	return nil
}

func (app *localClient) Echo(_ context.Context, msg string) (*types.ResponseEcho, error) {
	return &types.ResponseEcho{Message: msg}, nil
}

func (app *localClient) Info(ctx context.Context, req *types.RequestInfo) (*types.ResponseInfo, error) {
	return app.Application.Info(ctx, req)
}

func (app *localClient) CheckTx(ctx context.Context, req *types.RequestCheckTx) (*types.ResponseCheckTx, error) {
	return app.Application.CheckTx(ctx, req)
}

func (app *localClient) Query(ctx context.Context, req *types.RequestQuery) (*types.ResponseQuery, error) {
	return app.Application.Query(ctx, req)
}

func (app *localClient) Commit(ctx context.Context, req *types.RequestCommit) (*types.ResponseCommit, error) {
	return app.Application.Commit(ctx, req)
}

func (app *localClient) InitChain(ctx context.Context, req *types.RequestInitChain) (*types.ResponseInitChain, error) {
	return app.Application.InitChain(ctx, req)
}

func (app *localClient) ListSnapshots(ctx context.Context, req *types.RequestListSnapshots) (*types.ResponseListSnapshots, error) {
	return app.Application.ListSnapshots(ctx, req)
}

func (app *localClient) OfferSnapshot(ctx context.Context, req *types.RequestOfferSnapshot) (*types.ResponseOfferSnapshot, error) {
	return app.Application.OfferSnapshot(ctx, req)
}

func (app *localClient) LoadSnapshotChunk(ctx context.Context, req *types.RequestLoadSnapshotChunk) (*types.ResponseLoadSnapshotChunk, error) {
	return app.Application.LoadSnapshotChunk(ctx, req)
}

func (app *localClient) ApplySnapshotChunk(ctx context.Context, req *types.RequestApplySnapshotChunk) (*types.ResponseApplySnapshotChunk, error) {
	return app.Application.ApplySnapshotChunk(ctx, req)
}

func (app *localClient) PrepareProposal(ctx context.Context, req *types.RequestPrepareProposal) (*types.ResponsePrepareProposal, error) {
	return app.Application.PrepareProposal(ctx, req)
}

func (app *localClient) ProcessProposal(ctx context.Context, req *types.RequestProcessProposal) (*types.ResponseProcessProposal, error) {
	return app.Application.ProcessProposal(ctx, req)
}

func (app *localClient) ExtendVote(ctx context.Context, req *types.RequestExtendVote) (*types.ResponseExtendVote, error) {
	return app.Application.ExtendVote(ctx, req)
}

func (app *localClient) VerifyVoteExtension(ctx context.Context, req *types.RequestVerifyVoteExtension) (*types.ResponseVerifyVoteExtension, error) {
	return app.Application.VerifyVoteExtension(ctx, req)
}

func (app *localClient) FinalizeBlock(ctx context.Context, req *types.RequestFinalizeBlock) (*types.ResponseFinalizeBlock, error) {
	return app.Application.FinalizeBlock(ctx, req)
}
