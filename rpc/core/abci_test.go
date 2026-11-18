package core

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/consts"
	proxymocks "github.com/cometbft/cometbft/proxy/mocks"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

func TestABCIQueryRejectsProofPaths(t *testing.T) {
	query := proxymocks.NewAppConnQuery(t)
	env := &Environment{ProxyAppQuery: query}

	for _, path := range []string{
		"custom/txInclusionProof/0",
		"custom/shareInclusionProof/0/1",
		"/custom/txInclusionProof/0",
		"/custom/shareInclusionProof/0/1",
		"custom/txInclusionProof",
	} {
		t.Run(path, func(t *testing.T) {
			res, err := env.ABCIQuery(&rpctypes.Context{}, path, bytes.HexBytes{1}, 0, false)
			require.Error(t, err)
			require.Nil(t, res)
		})
	}
	// The mock has no expectations, so it fails the test if Query is called.
}

func TestABCIQueryPassesOtherPaths(t *testing.T) {
	for _, path := range []string{
		"custom/bank/balance",
		"store/bank/key",
		"/cosmos.bank.v1beta1.Query/Balance",
		"custom/TxInclusionProof/0",
		"//custom/txInclusionProof/0",
	} {
		t.Run(path, func(t *testing.T) {
			query := proxymocks.NewAppConnQuery(t)
			query.On("Query", mock.Anything, mock.MatchedBy(func(req *abci.RequestQuery) bool {
				return req.Path == path
			})).Return(&abci.ResponseQuery{Value: []byte("ok")}, nil).Once()
			env := &Environment{ProxyAppQuery: query}

			res, err := env.ABCIQuery(&rpctypes.Context{}, path, nil, 0, false)
			require.NoError(t, err)
			require.Equal(t, []byte("ok"), res.Response.Value)
		})
	}
}

// TestIsProofQueryPathMatchesConsts checks that the paths rpc/core uses for
// its own proof routes are the ones abci_query rejects.
func TestIsProofQueryPathMatchesConsts(t *testing.T) {
	require.True(t, isProofQueryPath(fmt.Sprintf(consts.TxInclusionProofQueryPath, 7)))
	require.True(t, isProofQueryPath(fmt.Sprintf(consts.ShareInclusionProofQueryPath, 3, 5)))
}
