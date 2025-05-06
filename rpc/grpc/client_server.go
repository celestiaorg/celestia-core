package coregrpc

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	cmtnet "github.com/cometbft/cometbft/libs/net"
	"github.com/cometbft/cometbft/rpc/core"
)

func NormalizeGRPCAddress(address string) (string, error) {
	if strings.HasPrefix(address, "tcp://") {
		u, err := url.Parse(address)
		if err != nil {
			return "", fmt.Errorf("failed to parse gRPC address: %w", err)
		}
		return u.Host, nil
	}
	return address, nil
}

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

	// block api
	api := NewBlockAPI(env)
	RegisterBlockAPIServer(grpcServer, api)

	// blobstream api
	blobstreamAPI := NewBlobstreamAPI(env)
	RegisterBlobstreamAPIServer(grpcServer, blobstreamAPI)

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
	normalizedAddr, err := NormalizeGRPCAddress(protoAddr)
	if err != nil {
		panic(fmt.Sprintf("Invalid gRPC address: %v", err))
	}

	conn, err := grpc.NewClient(normalizedAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(fmt.Sprintf("Failed to create gRPC client: %v", err))
	}
	return NewBroadcastAPIClient(conn)
}

// StartBlockAPIGRPCClient dials the gRPC server using protoAddr and returns a new
// BlockAPIClient.
func StartBlockAPIGRPCClient(protoAddr string, opts ...grpc.DialOption) (BlockAPIClient, error) {
	normalizedAddr, err := NormalizeGRPCAddress(protoAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid gRPC address: %w", err)
	}
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(normalizedAddr, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}
	return NewBlockAPIClient(conn), nil
}

// StartBlobstreamAPIGRPCClient dials the gRPC server using protoAddr and returns a new
// BlobstreamAPIClient.
func StartBlobstreamAPIGRPCClient(protoAddr string, opts ...grpc.DialOption) (BlobstreamAPIClient, error) {
	normalizedAddr, err := NormalizeGRPCAddress(protoAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid gRPC address: %w", err)
	}

	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(normalizedAddr, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}
	return NewBlobstreamAPIClient(conn), nil
}
