package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
