package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	lighthttp "github.com/cometbft/cometbft/light/provider/http"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

const fuzzHTTPChainID = "fuzz-http"

// fuzzRPC is a JSON-RPC server whose /commit and /validators responses are
// set per fuzz input. A body is either the raw "result" value or, when the
// matching error flag is set, the "data" of an RPC error.
type fuzzRPC struct {
	mtx                sync.Mutex
	commit, validators []byte
	commitErr, valsErr bool
}

func (s *fuzzRPC) set(commit, validators []byte, commitErr, valsErr bool) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.commit, s.validators, s.commitErr, s.valsErr = commit, validators, commitErr, valsErr
}

func (s *fuzzRPC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mtx.Lock()
	body, isErr := s.commit, s.commitErr
	if req.Method == "validators" {
		body, isErr = s.validators, s.valsErr
	}
	s.mtx.Unlock()

	var resp bytes.Buffer
	resp.WriteString(`{"jsonrpc":"2.0","id":`)
	resp.Write(req.ID)
	if isErr {
		data, _ := json.Marshal(string(body))
		resp.WriteString(`,"error":{"code":-32603,"message":"Internal error","data":`)
		resp.Write(data)
		resp.WriteString(`}}`)
	} else {
		resp.WriteString(`,"result":`)
		resp.Write(body)
		resp.WriteString(`}`)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp.Bytes())
}

// fuzzHTTPSeeds returns valid /commit and /validators results for height 3.
func fuzzHTTPSeeds(tb testing.TB) (commit, validators []byte) {
	tb.Helper()
	pvs := make([]cmttypes.PrivValidator, 3)
	vals := make([]*cmttypes.Validator, 3)
	for i := range pvs {
		key := ed25519.GenPrivKeyFromSecret([]byte{'h', byte(i)})
		pvs[i] = cmttypes.NewMockPVWithParams(key, false, false)
		vals[i] = cmttypes.NewValidator(key.PubKey(), int64(10*(i+1)))
	}
	valSet := cmttypes.NewValidatorSet(vals)
	ordered := make([]cmttypes.PrivValidator, len(pvs))
	for i, v := range valSet.Validators {
		for _, pv := range pvs {
			if pk, _ := pv.GetPubKey(); bytes.Equal(pk.Address(), v.Address) {
				ordered[i] = pv
			}
		}
	}
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	h := &cmttypes.Header{
		Version: cmtversion.Consensus{Block: version.BlockProtocol}, ChainID: fuzzHTTPChainID, Height: 3, Time: now,
		ValidatorsHash: valSet.Hash(), NextValidatorsHash: valSet.Hash(), ProposerAddress: valSet.GetProposer().Address,
	}
	blockID := cmttypes.BlockID{Hash: h.Hash(), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	voteSet := cmttypes.NewVoteSet(fuzzHTTPChainID, h.Height, 0, cmtproto.PrecommitType, valSet)
	ec, err := cmttypes.MakeExtCommit(blockID, h.Height, 0, voteSet, ordered, now, false)
	require.NoError(tb, err)
	sh := cmttypes.SignedHeader{Header: h, Commit: ec.ToCommit()}
	require.NoError(tb, (&cmttypes.LightBlock{SignedHeader: &sh, ValidatorSet: valSet}).ValidateBasic(fuzzHTTPChainID))

	commit, err = cmtjson.Marshal(ctypes.ResultCommit{SignedHeader: sh, CanonicalCommit: true})
	require.NoError(tb, err)
	validators, err = cmtjson.Marshal(ctypes.ResultValidators{
		BlockHeight: h.Height, Validators: valSet.Validators, Count: valSet.Size(), Total: valSet.Size(),
	})
	require.NoError(tb, err)
	return commit, validators
}

// FuzzHTTPProviderLightBlock serves fuzzed JSON-RPC /commit and /validators
// responses to the light client HTTP provider, which the light client and
// state sync use to fetch light blocks from untrusted full nodes. An accepted
// light block must be for the requested height and pass ValidateBasic.
func FuzzHTTPProviderLightBlock(f *testing.F) {
	commit, validators := fuzzHTTPSeeds(f)
	f.Add(commit, validators, uint8(0), int64(3))
	f.Add(commit, validators, uint8(0), int64(0))
	f.Add(commit, validators, uint8(0), int64(4))
	f.Add([]byte("height 3 is not available, lowest height is 4"), validators, uint8(1), int64(3))
	f.Add(commit, []byte("height 3 must be less than or equal to the current blockchain height 2"), uint8(2), int64(3))
	f.Add(commit, []byte(`{"block_height":"3","validators":[],"count":"0","total":"0"}`), uint8(0), int64(3))
	f.Add([]byte(`{"signed_header":{"header":null,"commit":null},"canonical":false}`), validators, uint8(0), int64(-1))

	rpc := &fuzzRPC{}
	server := httptest.NewServer(rpc)
	f.Cleanup(server.Close)
	client, err := rpchttp.New(server.URL, "/websocket")
	require.NoError(f, err)
	p := lighthttp.NewWithClient(fuzzHTTPChainID, client)

	f.Fuzz(func(t *testing.T, commit, validators []byte, mode uint8, height int64) {
		// RPC errors mentioning a timeout make the provider back off for
		// seconds per retry by design.
		if bytes.Contains(commit, []byte("Timeout exceeded")) || bytes.Contains(validators, []byte("Timeout exceeded")) {
			return
		}
		if len(commit) > 64<<10 || len(validators) > 64<<10 {
			return
		}
		height %= 8
		rpc.set(commit, validators, mode&1 != 0, mode&2 != 0)

		lb, err := p.LightBlock(context.Background(), height)
		if err != nil {
			require.Nil(t, lb)
			return
		}
		require.NoError(t, lb.ValidateBasic(fuzzHTTPChainID))
		if height != 0 {
			require.Equal(t, height, lb.Height, "light block for another height accepted")
		}
		seen := map[string]bool{}
		for _, v := range lb.ValidatorSet.Validators {
			require.False(t, seen[string(v.Address)], "duplicate validator accepted")
			seen[string(v.Address)] = true
		}
	})
}

// TestHTTPProviderFuzzSeed keeps the seed meaningful: it yields a light block.
func TestHTTPProviderFuzzSeed(t *testing.T) {
	commit, validators := fuzzHTTPSeeds(t)
	rpc := &fuzzRPC{}
	rpc.set(commit, validators, false, false)
	server := httptest.NewServer(rpc)
	t.Cleanup(server.Close)
	client, err := rpchttp.New(server.URL, "/websocket")
	require.NoError(t, err)
	lb, err := lighthttp.NewWithClient(fuzzHTTPChainID, client).LightBlock(context.Background(), 3)
	require.NoError(t, err)
	require.EqualValues(t, 3, lb.Height)
}
