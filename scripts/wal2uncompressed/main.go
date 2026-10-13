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

	in, err := os.Open(os.Args[1])
	if err != nil {
		panic(fmt.Errorf("failed to open input WAL: %w", err))
	}
	defer in.Close()

	out, err := os.OpenFile(os.Args[2], os.O_EXCL|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		panic(fmt.Errorf("failed to create output WAL: %w", err))
	}
	defer out.Close()

	dec := cs.NewWALDecoder(in)
	enc := cs.NewWALEncoderWithoutCompression(out)
	for {
		msg, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(fmt.Errorf("failed to decode WAL message: %w", err))
		}
		if err := enc.Encode(msg); err != nil {
			panic(fmt.Errorf("failed to encode WAL message: %w", err))
		}
	}

	if err := out.Sync(); err != nil {
		panic(fmt.Errorf("failed to sync output WAL: %w", err))
	}
}
