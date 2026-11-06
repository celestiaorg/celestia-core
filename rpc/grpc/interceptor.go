package coregrpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// heavyGRPCMethods are the gRPC methods whose responses can be large or whose
// cost scales with chain data. They share the same process-wide semaphore as
// the equivalent HTTP RPC routes so a burst over either transport can't exceed
// the combined budget. The streaming ones (BlockByHash, BlockByHeight) are
// bounded by heavyStreamStallTimeout so a stalled client can't hold a slot
// indefinitely.
//
// ValidatorSet is gated here even though the HTTP validators route is not: the
// gRPC method returns the full validator set for a height unpaginated, while the
// HTTP route paginates (max_per_page), so only the gRPC form is unbounded.
//
// SubscribeNewHeights is deliberately excluded: it is a long-lived subscription
// stream and holding a slot for its lifetime would starve the budget; it is
// bounded separately by MaxHeightListeners (see BlockAPI.addHeightListener). Cheap or
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
		release, ok := acquireHeavyFn(sem, info.FullMethod)
		if !ok {
			return nil, errHeavyGRPCLimit
		}
		defer release()
		return handler(ctx, req)
	}
}

// heavyStreamStallTimeout bounds how long a single receive or send on a heavy
// stream may block. A client that does not send its request or stops reading
// the stream for longer has the stream canceled, which frees its slot.
const heavyStreamStallTimeout = 30 * time.Second

// heavyStreamInterceptor bounds concurrent heavy streaming responses (e.g.
// BlockByHash/BlockByHeight, which stream a block part by part) using the
// shared semaphore. The slot is held for the duration of the stream; each
// receive or send on the stream must complete within stallTimeout, otherwise
// the stream is canceled and the slot released.
func heavyStreamInterceptor(sem chan struct{}, stallTimeout time.Duration) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if _, heavy := heavyGRPCMethods[info.FullMethod]; !heavy {
			return handler(srv, ss)
		}
		release, ok := acquireHeavyFn(sem, info.FullMethod)
		if !ok {
			return errHeavyGRPCLimit
		}
		defer release()
		return handler(srv, &stallBoundedStream{ServerStream: ss, timeout: stallTimeout})
	}
}

// errHeavyStreamStalled is returned when a heavy stream's client does not
// send its request or read the response in time.
var errHeavyStreamStalled = status.Error(
	codes.DeadlineExceeded,
	"heavy RPC stream stalled: client did not send or receive in time",
)

// stallBoundedStream bounds each RecvMsg and SendMsg on the wrapped stream by
// timeout. gRPC can't abort a blocked send or receive directly, so the call
// runs in a goroutine; on timeout an error is returned to the handler, whose
// return makes gRPC finish the stream, which cancels the stream context and
// unblocks the pending call.
type stallBoundedStream struct {
	grpc.ServerStream
	timeout time.Duration
}

func (s *stallBoundedStream) SendMsg(m interface{}) error {
	return s.bounded(func() error { return s.ServerStream.SendMsg(m) })
}

func (s *stallBoundedStream) RecvMsg(m interface{}) error {
	return s.bounded(func() error { return s.ServerStream.RecvMsg(m) })
}

func (s *stallBoundedStream) bounded(fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errHeavyStreamStalled
	}
}

// acquireHeavyFn performs a non-blocking acquire of the shared slot for heavy
// methods. Non-heavy methods (and a nil semaphore) always admit with a no-op
// release. When the semaphore is full it returns ok == false so the caller can
// reject the request instead of queueing.
func acquireHeavyFn(sem chan struct{}, fullMethod string) (release func(), ok bool) {
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
