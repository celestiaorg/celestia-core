package consensus

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/cosmos/gogoproto/proto"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

// lockedBlockFile is where a stalled height's locked block is copied; a dedicated-disk option only changes this path.
func lockedBlockFile(config *cfg.ConsensusConfig) string {
	return filepath.Join(filepath.Dir(config.WalFile()), "locked_block") // not "wal*": the WAL group counts those
}

// directIOAlign is the buffer, length and offset alignment O_DIRECT writes need.
const directIOAlign = 4096

// lockedBlockWriter copies the locked block's parts to a file once a height has stayed open
// for a while, so a restarted node can restore the lock's bytes without peers. Never fsynced.
type lockedBlockWriter struct {
	path   string
	logger log.Logger

	mtx      sync.Mutex
	job      *lockedBlockJob
	tail     chan struct{} // closed once the last goroutine started is done with the file
	stale    bool          // the file holds a previous run's records
	restored types.BlockID // the stale file holds this block of the current height
	off      bool          // the filesystem rejected O_DIRECT
}

type lockedBlockJob struct {
	height  int64
	blockID types.BlockID
	quit    chan struct{}
}

// newLockedBlockWriter returns nil, turning the copy off, where O_DIRECT is unavailable.
func newLockedBlockWriter(path string, logger log.Logger) *lockedBlockWriter {
	if openLockedBlockFile == nil {
		return nil
	}
	_, err := os.Stat(path)
	return &lockedBlockWriter{path: path, logger: logger, stale: err == nil}
}

// arm writes parts after delay unless reset or stop comes first. The file is
// truncated when the write starts and removed when the job ends without stop.
func (w *lockedBlockWriter) arm(delay time.Duration, li LockInfo, parts *types.PartSet) {
	if w == nil {
		return
	}
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.off {
		return
	}
	if w.job != nil && w.job.height == li.Height && w.job.blockID.Equals(li.BlockID) {
		return
	}
	if w.stale && w.restored.Equals(li.BlockID) {
		return
	}
	w.endJob()
	job := &lockedBlockJob{height: li.Height, blockID: li.BlockID, quit: make(chan struct{})}
	w.goAfterTail(func() { w.run(job.quit, delay, li, parts) })
	w.job = job
	w.stale = false
}

// endJob stops the current job and removes its file. Callers hold mtx.
func (w *lockedBlockWriter) endJob() {
	if w.job != nil {
		close(w.job.quit)
		w.job = nil
		w.goAfterTail(w.remove)
	}
}

// goAfterTail runs f once every earlier goroutine is done with the file. Callers hold mtx.
func (w *lockedBlockWriter) goAfterTail(f func()) {
	prev, done := w.tail, make(chan struct{})
	w.tail = done
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		f()
	}()
}

func (w *lockedBlockWriter) run(quit <-chan struct{}, delay time.Duration, li LockInfo, parts *types.PartSet) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-quit:
	case <-timer.C:
		if err := w.write(quit, li, parts); errors.Is(err, syscall.EINVAL) {
			w.disable(err)
		} else if err != nil {
			w.logger.Error("failed to write the locked block's parts", "height", li.Height, "err", err)
		}
	}
}

// disable stops all later copies; buffered writes are not an option (their writeback stalls WAL fsyncs).
func (w *lockedBlockWriter) disable(err error) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if !w.off {
		w.off = true
		w.logger.Info("not copying locked blocks: the WAL filesystem does not support O_DIRECT", "err", err)
	}
}

func (w *lockedBlockWriter) remove() {
	if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
		w.logger.Error("failed to remove the locked block's parts", "err", err)
	}
}

// write stores li as a header followed by one record per part, as the WAL encodes them,
// each zero-padded to directIOAlign.
func (w *lockedBlockWriter) write(quit <-chan struct{}, li LockInfo, parts *types.PartSet) error {
	f, err := openLockedBlockFile(w.path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := NewWALEncoder(&alignedWriter{f: f})
	if err := enc.Encode(&TimedWALMessage{cmttime.Now(), li}); err != nil {
		return err
	}
	for i := 0; i < int(parts.Total()); i++ {
		select {
		case <-quit:
			return nil
		default:
		}
		msg := msgInfo{Msg: &BlockPartMessage{Height: li.Height, Round: li.Round, Part: parts.GetPart(i)}}
		if err := enc.Encode(&TimedWALMessage{cmttime.Now(), msg}); err != nil {
			return err
		}
	}
	return nil
}

// alignedWriter writes each record zero-padded to directIOAlign from one aligned buffer.
type alignedWriter struct {
	f   *os.File
	buf []byte
}

func (a *alignedWriter) Write(p []byte) (int, error) {
	n := alignUp(len(p))
	if cap(a.buf) < n { // sized for a part record, so a job allocates once
		a.buf = alignedBuf(max(n, alignUp(int(types.BlockPartSizeBytes)+1)))
	}
	b := a.buf[:n]
	clear(b[copy(b, p):])
	if _, err := a.f.Write(b); err != nil {
		return 0, err
	}
	return len(p), nil
}

func alignUp(n int) int { return (n + directIOAlign - 1) &^ (directIOAlign - 1) }

func alignedBuf(n int) []byte {
	b := make([]byte, n+directIOAlign)
	skip := (directIOAlign - int(uintptr(unsafe.Pointer(&b[0]))%directIOAlign)) % directIOAlign
	return b[skip : skip+n]
}

// decodePadded decodes the next record and skips its padding.
func decodePadded(f *os.File, dec *WALDecoder) (*TimedWALMessage, error) {
	msg, err := dec.Decode()
	if err != nil {
		return nil, err
	}
	off, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = f.Seek(int64(alignUp(int(off))), io.SeekStart)
	}
	return msg, err
}

// reset drops the current height's job and its file.
func (w *lockedBlockWriter) reset() {
	if w == nil {
		return
	}
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.stale {
		w.goAfterTail(w.remove)
	}
	w.endJob()
	w.stale = false
}

// stop ends the job and waits for every goroutine, keeping the job's file for the next start.
func (w *lockedBlockWriter) stop() {
	if w == nil {
		return
	}
	w.mtx.Lock()
	job, tail := w.job, w.tail
	w.job = nil
	w.mtx.Unlock()
	if job != nil {
		close(job.quit)
	}
	if tail != nil {
		<-tail
	}
}

// read returns li's block if the file holds all of its parts; anything less returns nil.
func (w *lockedBlockWriter) read(li LockInfo) (*types.Block, *types.PartSet) {
	if w == nil {
		return nil, nil
	}
	f, err := os.Open(w.path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	dec := NewWALDecoder(f)
	msg, err := decodePadded(f, dec)
	if err != nil {
		return nil, nil
	}
	if hdr, ok := msg.Msg.(LockInfo); !ok || hdr.Height != li.Height || !hdr.BlockID.Equals(li.BlockID) {
		return nil, nil
	}
	parts := types.NewPartSetFromHeader(li.BlockID.PartSetHeader, types.BlockPartSizeBytes)
	for !parts.IsComplete() {
		msg, err := decodePadded(f, dec)
		if err != nil {
			return nil, nil
		}
		mi, _ := msg.Msg.(msgInfo)
		if bp, ok := mi.Msg.(*BlockPartMessage); ok {
			_, _ = parts.AddPart(bp.Part) // verifies the part's proof against the header
		}
	}
	pbb := new(cmtproto.Block)
	if err := proto.Unmarshal(parts.GetBytes(), pbb); err != nil {
		return nil, nil
	}
	block, err := types.BlockFromProto(pbb)
	if err != nil || !block.HashesTo(li.BlockID.Hash) {
		return nil, nil
	}
	block.FillCachedHashes()
	w.mtx.Lock()
	w.restored = li.BlockID
	w.mtx.Unlock()
	return block, parts
}
