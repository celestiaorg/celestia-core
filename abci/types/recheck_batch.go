package types

import (
	"context"
	"errors"
)

// RequestRecheckTxs asks an in-process application to recheck the mempool in
// one call. It is a Go-level request with no wire encoding.
type RequestRecheckTxs struct {
	// Txs are in the mempool's recheck order. The verdicts and check state
	// must equal those of CheckTx(Type=Recheck) on each tx in this order.
	Txs [][]byte
}

// ResponseRecheckTxs answers a RequestRecheckTxs.
type ResponseRecheckTxs struct {
	// Responses is aligned with Txs; a nil entry leaves that tx in the mempool.
	Responses []*ResponseCheckTx
}

// RecheckTxsApplication is implemented by applications, and the clients that
// forward to them, that can recheck a batch of txs in one call. It is optional.
type RecheckTxsApplication interface {
	RecheckTxs(context.Context, *RequestRecheckTxs) (*ResponseRecheckTxs, error)
}

// ErrRecheckTxsUnsupported means the batch did not run and changed nothing, so
// the caller can recheck one tx at a time instead.
var ErrRecheckTxsUnsupported = errors.New("abci: RecheckTxs not supported")
