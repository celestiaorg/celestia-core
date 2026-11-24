// Package walfsync lets pebble databases skip write-ahead log fsyncs while a
// node catches up, and only then.
//
// WrapFS returns a pebble vfs.FS whose WAL files (names ending in ".log") turn
// Sync, SyncData and SyncTo into no-ops while the process-wide catch-up flag is
// set. The flag is read at each Sync call. Writes still use pebble.Sync, so a
// write returns only after pebble has handed its bytes to the kernel: a process
// crash (panic, SIGKILL, OOM kill) loses nothing. sstables, the MANIFEST,
// OPTIONS files and directories keep their real fsyncs, so pebble's own crash
// safety is unchanged.
//
// Two rules keep each database a prefix of its writes after a power loss:
//   - Closing a WAL file (pebble closes the old WAL on every memtable rotation,
//     before the new WAL takes writes) always fsyncs it if a sync was skipped.
//     Only the newest WAL of a database can therefore have an unsynced tail.
//   - Barrier fsyncs every WAL file that is still open. FinishCatchup clears the
//     flag and then calls Barrier, so once it returns every write acknowledged
//     before it is durable, as with stock fsync.
//
// What a power loss or kernel crash during catch-up can still break is the
// order between databases (for example the blockstore ahead of the state
// store). A Controller records that risk in a marker file that holds the boot
// ID, and CheckMarker refuses to start after an OS crash until the operator
// picks a recovery path.
package walfsync

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/vfs"
)

// catchingUp counts the catch-ups in progress in this process. WAL syncs are
// skipped while it is positive.
var catchingUp atomic.Int64

// open holds the WAL files handed out by every wrapped FS that are not closed.
var open = struct {
	sync.Mutex
	files map[*walFile]struct{}
}{files: make(map[*walFile]struct{})}

// syncOnClose is only turned off by tests, to show the hole it prevents.
var syncOnClose = true

// Skipping reports whether WAL syncs are skipped right now.
func Skipping() bool { return catchingUp.Load() > 0 }

// begin and end move the process-wide flag. Callers outside tests use a
// Controller, which writes the crash marker first.
func begin() { catchingUp.Add(1) }

func end() {
	if catchingUp.Add(-1) < 0 {
		catchingUp.Store(0)
	}
}

// IsWAL reports whether a pebble file name is a write-ahead log.
func IsWAL(name string) bool { return strings.HasSuffix(name, ".log") }

// WrapFS wraps fs so that its WAL files follow the catch-up flag. Pass the
// result as pebble.Options.FS.
func WrapFS(fs vfs.FS) vfs.FS { return walFS{fs} }

type walFS struct{ vfs.FS }

func (fs walFS) Create(name string) (vfs.File, error) {
	f, err := fs.FS.Create(name)
	return track(name, f, err)
}

func (fs walFS) ReuseForWrite(oldname, newname string) (vfs.File, error) {
	f, err := fs.FS.ReuseForWrite(oldname, newname)
	return track(newname, f, err)
}

func (fs walFS) OpenReadWrite(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := fs.FS.OpenReadWrite(name, opts...)
	return track(name, f, err)
}

func track(name string, f vfs.File, err error) (vfs.File, error) {
	if err != nil || !IsWAL(name) {
		return f, err
	}
	w := &walFile{File: f}
	open.Lock()
	open.files[w] = struct{}{}
	open.Unlock()
	return w, nil
}

// walFile is a WAL file. mu serialises real syncs, Close and the dirty flag,
// so Barrier never returns while a Close-time fsync is still running.
type walFile struct {
	vfs.File
	mu     sync.Mutex
	dirty  bool // a sync was skipped since the last real one
	closed bool
}

func (f *walFile) Sync() error { return f.sync(f.File.Sync) }

func (f *walFile) SyncData() error { return f.sync(f.File.SyncData) }

func (f *walFile) SyncTo(length int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if Skipping() {
		f.dirty = true
		return true, nil
	}
	full, err := f.File.SyncTo(length)
	if err == nil && full {
		f.dirty = false
	}
	return full, err
}

func (f *walFile) sync(fn func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if Skipping() {
		f.dirty = true
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	f.dirty = false
	return nil
}

// Close fsyncs the file if a sync was skipped, whatever the flag says now, and
// then closes it.
func (f *walFile) Close() error {
	open.Lock()
	delete(open.files, f)
	open.Unlock()

	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	var err error
	if f.dirty && syncOnClose {
		err = f.File.Sync()
		f.dirty = false
	}
	return errors.Join(err, f.File.Close())
}

// syncNow fsyncs an open file. A file closed in the meantime was synced by
// Close, which holds mu until it is done.
func (f *walFile) syncNow() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.dirty {
		return nil
	}
	if err := f.File.Sync(); err != nil {
		return err
	}
	f.dirty = false
	return nil
}

// Barrier fsyncs every open WAL file of every wrapped FS whose last sync was
// skipped. Together with the sync on Close, every write acknowledged before
// Barrier was called is durable when it returns without error.
func Barrier() error {
	open.Lock()
	files := make([]*walFile, 0, len(open.files))
	for f := range open.files {
		files = append(files, f)
	}
	open.Unlock()

	var errs []error
	for _, f := range files {
		errs = append(errs, f.syncNow())
	}
	return errors.Join(errs...)
}
