package state

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
)

// slowCompactDB wraps a dbm.DB so that Compact blocks until release is closed
// and records the byte ranges it was called with. Mirrors the helper in
// store/store_test.go.
type slowCompactDB struct {
	dbm.DB
	compactCount atomic.Int64
	release      chan struct{}

	mu     sync.Mutex
	ranges [][2][]byte
}

func newSlowCompactDB(db dbm.DB) *slowCompactDB {
	return &slowCompactDB{
		DB:      db,
		release: make(chan struct{}),
	}
}

func (s *slowCompactDB) Compact(start, end []byte) error {
	s.compactCount.Add(1)
	s.mu.Lock()
	s.ranges = append(s.ranges, [2][]byte{
		append([]byte(nil), start...),
		append([]byte(nil), end...),
	})
	s.mu.Unlock()
	<-s.release
	return nil
}

func (s *slowCompactDB) capturedRanges() [][2][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2][]byte, len(s.ranges))
	copy(out, s.ranges)
	return out
}

// TestStateStore_CompactRange_BoundsPerFamily verifies that a single
// compaction trigger issues one Compact call per height-keyed family with
// the expected byte ranges.
func TestStateStore_CompactRange_BoundsPerFamily(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	close(db.release) // never block
	store := &dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
	}

	store.triggerCompactionAsync(100, 500)
	store.compactionWg.Wait()

	require.EqualValues(t, 3, db.compactCount.Load(),
		"one Compact per height-keyed family (validators, params, abci responses)")
	require.EqualValues(t, 500, store.compactionFrom, "marker must advance on success")

	expected := [][2][]byte{
		{[]byte("validatorsKey:100"), []byte("validatorsKey:500")},
		{[]byte("consensusParamsKey:100"), []byte("consensusParamsKey:500")},
		{[]byte("abciResponsesKey:100"), []byte("abciResponsesKey:500")},
	}
	require.Equal(t, expected, db.capturedRanges())
}

// TestStateStore_CompactRange_AdvancesMarker verifies that compactionFrom
// advances to `to` after a successful compaction and that the next trigger
// starts from the new marker.
func TestStateStore_CompactRange_AdvancesMarker(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	close(db.release) // never block
	store := &dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
	}

	store.triggerCompactionAsync(50, 300)
	store.compactionWg.Wait()
	require.EqualValues(t, 300, store.compactionFrom)

	db.mu.Lock()
	db.ranges = nil
	db.mu.Unlock()

	// Subsequent trigger: fromAtCallSite of 280 is ignored because the marker
	// is already 300 — the new range is [300, 800).
	store.triggerCompactionAsync(280, 800)
	store.compactionWg.Wait()
	require.EqualValues(t, 800, store.compactionFrom)

	ranges := db.capturedRanges()
	require.Len(t, ranges, 3)
	require.Equal(t, []byte("validatorsKey:300"), ranges[0][0])
	require.Equal(t, []byte("validatorsKey:800"), ranges[0][1])
}

// TestStateStore_CompactRange_NoopWhenEmpty verifies that no goroutine is
// spawned when the marker is already at or past the retain height.
func TestStateStore_CompactRange_NoopWhenEmpty(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	store := &dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
	}
	store.compactionFrom = 1000

	store.triggerCompactionAsync(500, 900)

	require.False(t, store.compacting.Load())
	require.EqualValues(t, 0, db.compactCount.Load())
}

// TestStateStore_CompactRange_SingleFlight verifies that a second trigger
// while one is in flight does not spawn a second goroutine.
func TestStateStore_CompactRange_SingleFlight(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	store := &dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
	}

	store.triggerCompactionAsync(0, 100)
	require.Eventually(t, func() bool {
		return db.compactCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.True(t, store.compacting.Load())

	// Second trigger while the first is blocked — must be skipped.
	store.triggerCompactionAsync(100, 200)
	require.Equal(t, int64(1), db.compactCount.Load())

	close(db.release)
	store.compactionWg.Wait()
	require.EqualValues(t, 3, db.compactCount.Load())
}

// TestStateStore_Close_WaitsForCompaction verifies that Close blocks until an
// in-flight compaction has finished.
func TestStateStore_Close_WaitsForCompaction(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	store := &dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
	}

	store.triggerCompactionAsync(0, 100)
	require.Eventually(t, func() bool {
		return db.compactCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()

	select {
	case <-closeDone:
		t.Fatal("Close returned before compaction finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(db.release)
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after compaction released")
	}
}
