package consensus

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// lockAndWaitForCopy locks a fresh node on X at 1/0, waits until X is in the
// locked-block file and stops the node, which must keep the file.
func lockAndWaitForCopy(t *testing.T, f *lockFixture) (*types.PartSet, types.BlockID) {
	t.Helper()
	f.lazyDelay = 10 * time.Millisecond
	cs := f.newState()
	_, voteCh := startRestarted(t, cs) // its cleanup stops a failed run before the WAL dir goes
	_, block, parts := f.lockOnX(t, cs)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	requireCopied(t, cs.lockedBlocks, LockInfo{Height: 1, BlockID: xID})

	require.NoError(t, cs.Stop())
	cs.Wait()
	require.FileExists(t, lockedBlockFile(f.config.Consensus))
	return parts, xID
}

func requireCopied(t *testing.T, w *lockedBlockWriter, li LockInfo) {
	t.Helper()
	require.Eventually(t, func() bool {
		b, _ := w.read(li)
		return b != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// testLock returns a lock on a fresh 3-part block at height 1 and its parts.
func testLock(t *testing.T) (LockInfo, *types.PartSet) {
	t.Helper()
	state, _ := randGenesisState(1, false, testMinPower, types.DefaultConsensusParams())
	data := types.MakeData([]types.Tx{cmtrand.Bytes(2 * int(types.BlockPartSizeBytes))})
	block, parts, err := state.MakeBlock(1, data, &types.Commit{}, nil, state.Validators.Proposer.Address)
	require.NoError(t, err)
	require.EqualValues(t, 3, parts.Total())
	return LockInfo{Height: 1, BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}}, parts
}

// writeCopy has a writer copy li's block to path and returns the file.
func writeCopy(t *testing.T, path string, li LockInfo, parts *types.PartSet) []byte {
	t.Helper()
	w := newLockedBlockWriter(path, log.TestingLogger())
	w.arm(0, li, parts)
	require.Eventually(t, func() bool { return countRecords(t, path) == 1+int(parts.Total()) }, 5*time.Second, time.Millisecond)
	w.stop()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

// openBufferedLockedBlockFile stands in for the O_DIRECT opener where the tests cannot use it.
func openBufferedLockedBlockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
}

// A height still open after the delay copies the locked block; a restart then restores the
// lock's bytes (and Valid*) from the file without a refetch and hands them to the propagator.
func TestLockedBlockRestoredFromFile(t *testing.T) {
	f := newLockFixture(t, true, false)
	parts, xID := lockAndWaitForCopy(t, f)

	cs := f.newState()
	rec := &callRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	startRestarted(t, cs)
	rs := cs.GetRoundState()
	require.Equal(t, xID, rs.LockedBlockID)
	require.True(t, blockMatchesBlockID(rs.LockedBlock, rs.LockedBlockParts, xID))
	require.True(t, blockMatchesBlockID(rs.ProposalBlock, rs.ProposalBlockParts, xID))
	require.EqualValues(t, 0, rs.ValidRound)
	require.True(t, blockMatchesBlockID(rs.ValidBlock, rs.ValidBlockParts, xID))
	require.NotContains(t, rec.snapshot(), propagatorCall{commitment: true, height: 1, round: 0, psh: parts.Header()})
	require.Contains(t, rec.snapshot(), propagatorCall{block: true, height: 1, round: 0, psh: parts.Header()})

	// Relocking on the restored block keeps the file instead of rewriting it.
	cs.lockedBlocks.arm(0, LockInfo{Height: 1, BlockID: xID}, parts)
	cs.lockedBlocks.mtx.Lock()
	defer cs.lockedBlocks.mtx.Unlock()
	require.Nil(t, cs.lockedBlocks.job)
}

// A torn file degrades to the refetch path.
func TestLockedBlockPartialFileRefetches(t *testing.T) {
	f := newLockFixture(t, true, false)
	parts, xID := lockAndWaitForCopy(t, f)
	path := lockedBlockFile(f.config.Consensus)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(path, fi.Size()-directIOAlign+10)) // 10 bytes into the last record

	cs := f.newState()
	rec := &callRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	startRestarted(t, cs)
	rs := cs.GetRoundState()
	require.Equal(t, xID, rs.LockedBlockID)
	require.Nil(t, rs.LockedBlock)
	require.Contains(t, rec.snapshot(), propagatorCall{commitment: true, height: 1, round: 0, psh: parts.Header()})
}

// A lock restored by ID copies its block once a peer delivers it, for the next restart.
func TestLockedBlockCopiedAfterRefetch(t *testing.T) {
	f := newLockFixture(t, true, false)
	prop, block, parts := f.lockAndStop(t)
	require.NoFileExists(t, lockedBlockFile(f.config.Consensus))

	f.lazyDelay = 10 * time.Millisecond
	cs := f.newState()
	startRestarted(t, cs)
	require.Nil(t, cs.GetRoundState().LockedBlock)
	require.NoError(t, cs.SetProposalAndBlock(prop, block, parts, "peer"))
	requireCopied(t, cs.lockedBlocks, LockInfo{Height: 1, BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}})
}

// So does one whose block the propagator completes after the node left the lock round.
func TestLockedBlockCopiedAfterLaterRoundRefetch(t *testing.T) {
	f := newLockFixture(t, true, false)
	_, block, parts := f.lockAndStop(t)

	f.lazyDelay = 10 * time.Millisecond
	cs := f.newState()
	feed := &lockRoundFeeder{Propagator: cs.propagator, partChan: make(chan types.PartInfo, parts.Total())}
	cs.propagator = feed
	newRoundCh, _ := startRestarted(t, cs)
	f.moveToRound1(t, cs, newRoundCh)
	feed.deliver(0, parts, true)
	requireCopied(t, cs.lockedBlocks, LockInfo{Height: 1, BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}})
}

// A height that commits before the delay never writes the file.
func TestLockedBlockNotWrittenWhenHeightCommits(t *testing.T) {
	f := newLockFixture(t, true, false)
	f.lazyDelay = 300 * time.Millisecond
	cs := f.newState()
	newBlockCh := subscribe(cs.eventBus, types.EventQueryNewBlock)
	_, voteCh := startRestarted(t, cs)
	_, block, parts := f.lockOnX(t, cs)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	signAddVotes(cs, cmtproto.PrecommitType, block.Hash(), parts.Header(), false, f.others...)
	ensureNewBlock(newBlockCh, 1)

	time.Sleep(2 * f.lazyDelay)
	require.NoFileExists(t, lockedBlockFile(f.config.Consensus))
}

// A proposer's own block is already in its WAL, so it is not copied.
func TestLockedBlockNotWrittenForOwnProposal(t *testing.T) {
	f := newLockFixture(t, true, true)
	f.lazyDelay = time.Millisecond
	cs := f.newState()
	_, voteCh := startRestarted(t, cs)
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	signAddVotes(cs, cmtproto.PrevoteType, v.BlockID.Hash, v.BlockID.PartSetHeader, false, f.others...)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.EqualValues(t, 0, cs.GetRoundState().LockedRound)

	time.Sleep(200 * time.Millisecond)
	require.NoFileExists(t, lockedBlockFile(f.config.Consensus))
}

// stop and reset end the writer goroutine; only reset removes the file.
func TestLockedBlockWriterLifecycle(t *testing.T) {
	path := t.TempDir() + "/locked_block"
	li, parts := testLock(t)
	w := newLockedBlockWriter(path, log.TestingLogger())

	// Stop before the delay: nothing written.
	w.arm(time.Hour, li, parts)
	w.stop()
	require.NoFileExists(t, path)

	// Stop after the write keeps all records for the next start.
	w.arm(0, li, parts)
	require.Eventually(t, func() bool { return countRecords(t, path) == 1+int(parts.Total()) }, 5*time.Second, time.Millisecond)
	w.stop()
	require.Equal(t, 1+int(parts.Total()), countRecords(t, path))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Zero(t, fi.Size()%directIOAlign)

	// A restart finds the previous run's file; the next height removes it in the background.
	w = newLockedBlockWriter(path, log.TestingLogger())
	w.reset()
	w.stop()
	require.NoFileExists(t, path)

	// A new height ends the job and removes its file.
	w.arm(0, li, parts)
	require.Eventually(t, func() bool { return countRecords(t, path) == 1+int(parts.Total()) }, 5*time.Second, time.Millisecond)
	w.reset()
	w.stop()
	require.NoFileExists(t, path)
}

func countRecords(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	dec := NewWALDecoder(f)
	n := 0
	for {
		if _, err := decodePadded(f, dec); err != nil {
			return n
		}
		n++
	}
}

// stop also joins a job that a new height already ended.
func TestLockedBlockWriterStopJoinsEndedJob(t *testing.T) {
	li, parts := testLock(t)
	w := newLockedBlockWriter(t.TempDir()+"/locked_block", log.TestingLogger())
	w.arm(time.Hour, li, parts)
	done := w.tail
	w.reset()
	w.stop()
	select {
	case <-done:
	default:
		t.Fatal("ended job outlives stop")
	}
}

// A filesystem without O_DIRECT turns the copy off instead of writing through the page cache.
func TestLockedBlockWriterOffWithoutDirectIO(t *testing.T) {
	li, parts := testLock(t)
	open := openLockedBlockFile
	t.Cleanup(func() { openLockedBlockFile = open })
	openLockedBlockFile = func(path string) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EINVAL}
	}
	w := newLockedBlockWriter(t.TempDir()+"/locked_block", log.TestingLogger())
	w.arm(0, li, parts)
	require.Eventually(t, func() bool {
		w.mtx.Lock()
		defer w.mtx.Unlock()
		return w.off
	}, 5*time.Second, time.Millisecond)
	w.reset()
	li.Height++
	w.arm(0, li, parts)
	require.Nil(t, w.job)
	w.stop()
}

// read returns nil, so the node refetches, unless the file holds a whole copy of li's block.
func TestLockedBlockReadRejects(t *testing.T) {
	dir := t.TempDir()
	li, parts := testLock(t)
	other, otherParts := testLock(t)
	read := func(raw []byte, li LockInfo) *types.Block {
		path := dir + "/locked_block"
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		b, _ := newLockedBlockWriter(path, log.TestingLogger()).read(li)
		return b
	}
	good := writeCopy(t, dir+"/good", li, parts)
	require.NotNil(t, read(good, li))

	wrongHash := LockInfo{Height: li.Height, BlockID: types.BlockID{Hash: other.BlockID.Hash, PartSetHeader: li.BlockID.PartSetHeader}}
	for name, wrong := range map[string]LockInfo{
		"height": {Height: li.Height + 1, BlockID: li.BlockID},
		"block":  other,
		"hash":   wrongHash,
	} {
		require.Nil(t, read(good, wrong), name)
	}
	// A header for another hash over this block's parts.
	require.Nil(t, read(writeCopy(t, dir+"/hash", wrongHash, parts), wrongHash))
	// This block's header over another block's parts, read for that block.
	otherRaw := writeCopy(t, dir+"/other", other, otherParts)
	require.Nil(t, read(append(good[:directIOAlign:directIOAlign], otherRaw[directIOAlign:]...), other))

	for name, n := range map[string]int{"torn tail": len(good) - directIOAlign + 10, "header only": directIOAlign} {
		require.Nil(t, read(good[:n], li), name)
	}
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)-directIOAlign] ^= 1 // the last record's checksum
	require.Nil(t, read(flipped, li))
}

// A restart over a previous run's copy of another block refetches the lock and
// does not restore that block; the lock's block is copied once a peer delivers it.
func TestLockedBlockStaleFileOfAnotherBlock(t *testing.T) {
	f := newLockFixture(t, true, false)
	prop, block, parts := f.lockAndStop(t)
	xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	other, otherParts := testLock(t)
	writeCopy(t, lockedBlockFile(f.config.Consensus), other, otherParts)

	f.lazyDelay = 10 * time.Millisecond
	cs := f.newState()
	rec := &callRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	startRestarted(t, cs)
	rs := cs.GetRoundState()
	require.Equal(t, xID, rs.LockedBlockID)
	require.Nil(t, rs.LockedBlock)
	require.Nil(t, rs.ProposalBlock)
	require.Contains(t, rec.snapshot(), propagatorCall{commitment: true, height: 1, round: 0, psh: parts.Header()})

	require.NoError(t, cs.SetProposalAndBlock(prop, block, parts, "peer"))
	requireCopied(t, cs.lockedBlocks, LockInfo{Height: 1, BlockID: xID})
}

// Ending a job mid-write stops the write before the next part. The file is a pipe
// smaller than a part, so the write is held mid-part until the test reads.
func TestLockedBlockWriterEndedMidWrite(t *testing.T) {
	li, parts := testLock(t)
	open := openLockedBlockFile
	t.Cleanup(func() { openLockedBlockFile = open })
	for _, name := range []string{"reset", "stop"} {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/locked_block"
			r, pw, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			openLockedBlockFile = func(path string) (*os.File, error) {
				if err := os.WriteFile(path, nil, 0o600); err != nil { // what reset must remove
					return nil, err
				}
				return pw, nil
			}
			w := newLockedBlockWriter(path, log.TestingLogger())
			w.arm(0, li, parts)
			_, err = io.ReadFull(r, make([]byte, directIOAlign)) // the header
			require.NoError(t, err)
			stopped := make(chan struct{})
			if name == "reset" {
				w.reset()
				close(stopped)
			} else {
				go func() { w.stop(); close(stopped) }()
				require.Eventually(t, func() bool {
					w.mtx.Lock()
					defer w.mtx.Unlock()
					return w.job == nil
				}, 5*time.Second, time.Millisecond)
			}
			n, err := io.Copy(io.Discard, r) // until write returns and closes the pipe
			require.NoError(t, err)
			require.Less(t, n, 2*int64(types.BlockPartSizeBytes), "write went on after the job ended")
			<-stopped
			w.stop()
			if name == "reset" {
				require.NoFileExists(t, path)
			} else {
				require.FileExists(t, path)
			}
		})
	}
}

// Arming the block already armed keeps its job, so the block is written once.
func TestLockedBlockWriterArmTwice(t *testing.T) {
	path := t.TempDir() + "/locked_block"
	li, parts := testLock(t)
	open := openLockedBlockFile
	t.Cleanup(func() { openLockedBlockFile = open })
	var opens atomic.Int32
	openLockedBlockFile = func(path string) (*os.File, error) {
		opens.Add(1)
		return open(path)
	}
	w := newLockedBlockWriter(path, log.TestingLogger())
	for i := 0; i < 2; i++ {
		w.arm(0, li, parts)
		w.mtx.Lock()
		tail := w.tail
		w.mtx.Unlock()
		<-tail
	}
	w.stop()
	require.EqualValues(t, 1, opens.Load())
	require.Equal(t, 1+int(parts.Total()), countRecords(t, path))
}

// Only our own proposal's parts are in the WAL: a lock filled by a refetch while we
// propose another block is still copied.
func TestLockedBlockCopiedWhileProposingAnotherBlock(t *testing.T) {
	f := newLockFixture(t, true, false)
	_, block, parts := f.lockAndStop(t)
	xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

	f.lazyDelay = 10 * time.Millisecond
	cs := f.newState()
	feed := &lockRoundFeeder{Propagator: cs.propagator, partChan: make(chan types.PartInfo, parts.Total())}
	cs.propagator = feed
	newRoundCh, _ := startRestarted(t, cs)
	p := f.nextOwnProposal(t, cs, newRoundCh)
	require.NotEqual(t, xID, p.BlockID)

	feed.deliver(0, parts, true)
	requireCopied(t, cs.lockedBlocks, LockInfo{Height: 1, BlockID: xID})
}

// A lock restored from the file does not replace a proposal block for another block.
func TestLockRestoredFromFileKeepsProposalBlock(t *testing.T) {
	f := newLockFixture(t, true, false)
	li, parts := testLock(t)
	other, _ := testLock(t)
	path := lockedBlockFile(f.config.Consensus)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	writeCopy(t, path, li, parts)

	cs := f.newState()
	cs.lockedBlocks = newLockedBlockWriter(path, cs.Logger)
	cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(other.BlockID.PartSetHeader, types.BlockPartSizeBytes)
	cs.applyLockInfo(li)
	require.True(t, blockMatchesBlockID(cs.rs.LockedBlock, cs.rs.LockedBlockParts, li.BlockID))
	require.True(t, cs.rs.ProposalBlockParts.HasHeader(other.BlockID.PartSetHeader))
	require.Nil(t, cs.rs.ProposalBlock)
}

// Without only_internal_wal the WAL holds every block, so nothing is copied, and an
// unlock finds no writer to reset.
func TestLockedBlockOffWithoutOnlyInternalWal(t *testing.T) {
	f := newLockFixture(t, false, false)
	f.lazyDelay = time.Millisecond
	cs := f.newState()
	newRoundCh, voteCh := startRestarted(t, cs)
	f.lockOnX(t, cs)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.Nil(t, cs.lockedBlocks)
	time.Sleep(200 * time.Millisecond)
	require.NoFileExists(t, lockedBlockFile(f.config.Consensus))

	f.moveToRound1(t, cs, newRoundCh)
	signAddVotes(cs, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, f.others...)
	require.Eventually(t, func() bool { return !cs.GetRoundState().IsLocked() }, 5*time.Second, 10*time.Millisecond)
}

// An unlock drops the pending copy: a released lock's block is never read.
func TestLockedBlockNotWrittenAfterUnlock(t *testing.T) {
	f := newLockFixture(t, true, false)
	f.lazyDelay = 500 * time.Millisecond
	cs := f.newState()
	newRoundCh, voteCh := startRestarted(t, cs)
	start := time.Now()
	f.lockOnX(t, cs)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	f.moveToRound1(t, cs, newRoundCh)
	signAddVotes(cs, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, f.others...)
	require.Eventually(t, func() bool { return !cs.GetRoundState().IsLocked() }, 5*time.Second, 10*time.Millisecond)

	time.Sleep(time.Until(start.Add(2 * f.lazyDelay)))
	require.NoFileExists(t, lockedBlockFile(f.config.Consensus))
}
