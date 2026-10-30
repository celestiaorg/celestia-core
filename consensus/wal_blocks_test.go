package consensus

import (
	"os"
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
func lockAndWaitForCopy(t *testing.T, f *lockFixture) (*types.Block, *types.PartSet, types.BlockID) {
	t.Helper()
	f.lazyDelay = 10 * time.Millisecond
	cs := f.newState()
	require.NoError(t, cs.Start())
	t.Cleanup(func() { _ = cs.Stop(); cs.Wait() }) // a failure must not leave the WAL running
	voteCh := subscribeToVoter(cs, cs.privValidatorPubKey.Address())
	_, block, parts := f.lockOnX(t, cs)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	li := LockInfo{Height: 1, BlockID: xID}
	require.Eventually(t, func() bool {
		b, _ := cs.lockedBlocks.read(li)
		return b != nil
	}, 5*time.Second, 10*time.Millisecond)

	cs.lockedBlocks.mtx.Lock()
	job := cs.lockedBlocks.job
	cs.lockedBlocks.mtx.Unlock()
	require.NoError(t, cs.Stop())
	cs.Wait()
	select {
	case <-job.done:
	default:
		t.Fatal("writer goroutine still running after Stop")
	}
	require.FileExists(t, lockedBlockFile(f.config.Consensus))
	return block, parts, xID
}

// A height still open after the delay copies the locked block; a restart then restores the
// lock's bytes (and Valid*) from the file without a refetch and hands them to the propagator.
func TestLockedBlockRestoredFromFile(t *testing.T) {
	f := newLockFixture(t, true, false)
	_, parts, xID := lockAndWaitForCopy(t, f)

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
	_, parts, xID := lockAndWaitForCopy(t, f)
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
	li := LockInfo{Height: 1, BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}}
	require.Eventually(t, func() bool {
		b, _ := cs.lockedBlocks.read(li)
		return b != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// A height that commits before the delay never writes the file.
func TestLockedBlockNotWrittenWhenHeightCommits(t *testing.T) {
	f := newLockFixture(t, true, false)
	f.lazyDelay = 300 * time.Millisecond
	cs := f.newState()
	newBlockCh := subscribe(cs.eventBus, types.EventQueryNewBlock)
	require.NoError(t, cs.Start())
	t.Cleanup(func() { _ = cs.Stop(); cs.Wait() })
	voteCh := subscribeToVoter(cs, cs.privValidatorPubKey.Address())
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
	voteCh := subscribeToVoter(cs, cs.privValidatorPubKey.Address())
	require.NoError(t, cs.Start())
	t.Cleanup(func() { _ = cs.Stop(); cs.Wait() })
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
	parts, err := types.NewPartSetFromData(cmtrand.Bytes(3*int(types.BlockPartSizeBytes)), types.BlockPartSizeBytes)
	require.NoError(t, err)
	li := LockInfo{Height: 5, BlockID: types.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: parts.Header()}}
	w := newLockedBlockWriter(path, log.TestingLogger())

	// Stop before the delay: nothing written, goroutine gone.
	w.arm(time.Hour, li, parts)
	job := w.job
	w.stop()
	<-job.done
	require.NoFileExists(t, path)

	// Stop after the write keeps all records for the next start.
	w.arm(0, li, parts)
	job = w.job
	require.Eventually(t, func() bool { return countRecords(t, path) == 1+int(parts.Total()) }, 5*time.Second, time.Millisecond)
	w.stop()
	<-job.done
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
	job = w.job
	require.Eventually(t, func() bool { return countRecords(t, path) == 1+int(parts.Total()) }, 5*time.Second, time.Millisecond)
	w.reset()
	<-job.done
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
	parts, err := types.NewPartSetFromData(cmtrand.Bytes(3*int(types.BlockPartSizeBytes)), types.BlockPartSizeBytes)
	require.NoError(t, err)
	li := LockInfo{Height: 5, BlockID: types.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: parts.Header()}}
	w := newLockedBlockWriter(t.TempDir()+"/locked_block", log.TestingLogger())
	w.arm(time.Hour, li, parts)
	job := w.job
	w.reset()
	w.stop()
	select {
	case <-job.done:
	default:
		t.Fatal("ended job outlives stop")
	}
}

// A filesystem without O_DIRECT turns the copy off instead of writing through the page cache.
func TestLockedBlockWriterOffWithoutDirectIO(t *testing.T) {
	parts, err := types.NewPartSetFromData(cmtrand.Bytes(3*int(types.BlockPartSizeBytes)), types.BlockPartSizeBytes)
	require.NoError(t, err)
	li := LockInfo{Height: 5, BlockID: types.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: parts.Header()}}
	w := newLockedBlockWriter(t.TempDir()+"/locked_block", log.TestingLogger())
	w.open = func(path string) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EINVAL}
	}
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
