package proxy

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	abcimocks "github.com/cometbft/cometbft/abci/client/mocks"
	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/abci/server"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
)

var SOCKET = "socket"

func TestEcho(t *testing.T) {
	sockPath := fmt.Sprintf("unix:///tmp/echo_%v.sock", cmtrand.Str(6))
	clientCreator := NewRemoteClientCreator(sockPath, SOCKET, true)

	// Start server
	s := server.NewSocketServer(sockPath, kvstore.NewInMemoryApplication())
	s.SetLogger(log.TestingLogger().With("module", "abci-server"))
	if err := s.Start(); err != nil {
		t.Fatalf("Error starting socket server: %v", err.Error())
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Error(err)
		}
	})

	// Start client
	cli, err := clientCreator.NewABCIClient()
	if err != nil {
		t.Fatalf("Error creating ABCI client: %v", err.Error())
	}
	cli.SetLogger(log.TestingLogger().With("module", "abci-client"))
	if err := cli.Start(); err != nil {
		t.Fatalf("Error starting ABCI client: %v", err.Error())
	}

	proxy := NewAppConnMempool(cli, NopMetrics())
	t.Log("Connected")

	for i := 0; i < 1000; i++ {
		_, err = proxy.CheckTx(context.Background(), &abci.RequestCheckTx{Tx: []byte(fmt.Sprintf("echo-%v", i))})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := proxy.Flush(context.Background()); err != nil {
		t.Error(err)
	}
}

func BenchmarkEcho(b *testing.B) {
	b.StopTimer() // Initialize
	sockPath := fmt.Sprintf("unix:///tmp/echo_%v.sock", cmtrand.Str(6))
	clientCreator := NewRemoteClientCreator(sockPath, SOCKET, true)

	// Start server
	s := server.NewSocketServer(sockPath, kvstore.NewInMemoryApplication())
	s.SetLogger(log.TestingLogger().With("module", "abci-server"))
	if err := s.Start(); err != nil {
		b.Fatalf("Error starting socket server: %v", err.Error())
	}
	b.Cleanup(func() {
		if err := s.Stop(); err != nil {
			b.Error(err)
		}
	})

	// Start client
	cli, err := clientCreator.NewABCIClient()
	if err != nil {
		b.Fatalf("Error creating ABCI client: %v", err.Error())
	}
	cli.SetLogger(log.TestingLogger().With("module", "abci-client"))
	if err := cli.Start(); err != nil {
		b.Fatalf("Error starting ABCI client: %v", err.Error())
	}

	proxy := NewAppConnMempool(cli, NopMetrics())
	b.Log("Connected")
	b.StartTimer() // Start benchmarking tests

	for i := 0; i < b.N; i++ {
		_, err = proxy.CheckTx(context.Background(), &abci.RequestCheckTx{Tx: []byte("hello")})
		if err != nil {
			b.Error(err)
		}
	}
	if err := proxy.Flush(context.Background()); err != nil {
		b.Error(err)
	}

	b.StopTimer()
}

type recheckTxsApp struct {
	*kvstore.Application
}

func (recheckTxsApp) RecheckTxs(_ context.Context, req *abci.RequestRecheckTxs) (*abci.ResponseRecheckTxs, error) {
	return &abci.ResponseRecheckTxs{Responses: make([]*abci.ResponseCheckTx, len(req.Txs))}, nil
}

func TestAppConnMempoolRecheckTxs(t *testing.T) {
	ctx := context.Background()
	req := &abci.RequestRecheckTxs{Txs: [][]byte{[]byte("a")}}

	app := recheckTxsApp{kvstore.NewInMemoryApplication()}
	for name, cc := range map[string]ClientCreator{
		"local":     NewLocalClientCreator(app),
		"conn-sync": NewConnSyncLocalClientCreator(app),
	} {
		cli, err := cc.NewABCIClient()
		require.NoError(t, err)
		rsp, err := NewAppConnMempool(cli, NopMetrics()).(abci.RecheckTxsApplication).RecheckTxs(ctx, req)
		require.NoError(t, err, name)
		require.Len(t, rsp.Responses, 1, name)
	}

	// Remote clients cannot carry the Go-level request.
	remote := NewAppConnMempool(&abcimocks.Client{}, NopMetrics())
	_, err := remote.(abci.RecheckTxsApplication).RecheckTxs(ctx, req)
	require.ErrorIs(t, err, abci.ErrRecheckTxsUnsupported)
}
