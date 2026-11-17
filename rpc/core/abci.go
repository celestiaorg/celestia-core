package core

import (
	"context"
	"errors"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/proxy"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// ABCIQuery queries the application for some information.
// More: https://docs.cometbft.com/v0.38.x/rpc/#/ABCI/abci_query
func (env *Environment) ABCIQuery(
	_ *rpctypes.Context,
	path string,
	data bytes.HexBytes,
	height int64,
	prove bool,
) (*ctypes.ResultABCIQuery, error) {
	if isProofQueryPath(path) {
		return nil, errProofQueryPath
	}
	resQuery, err := env.ProxyAppQuery.Query(context.TODO(), &abci.RequestQuery{
		Path:   path,
		Data:   data,
		Height: height,
		Prove:  prove,
	})
	if err != nil {
		return nil, err
	}

	return &ctypes.ResultABCIQuery{Response: *resQuery}, nil
}

// ABCIInfo gets some info about the application.
// More: https://docs.cometbft.com/v0.38.x/rpc/#/ABCI/abci_info
func (env *Environment) ABCIInfo(_ *rpctypes.Context) (*ctypes.ResultABCIInfo, error) {
	resInfo, err := env.ProxyAppQuery.Info(context.TODO(), proxy.RequestInfo)
	if err != nil {
		return nil, err
	}

	return &ctypes.ResultABCIInfo{Response: *resInfo}, nil
}

var errProofQueryPath = errors.New("abci_query does not serve inclusion proof paths; use the tx, tx_search or prove_shares_v2 routes")

// isProofQueryPath reports whether path routes to an inclusion proof query in
// celestia-app. Those queries build a data square from the block bytes in the
// request. rpc/core calls them with blocks from the block store (see tx.go),
// so abci_query must not let callers send their own block bytes to them.
//
// It splits the path the same way the Cosmos SDK BaseApp does: on "/", after
// dropping one leading empty element.
func isProofQueryPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	return len(parts) >= 2 && parts[0] == "custom" &&
		(parts[1] == "txInclusionProof" || parts[1] == "shareInclusionProof")
}
