package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/service"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"
)

func TestSleepUnlessStopped(t *testing.T) {
	conR := &Reactor{}
	peer := p2pmock.NewPeer(nil)

	start := time.Now()
	require.True(t, conR.sleepUnlessStopped(peer, 20*time.Millisecond), "a live peer waits the full duration")
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = peer.Stop()
	}()
	start = time.Now()
	require.False(t, conR.sleepUnlessStopped(peer, 10*time.Second), "a stopped peer ends the wait")
	require.Less(t, time.Since(start), time.Second)
}

func TestSleepUnlessStoppedReactorStop(t *testing.T) {
	// A started service gives the reactor a real Quit channel; a noop
	// implementation avoids starting consensus.
	conR := &Reactor{}
	conR.BaseService = *service.NewBaseService(nil, "Consensus", service.NewBaseService(nil, "noop", nil))
	require.NoError(t, conR.BaseService.Start())
	peer := p2pmock.NewPeer(nil)

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = conR.BaseService.Stop()
	}()
	start := time.Now()
	require.False(t, conR.sleepUnlessStopped(peer, 10*time.Second), "a stopped reactor ends the wait")
	require.Less(t, time.Since(start), time.Second)
	require.True(t, peer.IsRunning(), "only the reactor stopped")
}
