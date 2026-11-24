package walfsync

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/stretchr/testify/require"
)

func key(i int) []byte { return []byte(fmt.Sprintf("k%06d", i)) }

// gateFS blocks sstable creation (so memtable flushes) until open is closed.
// That keeps several WALs live at once.
type gateFS struct {
	vfs.FS
	open chan struct{}
}

func (fs gateFS) Create(name string) (vfs.File, error) {
	if strings.HasSuffix(name, ".sst") {
		<-fs.open
	}
	return fs.FS.Create(name)
}

type walStats struct {
	mu        sync.Mutex
	created   int
	recycled  int
	ackedAtMk int // writes acknowledged when the newest WAL was created
	acked     int
}

func (s *walStats) listener() pebble.EventListener {
	return pebble.EventListener{WALCreated: func(info pebble.WALCreateInfo) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.created++
		if info.RecycledFileNum != 0 {
			s.recycled++
		}
		s.ackedAtMk = s.acked
	}}
}

func (s *walStats) ack(n int) {
	s.mu.Lock()
	s.acked = n
	s.mu.Unlock()
}

func openTestDB(t *testing.T, fs vfs.FS, stats *walStats) *pebble.DB {
	t.Helper()
	opts := &pebble.Options{
		FS:                          fs,
		MemTableSize:                64 << 10,
		MemTableStopWritesThreshold: 1000,
	}
	if stats != nil {
		l := stats.listener()
		opts.EventListener = &l
	}
	db, err := pebble.Open("", opts)
	require.NoError(t, err)
	return db
}

// survivors returns how many keys of 0..n-1 survived and fails if they are not
// a prefix (a hole inside the database).
func survivors(db *pebble.DB, n int) (int, error) {
	m := 0
	for i := 0; i < n; i++ {
		_, c, err := db.Get(key(i))
		if err == pebble.ErrNotFound {
			for j := i + 1; j < n; j++ {
				if _, c, err := db.Get(key(j)); err == nil {
					c.Close()
					return m, fmt.Errorf("hole: key %d missing but key %d present", i, j)
				}
			}
			return m, nil
		}
		if err != nil {
			return m, err
		}
		c.Close()
		m++
	}
	return m, nil
}

// powerCut drops every write that was not fsynced, then reopens the database.
func powerCut(t *testing.T, mem *vfs.MemFS, gate chan struct{}, db *pebble.DB) (*pebble.DB, error) {
	t.Helper()
	mem.SetIgnoreSyncs(true)
	if gate != nil {
		close(gate)
	}
	require.NoError(t, db.Close())
	mem.ResetToSyncedState()
	mem.SetIgnoreSyncs(false)
	return pebble.Open("", &pebble.Options{FS: mem})
}

func resetFlag(t *testing.T) {
	catchingUp.Store(0)
	t.Cleanup(func() { catchingUp.Store(0) })
}

// TestMultiWALPowerLoss writes through several WAL rotations while catching up
// and cuts power at random points. What survives must be a prefix that holds
// at least every write of the closed WALs, and after Barrier everything.
func TestMultiWALPowerLoss(t *testing.T) {
	const n = 300
	val := make([]byte, 4<<10)
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	for _, blockFlush := range []bool{true, false} {
		for iter := 0; iter < 10; iter++ {
			t.Run(fmt.Sprintf("blockFlush=%v/%d", blockFlush, iter), func(t *testing.T) {
				resetFlag(t)
				mem := vfs.NewStrictMem()
				var gate chan struct{}
				var fs vfs.FS = mem
				if blockFlush {
					gate = make(chan struct{})
					fs = gateFS{mem, gate}
				}
				stats := &walStats{}
				db := openTestDB(t, WrapFS(fs), stats)
				begin()

				cut := rng.Intn(n + 1)
				barrier := rng.Intn(2) == 0
				for i := 0; i < cut; i++ {
					require.NoError(t, db.Set(key(i), val, pebble.Sync))
					stats.ack(i + 1)
				}
				if barrier {
					end()
					require.NoError(t, Barrier())
				}
				stats.mu.Lock()
				minSurvive, created, recycled := stats.ackedAtMk, stats.created, stats.recycled
				stats.mu.Unlock()

				db2, err := powerCut(t, mem, gate, db)
				require.NoError(t, err)
				defer db2.Close()
				m, err := survivors(db2, n)
				require.NoError(t, err)
				t.Logf("cut=%d barrier=%v wals=%d recycled=%d survived=%d min=%d", cut, barrier, created, recycled, m, minSurvive)
				require.LessOrEqual(t, m, cut)
				require.GreaterOrEqual(t, m, minSurvive, "a closed WAL lost writes")
				if barrier {
					require.Equal(t, cut, m)
				}
			})
		}
	}
}

// TestMultiWALPowerLossRotations checks that the test above really spans
// several live WALs and recycles WAL files.
func TestMultiWALPowerLossRotations(t *testing.T) {
	resetFlag(t)
	val := make([]byte, 4<<10)
	for _, blockFlush := range []bool{true, false} {
		mem := vfs.NewStrictMem()
		var fs vfs.FS = mem
		gate := make(chan struct{})
		if blockFlush {
			fs = gateFS{mem, gate}
		}
		stats := &walStats{}
		db := openTestDB(t, WrapFS(fs), stats)
		for i := 0; i < 300; i++ {
			require.NoError(t, db.Set(key(i), val, pebble.Sync))
			if !blockFlush && i%20 == 0 {
				require.NoError(t, db.Flush())
			}
		}
		close(gate)
		require.NoError(t, db.Close())
		require.GreaterOrEqual(t, stats.created, 5)
		if !blockFlush {
			require.Positive(t, stats.recycled, "WAL recycling did not happen")
		}
	}
}

// TestNoCloseSyncLeavesHole shows why Close must fsync: without it, a WAL
// rotated away while catching up keeps its unsynced tail, and a later real
// sync of the newest WAL makes a database with a hole (or one that does not
// open).
func TestNoCloseSyncLeavesHole(t *testing.T) {
	resetFlag(t)
	syncOnClose = false
	t.Cleanup(func() { syncOnClose = true })

	const n = 300
	val := make([]byte, 4<<10)
	mem := vfs.NewStrictMem()
	gate := make(chan struct{})
	stats := &walStats{}
	db := openTestDB(t, WrapFS(gateFS{mem, gate}), stats)
	begin()
	for i := 0; i < n; i++ {
		require.NoError(t, db.Set(key(i), val, pebble.Sync))
	}
	end()
	require.NoError(t, Barrier())
	require.NoError(t, db.Set(key(n), val, pebble.Sync)) // a real sync at the tip
	require.GreaterOrEqual(t, stats.created, 3)

	db2, err := powerCut(t, mem, gate, db)
	if err != nil {
		t.Logf("database does not open: %v", err)
		return
	}
	defer db2.Close()
	m, err := survivors(db2, n+1)
	require.Error(t, err, "expected a hole, %d of %d keys survived", m, n+1)
	t.Log(err)
}

// TestSIGKILL kills a child that writes while catching up and checks that every
// acknowledged write survives.
func TestSIGKILL(t *testing.T) {
	if os.Getenv("WALFSYNC_CHILD_DIR") != "" {
		sigkillChild(os.Getenv("WALFSYNC_CHILD_DIR"))
		return
	}
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^TestSIGKILL$", "-test.count=1")
		cmd.Env = append(os.Environ(), "WALFSYNC_CHILD_DIR="+dir)
		out, err := cmd.StdoutPipe()
		require.NoError(t, err)
		require.NoError(t, cmd.Start())

		acked := -1
		sc := bufio.NewScanner(out)
		killAt := 200 + rand.Intn(2000)
		for sc.Scan() {
			v, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
			if err != nil {
				continue
			}
			acked = v
			if acked == killAt {
				require.NoError(t, cmd.Process.Signal(syscall.SIGKILL))
			}
		}
		_ = cmd.Wait()
		require.Positive(t, acked, "child wrote nothing")

		db, err := pebble.Open(dir, &pebble.Options{})
		require.NoError(t, err)
		m, err := survivors(db, acked+100)
		require.NoError(t, db.Close())
		require.NoError(t, err)
		t.Logf("acknowledged %d, survived %d", acked+1, m)
		require.GreaterOrEqual(t, m, acked+1)
	}
}

// sigkillChild writes keys with pebble.Sync while catching up and prints the
// index of each acknowledged write. It never closes the database.
func sigkillChild(dir string) {
	db, err := pebble.Open(dir, &pebble.Options{FS: WrapFS(vfs.Default), MemTableSize: 64 << 10})
	if err != nil {
		panic(err)
	}
	begin()
	val := make([]byte, 1<<10)
	for i := 0; ; i++ {
		if err := db.Set(key(i), val, pebble.Sync); err != nil {
			panic(err)
		}
		fmt.Fprintln(os.Stdout, i)
		if i == 5000 {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
	}
}

// blockingSyncFS makes the underlying Sync of WAL files wait for release.
type blockingSyncFS struct {
	vfs.FS
	release chan struct{}
	entered chan struct{}
}

type blockingSyncFile struct {
	vfs.File
	fs *blockingSyncFS
}

func (fs *blockingSyncFS) Create(name string) (vfs.File, error) {
	f, err := fs.FS.Create(name)
	if err != nil || !IsWAL(name) {
		return f, err
	}
	return blockingSyncFile{f, fs}, nil
}

func (f blockingSyncFile) Sync() error {
	select {
	case f.fs.entered <- struct{}{}:
	default:
	}
	<-f.fs.release
	return f.File.Sync()
}

func TestControllerMarker(t *testing.T) {
	resetFlag(t)
	dir := t.TempDir()
	bootID = func() string { return "boot-a" }
	t.Cleanup(func() { bootID = defaultBootID })

	require.Nil(t, NewController(ModeAlways, dir))
	var nilCtrl *Controller
	require.NoError(t, nilCtrl.StartCatchup())
	require.NoError(t, nilCtrl.FinishCatchup())
	require.False(t, Skipping())

	c := NewController(ModeCatchup, dir)
	require.NoError(t, c.StartCatchup())
	require.True(t, Skipping())
	b, err := os.ReadFile(dir + "/" + MarkerFile)
	require.NoError(t, err)
	require.Equal(t, "boot-a\n", string(b))

	require.NoError(t, c.FinishCatchup())
	require.False(t, Skipping())
	require.NoFileExists(t, dir+"/"+MarkerFile)
	require.NoError(t, c.FinishCatchup()) // idempotent
}

// TestMarkerRemovedAfterBarrier checks that the marker outlives the WAL fsync.
func TestMarkerRemovedAfterBarrier(t *testing.T) {
	resetFlag(t)
	dir := t.TempDir()
	fs := &blockingSyncFS{FS: vfs.NewMem(), release: make(chan struct{}), entered: make(chan struct{}, 1)}
	c := NewController(ModeCatchup, dir)
	require.NoError(t, c.StartCatchup())

	db := openTestDB(t, WrapFS(fs), nil)
	require.NoError(t, db.Set(key(0), []byte("v"), pebble.Sync)) // sync skipped: no block

	done := make(chan error, 1)
	go func() { done <- c.FinishCatchup() }()
	<-fs.entered
	require.FileExists(t, dir+"/"+MarkerFile, "marker removed before the WAL fsync finished")
	select {
	case <-done:
		t.Fatal("FinishCatchup returned before the WAL fsync finished")
	default:
	}
	close(fs.release)
	require.NoError(t, <-done)
	require.NoFileExists(t, dir+"/"+MarkerFile)
	require.NoError(t, db.Close())
}

func TestCheckMarker(t *testing.T) {
	resetFlag(t)
	t.Cleanup(func() { bootID = defaultBootID })
	write := func(t *testing.T) string {
		dir := t.TempDir()
		bootID = func() string { return "boot-a" }
		c := NewController(ModeCatchup, dir)
		require.NoError(t, c.StartCatchup())
		end() // simulate a crash: the flag dies with the process
		return dir
	}

	t.Run("none", func(t *testing.T) {
		found, err := CheckMarker(t.TempDir(), false)
		require.NoError(t, err)
		require.False(t, found)
	})
	t.Run("same boot starts", func(t *testing.T) {
		dir := write(t)
		require.NoError(t, os.WriteFile(dir+"/000001.log", []byte("x"), 0o600))
		found, err := CheckMarker(dir, false)
		require.NoError(t, err)
		require.True(t, found)
		require.NoFileExists(t, dir+"/"+MarkerFile)
	})
	t.Run("other boot refuses", func(t *testing.T) {
		dir := write(t)
		bootID = func() string { return "boot-b" }
		found, err := CheckMarker(dir, false)
		require.ErrorIs(t, err, ErrUnsyncedCrash)
		require.True(t, found)
		for _, s := range []string{"rollback", "snapshot", "priv_validator_state.json", "accept_unsynced_crash"} {
			require.Contains(t, err.Error(), s)
		}
		require.FileExists(t, dir+"/"+MarkerFile)
	})
	t.Run("no boot id refuses", func(t *testing.T) {
		dir := write(t)
		bootID = func() string { return "" }
		_, err := CheckMarker(dir, false)
		require.ErrorIs(t, err, ErrUnsyncedCrash)
	})
	t.Run("override starts", func(t *testing.T) {
		dir := write(t)
		bootID = func() string { return "boot-b" }
		found, err := CheckMarker(dir, true)
		require.NoError(t, err)
		require.True(t, found)
		require.NoFileExists(t, dir+"/"+MarkerFile)
	})
}

func TestValidateMode(t *testing.T) {
	for mode, ok := range map[string]bool{"": true, ModeCatchup: true, ModeAlways: true, "never": false, "Catchup": false} {
		if ok {
			require.NoError(t, ValidateMode(mode), mode)
		} else {
			require.ErrorContains(t, ValidateMode(mode), "wal_fsync", mode)
		}
	}
}

func TestOnlyWALSyncsAreSkipped(t *testing.T) {
	resetFlag(t)
	mem := vfs.NewStrictMem()
	fs := WrapFS(mem)
	begin()
	for _, name := range []string{"000001.log", "000002.sst", "MANIFEST-000001", "OPTIONS-000003"} {
		f, err := fs.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, f.Sync())
		_, isWAL := f.(*walFile)
		require.Equal(t, IsWAL(name), isWAL, name)
	}
	d, err := mem.OpenDir("")
	require.NoError(t, err)
	require.NoError(t, d.Sync())
	mem.ResetToSyncedState()
	for name, size := range map[string]int64{"000001.log": 0, "000002.sst": 4, "MANIFEST-000001": 4, "OPTIONS-000003": 4} {
		st, err := mem.Stat(name)
		require.NoError(t, err)
		require.Equal(t, size, st.Size(), name)
	}
}
