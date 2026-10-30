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
			want, jsonBody := fuzzHTTPReplies(body)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			fuzzCheckHTTPResponse(t, handler, req, want, jsonBody)
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
			// URI requests carry no id; the server answers with id -1.
			fuzzCheckHTTPResponse(t, handler, req, []fuzzReply{{id: rpctypes.JSONRPCIntID(-1)}}, true)
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

// fuzzReply is a response the server must send: id is the request ID it
// echoes, or nil when the server could not determine one, in which case code
// is the JSON-RPC error code it must carry.
type fuzzReply struct {
	id   any
	code int
}

// fuzzHTTPReplies models which responses makeJSONRPCHandler behind
// PreChecksHandler writes for a POST body, in order. ok is false for an empty
// body, which is answered with the HTML list of endpoints.
func fuzzHTTPReplies(body []byte) (replies []fuzzReply, ok bool) {
	if len(body) == 0 {
		return nil, false
	}
	var reqs []rpctypes.RPCRequest
	if json.Unmarshal(body, &reqs) == nil && len(reqs) > rpcserver.DefaultConfig().MaxRequestBatchSize {
		return []fuzzReply{{code: -32600}}, true
	}
	if json.Unmarshal(body, &reqs) != nil {
		var req rpctypes.RPCRequest
		if json.Unmarshal(body, &req) != nil {
			return []fuzzReply{{code: -32700}}, true
		}
		reqs = []rpctypes.RPCRequest{req}
	}
	for _, req := range reqs {
		// Notifications (no id) get no response.
		if req.ID != nil {
			replies = append(replies, fuzzReply{id: req.ID})
		}
	}
	return replies, true
}

// fuzzCheckEnvelope validates a single JSON-RPC response object against what
// the server emits: jsonrpc "2.0", exactly one of result and error, and an id
// that is omitted only on errors raised before a request id is known.
func fuzzCheckEnvelope(t *testing.T, raw []byte) rpctypes.RPCResponse {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "%s", raw)
	for k := range fields {
		require.Contains(t, []string{"jsonrpc", "id", "result", "error"}, k, "%s", raw)
	}
	require.Equal(t, `"2.0"`, string(fields["jsonrpc"]), "%s", raw)
	_, hasResult := fields["result"]
	errRaw, hasError := fields["error"]
	require.True(t, hasResult != hasError, "want exactly one of result and error: %s", raw)
	var res rpctypes.RPCResponse
	require.NoError(t, json.Unmarshal(raw, &res), "%s", raw)
	if hasError {
		require.NotEqual(t, "null", string(errRaw), "%s", raw)
		require.NotNil(t, res.Error, "%s", raw)
		require.NotZero(t, res.Error.Code, "%s", raw)
	}
	if _, hasID := fields["id"]; hasID {
		require.NotNil(t, res.ID, "null id: %s", raw)
	} else {
		require.NotNil(t, res.Error, "success response without id: %s", raw)
		require.Contains(t, []int{-32700, -32600}, res.Error.Code, "error without id: %s", raw)
	}
	return res
}

// fuzzMatchReply reports whether res is the response want describes.
func fuzzMatchReply(res rpctypes.RPCResponse, want fuzzReply) bool {
	if want.id != nil {
		return any(res.ID) == want.id
	}
	return res.ID == nil && res.Error != nil && res.Error.Code == want.code
}

// fuzzCheckHTTPResponse serves req and checks that the response carries
// exactly the replies (single object for one, array otherwise), in order.
func fuzzCheckHTTPResponse(t *testing.T, handler http.Handler, req *http.Request, want []fuzzReply, jsonBody bool) {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	blob, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	require.Less(t, len(blob), 1<<20)
	if !jsonBody {
		return
	}
	if len(want) == 0 {
		require.Empty(t, blob)
		return
	}
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"), "%s", blob)
	raws := []json.RawMessage{blob}
	if len(want) > 1 {
		require.NoError(t, json.Unmarshal(blob, &raws), "%s", blob)
		require.Len(t, raws, len(want), "%s", blob)
	}
	for i, raw := range raws {
		res := fuzzCheckEnvelope(t, raw)
		require.True(t, fuzzMatchReply(res, want[i]), "response %d: want %+v, got %s", i, want[i], raw)
	}
}

// fuzzWebsocket sends frames followed by a sentinel request and checks that,
// before the sentinel's response, the server answers every frame it must
// answer, in order (see wsConnection.readRoutine), without closing the
// connection.
func fuzzWebsocket(t *testing.T, url string, frames [][]byte) {
	var want []fuzzReply
	ids := map[any]bool{}
	subs := map[any]bool{}
	for _, frame := range frames {
		var req rpctypes.RPCRequest
		switch {
		case json.NewDecoder(bytes.NewReader(frame)).Decode(&req) != nil:
			want = append(want, fuzzReply{code: -32700})
		case req.ID == nil: // notification: no response
		default:
			want = append(want, fuzzReply{id: req.ID})
			ids[req.ID] = true
			if req.Method == "subscribe" {
				// Events are delivered asynchronously under this id.
				subs[req.ID] = true
			}
		}
	}
	sentinelID := rpctypes.JSONRPCStringID("fuzz-sentinel")
	for i := 0; ids[sentinelID]; i++ {
		sentinelID = rpctypes.JSONRPCStringID(fmt.Sprintf("fuzz-sentinel-%d", i))
	}
	sentinel, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": string(sentinelID), "method": "health"})
	require.NoError(t, err)

	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	defer conn.Close()
	for _, frame := range append(frames, sentinel) {
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, frame))
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	for {
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err, "connection ended with %d responses outstanding", len(want))
		res := fuzzCheckEnvelope(t, msg)
		switch {
		case len(want) > 0 && fuzzMatchReply(res, want[0]):
			want = want[1:]
		case any(res.ID) == any(sentinelID):
			require.Empty(t, want, "sentinel answered before responses %+v", want)
			return
		case res.ID != nil && subs[any(res.ID)]:
		default:
			require.Failf(t, "unexpected websocket response", "want %+v, got %s", want, msg)
		}
	}
}
