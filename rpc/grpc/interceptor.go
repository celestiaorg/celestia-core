package coregrpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// heavyGRPCMethods are the gRPC methods whose responses can be large or whose
// cost scales with chain data. They share the same process-wide semaphore as
// the equivalent HTTP RPC routes so a burst over either transport can't exceed
// the combined budget.
//
// ValidatorSet is gated here even though the HTTP validators route is not: the
// gRPC method returns the full validator set for a height unpaginated, while the
// HTTP route paginates (max_per_page), so only the gRPC form is unbounded.
//
// SubscribeNewHeights is deliberately excluded: it is a long-lived subscription
// stream and holding a slot for its lifetime would starve the budget. Cheap or
// bounded methods (Ping, Status, BroadcastTx, Commit) are excluded as well.
var heavyGRPCMethods = map[string]struct{}{
	"/tendermint.rpc.grpc.BlockAPI/BlockByHash":                 {},
	"/tendermint.rpc.grpc.BlockAPI/BlockByHeight":               {},
	"/tendermint.rpc.grpc.BlockAPI/ValidatorSet":                {},
	"/tendermint.rpc.grpc.BlobstreamAPI/DataRootInclusionProof": {},
}

// errHeavyGRPCLimit is returned when the shared heavy-request budget is
// saturated. ResourceExhausted maps to HTTP 503 and signals clients to back off
// and retry, mirroring the HTTP RPC handlers.
var errHeavyGRPCLimit = status.Error(
	codes.ResourceExhausted,
	"server busy: too many concurrent heavy RPC requests, retry later",
)

// heavyUnaryInterceptor bounds concurrent heavy unary responses using the
// shared semaphore. A nil semaphore (limit disabled) is a no-op.
func heavyUnaryInterceptor(sem chan struct{}) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		release, ok := acquireHeavy(sem, info.FullMethod)
		if !ok {
			return nil, errHeavyGRPCLimit
		}
		defer release()
		return handler(ctx, req)
	}
}

// heavyStreamInterceptor bounds concurrent heavy streaming responses (e.g.
// BlockByHash/BlockByHeight, which stream a full block) using the shared
// semaphore. The slot is held for the duration of the stream, since the block
// data lives until the stream completes.
func heavyStreamInterceptor(sem chan struct{}) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		release, ok := acquireHeavy(sem, info.FullMethod)
		if !ok {
			return errHeavyGRPCLimit
		}
		defer release()
		return handler(srv, ss)
	}
}

// acquireHeavy performs a non-blocking acquire of the shared slot for heavy
// methods. Non-heavy methods (and a nil semaphore) always admit with a no-op
// release. When the semaphore is full it returns ok == false so the caller can
// reject the request instead of queueing.
func acquireHeavy(sem chan struct{}, fullMethod string) (release func(), ok bool) {
	if sem == nil {
		return func() {}, true
	}
	if _, heavy := heavyGRPCMethods[fullMethod]; !heavy {
		return func() {}, true
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		return nil, false
	}
}
