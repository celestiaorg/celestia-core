package store

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
)

// recordingDB wraps a real dbm.DB so tests can observe Compact calls and
// optionally inject blocking / failing behaviour.
type recordingDB struct {
	dbm.DB

	mu          sync.Mutex
	compactArgs [][2][]byte

	// optional hooks
	beforeCompact func()       // runs before delegating
	afterCompact  func()       // runs after delegating
	failCompact   func() error // when non-nil, returned instead of delegating
}

func (d *recordingDB) Compact(start, end []byte) error {
	d.mu.Lock()
	d.compactArgs = append(d.compactArgs, [2][]byte{
		append([]byte(nil), start...),
		append([]byte(nil), end...),
	})
	bc := d.beforeCompact
	ac := d.afterCompact
	fc := d.failCompact
	d.mu.Unlock()

	if bc != nil {
		bc()
	}
	defer func() {
		if ac != nil {
			ac()
		}
	}()
	if fc != nil {
		return fc()
	}
	return d.DB.Compact(start, end)
}

func (d *recordingDB) calls() [][2][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][2][]byte, len(d.compactArgs))
	copy(out, d.compactArgs)
	return out
}

func TestExtendRange(t *testing.T) {
	var min, max []byte
	extendRange(&min, &max, []byte("H:5"))
	assert.Equal(t, []byte("H:5"), min)
	assert.Equal(t, []byte("H:5"), max)

	extendRange(&min, &max, []byte("H:3"))
	assert.Equal(t, []byte("H:3"), min)
	assert.Equal(t, []byte("H:5"), max)

	extendRange(&min, &max, []byte("H:7"))
	assert.Equal(t, []byte("H:3"), min)
	assert.Equal(t, []byte("H:7"), max)

	// Re-inserting an existing key is a no-op.
	extendRange(&min, &max, []byte("H:3"))
	extendRange(&min, &max, []byte("H:7"))
	assert.Equal(t, []byte("H:3"), min)
	assert.Equal(t, []byte("H:7"), max)
}

func TestMergePrunedRanges(t *testing.T) {
	dst := prunedRanges{
		H: [2][]byte{[]byte("H:10"), []byte("H:20")},
	}
	src := prunedRanges{
		H: [2][]byte{[]byte("H:05"), []byte("H:30")},
		P: [2][]byte{[]byte("P:1:0"), []byte("P:1:9")},
	}
	mergePrunedRanges(&dst, src)

	assert.Equal(t, []byte("H:05"), dst.H[0])
	assert.Equal(t, []byte("H:30"), dst.H[1])
	assert.Equal(t, []byte("P:1:0"), dst.P[0])
	assert.Equal(t, []byte("P:1:9"), dst.P[1])
	// C: and SC: stay nil because neither side populated them.
	assert.Nil(t, dst.C[0])
	assert.Nil(t, dst.SC[0])
}

func TestPrunedRangesHasAny(t *testing.T) {
	var empty prunedRanges
	assert.False(t, empty.hasAny())

	r := prunedRanges{C: [2][]byte{[]byte("C:1"), []byte("C:1")}}
	assert.True(t, r.hasAny())
}

func TestCompactorDisabledIsNoop(t *testing.T) {
	rdb := &recordingDB{DB: dbm.NewMemDB()}
	c := newCompactor(rdb, 0, log.TestingLogger(), NopMetrics())

	// run() should not have been started; done is already closed.
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("done channel not closed for disabled compactor")
	}

	c.recordAndMaybeSignal(prunedRanges{H: [2][]byte{[]byte("H:1"), []byte("H:9")}})
	c.stopAndWait() // must be safe even though run() never started

	assert.Empty(t, rdb.calls())
}

func TestCompactorIssuesPerPrefixCompactsAtThreshold(t *testing.T) {
	rdb := &recordingDB{DB: dbm.NewMemDB()}
	c := newCompactor(rdb, 3, log.TestingLogger(), NopMetrics())
	defer c.stopAndWait()

	// Three PruneBlocks-equivalent calls, each touching every tracked prefix.
	for i := 0; i < 3; i++ {
		c.recordAndMaybeSignal(prunedRanges{
			H:  [2][]byte{[]byte("H:1"), []byte("H:9")},
			C:  [2][]byte{[]byte("C:1"), []byte("C:9")},
			SC: [2][]byte{[]byte("SC:1"), []byte("SC:9")},
			P:  [2][]byte{[]byte("P:1:0"), []byte("P:9:0")},
		})
	}

	require.Eventually(t, func() bool {
		return len(rdb.calls()) == 4
	}, 2*time.Second, 10*time.Millisecond, "expected one Compact per tracked prefix")

	// Sanity: the lower bound matches the smallest recorded key per prefix.
	calls := rdb.calls()
	lowerBounds := map[string]bool{
		"H:1": false, "C:1": false, "SC:1": false, "P:1:0": false,
	}
	for _, c := range calls {
		lowerBounds[string(c[0])] = true
	}
	for k, seen := range lowerBounds {
		assert.True(t, seen, "no Compact issued with lower bound %q", k)
	}
}

func TestCompactorBelowThresholdDoesNothing(t *testing.T) {
	rdb := &recordingDB{DB: dbm.NewMemDB()}
	c := newCompactor(rdb, 5, log.TestingLogger(), NopMetrics())
	defer c.stopAndWait()

	for i := 0; i < 4; i++ {
		c.recordAndMaybeSignal(prunedRanges{H: [2][]byte{[]byte("H:1"), []byte("H:9")}})
	}

	// Give the worker time to wake up if it were going to.
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, rdb.calls(), "compactor must not fire below threshold")
}

func TestCompactorSingleFlightCollapsesSignals(t *testing.T) {
	release := make(chan struct{})
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	rdb := &recordingDB{
		DB: dbm.NewMemDB(),
		beforeCompact: func() {
			n := inFlight.Add(1)
			if n > maxInFlight.Load() {
				maxInFlight.Store(n)
			}
			<-release
		},
		afterCompact: func() {
			inFlight.Add(-1)
		},
	}
	metrics := NopMetrics()
	c := newCompactor(rdb, 1, log.TestingLogger(), metrics)
	defer func() {
		// release any pending compactions so stopAndWait doesn't hang
		close(release)
		c.stopAndWait()
	}()

	// First batch triggers the worker; the Compact blocks on `release`.
	c.recordAndMaybeSignal(prunedRanges{H: [2][]byte{[]byte("H:1"), []byte("H:1")}})

	// Spam more signals while the worker is blocked. These collapse into at
	// most one pending wake-up.
	for i := 0; i < 20; i++ {
		c.recordAndMaybeSignal(prunedRanges{H: [2][]byte{[]byte("H:1"), []byte("H:1")}})
	}

	// Verify the first compact is actually running before we release.
	require.Eventually(t, func() bool {
		return inFlight.Load() == 1
	}, time.Second, 10*time.Millisecond)

	// Only one Compact can be in flight at a time.
	assert.Equal(t, int32(1), maxInFlight.Load())
}

func TestCompactorStopWaitsForInFlightCompact(t *testing.T) {
	release := make(chan struct{})
	compactReturned := make(chan struct{})

	rdb := &recordingDB{
		DB: dbm.NewMemDB(),
		beforeCompact: func() {
			<-release
		},
		afterCompact: func() {
			close(compactReturned)
		},
	}
	c := newCompactor(rdb, 1, log.TestingLogger(), NopMetrics())

	c.recordAndMaybeSignal(prunedRanges{H: [2][]byte{[]byte("H:1"), []byte("H:1")}})
	// Wait for compact to be in flight.
	time.Sleep(50 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		c.stopAndWait()
		close(stopped)
	}()

	// stopAndWait must not return before the in-flight compact finishes.
	select {
	case <-stopped:
		t.Fatal("stopAndWait returned before Compact finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case <-compactReturned:
	case <-time.After(time.Second):
		t.Fatal("Compact never returned")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stopAndWait did not return after Compact finished")
	}
}

func TestCompactorIgnoresEmptyRanges(t *testing.T) {
	rdb := &recordingDB{DB: dbm.NewMemDB()}
	c := newCompactor(rdb, 1, log.TestingLogger(), NopMetrics())
	defer c.stopAndWait()

	// Empty prunedRanges should not trigger a wakeup.
	c.recordAndMaybeSignal(prunedRanges{})

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, rdb.calls())
}

func TestCompactorCompactsExtendedUpperBound(t *testing.T) {
	rdb := &recordingDB{DB: dbm.NewMemDB()}
	c := newCompactor(rdb, 1, log.TestingLogger(), NopMetrics())
	defer c.stopAndWait()

	c.recordAndMaybeSignal(prunedRanges{H: [2][]byte{[]byte("H:1"), []byte("H:9")}})

	require.Eventually(t, func() bool {
		return len(rdb.calls()) >= 1
	}, time.Second, 10*time.Millisecond)

	call := rdb.calls()[0]
	assert.Equal(t, []byte("H:1"), call[0])
	// Upper bound has a 0x00 sentinel appended so the range covers the max key.
	require.Equal(t, len("H:9")+1, len(call[1]))
	assert.True(t, bytes.HasPrefix(call[1], []byte("H:9")))
	assert.Equal(t, byte(0x00), call[1][len(call[1])-1])
}
