package store

import (
	"bytes"
	"sync"
	"time"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
)

// Prefix names used for metric labels and log fields.
const (
	prefixH  = "H"  // block meta
	prefixC  = "C"  // block commit
	prefixSC = "SC" // seen commit
	prefixP  = "P"  // block parts
)

// prunedRanges holds, per height-keyed prefix, the lex-min and lex-max raw key
// bytes that were Deleted during a single PruneBlocks invocation.
//
// A zero-length min or max for a prefix means "no key with this prefix was
// touched in this call" and is skipped at compact time.
type prunedRanges struct {
	H, C, SC, P [2][]byte
}

// hasAny reports whether any prefix range is populated.
func (r prunedRanges) hasAny() bool {
	return r.H[0] != nil || r.C[0] != nil || r.SC[0] != nil || r.P[0] != nil
}

// compactor owns the background range-compaction loop for a BlockStore.
//
// It is fed by PruneBlocks via recordAndMaybeSignal. After interval successful
// PruneBlocks calls the accumulated per-prefix byte ranges are drained and one
// db.Compact is issued per prefix. The worker is single-flight: while a
// Compact is in progress further signals collapse into one pending wake-up.
type compactor struct {
	db       dbm.DB
	interval int // PruneBlocks calls between Compacts; 0 disables the worker
	logger   log.Logger
	metrics  *Metrics

	mu      sync.Mutex
	pending prunedRanges // accumulated min/max across PruneBlocks since last drain
	count   int          // PruneBlocks calls since last drain

	wakeup chan struct{} // cap 1; drop-on-full
	done   chan struct{} // closed when the worker goroutine exits
	stop   chan struct{} // closed by stopAndWait to signal shutdown
}

// newCompactor builds a compactor. When interval > 0 the worker goroutine is
// started immediately. When interval == 0 no goroutine runs and
// recordAndMaybeSignal becomes a no-op.
func newCompactor(db dbm.DB, interval int, logger log.Logger, metrics *Metrics) *compactor {
	if metrics == nil {
		metrics = NopMetrics()
	}
	if logger == nil {
		logger = log.NewNopLogger()
	}
	c := &compactor{
		db:       db,
		interval: interval,
		logger:   logger,
		metrics:  metrics,
		wakeup:   make(chan struct{}, 1),
		done:     make(chan struct{}),
		stop:     make(chan struct{}),
	}
	if interval > 0 {
		go c.run()
	} else {
		close(c.done)
	}
	return c
}

// recordAndMaybeSignal merges the latest PruneBlocks ranges into the
// accumulator and, if the call-count threshold has been hit, signals the
// worker. It is called on the consensus thread and must never block.
func (c *compactor) recordAndMaybeSignal(r prunedRanges) {
	if c.interval == 0 || !r.hasAny() {
		return
	}
	c.mu.Lock()
	mergePrunedRanges(&c.pending, r)
	c.count++
	over := c.count >= c.interval
	c.mu.Unlock()
	if !over {
		return
	}
	select {
	case c.wakeup <- struct{}{}:
	default:
		c.metrics.CompactDroppedSignals.Add(1)
	}
}

func (c *compactor) run() {
	defer close(c.done)
	for {
		select {
		case <-c.stop:
			return
		case <-c.wakeup:
			c.drainAndCompact()
		}
	}
}

func (c *compactor) drainAndCompact() {
	c.mu.Lock()
	snap := c.pending
	c.pending = prunedRanges{}
	c.count = 0
	c.mu.Unlock()

	c.compactPrefix(prefixH, snap.H)
	c.compactPrefix(prefixC, snap.C)
	c.compactPrefix(prefixSC, snap.SC)
	c.compactPrefix(prefixP, snap.P)
}

func (c *compactor) compactPrefix(name string, r [2][]byte) {
	if r[0] == nil || r[1] == nil {
		return
	}
	// Both backends treat the upper bound as exclusive. Append a sentinel byte
	// so the range covers the actual max key.
	upper := append(append([]byte(nil), r[1]...), 0x00)

	start := time.Now()
	err := c.db.Compact(r[0], upper)
	elapsed := time.Since(start)

	if err != nil {
		c.logger.Error("blockstore compact failed",
			"prefix", name, "elapsed", elapsed, "err", err)
		c.metrics.CompactErrors.With("prefix", name).Add(1)
		return
	}
	c.logger.Info("blockstore compact ok",
		"prefix", name, "elapsed", elapsed,
		"min_len", len(r[0]), "max_len", len(r[1]))
	c.metrics.CompactTotal.With("prefix", name).Add(1)
	c.metrics.CompactDurationSeconds.With("prefix", name).Observe(elapsed.Seconds())
}

// stopAndWait signals shutdown and blocks until the worker goroutine returns.
// If a Compact is in flight it is allowed to finish first — cometbft-db has no
// cancellation API for Compact.
func (c *compactor) stopAndWait() {
	if c == nil {
		return
	}
	select {
	case <-c.stop:
		// already stopped
	default:
		close(c.stop)
	}
	<-c.done
}

// mergePrunedRanges extends dst's per-prefix [min, max] to cover src.
func mergePrunedRanges(dst *prunedRanges, src prunedRanges) {
	mergeOne(&dst.H, src.H)
	mergeOne(&dst.C, src.C)
	mergeOne(&dst.SC, src.SC)
	mergeOne(&dst.P, src.P)
}

func mergeOne(dst *[2][]byte, src [2][]byte) {
	if src[0] == nil {
		return
	}
	if dst[0] == nil || bytes.Compare(src[0], dst[0]) < 0 {
		dst[0] = append([]byte(nil), src[0]...)
	}
	if dst[1] == nil || bytes.Compare(src[1], dst[1]) > 0 {
		dst[1] = append([]byte(nil), src[1]...)
	}
}

// extendRange grows [min, max] to include k. Used in PruneBlocks while building
// per-prefix ranges over the delete loop.
func extendRange(minOut, maxOut *[]byte, k []byte) {
	if *minOut == nil || bytes.Compare(k, *minOut) < 0 {
		*minOut = append((*minOut)[:0], k...)
	}
	if *maxOut == nil || bytes.Compare(k, *maxOut) > 0 {
		*maxOut = append((*maxOut)[:0], k...)
	}
}
