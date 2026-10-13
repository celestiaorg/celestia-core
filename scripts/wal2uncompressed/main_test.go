package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConvertRemovesPartialOutputOnError(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "wal")
	output := filepath.Join(dir, "wal.uncompressed")
	if err := os.WriteFile(input, []byte("invalid WAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := convert(input, output); err == nil {
		t.Fatal("expected conversion to fail")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected partial output to be removed, got %v", err)
	}
}
