package blocksync

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/vfs"
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
	probe  *walProbe
	ch     chan [2]bool
}

func (r *switchRecorder) SwitchToConsensus(sm.State, bool) {
	_, err := os.Stat(r.marker)
	r.ch <- [2]bool{r.probe.skipping(), err == nil}
}

// walProbe is a WAL file of a wrapped FS. A Sync that does not reach the
// underlying file means WAL fsyncs are skipped.
type walProbe struct {
	vfs.File
	syncs *atomic.Int64
}

type countSyncFS struct {
	vfs.FS
	syncs *atomic.Int64
}

type countSyncFile struct {
	vfs.File
	syncs *atomic.Int64
}

func (fs countSyncFS) Create(name string) (vfs.File, error) {
	f, err := fs.FS.Create(name)
	return countSyncFile{f, fs.syncs}, err
}

func (f countSyncFile) Sync() error {
	f.syncs.Add(1)
	return f.File.Sync()
}

func newWALProbe(t *testing.T) *walProbe {
	syncs := new(atomic.Int64)
	f, err := walfsync.WrapFS(countSyncFS{vfs.NewMem(), syncs}).Create("probe.log")
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return &walProbe{f, syncs}
}

func (p *walProbe) skipping() bool {
	before := p.syncs.Load()
	if _, err := p.Write([]byte{1}); err != nil {
		panic(err)
	}
	if err := p.Sync(); err != nil {
		panic(err)
	}
	return p.syncs.Load() == before
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
			rec := &switchRecorder{marker: filepath.Join(dir, walfsync.MarkerFile), probe: newWALProbe(t), ch: make(chan [2]bool, 1)}
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
				require.True(t, rec.probe.skipping(), "block sync did not skip WAL fsyncs")
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
			require.False(t, rec.probe.skipping())
		})
	}
}
