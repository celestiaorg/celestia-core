package propagation

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// BenchmarkForwardCompactBlock proposes blocks down a chain of peers and
// reports how long each hop takes to pass on the compact block, and how
// long the last peer takes to get the compact block and the full block.
// Latencies are measured from the first peer receiving the compact block.
func BenchmarkForwardCompactBlock(b *testing.B) {
	const peers, txSize = 4, 128 << 10
	for _, blockMB := range []int{8, 32} {
		for _, hitPct := range []int{0, 50, 100} {
			b.Run(fmt.Sprintf("block=%dMB/mempool_hit=%d%%", blockMB, hitPct), func(b *testing.B) {
				benchmarkForward(b, peers, blockMB<<20/txSize, txSize, hitPct)
			})
		}
	}
}

func benchmarkForward(b *testing.B, peers, txCount, txSize, hitPct int) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(b)
	b.Cleanup(func() { cleanup(b) })

	reactors := chainReactors(b, peers+1)
	pools := make([]swapMempool, len(reactors))
	for i, r := range reactors {
		r.mempool = &pools[i]
	}

	var total atomic.Uint32
	cbs, blocks := make(chan arrival, peers), make(chan arrival, peers)
	ctx := b.Context()
	for i, r := range reactors[1:] {
		go drainPeer(ctx, r, i, &total, cbs, blocks)
	}

	hop, cbLast, blockLast := make([]float64, 0, (peers-1)*b.N), make([]float64, 0, b.N), make([]float64, 0, b.N)
	for i := range b.N {
		b.StopTimer()
		h := int64(i + 1)
		waitSettled(b, reactors, h-1)
		prop, ps, block, metaData := createTestProposal(b, sm, pv, h, 0, txCount, txSize)
		hits := mempoolWith(block.Txs[:len(block.Txs)*hitPct/100])
		for j, r := range reactors {
			r.Prune(h)
			pools[j].Store(hits)
		}
		total.Store(ps.Total())
		b.StartTimer()

		require.NoError(b, reactors[0].ProposeBlock(prop, ps, metaData))
		cbAt, blockAt := collect(b, cbs, peers), collect(b, blocks, peers)
		for r := 1; r < peers; r++ {
			hop = append(hop, ms(cbAt[r].Sub(cbAt[r-1])))
		}
		cbLast = append(cbLast, ms(cbAt[peers-1].Sub(cbAt[0])))
		blockLast = append(blockLast, ms(blockAt[peers-1].Sub(cbAt[0])))
	}
	report(b, "hop", hop)
	report(b, "last-cb", cbLast)
	report(b, "last-block", blockLast)
}

// chainReactors connects n reactors in a line so each one has at most two
// peers, and stops them when the benchmark ends.
func chainReactors(b *testing.B, n int) []*Reactor {
	reactors := make([]*Reactor, n)
	p2pCfg := defaultTestP2PConf()
	p2pCfg.FlushThrottleTimeout = time.Millisecond
	switches := p2p.MakeConnectedSwitches(p2pCfg, n, func(i int, s *p2p.Switch) *p2p.Switch {
		reactors[i] = newPropagationReactor(s, trace.NoOpTracer(), mockPrivVal)
		reactors[i].SetLogger(log.NewNopLogger())
		s.AddReactor("BlockProp", reactors[i])
		return s
	}, func(s []*p2p.Switch, i, j int) {
		if j == i+1 {
			p2p.Connect2Switches(s, i, j)
		}
	})
	b.Cleanup(func() {
		for _, s := range switches {
			_ = s.Stop()
		}
	})
	return reactors
}

// waitSettled blocks until every reactor has decoded the block at height and
// the connections are quiet, so work left over from one height doesn't delay
// the next. Decoding runs on the receive routine and can outlast the block
// reaching consensus.
func waitSettled(b *testing.B, reactors []*Reactor, height int64) {
	decoded := func() bool {
		for _, r := range reactors {
			_, parts, _, has := r.getAllState(height, 0, true)
			if has && !parts.IsComplete() {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(30 * time.Second)
	var last int64
	for quiet := 0; quiet < 5; {
		if time.Now().After(deadline) {
			b.Fatal("timed out waiting for reactors to settle")
		}
		time.Sleep(10 * time.Millisecond)
		var total int64
		for _, r := range reactors {
			for _, p := range r.Switch.Peers().List() {
				st := p.Status()
				total += st.SendMonitor.Bytes + st.RecvMonitor.Bytes
			}
		}
		if total != last || !decoded() {
			quiet = 0
		} else {
			quiet++
		}
		last = total
	}
}

type arrival struct {
	peer int
	at   time.Time
}

// drainPeer records when a peer hands the compact block and the last missing
// original part of the current height to consensus.
func drainPeer(ctx context.Context, r *Reactor, peer int, total *atomic.Uint32, cbs, blocks chan<- arrival) {
	var height int64
	seen := map[uint32]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.proposalChan:
			cbs <- arrival{peer, time.Now()}
		case p := <-r.partChan:
			if p.Height > height {
				height, seen = p.Height, map[uint32]bool{}
			}
			if p.Height < height || seen[p.Index] {
				continue
			}
			seen[p.Index] = true
			if len(seen) == int(total.Load()) {
				blocks <- arrival{peer, time.Now()}
			}
		}
	}
}

func collect(b *testing.B, ch <-chan arrival, n int) []time.Time {
	at := make([]time.Time, n)
	for range n {
		select {
		case a := <-ch:
			at[a.peer] = a.at
		case <-time.After(30 * time.Second):
			b.Fatal("timed out waiting for peers")
		}
	}
	return at
}

func report(b *testing.B, name string, xs []float64) {
	slices.Sort(xs)
	b.ReportMetric(xs[len(xs)/2], name+"-p50-ms")
	b.ReportMetric(xs[len(xs)*95/100], name+"-p95-ms")
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// swapMempool lets the benchmark replace a peer's mempool between heights.
type swapMempool struct{ atomic.Pointer[mockMempool] }

func (m *swapMempool) GetTxByKey(key types.TxKey) (*types.CachedTx, bool) {
	return m.Load().GetTxByKey(key)
}
