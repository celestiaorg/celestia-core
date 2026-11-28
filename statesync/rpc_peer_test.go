package statesync

import (
	"net/http"
	"net/http/httptest"
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
	require.Len(t, s.snapshots.Ranked(), 1)
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
