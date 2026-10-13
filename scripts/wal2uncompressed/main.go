/*
	wal2uncompressed converts one WAL segment to the legacy uncompressed format.

	Usage:
		wal2uncompressed <input-wal> <output-wal>
*/

package main

import (
	"fmt"
	"io"
	"os"

	cs "github.com/cometbft/cometbft/consensus"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: wal2uncompressed <input-wal> <output-wal>")
		os.Exit(1)
	}
	if err := convert(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func convert(inputPath, outputPath string) (err error) {
	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open input WAL: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(outputPath, os.O_EXCL|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create output WAL: %w", err)
	}
	defer func() {
		closeErr := out.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close output WAL: %w", closeErr)
		}
		if err != nil {
			_ = os.Remove(outputPath)
		}
	}()

	dec := cs.NewWALDecoder(in)
	enc := cs.NewWALEncoderWithoutCompression(out)
	for {
		msg, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to decode WAL message: %w", err)
		}
		if err := enc.Encode(msg); err != nil {
			return fmt.Errorf("failed to encode WAL message: %w", err)
		}
	}

	if err := out.Sync(); err != nil {
		return fmt.Errorf("failed to sync output WAL: %w", err)
	}
	return nil
}
