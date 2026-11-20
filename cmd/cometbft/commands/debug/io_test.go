package debug

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZipDirCreatesOwnerOnlyArchive(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "config.toml"), []byte("secret"), 0o600))

	dest := filepath.Join(t.TempDir(), "bundle.zip")
	require.NoError(t, zipDir(src, dest))

	info, err := os.Stat(dest)
	require.NoError(t, err)
	require.EqualValues(t, 0, info.Mode().Perm()&0o077, "archive must not be group or world accessible")
}
