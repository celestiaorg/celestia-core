package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/walfsync"
)

// TestNodeWALFsyncMarker checks the start-up decision on a crash marker left
// while WAL fsyncs were skipped, and that a node that block synced to the tip
// and stopped leaves no marker.
func TestNodeWALFsyncMarker(t *testing.T) {
	bootID, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	for _, tc := range []struct {
		name    string
		marker  string // "" = none
		accept  bool
		refused bool
		bsDir   bool // separate blockstore_dir
	}{
		{name: "no marker"},
		{name: "same boot", marker: strings.TrimSpace(string(bootID))},
		{name: "same boot with blockstore_dir", marker: strings.TrimSpace(string(bootID)), bsDir: true},
		{name: "other boot", marker: "another-boot", refused: true},
		{name: "other boot accepted", marker: "another-boot", accept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.HasPrefix(tc.name, "same boot") && len(bootID) == 0 {
				t.Skip("no boot ID on this platform")
			}
			config := test.ResetTestRoot("node_walfsync_test")
			defer os.RemoveAll(config.RootDir)
			config.DBBackend = "pebbledb"
			if tc.bsDir {
				config.BlockstorePath = "blocks"
			}
			marker := filepath.Join(config.DBDir(), walfsync.MarkerFile)
			if tc.marker != "" {
				require.NoError(t, os.MkdirAll(config.DBDir(), 0o700))
				require.NoError(t, os.WriteFile(marker, []byte(tc.marker+"\n"), 0o600))
			}

			if tc.refused {
				_, err := DefaultNewNode(config, log.TestingLogger())
				require.ErrorIs(t, err, walfsync.ErrUnsyncedCrash)
				require.ErrorContains(t, err, walfsync.AcceptFlag)
				require.FileExists(t, marker)
				return
			}
			if tc.accept {
				require.NoError(t, AcceptUnsyncedDBCrash(config, log.TestingLogger()))
				require.NoFileExists(t, marker)
			}
			n := startNodeRetryingPortConflicts(t, func() (*Node, error) {
				testFreeConfig(t, config)
				n, err := DefaultNewNode(config, log.TestingLogger())
				if err != nil {
					return nil, err
				}
				return n, n.Start()
			})
			require.NoError(t, n.Stop())
			n.Wait()
			require.NoFileExists(t, marker)
			if tc.bsDir {
				require.DirExists(t, filepath.Join(config.BlockstoreDir(), "blockstore.db"))
			}
		})
	}
}
