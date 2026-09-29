package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
)

// TestGenesisRoutesAreHeavy checks that the genesis routes draw from the shared
// heavy-request budget: a full genesis chunk is ~21 MiB of JSON, so these must
// be rejected while the budget is saturated like the other large responses.
func TestGenesisRoutesAreHeavy(t *testing.T) {
	env := &Environment{Config: cfg.RPCConfig{MaxConcurrentHeavyRequests: 1}}
	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, env.GetRoutes(), log.NewNopLogger())

	// Occupy the only heavy slot.
	env.HeavySem() <- struct{}{}
	t.Cleanup(func() { <-env.HeavySem() })

	for _, path := range []string{"/genesis", "/genesis_chunked?chunk=0"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, path)
	}
}
