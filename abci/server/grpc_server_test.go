package server

import (
	"testing"

	"github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/require"
)

func TestGRPCServer(t *testing.T) {
	protoAddr := "tcp://"
	app := types.NewGRPCApplication(types.NewBaseApplication())
	server := NewGRPCServer(protoAddr, app)

	// Start the server
	require.NoError(t, server.OnStart())
	defer server.OnStop()

	// Verify that the server's MaxConcurrentConnections
	// It is not immediately obvious how to get this value from the server.
}
