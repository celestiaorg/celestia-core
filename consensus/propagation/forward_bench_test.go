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
	const (
		kb = 1024
		mb = 1024 * kb

		// peers is the number of nodes that forward the block after the proposer.
		peers = 4
		// txSize is the size of every tx in the block.
		txSize = 128 * kb
	)
	// blockSizes are the block sizes to benchmark. 32MB is the mainnet
	// block size; 8MB shows whether latency scales with block size.
	blockSizes := []int{8 * mb, 32 * mb}
	// mempoolHitPcts are the shares of the block's txs each peer already has
	// in its mempool, so it doesn't need to download them.
	mempoolHitPcts := []int{0, 50, 100}

	for _, blockSize := range blockSizes {
		for _, hitPct := range mempoolHitPcts {
			name := fmt.Sprintf("block=%dMB/mempool_hit=%d%%", blockSize/mb, hitPct)
			b.Run(name, func(b *testing.B) {
				benchmarkForward(b, peers, blockSize/txSize, txSize, hitPct)
			})
		}
	}
}

func benchmarkForward(b *testing.B, peers, txCount, txSize, hitPct int) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(b)
	b.Cleanup(func() { cleanup(b) })

	// Reactor 0 proposes; reactors 1..peers forward the block down the chain.
	reactors := chainReactors(b, peers+1)
	pools := make([]swapMempool, len(reactors))
	for i, r := range reactors {
		r.mempool = &pools[i]
	}

	// Each forwarding peer reports when it receives the compact block and
	// when it has every part of the full block.
	var partsTotal atomic.Uint32
	cbArrivals := make(chan arrival, peers)
	blockArrivals := make(chan arrival, peers)
	for i, r := range reactors[1:] {
		go drainPeer(b.Context(), r, i, &partsTotal, cbArrivals, blockArrivals)
	}

	// Drop setup time so ns/op only covers proposing and collecting.
	b.ResetTimer()
	var lat latencies
	for i := range b.N {
		height := int64(i + 1)

		// Setup is excluded from the timed section.
		b.StopTimer()
		waitSettled(b, reactors, height-1)
		prop, ps, block, metaData := createTestProposal(b, sm, pv, height, 0, txCount, txSize)
		seedMempools(reactors, pools, block, hitPct, height)
		partsTotal.Store(ps.Total())
		b.StartTimer()

		require.NoError(b, reactors[0].ProposeBlock(prop, ps, metaData))
		cbAt := collect(b, cbArrivals, peers)
		blockAt := collect(b, blockArrivals, peers)
		lat.record(cbAt, blockAt)
	}
	lat.report(b)
}

// seedMempools gives every reactor a fresh mempool holding the first hitPct%
// of the block's txs, and drops state from earlier heights.
func seedMempools(reactors []*Reactor, pools []swapMempool, block *types.Block, hitPct int, height int64) {
	hits := mempoolWith(block.Txs[:len(block.Txs)*hitPct/100])
	for i, r := range reactors {
		r.Prune(height)
		pools[i].Store(hits)
	}
}

// latencies collects per-proposal timings, all measured from the first peer
// receiving the compact block.
type latencies struct {
	hop       []float64 // compact block passing from one peer to the next
	lastCB    []float64 // compact block reaching the last peer
	lastBlock []float64 // full block reaching the last peer
}

func (l *latencies) record(cbAt, blockAt []time.Time) {
	last := len(cbAt) - 1
	for i := 1; i <= last; i++ {
		l.hop = append(l.hop, ms(cbAt[i].Sub(cbAt[i-1])))
	}
	l.lastCB = append(l.lastCB, ms(cbAt[last].Sub(cbAt[0])))
	l.lastBlock = append(l.lastBlock, ms(blockAt[last].Sub(cbAt[0])))
}

func (l *latencies) report(b *testing.B) {
	report(b, "hop", l.hop)
	report(b, "last-cb", l.lastCB)
	report(b, "last-block", l.lastBlock)
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
