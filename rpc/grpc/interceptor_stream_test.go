package coregrpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// streamingBlockAPI streams many block parts from BlockByHeight and
// answers ValidatorSet, a heavy unary method.
type streamingBlockAPI struct {
	UnimplementedBlockAPIServer
}

func (*streamingBlockAPI) BlockByHeight(_ *BlockByHeightRequest, stream BlockAPI_BlockByHeightServer) error {
	part := &cmtproto.Part{Bytes: make([]byte, 64<<10)}
	for i := 0; i < 200; i++ {
		if err := stream.Send(&BlockByHeightResponse{BlockPart: part}); err != nil {
			return err
		}
	}
	return nil
}

func (*streamingBlockAPI) ValidatorSet(context.Context, *ValidatorSetRequest) (*ValidatorSetResponse, error) {
	return &ValidatorSetResponse{}, nil
}

// startHeavyTestServer starts a gRPC server wired with the heavy-request
// interceptors the way StartGRPCServer wires them, and returns a client
// connection to it and the stream semaphore.
func startHeavyTestServer(t *testing.T, sem chan struct{}, stallTimeout time.Duration) (*grpc.ClientConn, chan struct{}) {
	t.Helper()
	streamSem := newHeavyStreamSem(sem)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(heavyUnaryInterceptor(sem)),
		grpc.StreamInterceptor(heavyStreamInterceptor(streamSem, stallTimeout)),
	)
	RegisterBlockAPIServer(srv, &streamingBlockAPI{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	cc, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return cc, streamSem
}

// TestHeavyStreamStalledClientReleasesSlot verifies that a block stream whose
// client never sends its request, or stops reading the response, is canceled
// after the stall timeout and its slot released, and that such streams never
// occupy the shared heavy budget used by unary and HTTP heavy requests.
func TestHeavyStreamStalledClientReleasesSlot(t *testing.T) {
	const stallTimeout = 300 * time.Millisecond

	testCases := map[string]func(t *testing.T, cc *grpc.ClientConn){
		"request never sent": func(t *testing.T, cc *grpc.ClientConn) {
			_, err := cc.NewStream(t.Context(), &_BlockAPI_serviceDesc.Streams[1], heavyMethod)
			require.NoError(t, err)
		},
		"response never read": func(t *testing.T, cc *grpc.ClientConn) {
			stream, err := NewBlockAPIClient(cc).BlockByHeight(t.Context(), &BlockByHeightRequest{Height: 1})
			require.NoError(t, err)
			_, err = stream.Recv() // read one part, then stall
			require.NoError(t, err)
		},
	}
	for name, stall := range testCases {
		t.Run(name, func(t *testing.T) {
			sem := make(chan struct{}, 1)
			cc, streamSem := startHeavyTestServer(t, sem, stallTimeout)

			stall(t, cc)
			require.Eventually(t, func() bool { return len(streamSem) == 1 }, 5*time.Second, 5*time.Millisecond,
				"the stalled stream must hold a stream slot")
			require.Empty(t, sem, "a stream must not hold a shared heavy slot")

			// Heavy unary requests are still served while the stream stalls.
			_, err := NewBlockAPIClient(cc).ValidatorSet(t.Context(), &ValidatorSetRequest{Height: 1})
			require.NoError(t, err)

			// The stalled stream is canceled and its slot freed.
			require.Eventually(t, func() bool { return len(streamSem) == 0 }, 5*time.Second, 5*time.Millisecond,
				"the stalled stream must release its slot after the stall timeout")
		})
	}
}

// TestHeavyStreamCompletesWithinStallTimeout verifies that a client reading
// the stream normally receives the whole stream even when it takes longer in
// total than the per-message stall timeout.
func TestHeavyStreamCompletesWithinStallTimeout(t *testing.T) {
	sem := make(chan struct{}, 1)
	const stallTimeout = time.Second
	cc, streamSem := startHeavyTestServer(t, sem, stallTimeout)

	stream, err := NewBlockAPIClient(cc).BlockByHeight(t.Context(), &BlockByHeightRequest{Height: 1})
	require.NoError(t, err)
	n := 0
	start := time.Now()
	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
		n++
		if n%50 == 0 {
			time.Sleep(stallTimeout / 2)
		}
	}
	require.Equal(t, 200, n)
	require.Greater(t, time.Since(start), stallTimeout)
	require.Eventually(t, func() bool { return len(streamSem) == 0 }, 5*time.Second, 5*time.Millisecond)
}
