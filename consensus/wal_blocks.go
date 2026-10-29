package consensus

import (
	"os"
	"path/filepath"
	"sync"
	"time"

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
}

type lockedBlockJob struct {
	height  int64
	blockID types.BlockID
	quit    chan struct{}
	done    chan struct{}
	keep    bool // set before quit is closed: leave the file for the next start
}

func newLockedBlockWriter(path string, logger log.Logger) *lockedBlockWriter {
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
	if w.job != nil && w.job.height == li.Height && w.job.blockID.Equals(li.BlockID) {
		return
	}
	if w.job == nil && w.stale && w.restored.Equals(li.BlockID) {
		return
	}
	if w.job != nil {
		close(w.job.quit)
	}
	job := &lockedBlockJob{height: li.Height, blockID: li.BlockID, quit: make(chan struct{})}
	job.done = w.goAfterTail(func() { w.run(job, delay, li, parts) })
	w.job = job
	w.stale = false
}

// goAfterTail runs f once every earlier goroutine is done with the file. Callers hold mtx.
func (w *lockedBlockWriter) goAfterTail(f func()) chan struct{} {
	prev, done := w.tail, make(chan struct{})
	w.tail = done
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		f()
	}()
	return done
}

func (w *lockedBlockWriter) run(job *lockedBlockJob, delay time.Duration, li LockInfo, parts *types.PartSet) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-job.quit:
	case <-timer.C:
		if err := w.write(job.quit, li, parts); err != nil {
			w.logger.Error("failed to write the locked block's parts", "height", li.Height, "err", err)
		}
		<-job.quit
	}
	if !job.keep {
		w.remove()
	}
}

func (w *lockedBlockWriter) remove() {
	if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
		w.logger.Error("failed to remove the locked block's parts", "err", err)
	}
}

// write stores li as a header followed by one record per part, as the WAL encodes them.
func (w *lockedBlockWriter) write(quit <-chan struct{}, li LockInfo, parts *types.PartSet) error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := NewWALEncoder(f)
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

// reset drops the current height's job and its file.
func (w *lockedBlockWriter) reset() {
	if w == nil {
		return
	}
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.job != nil {
		close(w.job.quit)
		w.job = nil
	} else if w.stale {
		w.goAfterTail(w.remove)
	}
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
		job.keep = true
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
	msg, err := dec.Decode()
	if err != nil {
		return nil, nil
	}
	if hdr, ok := msg.Msg.(LockInfo); !ok || hdr.Height != li.Height || !hdr.BlockID.Equals(li.BlockID) {
		return nil, nil
	}
	parts := types.NewPartSetFromHeader(li.BlockID.PartSetHeader, types.BlockPartSizeBytes)
	for !parts.IsComplete() {
		msg, err := dec.Decode()
		if err != nil {
			return nil, nil
		}
		if mi, ok := msg.Msg.(msgInfo); ok {
			if bp, ok := mi.Msg.(*BlockPartMessage); ok {
				_, _ = parts.AddPart(bp.Part) // verifies the part's proof against the header
			}
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
