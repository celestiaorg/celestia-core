package config

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// Automatic pebble sizing ([storage] pebble_cache_size_mb = 0 and
// pebble_max_compactions = 0).
//
// RAM is the smaller of the machine's memory and the cgroup memory limit.
// reserve is [storage] pebble_memory_reserve_mb (default 20 GiB): memory left
// for the application (the multiplexer's app process, or the app in this
// process), the node's own heap and the OS. The app should keep within about
// reserve - 4 GiB.
//
//	cache       = clamp((RAM - reserve) * 2/5, 256 MiB, 64 GiB), one cache shared by all node DBs
//	compactions = clamp(CPUs / 4, 2, 8) per DB
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

// AutoPebbleCacheMB returns the automatic cache size for ramMB of memory
// (0 = unknown) after leaving reserveMB.
func AutoPebbleCacheMB(ramMB, reserveMB int64) int64 {
	if ramMB <= 0 {
		return unknownRAMPebbleCacheMB
	}
	return clampInt64((ramMB-reserveMB)*2/5, minAutoPebbleCacheMB, maxAutoPebbleCacheMB)
}

// AutoPebbleCompactions returns the automatic number of concurrent compactions
// per DB for cpus CPUs.
func AutoPebbleCompactions(cpus int) int {
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
	return AutoPebbleCacheMB(memoryMB(), int64(cfg.PebbleMemoryReserveMB))
}

func (cfg *StorageConfig) pebbleCompactions() int {
	if cfg.PebbleMaxCompactions > 0 {
		return cfg.PebbleMaxCompactions
	}
	return AutoPebbleCompactions(runtime.NumCPU())
}

// One block cache per size, shared by every DB of the process. A cache per DB
// would multiply the memory by the number of DBs.
var pebbleCaches = struct {
	sync.Mutex
	bySize map[int64]*pebble.Cache
}{bySize: make(map[int64]*pebble.Cache)}

func sharedPebbleCache(mb int64) *pebble.Cache {
	pebbleCaches.Lock()
	defer pebbleCaches.Unlock()
	c, ok := pebbleCaches.bySize[mb]
	if !ok {
		// Never unreferenced: it lives as long as the process.
		c = pebble.NewCache(mb << 20)
		pebbleCaches.bySize[mb] = c
	}
	return c
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
	compactions := cfg.pebbleCompactions()
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
// unknown: the smaller of MemTotal and the cgroup limit.
func memoryMB() int64 {
	mb := memTotalMB()
	if lim := cgroupLimitMB(); lim > 0 && (mb == 0 || lim < mb) {
		mb = lim
	}
	return mb
}

func memTotalMB() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb >> 10
		}
	}
	return 0
}

// cgroupLimitMB reads the cgroup v2, then v1, memory limit. 0 means none.
func cgroupLimitMB() int64 {
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "max" {
			return 0
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 || v >= 1<<62 {
			return 0
		}
		return v >> 20
	}
	return 0
}
