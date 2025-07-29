package coregrpc

import (
	"context"
	"net"
	"regexp"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	cmtnet "github.com/cometbft/cometbft/libs/net"
	"github.com/cometbft/cometbft/rpc/core"
)

// Config is an gRPC server configuration.
//
// Deprecated: A new gRPC API will be introduced after v0.38.
type Config struct {
	MaxOpenConnections int
}

// StartGRPCServer starts a new gRPC BroadcastAPIServer using the given
// net.Listener.
// NOTE: This function blocks - you may want to call it in a go-routine.
//
// Deprecated: A new gRPC API will be introduced after v0.38.
func StartGRPCServer(env *core.Environment, ln net.Listener) error {
	grpcServer := grpc.NewServer()
	RegisterBroadcastAPIServer(grpcServer, &broadcastAPI{env: env})

	api := NewBlockAPI(env)
	RegisterBlockAPIServer(grpcServer, api)

	blobstreamAPI := NewBlobstreamAPI(env)
	RegisterBlobstreamAPIServer(grpcServer, blobstreamAPI)

	reflection.Register(grpcServer)

	errCh := make(chan error, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		errCh <- api.StartNewBlockEventListener(ctx)
	}()
	go func() {
		errCh <- grpcServer.Serve(ln)
	}()
	defer grpcServer.GracefulStop()
	defer func(api *BlockAPI, ctx context.Context) {
		err := api.Stop(ctx)
		if err != nil {
			env.Logger.Error("error stopping block api", "err", err)
		}
	}(api, ctx)
	// blocks until one errors or returns nil
	return <-errCh
}

// StartGRPCClient dials the gRPC server using protoAddr and returns a new
// BroadcastAPIClient.
//
// Deprecated: A new gRPC API will be introduced after v0.38.
func StartGRPCClient(protoAddr string) BroadcastAPIClient {
	parsedAddr := ParseProtoAddr(protoAddr)

	conn, err := grpc.NewClient(parsedAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(dialerFunc))
	if err != nil {
		panic(err)
	}
	return NewBroadcastAPIClient(conn)
}

func dialerFunc(_ context.Context, addr string) (net.Conn, error) {
	return cmtnet.Connect(addr)
}

// StartBlockAPIGRPCClient dials the gRPC server using protoAddr and returns a new
// BlockAPIClient.
func StartBlockAPIGRPCClient(protoAddr string, opts ...grpc.DialOption) (BlockAPIClient, error) {
	parsedAddr := ParseProtoAddr(protoAddr)

	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, grpc.WithContextDialer(dialerFunc))
	conn, err := grpc.NewClient(
		parsedAddr,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	return NewBlockAPIClient(conn), nil
}

// StartBlobstreamAPIGRPCClient dials the gRPC server using protoAddr and returns a new
// BlobstreamAPIClient.
func StartBlobstreamAPIGRPCClient(protoAddr string, opts ...grpc.DialOption) (BlobstreamAPIClient, error) {
	parsedAddr := ParseProtoAddr(protoAddr)

	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, grpc.WithContextDialer(dialerFunc))
	conn, err := grpc.NewClient(
		parsedAddr,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	return NewBlobstreamAPIClient(conn), nil
}

// ParseProtoAddr parses the protoAddr and returns the address, preserving gRPC-supported
// schemes (dns:// and unix://) while stripping other URI schemes.
// Examples:
//   - dns://host:port -> dns://host:port (preserved)
//   - unix:///path -> unix:///path (preserved)
//   - unix:/path -> unix:/path (preserved)
//   - tcp://host:port -> host:port (stripped)
//   - http://host:port -> host:port (stripped)
func ParseProtoAddr(protoAddr string) string {
	// Check if it starts with gRPC-supported schemes - preserve them
	if strings.HasPrefix(protoAddr, "dns://") || strings.HasPrefix(protoAddr, "unix://") {
		return protoAddr
	}

	// Strip other URI schemes
	re := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
	return re.ReplaceAllString(protoAddr, "")
}
