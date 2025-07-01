package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/internal/test"
)

func ensureFiles(t *testing.T, rootDir string, files ...string) {
	for _, f := range files {
		p := filepath.Join(rootDir, f)
		_, err := os.Stat(p)
		assert.NoError(t, err, p)
	}
}

func TestEnsureRoot(t *testing.T) {
	require := require.New(t)

	// setup temp dir for test
	tmpDir, err := os.MkdirTemp("", "config-test")
	require.Nil(err)
	defer os.RemoveAll(tmpDir)

	// create root dir
	config.EnsureRoot(tmpDir)

	// make sure config is set properly
	data, err := os.ReadFile(filepath.Join(tmpDir, config.DefaultConfigDir, config.DefaultConfigFileName))
	require.Nil(err)

	assertValidConfig(t, string(data))

	ensureFiles(t, tmpDir, "data")
}

func TestEnsureTestRoot(t *testing.T) {
	require := require.New(t)

	// create root dir
	cfg := test.ResetTestRoot("ensureTestRoot")
	defer os.RemoveAll(cfg.RootDir)
	rootDir := cfg.RootDir

	// make sure config is set properly
	data, err := os.ReadFile(filepath.Join(rootDir, config.DefaultConfigDir, config.DefaultConfigFileName))
	require.Nil(err)

	assertValidConfig(t, string(data))

	// TODO: make sure the cfg returned and testconfig are the same!
	baseConfig := config.DefaultBaseConfig()
	ensureFiles(t, rootDir, config.DefaultDataDir, baseConfig.Genesis, baseConfig.PrivValidatorKey, baseConfig.PrivValidatorState)
}

func assertValidConfig(t *testing.T, configFile string) {
	t.Helper()
	// list of words we expect in the config
	var elems = []string{
		"moniker",
		"seeds",
		"proxy_app",
		"create_empty_blocks",
		"peer",
		"timeout",
		"broadcast",
		"send",
		"addr",
		"wal",
		"propose",
		"max",
		"genesis",
	}
	for _, e := range elems {
		assert.Contains(t, configFile, e)
	}
}

func TestMempoolTypeTemplate(t *testing.T) {
	// Test that mempool type is correctly templated and not hardcoded
	testCases := []struct {
		name        string
		mempoolType string
	}{
		{"default priority", config.MempoolTypePriority},
		{"flood", config.MempoolTypeFlood},
		{"nop", config.MempoolTypeNop},
		{"cat", config.MempoolTypeCAT},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test root with config
			cfg := test.ResetTestRoot(fmt.Sprintf("mempool-type-%s", tc.mempoolType))
			defer os.RemoveAll(cfg.RootDir)

			// Set mempool type
			cfg.Mempool.Type = tc.mempoolType

			// Write config using template
			configFile := filepath.Join(cfg.RootDir, config.DefaultConfigDir, config.DefaultConfigFileName)
			config.WriteConfigFile(configFile, cfg)

			// Read generated config file
			data, err := os.ReadFile(configFile)
			require.NoError(t, err)
			configContent := string(data)

			// Verify mempool type is correctly rendered
			expectedLine := fmt.Sprintf("type = \"%s\"", tc.mempoolType)
			assert.Contains(t, configContent, expectedLine,
				"Config should contain the correct mempool type")

			// Ensure the hardcoded "priority" is not present when using other types
			if tc.mempoolType != config.MempoolTypePriority {
				hardcodedLine := "type = \"priority\""
				assert.NotContains(t, configContent, hardcodedLine,
					"Config should not contain hardcoded 'priority' when using different mempool type")
			}
		})
	}
}
