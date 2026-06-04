package state

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
)

// slowCompactDB wraps a dbm.DB so Compact blocks until release is closed and
// counts how many times it was called.
type slowCompactDB struct {
	dbm.DB
	compactCount atomic.Int64
	release      chan struct{}
}

func newSlowCompactDB(db dbm.DB) *slowCompactDB {
	return &slowCompactDB{
		DB:      db,
		release: make(chan struct{}),
	}
}

func (s *slowCompactDB) Compact(start, end []byte) error {
	s.compactCount.Add(1)
	<-s.release
	return nil
}

// TestStateStore_Compact_SingleFlight verifies that a second compaction
// trigger while one is in flight does not spawn a second goroutine.
func TestStateStore_Compact_SingleFlight(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	store := dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
		compaction: &compactionState{},
	}

	store.triggerCompactionAsync(100)
	require.Eventually(t, func() bool {
		return db.compactCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.True(t, store.compaction.inFlight.Load())

	// Second trigger while the first is blocked must be skipped.
	store.triggerCompactionAsync(200)
	require.Equal(t, int64(1), db.compactCount.Load())

	close(db.release)
	store.compaction.wg.Wait()
}

// TestStateStore_Close_WaitsForCompaction verifies that Close blocks until an
// in-flight compaction has finished, so the goroutine never operates on a
// closed DB.
func TestStateStore_Close_WaitsForCompaction(t *testing.T) {
	db := newSlowCompactDB(dbm.NewMemDB())
	store := dbStore{
		db: db,
		StoreOptions: StoreOptions{
			Compact:            true,
			CompactionInterval: 1,
			Logger:             log.TestingLogger(),
		},
		compaction: &compactionState{},
	}

	store.triggerCompactionAsync(100)
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
