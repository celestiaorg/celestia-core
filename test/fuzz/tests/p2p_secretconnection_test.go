//go:build gofuzz || go1.21

package tests

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/internal/fuzzinput"
	"github.com/cometbft/cometbft/libs/async"
	sc "github.com/cometbft/cometbft/p2p/conn"
)

// secretConnFrameData is the number of plaintext bytes SecretConnection puts
// in one frame (dataMaxSize in p2p/conn).
const secretConnFrameData = 1024

// FuzzP2PSecretConnection writes data through a SecretConnection pair and
// checks that the reader receives exactly the bytes written, in order. With
// empty ops it writes raw in one call; otherwise ops describes a sequence of
// writes and the read buffer sizes.
func FuzzP2PSecretConnection(f *testing.F) {
	f.Add([]byte("Tendermint fuzzing"), []byte(nil))
	// Structured seeds: read size count and kinds, then write count and per
	// write a kind, its size fields and a content seed.
	f.Add([]byte(nil), []byte{1, 3, 3, 0, 1, 1, 2, 2, 7, 1, 2, 3, 0xFF, 0xFF, 1, 9})
	f.Add([]byte(nil), []byte{2, 0, 0, 1, 2, 4, 1, 0, 0, 1, 1, 4, 3, 2, 2, 10, 5, 3, 0x00, 0x00, 0x04, 6})

	f.Fuzz(func(t *testing.T, raw []byte, ops []byte) {
		if len(ops) == 0 {
			if len(raw) == 0 {
				return
			}
			// Read with a buffer as large as the data still missing.
			testSecretConnWrites(t, [][]byte{raw}, nil)
			return
		}
		writes, readSizes := genSecretConnOps(fuzzinput.New(ops))
		testSecretConnWrites(t, writes, readSizes)
	})
}

// genSecretConnOps builds up to eight read buffer sizes and up to 16 writes
// from r. Writes total at most 256 KiB, or 16 KiB when some read buffer is
// tiny, which bounds the number of Read calls.
func genSecretConnOps(r *fuzzinput.Reader) (writes [][]byte, readSizes []int) {
	maxTotal := 256 << 10
	for range 1 + r.Intn(8) {
		var size int
		switch r.Intn(4) {
		case 0:
			size = 1 + r.Intn(16)
			maxTotal = 16 << 10
		case 1: // around a frame boundary
			size = secretConnFrameData - 2 + r.Intn(5)
		case 2:
			size = 1 + int(r.Uint16())
		case 3: // "as much as is still missing"
			size = 0
		}
		readSizes = append(readSizes, size)
	}
	total := 0
	for range 1 + r.Intn(16) {
		var size int
		switch r.Intn(4) {
		case 0:
			size = 0
		case 1: // around a frame boundary
			size = (1+r.Intn(4))*secretConnFrameData - 2 + r.Intn(5)
		case 2:
			size = 1 + r.Intn(64)
		case 3: // large
			size = int(r.Uint16()) + int(r.Byte())<<16
		}
		size = min(size, maxTotal-total)
		total += size
		seed := r.Byte()
		w := make([]byte, size)
		for i := range w {
			w[i] = seed + byte(i) + byte(i>>8)*7
		}
		writes = append(writes, w)
	}
	return writes, readSizes
}

// testSecretConnWrites writes each slice of writes in its own Write call and
// reads with readSizes in turn (0, or no sizes, means a buffer as large as the
// data still missing), then checks the reader got the concatenated writes.
func testSecretConnWrites(t *testing.T, writes [][]byte, readSizes []int) {
	fooConn, barConn := makeSecretConnPair(t)

	want := bytes.Join(writes, nil)
	// Run Write in a separate goroutine because if data is greater than 1024
	// bytes, each Write must be followed by Read (see io.Pipe documentation).
	// Closing the connections unblocks it if the test fails first.
	writeErr := make(chan error, 1)
	go func() {
		for _, w := range writes {
			// Copy data because Write may modify the slice.
			n, err := fooConn.Write(append([]byte(nil), w...))
			if err != nil {
				writeErr <- err
				return
			}
			if n != len(w) {
				writeErr <- fmt.Errorf("wanted to write %d bytes, but %d was written", len(w), n)
				return
			}
		}
		// Every frame has been read off the pipe by now. Closing makes a
		// reader still waiting for lost bytes fail with EOF, not hang.
		writeErr <- fooConn.Close()
	}()

	got := make([]byte, 0, len(want))
	for i := 0; len(got) < len(want); i++ {
		size := len(want) - len(got)
		if len(readSizes) > 0 && readSizes[i%len(readSizes)] > 0 {
			size = readSizes[i%len(readSizes)]
		}
		buf := make([]byte, size)
		m, err := barConn.Read(buf)
		if err != nil {
			t.Fatalf("read after %d of %d bytes: %v", len(got), len(want), err)
		}
		if m > size {
			t.Fatalf("Read returned %d bytes into a %d-byte buffer", m, size)
		}
		got = append(got, buf[:m]...)
		if len(got) > len(want) {
			t.Fatalf("read %d bytes, but only %d were written", len(got), len(want))
		}
	}
	require.NoError(t, <-writeErr)
	require.True(t, bytes.Equal(want, got), "bytes written != read")
}

type kvstoreConn struct {
	*io.PipeReader
	*io.PipeWriter
}

func (drw kvstoreConn) Close() (err error) {
	err2 := drw.PipeWriter.CloseWithError(io.EOF)
	err1 := drw.PipeReader.Close()
	if err2 != nil {
		return err2
	}
	return err1
}

// Each returned ReadWriteCloser is akin to a net.Connection
func makeKVStoreConnPair() (fooConn, barConn kvstoreConn) {
	barReader, fooWriter := io.Pipe()
	fooReader, barWriter := io.Pipe()
	return kvstoreConn{fooReader, fooWriter}, kvstoreConn{barReader, barWriter}
}

// makeSecretConnPair returns both ends of a SecretConnection over in-memory
// pipes and closes them when t ends. The long-term keys are fixed; the
// handshake still uses fresh ephemeral keys, which only change ciphertext.
func makeSecretConnPair(t *testing.T) (fooSecConn, barSecConn *sc.SecretConnection) {
	var (
		fooConn, barConn = makeKVStoreConnPair()
		fooPrvKey        = ed25519.GenPrivKeyFromSecret([]byte("foo"))
		fooPubKey        = fooPrvKey.PubKey()
		barPrvKey        = ed25519.GenPrivKeyFromSecret([]byte("bar"))
		barPubKey        = barPrvKey.PubKey()
	)
	t.Cleanup(func() {
		_ = fooConn.Close()
		_ = barConn.Close()
	})

	// Make connections from both sides in parallel.
	trs, ok := async.Parallel(
		func(_ int) (val interface{}, abort bool, err error) {
			fooSecConn, err = sc.MakeSecretConnection(fooConn, fooPrvKey)
			if err != nil {
				return nil, true, fmt.Errorf("failed to establish SecretConnection for foo: %w", err)
			}
			if remote := fooSecConn.RemotePubKey(); !remote.Equals(barPubKey) {
				return nil, true, fmt.Errorf("unexpected fooSecConn.RemotePubKey. Expected %v, got %v", barPubKey, remote)
			}
			return nil, false, nil
		},
		func(_ int) (val interface{}, abort bool, err error) {
			barSecConn, err = sc.MakeSecretConnection(barConn, barPrvKey)
			if err != nil {
				return nil, true, fmt.Errorf("failed to establish SecretConnection for bar: %w", err)
			}
			if remote := barSecConn.RemotePubKey(); !remote.Equals(fooPubKey) {
				return nil, true, fmt.Errorf("unexpected barSecConn.RemotePubKey. Expected %v, got %v", fooPubKey, remote)
			}
			return nil, false, nil
		},
	)
	require.NoError(t, trs.FirstError())
	require.True(t, ok, "unexpected task abortion")
	return fooSecConn, barSecConn
}
