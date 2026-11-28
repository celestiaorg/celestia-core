package config

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// Automatic pebble sizing ([storage] pebble_cache_size_mb = 0).
//
// RAM is the smaller of the machine's memory and the memory limit of this
// process's cgroup and its ancestors (v1 or v2).
// reserve is [storage] pebble_memory_reserve_mb (default 20 GiB): memory left
// for the application (the multiplexer's app process, or the app in this
// process), the node's own heap and the OS. The app should keep within about
// reserve - 4 GiB.
//
//	cache       = clamp((RAM - reserve) * 2/5, 256 MiB, 64 GiB), one cache shared by all node DBs
//	compactions = clamp(CPUs / 4, 2, 8) per DB (always automatic)
//
// The cache is 2/5 rather than all of what is left because, without cgo, pebble
// allocates the cache on the Go heap, which the garbage collector lets grow to
// about twice the live size, and the OS page cache still needs room.
//
// Examples: 32 GiB -> 4.8 GiB cache (node about 12 GiB at worst, app 16 GiB);
// 64 GiB -> 17.6 GiB; 128 GiB -> 43 GiB; 16 GiB or less -> 256 MiB.
const (
	minAutoPebbleCacheMB = 256
	maxAutoPebbleCacheMB = 64 << 10
	// unknownRAMPebbleCacheMB is the cache when the memory size is unknown.
	unknownRAMPebbleCacheMB = 1 << 10
)

// autoPebbleCacheMB returns the automatic cache size for ramMB of memory
// (0 = unknown) after leaving reserveMB.
func autoPebbleCacheMB(ramMB, reserveMB int64) int64 {
	if ramMB <= 0 {
		return unknownRAMPebbleCacheMB
	}
	return clampInt64((ramMB-reserveMB)*2/5, minAutoPebbleCacheMB, maxAutoPebbleCacheMB)
}

// autoPebbleCompactions returns the number of concurrent compactions per DB
// for cpus CPUs.
func autoPebbleCompactions(cpus int) int {
	return int(clampInt64(int64(cpus/4), 2, 8))
}

func clampInt64(v, lo, hi int64) int64 {
	return max(lo, min(v, hi))
}

// pebbleCacheMB returns the configured cache size, or the automatic one.
func (cfg *StorageConfig) pebbleCacheMB() int64 {
	if cfg.PebbleCacheSizeMB > 0 {
		return int64(cfg.PebbleCacheSizeMB)
	}
	return autoPebbleCacheMB(memoryMB(""), int64(cfg.PebbleMemoryReserveMB))
}

// One block cache shared by every DB of the process, sized by the first DB
// opened. A cache per DB would multiply the memory by the number of DBs.
var (
	pebbleCacheOnce sync.Once
	pebbleCache     *pebble.Cache
)

func sharedPebbleCache(mb int64) *pebble.Cache {
	pebbleCacheOnce.Do(func() {
		// Never unreferenced: it lives as long as the process.
		pebbleCache = pebble.NewCache(mb << 20)
	})
	return pebbleCache
}

// tunePebble sets the options for the node's DBs when [storage] pebble_tuning
// is on: the shared cache, bloom filters, larger blocks and files, more
// concurrent compactions and more room before write stalls. None of these
// changes the on-disk format in a way stock pebble cannot open.
func (cfg *StorageConfig) tunePebble(opts *pebble.Options) {
	if !cfg.PebbleTuning {
		return
	}
	opts.Cache = sharedPebbleCache(cfg.pebbleCacheMB())
	compactions := autoPebbleCompactions(runtime.NumCPU())
	opts.MaxConcurrentCompactions = func() int { return compactions }
	opts.MemTableStopWritesThreshold = 4
	opts.L0CompactionThreshold = 4
	opts.L0StopWritesThreshold = 64
	opts.LBaseMaxBytes = 512 << 20
	opts.Levels = make([]pebble.LevelOptions, 7)
	for i := range opts.Levels {
		opts.Levels[i].BlockSize = 32 << 10
		opts.Levels[i].FilterPolicy = bloom.FilterPolicy(10)
		opts.Levels[i].FilterType = pebble.TableFilter
		opts.Levels[i].TargetFileSize = int64(64<<20) << uint(i/2)
	}
}

// memoryMB returns the memory available to this process in MiB, or 0 if
// unknown: the smaller of MemTotal and the cgroup limits. root prefixes /proc
// and /sys, for tests.
func memoryMB(root string) int64 {
	mb := memTotalMB(root)
	if lim := cgroupLimitMB(root); lim > 0 && (mb == 0 || lim < mb) {
		mb = lim
	}
	return mb
}

func memTotalMB(root string) int64 {
	f, err := os.Open(filepath.Join(root, "/proc/meminfo"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb >> 10
		}
	}
	return 0
}

// cgroupLimitMB returns the smallest memory limit of this process's cgroups
// and their ancestors, v2 and v1, in MiB; 0 means no limit found.
//
// v2: memory.max from /sys/fs/cgroup/<path> up to /sys/fs/cgroup.
// v1: memory.limit_in_bytes from /sys/fs/cgroup/memory/<path> up to the
// memory root, and hierarchical_memory_limit from memory.stat.
// A path that does not exist (another cgroup namespace) falls back to the
// root. "max", huge values and unparsable files mean no limit.
func cgroupLimitMB(root string) int64 {
	v2Path, v1Path, haveV2, haveV1 := "/", "/", true, true
	if b, err := os.ReadFile(filepath.Join(root, "/proc/self/cgroup")); err == nil {
		haveV2, haveV1 = false, false
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 {
				continue
			}
			switch {
			case parts[0] == "0" && parts[1] == "":
				v2Path, haveV2 = parts[2], true
			case containsController(parts[1], "memory"):
				v1Path, haveV1 = parts[2], true
			}
		}
	}
	var limit int64
	consider := func(v int64) {
		if v > 0 && v < 1<<62 && (limit == 0 || v < limit) {
			limit = v
		}
	}
	if haveV2 {
		base := filepath.Join(root, "/sys/fs/cgroup")
		for _, dir := range cgroupAncestors(base, v2Path) {
			consider(readLimit(filepath.Join(dir, "memory.max")))
		}
	}
	if haveV1 {
		base := filepath.Join(root, "/sys/fs/cgroup/memory")
		dirs := cgroupAncestors(base, v1Path)
		for _, dir := range dirs {
			consider(readLimit(filepath.Join(dir, "memory.limit_in_bytes")))
		}
		consider(hierarchicalLimit(filepath.Join(dirs[0], "memory.stat")))
	}
	return limit >> 20
}

func containsController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// cgroupAncestors returns base/path and its parents up to base, deepest
// first. If base/path does not exist it returns only base.
func cgroupAncestors(base, path string) []string {
	dir := filepath.Join(base, filepath.Clean("/"+path))
	if _, err := os.Stat(dir); err != nil {
		return []string{base}
	}
	dirs := []string{dir}
	for dir != base {
		dir = filepath.Dir(dir)
		dirs = append(dirs, dir)
	}
	return dirs
}

// readLimit reads a byte count; "max", a missing file or garbage give 0.
func readLimit(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func hierarchicalLimit(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "hierarchical_memory_limit" {
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return 0
			}
			return v
		}
	}
	return 0
}
