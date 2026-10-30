//go:build gofuzz || go1.21

package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/fuzzinput"
	"github.com/cometbft/cometbft/libs/log"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// JSON-RPC error codes the server uses.
const (
	rpcCodeParse          = -32700
	rpcCodeInvalidRequest = -32600
	rpcCodeMethodNotFound = -32601
	rpcCodeInvalidParams  = -32602
	rpcCodeInternal       = -32603
)

// FuzzRPCJSONRPCServer posts JSON-RPC bodies to a minimal server with a
// single method "c", behind the same pre-checks as a production server. With
// empty ops it posts raw; otherwise ops describes a single or batch request.
func FuzzRPCJSONRPCServer(f *testing.F) {
	type args struct {
		S string `json:"s"`
		I int    `json:"i"`
	}
	rpcFuncMap := map[string]*rpcserver.RPCFunc{
		"c": rpcserver.NewRPCFunc(func(ctx *rpctypes.Context, args *args, options ...rpcserver.Option) (string, error) {
			return "foo", nil
		}, "args"),
	}

	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, rpcFuncMap, log.NewNopLogger())
	cfg := rpcserver.DefaultConfig()
	handler := rpcserver.PreChecksHandler(mux, cfg)

	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"c","params":{"args":{"s":"x","i":"1"}}}`), []byte(nil))
	f.Add([]byte(`[{"jsonrpc":"2.0","id":"a","method":"c","params":[{}]},{"jsonrpc":"2.0","method":"c"}]`), []byte(nil))
	// Structured seeds: batch kind, then per request the jsonrpc, id, method
	// and params kinds with their fields.
	f.Add([]byte(nil), []byte{0, 0, 2, 1, 0, 3})
	f.Add([]byte(nil), []byte{1, 3, 0, 3, 1, 'x', 0, 4, 1, 1, 0, 0, 0, 5, 2, 2, 1, 'q', 7})
	f.Add([]byte(nil), []byte{2, 11})
	f.Add([]byte(nil), []byte{1, 0})

	f.Fuzz(func(t *testing.T, raw []byte, ops []byte) {
		if len(ops) == 0 {
			if len(raw) == 0 || len(raw) > int(cfg.MaxBodyBytes) {
				return
			}
			want, ok := rawRPCReplies(raw, cfg.MaxRequestBatchSize)
			if !ok {
				return
			}
			postRPC(t, handler, raw, want)
			return
		}
		body, want := genRPCBody(t, fuzzinput.New(ops), cfg.MaxRequestBatchSize)
		postRPC(t, handler, body, want)
	})
}

// rpcReply is a response the server must send. id is the echoed request id,
// or nil for an error raised before any id is known. A zero code with result
// false means any known outcome is acceptable.
type rpcReply struct {
	id     any
	result bool // the call succeeds with result "foo"
	code   int  // the error code, when not result
}

// rawRPCReplies models the replies to a raw body from what it decodes to; it
// cannot predict per-request outcomes. ok is false for inputs that must be
// skipped.
func rawRPCReplies(body []byte, maxBatch int) (replies []rpcReply, ok bool) {
	var reqs []rpctypes.RPCRequest
	if json.Unmarshal(body, &reqs) == nil {
		if len(reqs) > maxBatch {
			return []rpcReply{{code: rpcCodeInvalidRequest}}, true
		}
	} else {
		var req rpctypes.RPCRequest
		if json.Unmarshal(body, &req) != nil {
			return []rpcReply{{code: rpcCodeParse}}, true
		}
		reqs = []rpctypes.RPCRequest{req}
	}
	for _, req := range reqs {
		if req.ID == nil {
			continue // notifications get no response
		}
		// Excluded pending PROTOCO-3521; remove with the fix.
		if req.Method == "c" && len(req.Params) == 0 {
			return nil, false
		}
		replies = append(replies, rpcReply{id: req.ID})
	}
	return replies, true
}

// genRPCBody builds a single or batch JSON-RPC body from r and the replies
// the server must send for it.
func genRPCBody(t *testing.T, r *fuzzinput.Reader, maxBatch int) ([]byte, []rpcReply) {
	var (
		replies     []rpcReply
		unparseable bool // the body fails to decode as RPCRequest(s)
	)
	batch := r.Intn(3)
	n := 1
	switch batch {
	case 1: // batch, possibly empty or one element
		n = r.Intn(maxBatch + 1)
	case 2: // oversized batch
		n = maxBatch + 1 + r.Intn(6)
	}
	reqs := make([]map[string]any, 0, n) // an empty batch marshals as []
	for range n {
		req, reply, bad := genRPCRequest(r)
		reqs = append(reqs, req)
		unparseable = unparseable || bad
		if reply != nil {
			replies = append(replies, *reply)
		}
	}

	var v any = reqs
	if batch == 0 {
		v = reqs[0]
	}
	body, err := json.Marshal(v)
	require.NoError(t, err)
	switch {
	case unparseable:
		replies = []rpcReply{{code: rpcCodeParse}}
	case batch != 0 && n > maxBatch:
		replies = []rpcReply{{code: rpcCodeInvalidRequest}}
	}
	return body, replies
}

// genRPCRequest builds one request object. reply is nil for a notification;
// bad reports a field the server cannot decode, which fails the whole body.
func genRPCRequest(r *fuzzinput.Reader) (req map[string]any, reply *rpcReply, bad bool) {
	req = map[string]any{}
	switch r.Intn(6) {
	case 0:
		req["jsonrpc"] = "2.0"
	case 1:
		req["jsonrpc"] = "1.0"
	case 2:
		req["jsonrpc"] = ""
	case 3: // missing
	case 4:
		req["jsonrpc"] = rpcString(r)
	case 5:
		req["jsonrpc"] = 2
		bad = true
	}

	var id any
	switch r.Intn(6) {
	case 0: // missing: a notification
	case 1:
		req["id"] = nil // null: also a notification
	case 2:
		v := int(r.Int32())
		req["id"], id = v, rpctypes.JSONRPCIntID(v)
	case 3: // fractional ids are truncated toward zero
		v := int(r.Int32())
		frac := 0.5
		if v < 0 {
			frac = -0.5
		}
		req["id"], id = float64(v)+frac, rpctypes.JSONRPCIntID(v)
	case 4:
		v := rpcString(r)
		req["id"], id = v, rpctypes.JSONRPCStringID(v)
	case 5:
		req["id"] = map[string]any{"k": rpcString(r)}
		bad = true
	}

	var method string
	switch r.Intn(3) {
	case 0:
		method = "c"
	case 1:
		method = rpcString(r)
		if method == "c" {
			method = "cc"
		}
	case 2:
		method = ""
	}
	req["method"] = method

	// Excluded pending PROTOCO-3521 (a call to c without params panics);
	// remove with the fix.
	paramsKinds := 15
	if method == "c" && id != nil {
		paramsKinds = 14
	}
	args := map[string]any{"s": rpcString(r), "i": strconv.Itoa(int(r.Int32()))}
	ok := true
	switch r.Intn(paramsKinds) {
	case 0:
		req["params"] = nil
	case 1:
		req["params"] = map[string]any{}
	case 2:
		req["params"] = map[string]any{"args": args}
	case 3:
		req["params"] = []any{args}
	case 4: // extra top-level field
		req["params"] = map[string]any{"args": args, rpcString(r) + "x": 1}
	case 5: // extra field in args
		args[rpcString(r)+"x"] = []int{1}
		req["params"] = map[string]any{"args": args}
	case 6: // wrong type for s
		req["params"] = map[string]any{"args": map[string]any{"s": 5}}
		ok = false
	case 7: // int not encoded as a string
		req["params"] = map[string]any{"args": map[string]any{"i": 5}}
		ok = false
	case 8: // too few positional params
		req["params"] = []any{}
		ok = false
	case 9: // too many positional params
		req["params"] = []any{args, args}
		ok = false
	case 10: // positional param of the wrong type
		req["params"] = []any{rpcString(r)}
		ok = false
	case 11: // args of the wrong type
		req["params"] = map[string]any{"args": []int{1}}
		ok = false
	case 12: // params neither object nor array
		req["params"] = []any{5, rpcString(r), true}[r.Intn(3)]
		ok = false
	case 13: // non-integer i
		req["params"] = map[string]any{"args": map[string]any{"i": "1.5"}}
		ok = false
	case 14: // missing
	}

	if id == nil {
		return req, nil, bad
	}
	reply = &rpcReply{id: id}
	switch {
	case method != "c":
		reply.code = rpcCodeMethodNotFound
	case ok:
		reply.result = true
	default:
		reply.code = rpcCodeInvalidParams
	}
	return req, reply, bad
}

// rpcString reads a short string, valid UTF-8 so it round-trips through JSON.
func rpcString(r *fuzzinput.Reader) string {
	return strings.ToValidUTF8(string(r.Bytes(16)), "?")
}

// postRPC posts body and checks the response carries exactly want, in order:
// nothing for no replies, a single object for one, an array otherwise.
func postRPC(t *testing.T, handler http.Handler, body []byte, want []rpcReply) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	res := rec.Result()
	blob, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	if len(want) == 0 {
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Empty(t, blob)
		return
	}
	wantStatus := http.StatusOK
	if want[0].id == nil && want[0].code == rpcCodeParse {
		wantStatus = http.StatusInternalServerError
	}
	require.Equal(t, wantStatus, res.StatusCode, "%s", blob)
	require.Equal(t, "application/json", res.Header.Get("Content-Type"), "%s", blob)

	raws := []json.RawMessage{blob}
	if len(want) > 1 {
		require.NoError(t, json.Unmarshal(blob, &raws), "%s", blob)
		require.Len(t, raws, len(want), "%s", blob)
	} else {
		require.False(t, outputJSONIsSlice(blob), "single reply sent as an array: %s", blob)
	}
	for i, raw := range raws {
		checkRPCReply(t, raw, want[i])
	}
}

// checkRPCReply checks one response object: jsonrpc "2.0", exactly one of
// result and error, a known error code, and the expected id and outcome.
func checkRPCReply(t *testing.T, raw []byte, want rpcReply) {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "%s", raw)
	for k := range fields {
		require.Contains(t, []string{"jsonrpc", "id", "result", "error"}, k, "%s", raw)
	}
	require.Equal(t, `"2.0"`, string(fields["jsonrpc"]), "%s", raw)
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	require.True(t, hasResult != hasError, "want exactly one of result and error: %s", raw)

	var res rpctypes.RPCResponse
	require.NoError(t, json.Unmarshal(raw, &res), "%s", raw)
	require.Equal(t, want.id, any(res.ID), "%s", raw)
	if hasError {
		require.NotNil(t, res.Error, "%s", raw)
		require.Contains(t, []int{rpcCodeParse, rpcCodeInvalidRequest, rpcCodeMethodNotFound, rpcCodeInvalidParams, rpcCodeInternal}, res.Error.Code, "%s", raw)
	}
	switch {
	case want.result:
		require.Equal(t, `"foo"`, string(res.Result), "%s", raw)
	case want.code != 0:
		require.NotNil(t, res.Error, "%s", raw)
		require.Equal(t, want.code, res.Error.Code, "%s", raw)
	case hasResult:
		require.Equal(t, `"foo"`, string(res.Result), "%s", raw)
	}
	if want.id == nil {
		require.NotNil(t, res.Error, "success response without id: %s", raw)
	}
}

func outputJSONIsSlice(input []byte) bool {
	var slice []json.RawMessage
	return json.Unmarshal(input, &slice) == nil
}
