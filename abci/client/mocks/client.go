// Code generated by mockery. DO NOT EDIT.

package mocks

import (
	context "context"

	abcicli "github.com/cometbft/cometbft/abci/client"

	log "github.com/cometbft/cometbft/libs/log"

	mock "github.com/stretchr/testify/mock"

	types "github.com/cometbft/cometbft/abci/types"
)

// Client is an autogenerated mock type for the Client type
type Client struct {
	mock.Mock
}

// ApplySnapshotChunk provides a mock function with given fields: _a0, _a1
func (_m *Client) ApplySnapshotChunk(_a0 context.Context, _a1 *types.RequestApplySnapshotChunk) (*types.ResponseApplySnapshotChunk, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for ApplySnapshotChunk")
	}

	var r0 *types.ResponseApplySnapshotChunk
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestApplySnapshotChunk) (*types.ResponseApplySnapshotChunk, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestApplySnapshotChunk) *types.ResponseApplySnapshotChunk); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseApplySnapshotChunk)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestApplySnapshotChunk) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// CheckTx provides a mock function with given fields: _a0, _a1
func (_m *Client) CheckTx(_a0 context.Context, _a1 *types.RequestCheckTx) (*types.ResponseCheckTx, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for CheckTx")
	}

	var r0 *types.ResponseCheckTx
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestCheckTx) (*types.ResponseCheckTx, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestCheckTx) *types.ResponseCheckTx); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseCheckTx)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestCheckTx) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// CheckTxAsync provides a mock function with given fields: _a0, _a1
func (_m *Client) CheckTxAsync(_a0 context.Context, _a1 *types.RequestCheckTx) (*abcicli.ReqRes, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for CheckTxAsync")
	}

	var r0 *abcicli.ReqRes
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestCheckTx) (*abcicli.ReqRes, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestCheckTx) *abcicli.ReqRes); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*abcicli.ReqRes)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestCheckTx) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Commit provides a mock function with given fields: _a0, _a1
func (_m *Client) Commit(_a0 context.Context, _a1 *types.RequestCommit) (*types.ResponseCommit, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for Commit")
	}

	var r0 *types.ResponseCommit
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestCommit) (*types.ResponseCommit, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestCommit) *types.ResponseCommit); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseCommit)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestCommit) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Echo provides a mock function with given fields: _a0, _a1
func (_m *Client) Echo(_a0 context.Context, _a1 string) (*types.ResponseEcho, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for Echo")
	}

	var r0 *types.ResponseEcho
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, string) (*types.ResponseEcho, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, string) *types.ResponseEcho); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseEcho)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, string) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Error provides a mock function with no fields
func (_m *Client) Error() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Error")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// ExtendVote provides a mock function with given fields: _a0, _a1
func (_m *Client) ExtendVote(_a0 context.Context, _a1 *types.RequestExtendVote) (*types.ResponseExtendVote, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for ExtendVote")
	}

	var r0 *types.ResponseExtendVote
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestExtendVote) (*types.ResponseExtendVote, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestExtendVote) *types.ResponseExtendVote); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseExtendVote)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestExtendVote) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// FinalizeBlock provides a mock function with given fields: _a0, _a1
func (_m *Client) FinalizeBlock(_a0 context.Context, _a1 *types.RequestFinalizeBlock) (*types.ResponseFinalizeBlock, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for FinalizeBlock")
	}

	var r0 *types.ResponseFinalizeBlock
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestFinalizeBlock) (*types.ResponseFinalizeBlock, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestFinalizeBlock) *types.ResponseFinalizeBlock); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseFinalizeBlock)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestFinalizeBlock) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Flush provides a mock function with given fields: _a0
func (_m *Client) Flush(_a0 context.Context) error {
	ret := _m.Called(_a0)

	if len(ret) == 0 {
		panic("no return value specified for Flush")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context) error); ok {
		r0 = rf(_a0)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Info provides a mock function with given fields: _a0, _a1
func (_m *Client) Info(_a0 context.Context, _a1 *types.RequestInfo) (*types.ResponseInfo, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for Info")
	}

	var r0 *types.ResponseInfo
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestInfo) (*types.ResponseInfo, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestInfo) *types.ResponseInfo); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseInfo)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestInfo) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// InitChain provides a mock function with given fields: _a0, _a1
func (_m *Client) InitChain(_a0 context.Context, _a1 *types.RequestInitChain) (*types.ResponseInitChain, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for InitChain")
	}

	var r0 *types.ResponseInitChain
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestInitChain) (*types.ResponseInitChain, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestInitChain) *types.ResponseInitChain); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseInitChain)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestInitChain) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// IsRunning provides a mock function with no fields
func (_m *Client) IsRunning() bool {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for IsRunning")
	}

	var r0 bool
	if rf, ok := ret.Get(0).(func() bool); ok {
		r0 = rf()
	} else {
		r0 = ret.Get(0).(bool)
	}

	return r0
}

// ListSnapshots provides a mock function with given fields: _a0, _a1
func (_m *Client) ListSnapshots(_a0 context.Context, _a1 *types.RequestListSnapshots) (*types.ResponseListSnapshots, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for ListSnapshots")
	}

	var r0 *types.ResponseListSnapshots
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestListSnapshots) (*types.ResponseListSnapshots, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestListSnapshots) *types.ResponseListSnapshots); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseListSnapshots)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestListSnapshots) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// LoadSnapshotChunk provides a mock function with given fields: _a0, _a1
func (_m *Client) LoadSnapshotChunk(_a0 context.Context, _a1 *types.RequestLoadSnapshotChunk) (*types.ResponseLoadSnapshotChunk, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for LoadSnapshotChunk")
	}

	var r0 *types.ResponseLoadSnapshotChunk
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestLoadSnapshotChunk) (*types.ResponseLoadSnapshotChunk, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestLoadSnapshotChunk) *types.ResponseLoadSnapshotChunk); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseLoadSnapshotChunk)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestLoadSnapshotChunk) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// OfferSnapshot provides a mock function with given fields: _a0, _a1
func (_m *Client) OfferSnapshot(_a0 context.Context, _a1 *types.RequestOfferSnapshot) (*types.ResponseOfferSnapshot, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for OfferSnapshot")
	}

	var r0 *types.ResponseOfferSnapshot
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestOfferSnapshot) (*types.ResponseOfferSnapshot, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestOfferSnapshot) *types.ResponseOfferSnapshot); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseOfferSnapshot)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestOfferSnapshot) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// OnReset provides a mock function with no fields
func (_m *Client) OnReset() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for OnReset")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// OnStart provides a mock function with no fields
func (_m *Client) OnStart() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for OnStart")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// OnStop provides a mock function with no fields
func (_m *Client) OnStop() {
	_m.Called()
}

// PrepareProposal provides a mock function with given fields: _a0, _a1
func (_m *Client) PrepareProposal(_a0 context.Context, _a1 *types.RequestPrepareProposal) (*types.ResponsePrepareProposal, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for PrepareProposal")
	}

	var r0 *types.ResponsePrepareProposal
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestPrepareProposal) (*types.ResponsePrepareProposal, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestPrepareProposal) *types.ResponsePrepareProposal); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponsePrepareProposal)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestPrepareProposal) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// ProcessProposal provides a mock function with given fields: _a0, _a1
func (_m *Client) ProcessProposal(_a0 context.Context, _a1 *types.RequestProcessProposal) (*types.ResponseProcessProposal, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for ProcessProposal")
	}

	var r0 *types.ResponseProcessProposal
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestProcessProposal) (*types.ResponseProcessProposal, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestProcessProposal) *types.ResponseProcessProposal); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseProcessProposal)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestProcessProposal) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Query provides a mock function with given fields: _a0, _a1
func (_m *Client) Query(_a0 context.Context, _a1 *types.RequestQuery) (*types.ResponseQuery, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for Query")
	}

	var r0 *types.ResponseQuery
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestQuery) (*types.ResponseQuery, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestQuery) *types.ResponseQuery); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseQuery)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestQuery) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Quit provides a mock function with no fields
func (_m *Client) Quit() <-chan struct{} {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Quit")
	}

	var r0 <-chan struct{}
	if rf, ok := ret.Get(0).(func() <-chan struct{}); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(<-chan struct{})
		}
	}

	return r0
}

// Reset provides a mock function with no fields
func (_m *Client) Reset() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Reset")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SetLogger provides a mock function with given fields: _a0
func (_m *Client) SetLogger(_a0 log.Logger) {
	_m.Called(_a0)
}

// SetResponseCallback provides a mock function with given fields: _a0
func (_m *Client) SetResponseCallback(_a0 abcicli.Callback) {
	_m.Called(_a0)
}

// Start provides a mock function with no fields
func (_m *Client) Start() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Start")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Stop provides a mock function with no fields
func (_m *Client) Stop() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Stop")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// String provides a mock function with no fields
func (_m *Client) String() string {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for String")
	}

	var r0 string
	if rf, ok := ret.Get(0).(func() string); ok {
		r0 = rf()
	} else {
		r0 = ret.Get(0).(string)
	}

	return r0
}

// VerifyVoteExtension provides a mock function with given fields: _a0, _a1
func (_m *Client) VerifyVoteExtension(_a0 context.Context, _a1 *types.RequestVerifyVoteExtension) (*types.ResponseVerifyVoteExtension, error) {
	ret := _m.Called(_a0, _a1)

	if len(ret) == 0 {
		panic("no return value specified for VerifyVoteExtension")
	}

	var r0 *types.ResponseVerifyVoteExtension
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestVerifyVoteExtension) (*types.ResponseVerifyVoteExtension, error)); ok {
		return rf(_a0, _a1)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *types.RequestVerifyVoteExtension) *types.ResponseVerifyVoteExtension); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*types.ResponseVerifyVoteExtension)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, *types.RequestVerifyVoteExtension) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewClient creates a new instance of Client. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewClient(t interface {
	mock.TestingT
	Cleanup(func())
}) *Client {
	mock := &Client{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
