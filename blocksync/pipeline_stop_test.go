package blocksync

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/p2p"
)

func goroutineStacks() string {
	buf := make([]byte, 1<<22)
	return string(buf[:runtime.Stack(buf, true)])
}

// TestPipelineStopMidSync stops a pipelined sync while blocks are still being
// applied. The pool routine must exit without leaving a speculative goroutine
// behind, and the stored state must match the block store (no block is applied
// after the stop and none is saved without being applied).
func TestPipelineStopMidSync(t *testing.T) {
	config = test.ResetTestRoot("blocksync_pipeline_stop_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, byAddr, schedule := pipelineFixture(t)

	const maxHeight = int64(40)
	source := newPipeNode(t, genDoc, byAddr, schedule, maxHeight, 0)
	defer source.appConns.Stop() //nolint:errcheck
	receiver := newPipeNode(t, genDoc, byAddr, schedule, 0, 0)
	defer receiver.appConns.Stop() //nolint:errcheck
	receiver.reactor.pipeline = true
	source.reactor.pipeline = true
	nodes := []*pipeNode{source, receiver}
	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", nodes[i].reactor)
		return s
	}, p2p.Connect2Switches)

	start := time.Now()
	for receiver.blockStore.Height() < 10 {
		require.Less(t, time.Since(start), 60*time.Second, "receiver did not start syncing")
		time.Sleep(time.Millisecond)
	}
	for _, n := range nodes {
		require.NoError(t, n.reactor.Switch.Stop())
	}

	require.Eventually(t, func() bool {
		return !strings.Contains(goroutineStacks(), "blocksync.(*Reactor).poolRoutine")
	}, 20*time.Second, 10*time.Millisecond, "pool routine did not exit")
	require.NotContains(t, goroutineStacks(), "blocksync.(*blockSpec).run", "speculative goroutine leaked")

	st, err := receiver.stateStore.Load()
	require.NoError(t, err)
	require.Equal(t, receiver.blockStore.Height(), st.LastBlockHeight)
	require.Less(t, st.LastBlockHeight, maxHeight)
}
