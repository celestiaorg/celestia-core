package walfsync

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	// ModeCatchup skips WAL fsyncs while catching up (the default).
	ModeCatchup = "catchup"
	// ModeAlways fsyncs the WAL on every synced write, as stock pebble does.
	ModeAlways = "always"

	// MarkerFile is the name of the crash marker in the data directory. It
	// exists while WAL fsyncs may be skipped and holds the boot ID.
	MarkerFile = "db_wal_fsync_skipped"

	// AcceptFlag is the one-shot start flag that accepts an unsynced crash.
	AcceptFlag = "accept_unsynced_db_crash"
)

// ValidateMode checks a db_wal_fsync setting. The empty string means the default.
func ValidateMode(mode string) error {
	switch mode {
	case "", ModeCatchup, ModeAlways:
		return nil
	}
	return fmt.Errorf("db_wal_fsync must be %q or %q, got %q", ModeCatchup, ModeAlways, mode)
}

// ErrUnsyncedCrash is returned by CheckMarker when the operating system went
// down while WAL fsyncs were skipped.
var ErrUnsyncedCrash = errors.New("the operating system stopped while WAL fsyncs were skipped")

// bootID is a variable so tests can simulate a reboot.
var bootID = defaultBootID

// defaultBootID returns an ID that changes on every boot, or "" if the
// platform has none (then any leftover marker counts as an OS crash).
func defaultBootID() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Controller turns WAL fsync skipping on and off for one node and keeps the
// crash marker in its data directory. A nil Controller does nothing, which is
// what NewController returns for mode "always".
type Controller struct {
	marker string

	mu     sync.Mutex
	active bool
}

// NewController returns a Controller for a node whose databases live in dir.
func NewController(mode, dir string) *Controller {
	if mode == ModeAlways {
		return nil
	}
	return &Controller{marker: filepath.Join(dir, MarkerFile)}
}

// StartCatchup writes the crash marker and then starts skipping WAL fsyncs.
// It does nothing if the Controller is nil or already active.
func (c *Controller) StartCatchup() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		return nil
	}
	if err := writeMarker(c.marker); err != nil {
		return fmt.Errorf("write WAL fsync marker: %w", err)
	}
	c.active = true
	begin()
	return nil
}

// FinishCatchup stops skipping WAL fsyncs, fsyncs every WAL that still has an
// unsynced tail, and only then removes the crash marker. Call it before the
// node switches to consensus and when it stops.
func (c *Controller) FinishCatchup() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return nil
	}
	end()
	if err := Barrier(); err != nil {
		// Leave the marker: what is on disk is unknown.
		return fmt.Errorf("fsync WAL files: %w", err)
	}
	c.active = false
	return removeMarker(c.marker)
}

// CheckMarker looks for the crash marker in dirs[0] before the databases are
// used. dirs lists every directory that holds node databases.
//
//   - No marker: nothing to do.
//   - Marker from this boot: only the process died, and the kernel still holds
//     every write in order. The WAL files under dirs are fsynced and the marker
//     is removed.
//   - Marker from another boot (or no boot ID): returns ErrUnsyncedCrash unless
//     accept is true, in which case the WAL files are fsynced, the marker is
//     removed and the node goes on to the handshake.
func CheckMarker(dirs []string, accept bool) (found bool, err error) {
	path := filepath.Join(dirs[0], MarkerFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("read %s: %w", path, err)
	}
	cur := bootID()
	sameBoot := cur != "" && string(bytes.TrimSpace(b)) == cur
	if !sameBoot && !accept {
		return true, fmt.Errorf("%w: the node databases may be out of order with each other.\n"+
			"Choose one:\n"+
			"  1. roll back the last block (`rollback`, which also clears this marker) and restart;\n"+
			"  2. re-sync from a state sync snapshot (keep priv_validator_state.json: never delete or reset it);\n"+
			"  3. start once with --%s (cometbft start) or your application's equivalent flag\n"+
			"     to try the handshake anyway.\n"+
			"marker: %s", ErrUnsyncedCrash, AcceptFlag, path)
	}
	for _, dir := range dirs {
		if err := syncWALs(dir); err != nil {
			return true, err
		}
	}
	return true, removeMarker(path)
}

// syncWALs fsyncs every WAL file under dir. It follows a symlinked dir and
// symlinked directories inside it (a database directory on another disk).
func syncWALs(dir string) error {
	return syncWALsIn(dir, make(map[string]bool))
}

func syncWALsIn(dir string, seen map[string]bool) error {
	dir, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || seen[dir] {
		return err
	}
	seen[dir] = true
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			st, err := os.Stat(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil // dangling
			}
			if err != nil {
				return err
			}
			if st.IsDir() {
				return syncWALsIn(p, seen)
			}
		} else if d.IsDir() {
			return nil
		}
		if !IsWAL(d.Name()) {
			return nil
		}
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return err
		}
		if testSyncedWAL != nil {
			testSyncedWAL(p)
		}
		return nil
	})
}

// testSyncedWAL is a test hook called after syncWALs fsyncs a file.
var testSyncedWAL func(path string)

func writeMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(bootID() + "\n")
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func removeMarker(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory.
// TODO: use tempfile.SyncDir once #3820 is merged.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" { // directories cannot be fsynced there
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
