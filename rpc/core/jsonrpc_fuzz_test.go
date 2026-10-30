package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool/cat"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// panicLogger records the error the websocket read routine logs when it
// recovers a handler panic, so the harness can fail on it.
type panicLogger struct {
	log.Logger
	panicked *atomic.Pointer[string]
}

func (l panicLogger) Error(msg string, kv ...interface{}) {
	if strings.HasPrefix(msg, "Panic in") {
		msg := fmt.Sprint(kv...)
		l.panicked.Store(&msg)
	}
}
func (l panicLogger) With(...interface{}) log.Logger { return l }

// FuzzRPCCoreJSONRPC drives the real rpc/core route table through the
// JSON-RPC server: POST bodies (single and batch) behind PreChecksHandler, URI
// GET requests, and websocket frames, including subscribe/unsubscribe.
func FuzzRPCCoreJSONRPC(f *testing.F) {
	env := NewEnvForTest(f)
	mp := env.Mempool.(*cat.TxPool)
	routes := env.GetRoutes()
	// Their Environment dependencies (consensus state, p2p switch) are not
	// populated here; broadcast_tx_commit waits for a block to be committed.
	for _, r := range []string{"status", "net_info", "dump_consensus_state", "consensus_state", "broadcast_tx_commit"} {
		delete(routes, r)
	}
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	sort.Strings(names)

	cfg := rpcserver.DefaultConfig()
	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, routes, log.NewNopLogger())
	handler := rpcserver.PreChecksHandler(mux, cfg)

	var panicked atomic.Pointer[string]
	var wsWG sync.WaitGroup
	wm := rpcserver.NewWebsocketManager(routes,
		rpcserver.OnDisconnect(func(remoteAddr string) {
			_ = env.EventBus.UnsubscribeAll(context.Background(), remoteAddr)
		}),
		rpcserver.ReadLimit(cfg.MaxBodyBytes),
	)
	wm.SetLogger(panicLogger{log.NewNopLogger(), &panicked})
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsWG.Add(1)
		defer wsWG.Done()
		wm.WebsocketHandler(w, r)
	}))
	f.Cleanup(wsServer.Close)
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	f.Add(uint8(0), uint8(0), []byte(`{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":"2"}}`))
	f.Add(uint8(0), uint8(0), []byte(`[{"jsonrpc":"2.0","id":1,"method":"tx_search","params":["tx.height>1",true,"1","100","desc"]},{"jsonrpc":"2.0","id":2,"method":"data_commitment","params":{"start":"1","end":"5"}}]`))
	f.Add(uint8(1), uint8(0), []byte(`{"height":"3","startShare":"0","endShare":"1"}`))
	f.Add(uint8(1), uint8(5), []byte(`["0x00", 1]`))
	f.Add(uint8(2), uint8(0), []byte(`hash=0x0102&prove=true&height=1&query="tx.height=1"`))
	f.Add(uint8(3), uint8(0), []byte(`{"jsonrpc":"2.0","id":7,"method":"subscribe","params":{"query":"tm.event='Tx'"}}`+"\x00"+`{"jsonrpc":"2.0","id":8,"method":"unsubscribe_all"}`))
	f.Add(uint8(4), uint8(0), []byte(`{"query":"tm.event = 'NewBlock' AND block.height > 2"}`))

	f.Fuzz(func(t *testing.T, mode, method uint8, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		name := names[int(method)%len(names)]
		structured := func() []byte {
			req := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": name}
			if json.Valid(data) {
				req["params"] = json.RawMessage(data)
			} else {
				req["params"] = []string{string(data)}
			}
			b, err := json.Marshal(req)
			require.NoError(t, err)
			return b
		}
		switch mode % 5 {
		case 0, 3:
			if fuzzMissingParams(routes, bytes.Split(data, []byte{0}), mode%5 == 3) {
				return
			}
		}
		switch mode % 5 {
		case 0, 1:
			body := data
			if mode%5 == 1 {
				body = structured()
			}
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			fuzzCheckHTTPResponse(t, handler, req)
		case 2:
			req, err := http.NewRequest(http.MethodGet, "http://localhost/"+name+"?"+string(data), nil)
			if err != nil {
				return
			}
			// Excluded pending PROTOCO-3522; remove with the fix.
			for _, vs := range req.URL.Query() {
				for _, v := range vs {
					if v == `"` {
						return
					}
				}
			}
			req.Body = http.NoBody // as for any server-side request
			fuzzCheckHTTPResponse(t, handler, req)
		case 3, 4:
			frames := bytes.Split(data, []byte{0})
			if mode%5 == 4 {
				frames = [][]byte{structured()}
			}
			if len(frames) > 8 {
				return
			}
			fuzzWebsocket(t, wsURL, frames)
			wsWG.Wait()
			if msg := panicked.Swap(nil); msg != nil {
				t.Fatalf("websocket handler panicked: %s", *msg)
			}
		}
		mp.Flush()
		require.Zero(t, env.EventBus.NumClients())
	})
}

// fuzzMissingParams reports whether a request calls a route that takes
// parameters without passing any.
// Excluded pending PROTOCO-3521; remove with the fix.
func fuzzMissingParams(routes RoutesMap, bodies [][]byte, ws bool) bool {
	noArgs := map[string]bool{"health": true, "genesis": true, "abci_info": true, "num_unconfirmed_txs": true, "unsubscribe_all": true}
	for _, body := range bodies {
		var reqs []rpctypes.RPCRequest
		if ws {
			// The websocket reader decodes one request and ignores trailing bytes.
			var req rpctypes.RPCRequest
			if json.NewDecoder(bytes.NewReader(body)).Decode(&req) == nil {
				reqs = append(reqs, req)
			}
		} else if json.Unmarshal(body, &reqs) != nil {
			var req rpctypes.RPCRequest
			if json.Unmarshal(body, &req) != nil {
				continue
			}
			reqs = []rpctypes.RPCRequest{req}
		}
		for _, req := range reqs {
			if _, ok := routes[req.Method]; ok && !noArgs[req.Method] && len(req.Params) == 0 {
				return true
			}
		}
	}
	return false
}

func fuzzCheckHTTPResponse(t *testing.T, handler http.Handler, req *http.Request) {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	blob, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	require.Less(t, len(blob), 1<<20)
	if len(blob) == 0 || rec.Header().Get("Content-Type") != "application/json" {
		return
	}
	var batch []rpctypes.RPCResponse
	if json.Unmarshal(blob, &batch) == nil {
		require.LessOrEqual(t, len(batch), rpcserver.DefaultConfig().MaxRequestBatchSize)
		return
	}
	var single rpctypes.RPCResponse
	require.NoError(t, json.Unmarshal(blob, &single), "%s", blob)
}

func fuzzWebsocket(t *testing.T, url string, frames [][]byte) {
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	defer conn.Close()
	sentinel := []byte(`{"jsonrpc":"2.0","id":"fuzz-sentinel","method":"health"}`)
	for _, frame := range append(frames, sentinel) {
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			return
		}
	}
	// The server answers in order, so the sentinel response comes last.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			require.False(t, isTimeout(err), "no websocket response: %v", err)
			return
		}
		var res rpctypes.RPCResponse
		require.NoError(t, json.Unmarshal(msg, &res), "%s", msg)
		if res.ID == rpctypes.JSONRPCStringID("fuzz-sentinel") {
			return
		}
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(interface{ Timeout() bool })
	return ok && ne.Timeout()
}
