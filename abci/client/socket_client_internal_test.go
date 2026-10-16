package abcicli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/types"
)

// TestDidRecvResponseAfterFlushQueue ensures a response that arrives after
// flushQueue returns an error instead of panicking on a double Done.
func TestDidRecvResponseAfterFlushQueue(t *testing.T) {
	cli := NewSocketClient("", false).(*socketClient)
	cli.reqSent.PushBack(NewReqRes(types.ToRequestFlush()))

	cli.flushQueue()

	require.NotPanics(t, func() {
		require.Error(t, cli.didRecvResponse(types.ToResponseFlush()))
	})
}
