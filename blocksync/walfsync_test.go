package blocksync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/walfsync"
	"github.com/cometbft/cometbft/p2p"
	sm "github.com/cometbft/cometbft/state"
)

// switchRecorder stands in for the consensus reactor and records whether WAL
// fsyncs were still skipped, and the crash marker still present, when block
// sync handed over.
type switchRecorder struct {
	p2p.BaseReactor
	marker string
	ch     chan [2]bool
}

func (r *switchRecorder) SwitchToConsensus(sm.State, bool) {
	_, err := os.Stat(r.marker)
	r.ch <- [2]bool{walfsync.Skipping(), err == nil}
}

// TestWALFsyncBarrierBeforeSwitch checks that block sync makes its writes
// durable before it switches to consensus, both when it caught up with a peer
// and when the local node holds a third of the voting power.
func TestWALFsyncBarrierBeforeSwitch(t *testing.T) {
	for _, localBlocks := range []bool{false, true} {
		t.Run(map[bool]string{false: "caught up", true: "local node blocks the chain"}[localBlocks], func(t *testing.T) {
			config = test.ResetTestRoot("blocksync_walfsync_test")
			defer os.RemoveAll(config.RootDir)
			genDoc, privVals := randGenesisDoc(1, false, 30)

			dir := t.TempDir()
			ctrl := walfsync.NewController(walfsync.ModeCatchup, dir)
			rec := &switchRecorder{marker: filepath.Join(dir, walfsync.MarkerFile), ch: make(chan [2]bool, 1)}
			rec.BaseReactor = *p2p.NewBaseReactor("Recorder", rec)

			syncing := newReactor(t, log.TestingLogger(), genDoc, privVals, 0)
			syncing.reactor.switchToConsensusMs = 50
			syncing.reactor.walFsync = ctrl
			pairs := []ReactorPair{syncing}
			if localBlocks {
				pub, err := privVals[0].GetPubKey()
				require.NoError(t, err)
				syncing.reactor.localAddr = pub.Address()
			} else {
				pairs = append(pairs, newReactor(t, log.TestingLogger(), genDoc, privVals, 20))
			}
			defer func() {
				for _, r := range pairs {
					require.NoError(t, r.reactor.Stop())
					require.NoError(t, r.app.Stop())
				}
			}()

			switches := make([]*p2p.Switch, 0, len(pairs))
			for i, r := range pairs {
				switches = append(switches, p2p.MakeConnectedSwitches(config.P2P, 1, func(_ int, s *p2p.Switch) *p2p.Switch {
					s.AddReactor("BLOCKSYNC", r.reactor)
					if i == 0 {
						s.AddReactor("CONSENSUS", rec)
					}
					return s
				}, p2p.Connect2Switches)...)
			}
			if !localBlocks {
				require.True(t, ctrl.Active(), "block sync did not skip WAL fsyncs")
				require.True(t, walfsync.Skipping())
				require.FileExists(t, rec.marker)
				p2p.Connect2Switches(switches, 0, 1)
			}

			select {
			case got := <-rec.ch:
				require.False(t, got[0], "WAL fsyncs still skipped at the switch")
				require.False(t, got[1], "crash marker still present at the switch")
			case <-time.After(60 * time.Second):
				t.Fatal("block sync did not switch to consensus")
			}
			require.False(t, ctrl.Active())
		})
	}
}
