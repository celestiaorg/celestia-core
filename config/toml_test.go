package config_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/internal/test"
)

func TestRenderConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.MaxConcurrentHeavyRequests = 37
	var buf bytes.Buffer
	require.NoError(t, config.RenderConfig(&buf, cfg))
	require.Contains(t, buf.String(), "max_concurrent_heavy_requests = 37")
	require.Contains(t, buf.String(), "# Maximum number of memory-intensive RPC requests")
	path := filepath.Join(t.TempDir(), "config.toml")
	config.WriteConfigFile(path, cfg)
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, buf.Bytes(), written)
	require.ErrorIs(t, config.RenderConfig(failingConfigWriter{}, cfg), io.ErrClosedPipe)
}

type failingConfigWriter struct{}

func (failingConfigWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestConfigTemplateCoverage(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, config.RenderConfig(&buf, config.DefaultConfig()))
	values := viper.New()
	values.SetConfigType("toml")
	require.NoError(t, values.ReadConfig(&buf))
	// Root directories and P2P test options are internal; these mempool options are deliberately hidden.
	omitted := map[string]bool{
		"mempool.type": true, "mempool.max_persistent_sticky_peers": true,
		"p2p.test_dial_fail": true, "p2p.test_fuzz": true,
	}
	var check func(reflect.Type, []string)
	check = func(typ reflect.Type, path []string) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			key := strings.Split(field.Tag.Get("mapstructure"), ",")[0]
			if field.Anonymous {
				check(field.Type, path)
				continue
			}
			if key == "" || key == "home" {
				continue
			}
			full := append(append([]string(nil), path...), key)
			if field.Type.Kind() == reflect.Pointer {
				check(field.Type.Elem(), full)
			} else if !omitted[strings.Join(full, ".")] {
				assert.True(t, values.IsSet(strings.Join(full, ".")), "missing template field %s", strings.Join(full, "."))
			}
		}
	}
	check(reflect.TypeOf(config.Config{}), nil)
}

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

func TestMempoolTypeNotInTemplate(t *testing.T) {
	cfg := test.ResetTestRoot("mempool-type-not-in-template")
	defer os.RemoveAll(cfg.RootDir)

	configFile := filepath.Join(cfg.RootDir, config.DefaultConfigDir, config.DefaultConfigFileName)
	config.WriteConfigFile(configFile, cfg)

	data, err := os.ReadFile(configFile)
	require.NoError(t, err)
	configContent := string(data)

	// The mempool type field should not appear in the generated config.
	// Use a specific pattern to avoid matching unrelated fields like trace_type.
	for _, mempoolType := range []string{"cat", "nop"} {
		pattern := fmt.Sprintf("type = \"%s\"", mempoolType)
		assert.NotContains(t, configContent, pattern,
			"Config should not contain mempool type field")
	}
}
