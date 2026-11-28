package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/require"
)

func TestAutoPebbleSizing(t *testing.T) {
	const gib = 1 << 10
	for _, tc := range []struct{ ram, reserve, want int64 }{
		{0, 20 * gib, 1 * gib},     // unknown memory
		{8 * gib, 20 * gib, 256},   // floor
		{16 * gib, 20 * gib, 256},  // floor
		{32 * gib, 20 * gib, 4915}, // the app keeps its 20 GiB
		{64 * gib, 20 * gib, 18022},
		{128 * gib, 20 * gib, 44236},
		{512 * gib, 20 * gib, 64 * gib}, // cap
		{32 * gib, 0, 13107},
	} {
		require.Equal(t, tc.want, autoPebbleCacheMB(tc.ram, tc.reserve), "ram %d reserve %d", tc.ram, tc.reserve)
	}
	for cpus, want := range map[int]int{1: 2, 4: 2, 8: 2, 16: 4, 32: 8, 128: 8} {
		require.Equal(t, want, autoPebbleCompactions(cpus), "cpus %d", cpus)
	}
	// 32 GiB stays well clear of OOM: twice the cache (Go heap) plus the app's
	// reserve fits.
	require.Less(t, 2*autoPebbleCacheMB(32*gib, 20*gib)+20*gib, int64(32*gib))
}

func TestDefaultDBProviderPebbleTuning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.DBBackend = string(dbm.PebbleDBBackend)
	cfg.Storage.PebbleCacheSizeMB = 16
	require.True(t, cfg.Storage.PebbleTuning)

	write := func(id string) *dbm.PebbleDB {
		db, err := DefaultDBProvider(&DBContext{ID: id, Config: cfg})
		require.NoError(t, err)
		for i := 0; i < 1000; i++ {
			require.NoError(t, db.Set([]byte(strconv.Itoa(i)), bytes.Repeat([]byte{byte(i)}, 100)))
		}
		p := db.(*dbm.PebbleDB)
		require.NoError(t, p.DB().Flush())
		for i := 0; i < 1000; i++ {
			got, err := db.Get([]byte(strconv.Itoa(i)))
			require.NoError(t, err)
			require.Len(t, got, 100)
		}
		return p
	}
	a, b := write("state"), write("evidence")
	// Both DBs report the same cache, so it is shared.
	require.Positive(t, a.DB().Metrics().BlockCache.Count)
	require.Equal(t, a.DB().Metrics().BlockCache, b.DB().Metrics().BlockCache)
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	// Tables written with the tuned options open with engine defaults.
	cfg.Storage.PebbleTuning = false
	db, err := DefaultDBProvider(&DBContext{ID: "state", Config: cfg})
	require.NoError(t, err)
	got, err := db.Get([]byte("7"))
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{7}, 100), got)
	require.NoError(t, db.Close())
}

// fakeRoot writes files under a temporary root: path -> content.
func fakeRoot(t *testing.T, files map[string]string) string {
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o600))
	}
	return root
}

func TestMemoryMBCgroups(t *testing.T) {
	const gib = int64(1) << 30
	n := func(v int64) string { return strconv.FormatInt(v, 10) + "\n" }
	meminfo := "MemTotal:       134217728 kB\n" // 128 GiB
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  int64 // MiB
	}{
		{"no cgroup", map[string]string{"/proc/meminfo": meminfo}, 128 << 10},
		{"v2 leaf", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "0::/system.slice/node.service\n",
			"/sys/fs/cgroup/memory.max":                           "max\n",
			"/sys/fs/cgroup/system.slice/memory.max":              "max\n",
			"/sys/fs/cgroup/system.slice/node.service/memory.max": n(32 * gib),
		}, 32 << 10},
		{"v2 ancestor is smaller", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "0::/a/b\n",
			"/sys/fs/cgroup/a/memory.max":   n(16 * gib),
			"/sys/fs/cgroup/a/b/memory.max": "max\n",
		}, 16 << 10},
		{"v2 path missing falls back to the root", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "0::/elsewhere\n",
			"/sys/fs/cgroup/memory.max": n(8 * gib),
		}, 8 << 10},
		{"v2 garbage is no limit", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "0::/a\n",
			"/sys/fs/cgroup/a/memory.max": "lots\n",
		}, 128 << 10},
		{"v1 limit", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "12:cpu,cpuacct:/x\n5:memory:/docker/abc\n",
			"/sys/fs/cgroup/memory/memory.limit_in_bytes":            n(1 << 62),
			"/sys/fs/cgroup/memory/docker/memory.limit_in_bytes":     n(1<<63 - 1),
			"/sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": n(24 * gib),
		}, 24 << 10},
		{"v1 hierarchical limit", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "5:memory:/abc\n",
			"/sys/fs/cgroup/memory/abc/memory.limit_in_bytes": n(1 << 62),
			"/sys/fs/cgroup/memory/abc/memory.stat":           "cache 1\nhierarchical_memory_limit " + n(12*gib),
		}, 12 << 10},
		{"hybrid takes the smaller", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "5:memory:/abc\n0::/abc\n",
			"/sys/fs/cgroup/abc/memory.max":                   n(20 * gib),
			"/sys/fs/cgroup/memory/abc/memory.limit_in_bytes": n(10 * gib),
		}, 10 << 10},
		{"limit above RAM", map[string]string{
			"/proc/meminfo": meminfo, "/proc/self/cgroup": "0::/\n",
			"/sys/fs/cgroup/memory.max": n(512 * gib),
		}, 128 << 10},
		{"no meminfo", map[string]string{
			"/proc/self/cgroup": "0::/\n", "/sys/fs/cgroup/memory.max": n(4 * gib),
		}, 4 << 10},
		{"nothing", map[string]string{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, memoryMB(fakeRoot(t, tc.files)))
		})
	}
}
