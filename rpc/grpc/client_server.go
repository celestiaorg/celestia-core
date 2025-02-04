package coregrpc

import (
	"net"

	"github.com/tendermint/tendermint/rpc/core"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	cmtnet "github.com/tendermint/tendermint/libs/net"
)

// Config is an gRPC server configuration.
type Config struct {
	MaxOpenConnections int
}

// StartGRPCServer starts a new gRPC BroadcastAPIServer using the given
// net.Listener.
// NOTE: This function blocks - you may want to call it in a go-routine.
func StartGRPCServer(ln net.Listener) error {
	grpcServer := grpc.NewServer()
	RegisterBroadcastAPIServer(grpcServer, &broadcastAPI{})
	blockAPI := NewBlockAPI()
	RegisterBlockAPIServer(grpcServer, blockAPI)
	//blobstreamAPI := NewBlobstreamAPI()
	//RegisterBlobstreamAPIServer(grpcServer, blobstreamAPI)
	errCh := make(chan error, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		errCh <- blockAPI.StartNewBlockEventListener(ctx)
	}()
	go func() {
		errCh <- grpcServer.Serve(ln)
	}()
	defer grpcServer.GracefulStop()
	defer func(api *BlockAPI, ctx context.Context) {
		err := api.Stop(ctx)
		if err != nil {
			core.GetEnvironment().Logger.Error("error stopping block api", "err", err)
		}
	}(blockAPI, ctx)
	// blocks until one errors or returns nil
	return <-errCh
}

// StartGRPCClient dials the gRPC server using protoAddr and returns a new
// BroadcastAPIClient.
func StartGRPCClient(protoAddr string) BroadcastAPIClient {
	conn, err := grpc.Dial(protoAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(dialerFunc)) //nolint:staticcheck
	if err != nil {
		panic(err)
	}
	return NewBroadcastAPIClient(conn)
}

// StartBlockAPIGRPCClient dials the gRPC server using protoAddr and returns a new
// BlockAPIClient.
func StartBlockAPIGRPCClient(protoAddr string, opts ...grpc.DialOption) (BlockAPIClient, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, grpc.WithContextDialer(dialerFunc))
	conn, err := grpc.Dial( //nolint:staticcheck
		protoAddr,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	return NewBlockAPIClient(conn), nil
}

func dialerFunc(ctx context.Context, addr string) (net.Conn, error) {
	return cmtnet.Connect(addr)
}

// StartBlobstreamAPIGRPCClient dials the gRPC server using protoAddr and returns a new
// BlobstreamAPIClient.
func StartBlobstreamAPIGRPCClient(protoAddr string, opts ...grpc.DialOption) (BlobstreamAPIClient, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, grpc.WithContextDialer(dialerFunc))
	conn, err := grpc.Dial( //nolint:staticcheck
		protoAddr,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	return NewBlobstreamAPIClient(conn), nil
}
