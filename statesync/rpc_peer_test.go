package statesync

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	ssproto "github.com/cometbft/cometbft/proto/tendermint/statesync"
	proxymocks "github.com/cometbft/cometbft/proxy/mocks"
	rpccore "github.com/cometbft/cometbft/rpc/core"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
)

// Exercise the real URI handlers and JSON encoding with zero connected peers.
func TestSnapshotRPCWithoutPeers(t *testing.T) {
	source := &proxymocks.AppConnSnapshot{}
	source.On("ListSnapshots", mock.Anything, &abci.RequestListSnapshots{}).Return(&abci.ResponseListSnapshots{
		Snapshots: []*abci.Snapshot{
			{Height: 8, Format: 3, Chunks: 1, Hash: []byte("old")},
			{Height: 9, Format: 3, Chunks: 1, Hash: []byte("new")},
			{Height: 10, Format: 3, Chunks: 20, Hash: []byte("oversized")},
		},
	}, nil)
	req := &abci.RequestLoadSnapshotChunk{Height: 9, Format: 3, Chunk: 0}
	source.On("LoadSnapshotChunk", mock.Anything, req).Return(&abci.ResponseLoadSnapshotChunk{Chunk: []byte("data")}, nil)
	env := &rpccore.Environment{ProxyAppSnapshot: source}
	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, env.GetRoutes(), log.NewNopLogger())
	server := httptest.NewServer(mux)
	defer server.Close()

	s, _ := setupOfferSyncer()
	cfg := config.DefaultStateSyncConfig()
	cfg.TrustHeight = 9
	cfg.MaxSnapshotChunks = 5
	p := newRPCSnapshotPeer(t.Context(), 0, server.URL, s, *cfg)
	p.discover()
	require.Eventually(t, func() bool { return !p.discovering.Load() }, time.Second*5, time.Millisecond)
	require.Len(t, s.snapshots.Ranked(), 2) // Go can verify offers below its trust height backwards.
	best := s.snapshots.Best()
	require.Equal(t, uint64(9), best.Height)
	q, err := newChunkQueue(best, t.TempDir())
	require.NoError(t, err)
	defer q.Close()
	s.chunks = q
	require.True(t, s.snapshots.GetPeer(best).Send(p2p.Envelope{ChannelID: ChunkChannel, Message: &ssproto.ChunkRequest{Height: 9, Format: 3}}))
	got, err := q.Next()
	require.NoError(t, err)
	require.Equal(t, []byte("data"), got.Chunk)
	require.Equal(t, p.ID(), got.Sender)
	s.rejectSender(p.ID())
	require.NoError(t, q.Discard(0))
	added, err := s.AddChunk(&chunk{Height: 9, Format: 3, Index: 0, Chunk: []byte("late"), Sender: p.ID()})
	require.NoError(t, err)
	require.False(t, added)
	// Reuse the inventory between discovery rounds.
	var cached struct{ Snapshots []json.RawMessage }
	require.NoError(t, p.get("/snapshots", 1024, &cached))
	source.AssertNumberOfCalls(t, "ListSnapshots", 1)

	// Bulk routes cannot accumulate multiple large results in a batch.
	batch, err := http.Post(server.URL, "application/json", bytes.NewBufferString(`[{"jsonrpc":"2.0","id":1,"method":"snapshots"},{"jsonrpc":"2.0","id":2,"method":"snapshot_chunk"}]`))
	require.NoError(t, err)
	defer batch.Body.Close()
	var replies []struct{ Error struct{ Code int } }
	require.NoError(t, json.NewDecoder(batch.Body).Decode(&replies))
	require.Len(t, replies, 2)
	require.Equal(t, -32601, replies[0].Error.Code)
	source.AssertExpectations(t)
}

func TestSnapshotRPCResponseBounds(t *testing.T) {
	// A streaming response without a Content-Length is subject to the same cap.
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(`{"result":{"chunk":"ZGF0YQ=="}}`))
	}))
	defer source.Close()
	p := newRPCSnapshotPeer(t.Context(), 1, source.URL, nil, *config.DefaultStateSyncConfig())
	var resp abci.ResponseLoadSnapshotChunk
	require.ErrorContains(t, p.get("/snapshot_chunk", 8, &resp), "too large")

	redirect := httptest.NewServer(http.RedirectHandler(source.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()
	p.base = redirect.URL
	require.ErrorContains(t, p.get("/snapshot_chunk", 1024, &resp), "307")
}

func TestSnapshotRPCUnixEndpoint(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "rpc.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"chunk":"eA=="}}`))
	}))
	require.NoError(t, server.Listener.Close())
	server.Listener = listener
	server.Start()
	defer server.Close()
	peer := newRPCSnapshotPeer(t.Context(), 0, "unix://"+socket, nil, *config.DefaultStateSyncConfig())
	var result abci.ResponseLoadSnapshotChunk
	require.NoError(t, peer.get("/snapshot_chunk", 1024, &result))
	require.Equal(t, []byte("x"), result.Chunk)
}
