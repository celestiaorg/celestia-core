package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertRemovesPartialOutputOnError(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "wal")
	output := filepath.Join(dir, "wal.uncompressed")
	require.NoError(t, os.WriteFile(input, []byte("invalid WAL"), 0o600))

	require.Error(t, convert(input, output))
	_, err := os.Stat(output)
	require.ErrorIs(t, err, os.ErrNotExist)
}
