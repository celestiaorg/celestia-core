package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	types "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

func TestWebsocketManagerHandler(t *testing.T) {
	s := newWSServer()
	defer s.Close()

	// check upgrader works
	d := websocket.Dialer{}
	c, dialResp, err := d.Dial("ws://"+s.Listener.Addr().String()+"/websocket", nil)
	require.NoError(t, err)

	if got, want := dialResp.StatusCode, http.StatusSwitchingProtocols; got != want {
		t.Errorf("dialResp.StatusCode = %d, want %d", got, want)
	}

	// check basic functionality works
	req, err := types.MapToRequest(
		types.JSONRPCStringID("TestWebsocketManager"),
		"c",
		map[string]interface{}{"s": "a", "i": 10},
	)
	require.NoError(t, err)
	err = c.WriteJSON(req)
	require.NoError(t, err)

	var resp types.RPCResponse
	err = c.ReadJSON(&resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	dialResp.Body.Close()
}

func newWSServer() *httptest.Server {
	funcMap := map[string]*RPCFunc{
		"c": NewWSRPCFunc(func(ctx *types.Context, s string, i int) (string, error) { return "foo", nil }, "s,i"),
	}
	wm := NewWebsocketManager(funcMap)
	wm.SetLogger(log.TestingLogger())

	mux := http.NewServeMux()
	mux.HandleFunc("/websocket", wm.WebsocketHandler)

	return httptest.NewServer(mux)
}

// TestWebsocketHeavyPermitHeldUntilWritten verifies that a heavy WebSocket
// response keeps its heavy-request permit until the response has been written
// to the socket, so a client that stops reading cannot get more heavy
// responses built and queued than the permit budget allows.
func TestWebsocketHeavyPermitHeldUntilWritten(t *testing.T) {
	const (
		requests = 20
		respSize = 1 << 20 // 1 MiB per response
	)
	sem := make(chan struct{}, 1)
	var calls atomic.Int32
	payload := strings.Repeat("x", respSize)
	funcMap := map[string]*RPCFunc{
		"heavy": NewWSRPCFunc(func(ctx *types.Context) (string, error) {
			calls.Add(1)
			return payload, nil
		}, "", HeavyFn(sem)),
	}
	wm := NewWebsocketManager(funcMap)
	wm.SetLogger(log.TestingLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("/websocket", wm.WebsocketHandler)
	s := httptest.NewServer(mux)
	defer s.Close()

	// A client with a tiny receive buffer that never reads responses.
	d := websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetReadBuffer(4096)
			}
			return conn, nil
		},
	}
	c, dialResp, err := d.Dial("ws://"+s.Listener.Addr().String()+"/websocket", nil)
	require.NoError(t, err)
	defer dialResp.Body.Close()

	for i := 0; i < requests; i++ {
		req, err := types.MapToRequest(types.JSONRPCIntID(i), "heavy", map[string]interface{}{})
		require.NoError(t, err)
		require.NoError(t, c.WriteJSON(req))
	}

	// The permit must stay held by the response blocked in the write, and the
	// server must stop building further heavy responses.
	require.Eventually(t, func() bool { return len(sem) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	require.Len(t, sem, 1, "heavy permit must be held while the response is unwritten")
	require.Less(t, int(calls.Load()), requests, "heavy responses must not pile up behind a stalled reader")

	// Closing the client must release the permit.
	require.NoError(t, c.Close())
	require.Eventually(t, func() bool { return len(sem) == 0 }, 15*time.Second, 10*time.Millisecond)
}
