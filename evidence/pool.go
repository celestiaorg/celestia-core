package evidence

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cosmos/gogoproto/proto"
	gogotypes "github.com/cosmos/gogoproto/types"

	dbm "github.com/cometbft/cometbft-db"

	clist "github.com/cometbft/cometbft/libs/clist"
	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

const (
	baseKeyCommitted = byte(0x00)
	baseKeyPending   = byte(0x01)
	// baseKeyCommittedDuplicateVote marks that duplicate vote evidence for a
	// given (height, validator address) has been committed, regardless of the
	// round, vote type or encoding of the evidence that was committed. The
	// value is the evidence time, so that markers can be pruned once evidence
	// at that height has expired.
	baseKeyCommittedDuplicateVote = byte(0x02)
)

// Pool maintains a pool of valid evidence to be broadcasted and committed
type Pool struct {
	logger log.Logger

	evidenceStore dbm.DB
	evidenceList  *clist.CList // concurrent linked-list of evidence
	evidenceSize  uint32       // amount of pending evidence

	// needed to load validators to verify evidence
	stateDB sm.Store
	// needed to load headers and commits to verify evidence
	blockStore BlockStore

	mtx sync.Mutex
	// latest state
	state sm.State
	// evidence from consensus is buffered to this slice, awaiting until the next height
	// before being flushed to the pool. This prevents broadcasting and proposing of
	// evidence before the height with which the evidence happened is finished.
	consensusBuffer []duplicateVoteSet

	pruningHeight int64
	pruningTime   time.Time

	// dveMtx guards pendingDuplicateVotes. It must never be held while
	// acquiring mtx.
	dveMtx sync.Mutex
	// pendingDuplicateVotes indexes pending DuplicateVoteEvidence by
	// duplicateVoteKey so that at most one piece of evidence is admitted per
	// validator and height. One piece is enough to punish the validator, and
	// this bounds pending duplicate vote evidence by the evidence age window
	// and the validator set instead of by what an attacker can sign.
	pendingDuplicateVotes map[string]types.Evidence
}

// NewPool creates an evidence pool. If using an existing evidence store,
// it will add all pending evidence to the concurrent list.
func NewPool(evidenceDB dbm.DB, stateDB sm.Store, blockStore BlockStore) (*Pool, error) {
	state, err := stateDB.Load()
	if err != nil {
		return nil, fmt.Errorf("cannot load state: %w", err)
	}

	pool := &Pool{
		stateDB:         stateDB,
		blockStore:      blockStore,
		state:           state,
		logger:          log.NewNopLogger(),
		evidenceStore:   evidenceDB,
		evidenceList:    clist.New(),
		consensusBuffer: make([]duplicateVoteSet, 0),

		pendingDuplicateVotes: make(map[string]types.Evidence),
	}

	// if pending evidence already in db, in event of prior failure, then check for expiration,
	// update the size and load it back to the evidenceList
	pool.pruningHeight, pool.pruningTime = pool.removeExpiredPendingEvidence()
	evList, _, err := pool.listEvidence(baseKeyPending, -1)
	if err != nil {
		return nil, err
	}
	kept := evList[:0]
	for _, ev := range evList {
		// Drop pending evidence that is already committed. This can be left
		// behind if the node stopped while committing it, or, for duplicate
		// vote evidence, by pools written before it was deduplicated: those may
		// also hold several pieces of evidence for one validator and height,
		// of which only the first is kept.
		drop := pool.isCommitted(ev)
		if dve, ok := ev.(*types.DuplicateVoteEvidence); ok && !drop {
			if pool.isDuplicateVoteKnownLocked(dve) {
				drop = true
			} else {
				pool.pendingDuplicateVotes[duplicateVoteKey(dve)] = ev
			}
		}
		if drop {
			pool.logger.Info("Removing pending evidence that is already committed or pending", "ev", ev)
			if err := pool.evidenceStore.Delete(keyPending(ev)); err != nil {
				return nil, fmt.Errorf("cannot delete pending evidence: %w", err)
			}
			continue
		}
		kept = append(kept, ev)
	}
	atomic.StoreUint32(&pool.evidenceSize, uint32(len(kept)))
	for _, ev := range kept {
		pool.evidenceList.PushBack(ev)
	}

	return pool, nil
}

// PendingEvidence is used primarily as part of block proposal and returns up to maxNum of uncommitted evidence.
func (evpool *Pool) PendingEvidence(maxBytes int64) ([]types.Evidence, int64) {
	if evpool.Size() == 0 {
		return []types.Evidence{}, 0
	}
	evidence, size, err := evpool.listEvidence(baseKeyPending, maxBytes)
	if err != nil {
		evpool.logger.Error("Unable to retrieve pending evidence", "err", err)
	}
	return evidence, size
}

// Update takes both the new state and the evidence committed at that height and performs
// the following operations:
//  1. Take any conflicting votes from consensus and use the state's LastBlockTime to form
//     DuplicateVoteEvidence and add it to the pool.
//  2. Update the pool's state which contains evidence params relating to expiry.
//  3. Moves pending evidence that has now been committed into the committed pool.
//  4. Removes any expired evidence based on both height and time.
func (evpool *Pool) Update(state sm.State, ev types.EvidenceList) {
	// sanity check
	if state.LastBlockHeight <= evpool.state.LastBlockHeight {
		panic(fmt.Sprintf(
			"failed EvidencePool.Update new state height is less than or equal to previous state height: %d <= %d",
			state.LastBlockHeight,
			evpool.state.LastBlockHeight,
		))
	}
	evpool.logger.Debug("Updating evidence pool", "last_block_height", state.LastBlockHeight,
		"last_block_time", state.LastBlockTime)

	// flush conflicting vote pairs from the buffer, producing DuplicateVoteEvidence and
	// adding it to the pool
	evpool.processConsensusBuffer(state)
	// update state
	evpool.updateState(state)

	// move committed evidence out from the pending pool and into the committed pool
	evpool.markEvidenceAsCommitted(ev)

	// prune pending evidence when it has expired. This also updates when the next evidence will expire
	if evpool.Size() > 0 && state.LastBlockHeight > evpool.pruningHeight &&
		state.LastBlockTime.After(evpool.pruningTime) {
		evpool.pruningHeight, evpool.pruningTime = evpool.removeExpiredPendingEvidence()
	}
	evpool.removeExpiredCommittedDuplicateVotes()
}

// AddEvidence checks the evidence is valid and adds it to the pool.
func (evpool *Pool) AddEvidence(ev types.Evidence) error {
	evpool.logger.Info("Attempting to add evidence", "ev", ev)

	// We have already verified this piece of evidence - no need to do it again
	if evpool.isPending(ev) {
		evpool.logger.Info("Evidence already pending, ignoring this one", "ev", ev)
		return nil
	}

	// check that the evidence isn't already committed
	if evpool.isCommitted(ev) {
		// this can happen if the peer that sent us the evidence is behind so we shouldn't
		// punish the peer.
		evpool.logger.Info("Evidence was already committed, ignoring this one", "ev", ev)
		return nil
	}

	// Duplicate vote evidence for a validator and height that already has
	// evidence pending or committed is ignored before it is verified. This
	// keeps the cost of resubmitting variants low. As a result, a peer that
	// sends such evidence with invalid signatures is not punished.
	dve, isDuplicateVote := ev.(*types.DuplicateVoteEvidence)
	if isDuplicateVote && evpool.isDuplicateVoteKnown(dve) {
		evpool.logger.Info("Evidence for this validator and height already pending or committed, ignoring this one", "ev", ev)
		return nil
	}

	// 1) Verify against state.
	err := evpool.verify(ev)
	if err != nil {
		return types.NewErrInvalidEvidence(ev, err)
	}

	if isDuplicateVote {
		// Keep a canonical copy of the evidence. Fields not covered by the vote
		// signatures are reset rather than rejected, so that honest peers
		// relaying non-canonical but valid evidence are not disconnected.
		canonical, err := evpool.canonicalDuplicateVote(dve)
		if err != nil {
			return fmt.Errorf("can't canonicalize evidence: %w", err)
		}

		// 2) Save to store and 3) add evidence to clist.
		added, err := evpool.addPendingDuplicateVote(canonical)
		if err != nil {
			return fmt.Errorf("can't add evidence to pending list: %w", err)
		}
		if !added {
			evpool.logger.Info("Evidence for this validator and height already pending or committed, ignoring this one", "ev", ev)
			return nil
		}
	} else {
		// 2) Save to store.
		if err := evpool.addPendingEvidence(ev); err != nil {
			return fmt.Errorf("can't add evidence to pending list: %w", err)
		}

		// 3) Add evidence to clist.
		evpool.evidenceList.PushBack(ev)
	}

	evpool.logger.Info("Verified new evidence of byzantine behavior", "evidence", ev)

	return nil
}

// ReportConflictingVotes takes two conflicting votes and forms duplicate vote evidence,
// adding it eventually to the evidence pool.
//
// Duplicate vote attacks happen before the block is committed and the timestamp is
// finalized, thus the evidence pool holds these votes in a buffer, forming the
// evidence from them once consensus at that height has been reached and `Update()` with
// the new state called.
//
// Votes are not verified.
func (evpool *Pool) ReportConflictingVotes(voteA, voteB *types.Vote) {
	evpool.mtx.Lock()
	defer evpool.mtx.Unlock()
	evpool.consensusBuffer = append(evpool.consensusBuffer, duplicateVoteSet{
		VoteA: voteA,
		VoteB: voteB,
	})
}

// CheckEvidence takes an array of evidence from a block and verifies all the evidence there.
// If it has already verified the evidence then it jumps to the next one. It ensures that no
// evidence has already been committed or is being proposed twice. It also adds any
// evidence that it doesn't currently have so that it can quickly form ABCI Evidence later.
func (evpool *Pool) CheckEvidence(evList types.EvidenceList) error {
	hashes := make([][]byte, len(evList))
	for idx, ev := range evList {

		_, isLightEv := ev.(*types.LightClientAttackEvidence)

		// We must verify light client attack evidence regardless because there could be a
		// different conflicting block with the same hash.
		if isLightEv || !evpool.isPending(ev) {
			// check that the evidence isn't already committed
			if evpool.isCommitted(ev) {
				return &types.ErrInvalidEvidence{Evidence: ev, Reason: errors.New("evidence was already committed")}
			}

			err := evpool.verify(ev)
			if err != nil {
				return err
			}

			if dve, ok := ev.(*types.DuplicateVoteEvidence); ok {
				// Only keep one canonical piece of evidence per validator and height
				// pending. This does not affect whether the block's evidence is
				// considered valid.
				canonical, err := evpool.canonicalDuplicateVote(dve)
				if err == nil {
					_, err = evpool.addPendingDuplicateVoteToStore(canonical)
				}
				if err != nil {
					evpool.logger.Error("Can't add evidence to pending list", "err", err, "ev", ev)
				}
			} else if err := evpool.addPendingEvidence(ev); err != nil {
				// Something went wrong with adding the evidence but we already know it is valid
				// hence we log an error and continue
				evpool.logger.Error("Can't add evidence to pending list", "err", err, "ev", ev)
			}

			evpool.logger.Info("Check evidence: verified evidence of byzantine behavior", "evidence", ev)
		}

		// check for duplicate evidence. We cache hashes so we don't have to work them out again.
		hashes[idx] = ev.Hash()
		for i := idx - 1; i >= 0; i-- {
			if bytes.Equal(hashes[i], hashes[idx]) {
				return &types.ErrInvalidEvidence{Evidence: ev, Reason: errors.New("duplicate evidence")}
			}
		}
	}

	return nil
}

// EvidenceFront goes to the first evidence in the clist
func (evpool *Pool) EvidenceFront() *clist.CElement {
	return evpool.evidenceList.Front()
}

// EvidenceWaitChan is a channel that closes once the first evidence in the list is there. i.e Front is not nil
func (evpool *Pool) EvidenceWaitChan() <-chan struct{} {
	return evpool.evidenceList.WaitChan()
}

// SetLogger sets the Logger.
func (evpool *Pool) SetLogger(l log.Logger) {
	evpool.logger = l
}

// Size returns the number of evidence in the pool.
func (evpool *Pool) Size() uint32 {
	return atomic.LoadUint32(&evpool.evidenceSize)
}

// State returns the current state of the evpool.
func (evpool *Pool) State() sm.State {
	evpool.mtx.Lock()
	defer evpool.mtx.Unlock()
	return evpool.state
}

func (evpool *Pool) Close() error {
	return evpool.evidenceStore.Close()
}

// IsExpired checks whether evidence or a polc is expired by checking whether a height and time is older
// than set by the evidence consensus parameters
func (evpool *Pool) isExpired(height int64, time time.Time) bool {
	var (
		params       = evpool.State().ConsensusParams.Evidence
		ageDuration  = evpool.State().LastBlockTime.Sub(time)
		ageNumBlocks = evpool.State().LastBlockHeight - height
	)
	return ageNumBlocks > params.MaxAgeNumBlocks &&
		ageDuration > params.MaxAgeDuration
}

// IsCommitted returns true if we have already seen this exact evidence and it is already marked as committed.
func (evpool *Pool) isCommitted(evidence types.Evidence) bool {
	key := keyCommitted(evidence)
	ok, err := evpool.evidenceStore.Has(key)
	if err != nil {
		evpool.logger.Error("Unable to find committed evidence", "err", err)
	}
	return ok
}

// IsPending checks whether the evidence is already pending. DB errors are passed to the logger.
func (evpool *Pool) isPending(evidence types.Evidence) bool {
	key := keyPending(evidence)
	ok, err := evpool.evidenceStore.Has(key)
	if err != nil {
		evpool.logger.Error("Unable to find pending evidence", "err", err)
	}
	return ok
}

func (evpool *Pool) addPendingEvidence(ev types.Evidence) error {
	evpb, err := types.EvidenceToProto(ev)
	if err != nil {
		return fmt.Errorf("unable to convert to proto, err: %w", err)
	}

	evBytes, err := evpb.Marshal()
	if err != nil {
		return fmt.Errorf("unable to marshal evidence: %w", err)
	}

	key := keyPending(ev)

	err = evpool.evidenceStore.Set(key, evBytes)
	if err != nil {
		return fmt.Errorf("can't persist evidence: %w", err)
	}
	atomic.AddUint32(&evpool.evidenceSize, 1)
	return nil
}

func (evpool *Pool) removePendingEvidence(evidence types.Evidence) {
	key := keyPending(evidence)
	if err := evpool.evidenceStore.Delete(key); err != nil {
		evpool.logger.Error("Unable to delete pending evidence", "err", err)
	} else {
		atomic.AddUint32(&evpool.evidenceSize, ^uint32(0))
		evpool.logger.Debug("Deleted pending evidence", "evidence", evidence)
	}

	if dve, ok := evidence.(*types.DuplicateVoteEvidence); ok {
		dveKey := duplicateVoteKey(dve)
		evpool.dveMtx.Lock()
		if pending, exists := evpool.pendingDuplicateVotes[dveKey]; exists && evMapKey(pending) == evMapKey(evidence) {
			delete(evpool.pendingDuplicateVotes, dveKey)
		}
		evpool.dveMtx.Unlock()
	}
}

// isDuplicateVoteKnown returns true if duplicate vote evidence for the same
// validator and height as dve is already pending or committed, possibly at a
// different round or under a different hash.
func (evpool *Pool) isDuplicateVoteKnown(dve *types.DuplicateVoteEvidence) bool {
	evpool.dveMtx.Lock()
	defer evpool.dveMtx.Unlock()
	return evpool.isDuplicateVoteKnownLocked(dve)
}

func (evpool *Pool) isDuplicateVoteKnownLocked(dve *types.DuplicateVoteEvidence) bool {
	if _, ok := evpool.pendingDuplicateVotes[duplicateVoteKey(dve)]; ok {
		return true
	}
	ok, err := evpool.evidenceStore.Has(keyCommittedDuplicateVote(dve))
	if err != nil {
		evpool.logger.Error("Unable to find committed duplicate vote evidence", "err", err)
	}
	return ok
}

// addPendingDuplicateVote persists dve and adds it to the evidence list unless
// evidence for the same equivocation is already pending or committed. It
// returns whether dve was added.
func (evpool *Pool) addPendingDuplicateVote(dve *types.DuplicateVoteEvidence) (bool, error) {
	evpool.dveMtx.Lock()
	defer evpool.dveMtx.Unlock()
	added, err := evpool.addPendingDuplicateVoteLocked(dve)
	if added {
		evpool.evidenceList.PushBack(dve)
	}
	return added, err
}

// addPendingDuplicateVoteToStore is like addPendingDuplicateVote but does not
// add the evidence to the evidence list.
func (evpool *Pool) addPendingDuplicateVoteToStore(dve *types.DuplicateVoteEvidence) (bool, error) {
	evpool.dveMtx.Lock()
	defer evpool.dveMtx.Unlock()
	return evpool.addPendingDuplicateVoteLocked(dve)
}

func (evpool *Pool) addPendingDuplicateVoteLocked(dve *types.DuplicateVoteEvidence) (bool, error) {
	if evpool.isDuplicateVoteKnownLocked(dve) {
		return false, nil
	}
	if err := evpool.addPendingEvidence(dve); err != nil {
		return false, err
	}
	evpool.pendingDuplicateVotes[duplicateVoteKey(dve)] = dve
	return true, nil
}

// canonicalDuplicateVote returns a copy of dve with the fields that are not
// covered by the vote signatures set to their canonical values: each vote's
// ValidatorIndex is the validator's index in the validator set at the
// evidence height, and vote extensions are removed. Evidence verification does
// not use these fields, so the copy is valid whenever dve is. dve is not
// modified, since it may belong to a block.
func (evpool *Pool) canonicalDuplicateVote(dve *types.DuplicateVoteEvidence) (*types.DuplicateVoteEvidence, error) {
	valSet, err := evpool.stateDB.LoadValidators(dve.Height())
	if err != nil {
		return nil, err
	}
	idx, val := valSet.GetByAddress(dve.VoteA.ValidatorAddress)
	if val == nil {
		return nil, fmt.Errorf("address %X was not a validator at height %d", dve.VoteA.ValidatorAddress, dve.Height())
	}
	canonicalVote := func(v *types.Vote) *types.Vote {
		c := *v
		c.ValidatorIndex = idx
		c.Extension = nil
		c.ExtensionSignature = nil
		return &c
	}
	canonical := *dve
	canonical.VoteA = canonicalVote(dve.VoteA)
	canonical.VoteB = canonicalVote(dve.VoteB)
	return &canonical, nil
}

// markEvidenceAsCommitted processes all the evidence in the block, marking it as
// committed and removing it from the pending database.
func (evpool *Pool) markEvidenceAsCommitted(evidence types.EvidenceList) {
	blockEvidenceMap := make(map[string]struct{}, len(evidence))
	for _, ev := range evidence {
		for _, removed := range evpool.commitEvidence(ev) {
			blockEvidenceMap[evMapKey(removed)] = struct{}{}
		}
	}

	// remove committed evidence from the clist
	if len(blockEvidenceMap) != 0 {
		evpool.removeEvidenceFromList(blockEvidenceMap)
	}
}

// commitEvidence records ev as committed and deletes it from the pending store
// in one write. For duplicate vote evidence it also records the validator and
// height as committed and deletes any other pending evidence for them. It
// returns the pending evidence that was deleted.
//
// dveMtx is held across the write so that concurrent admission sees either the
// pending entry or the commit marker.
func (evpool *Pool) commitEvidence(ev types.Evidence) []types.Evidence {
	evpool.dveMtx.Lock()
	defer evpool.dveMtx.Unlock()

	batch := evpool.evidenceStore.NewBatch()
	defer batch.Close()

	var removed []types.Evidence
	var dveKey string
	dve, isDuplicateVote := ev.(*types.DuplicateVoteEvidence)
	if isDuplicateVote {
		ts, err := gogotypes.TimestampProto(dve.Time())
		if err == nil {
			var tsBytes []byte
			tsBytes, err = proto.Marshal(ts)
			if err == nil {
				err = batch.Set(keyCommittedDuplicateVote(dve), tsBytes)
			}
		}
		if err != nil {
			evpool.logger.Error("Unable to save committed duplicate vote evidence", "err", err)
		}

		// Pending evidence for the same validator and height that was
		// committed under a different hash.
		dveKey = duplicateVoteKey(dve)
		if pending, exists := evpool.pendingDuplicateVotes[dveKey]; exists && evMapKey(pending) != evMapKey(ev) {
			if err := batch.Delete(keyPending(pending)); err != nil {
				evpool.logger.Error("Unable to delete pending evidence", "err", err)
			} else {
				removed = append(removed, pending)
			}
		}
	}

	if evpool.isPending(ev) {
		if err := batch.Delete(keyPending(ev)); err != nil {
			evpool.logger.Error("Unable to delete pending evidence", "err", err)
		} else {
			removed = append(removed, ev)
		}
	}

	// Add evidence to the committed list. As the evidence is stored in the block store
	// we only need to record the height that it was saved at.
	key := keyCommitted(ev)
	h := gogotypes.Int64Value{Value: ev.Height()}
	evBytes, err := proto.Marshal(&h)
	if err != nil {
		evpool.logger.Error("failed to marshal committed evidence", "err", err, "key(height/hash)", key)
	} else if err := batch.Set(key, evBytes); err != nil {
		evpool.logger.Error("Unable to save committed evidence", "err", err, "key(height/hash)", key)
	}

	if err := batch.Write(); err != nil {
		evpool.logger.Error("Unable to commit evidence", "err", err, "ev", ev)
		return nil
	}

	if isDuplicateVote {
		delete(evpool.pendingDuplicateVotes, dveKey)
	}
	for _, r := range removed {
		atomic.AddUint32(&evpool.evidenceSize, ^uint32(0))
		evpool.logger.Debug("Deleted pending evidence", "evidence", r)
	}
	return removed
}

// listEvidence retrieves lists evidence from oldest to newest within maxBytes.
// If maxBytes is -1, there's no cap on the size of returned evidence.
func (evpool *Pool) listEvidence(prefixKey byte, maxBytes int64) ([]types.Evidence, int64, error) {
	var (
		evSize    int64
		totalSize int64
		evidence  []types.Evidence
		evList    cmtproto.EvidenceList // used for calculating the bytes size
	)

	iter, err := dbm.IteratePrefix(evpool.evidenceStore, []byte{prefixKey})
	if err != nil {
		return nil, totalSize, fmt.Errorf("database error: %v", err)
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var evpb cmtproto.Evidence
		err := evpb.Unmarshal(iter.Value())
		if err != nil {
			return evidence, totalSize, err
		}
		evList.Evidence = append(evList.Evidence, evpb)
		evSize = int64(evList.Size())
		if maxBytes != -1 && evSize > maxBytes {
			if err := iter.Error(); err != nil {
				return evidence, totalSize, err
			}
			return evidence, totalSize, nil
		}

		ev, err := types.EvidenceFromProto(&evpb)
		if err != nil {
			return nil, totalSize, err
		}

		totalSize = evSize
		evidence = append(evidence, ev)
	}

	if err := iter.Error(); err != nil {
		return evidence, totalSize, err
	}
	return evidence, totalSize, nil
}

func (evpool *Pool) removeExpiredPendingEvidence() (int64, time.Time) {
	iter, err := dbm.IteratePrefix(evpool.evidenceStore, []byte{baseKeyPending})
	if err != nil {
		evpool.logger.Error("Unable to iterate over pending evidence", "err", err)
		return evpool.State().LastBlockHeight, evpool.State().LastBlockTime
	}
	defer iter.Close()
	blockEvidenceMap := make(map[string]struct{})
	for ; iter.Valid(); iter.Next() {
		ev, err := bytesToEv(iter.Value())
		if err != nil {
			evpool.logger.Error("Error in transition evidence from protobuf", "err", err)
			continue
		}
		if !evpool.isExpired(ev.Height(), ev.Time()) {
			if len(blockEvidenceMap) != 0 {
				evpool.removeEvidenceFromList(blockEvidenceMap)
			}

			// return the height and time with which this evidence will have expired so we know when to prune next
			return ev.Height() + evpool.State().ConsensusParams.Evidence.MaxAgeNumBlocks + 1,
				ev.Time().Add(evpool.State().ConsensusParams.Evidence.MaxAgeDuration).Add(time.Second)
		}
		evpool.removePendingEvidence(ev)
		blockEvidenceMap[evMapKey(ev)] = struct{}{}
	}
	// We either have no pending evidence or all evidence has expired
	if len(blockEvidenceMap) != 0 {
		evpool.removeEvidenceFromList(blockEvidenceMap)
	}
	return evpool.State().LastBlockHeight, evpool.State().LastBlockTime
}

// removeExpiredCommittedDuplicateVotes deletes the commit markers of duplicate
// vote evidence that has expired. Expired evidence fails verification, so the
// markers are no longer needed. Markers are ordered by height, and the scan
// stops at the first marker that has not expired.
func (evpool *Pool) removeExpiredCommittedDuplicateVotes() {
	iter, err := dbm.IteratePrefix(evpool.evidenceStore, []byte{baseKeyCommittedDuplicateVote})
	if err != nil {
		evpool.logger.Error("Unable to iterate over committed duplicate vote evidence", "err", err)
		return
	}
	var expired [][]byte
	for ; iter.Valid(); iter.Next() {
		height, evTime, err := parseCommittedDuplicateVote(iter.Key(), iter.Value())
		if err != nil {
			evpool.logger.Error("Unable to parse committed duplicate vote evidence", "err", err)
			expired = append(expired, append([]byte(nil), iter.Key()...))
			continue
		}
		if !evpool.isExpired(height, evTime) {
			break
		}
		expired = append(expired, append([]byte(nil), iter.Key()...))
	}
	if err := iter.Error(); err != nil {
		evpool.logger.Error("Unable to iterate over committed duplicate vote evidence", "err", err)
	}
	if err := iter.Close(); err != nil {
		evpool.logger.Error("Unable to close iterator", "err", err)
	}
	for _, key := range expired {
		if err := evpool.evidenceStore.Delete(key); err != nil {
			evpool.logger.Error("Unable to delete committed duplicate vote evidence", "err", err)
		}
	}
}

func (evpool *Pool) removeEvidenceFromList(
	blockEvidenceMap map[string]struct{},
) {
	for e := evpool.evidenceList.Front(); e != nil; e = e.Next() {
		// Remove from clist
		ev := e.Value.(types.Evidence)
		if _, ok := blockEvidenceMap[evMapKey(ev)]; ok {
			evpool.evidenceList.Remove(e)
			e.DetachPrev()
		}
	}
}

func (evpool *Pool) updateState(state sm.State) {
	evpool.mtx.Lock()
	defer evpool.mtx.Unlock()
	evpool.state = state
}

// processConsensusBuffer converts all the duplicate votes witnessed from consensus
// into DuplicateVoteEvidence. It sets the evidence timestamp to the block height
// from the most recently committed block.
// Evidence is then added to the pool so as to be ready to be broadcasted and proposed.
func (evpool *Pool) processConsensusBuffer(state sm.State) {
	evpool.mtx.Lock()
	defer evpool.mtx.Unlock()
	for _, voteSet := range evpool.consensusBuffer {

		// Check the height of the conflicting votes and fetch the corresponding time and validator set
		// to produce the valid evidence
		var (
			dve *types.DuplicateVoteEvidence
			err error
		)
		switch {
		case voteSet.VoteA.Height == state.LastBlockHeight:
			dve, err = types.NewDuplicateVoteEvidence(
				voteSet.VoteA,
				voteSet.VoteB,
				state.LastBlockTime,
				state.LastValidators,
			)

		case voteSet.VoteA.Height < state.LastBlockHeight:
			var valSet *types.ValidatorSet
			valSet, err = evpool.stateDB.LoadValidators(voteSet.VoteA.Height)
			if err != nil {
				evpool.logger.Error("failed to load validator set for conflicting votes", "height",
					voteSet.VoteA.Height, "err", err,
				)
				continue
			}
			blockMeta := evpool.blockStore.LoadBlockMeta(voteSet.VoteA.Height)
			if blockMeta == nil {
				evpool.logger.Error("failed to load block time for conflicting votes", "height", voteSet.VoteA.Height)
				continue
			}
			dve, err = types.NewDuplicateVoteEvidence(
				voteSet.VoteA,
				voteSet.VoteB,
				blockMeta.Header.Time,
				valSet,
			)

		default:
			// evidence pool shouldn't expect to get votes from consensus of a height that is above the current
			// state. If this error is seen then perhaps consider keeping the votes in the buffer and retry
			// in following heights
			evpool.logger.Error("inbound duplicate votes from consensus are of a greater height than current state",
				"duplicate vote height", voteSet.VoteA.Height,
				"state.LastBlockHeight", state.LastBlockHeight)
			continue
		}
		if err != nil {
			evpool.logger.Error("error in generating evidence from votes", "err", err)
			continue
		}

		// check if we already have this evidence
		if evpool.isPending(dve) {
			evpool.logger.Info("evidence already pending; ignoring", "evidence", dve)
			continue
		}

		// check that the evidence is not already committed on chain
		if evpool.isCommitted(dve) {
			evpool.logger.Info("evidence already committed; ignoring", "evidence", dve)
			continue
		}

		canonical, err := evpool.canonicalDuplicateVote(dve)
		if err != nil {
			evpool.logger.Error("failed to canonicalize evidence from consensus buffer", "err", err)
			continue
		}
		added, err := evpool.addPendingDuplicateVote(canonical)
		if err != nil {
			evpool.logger.Error("failed to flush evidence from consensus buffer to pending list", "err", err)
			continue
		}
		if !added {
			evpool.logger.Info("evidence for this validator and height already pending or committed; ignoring", "evidence", dve)
			continue
		}
		dve = canonical

		evpool.logger.Info("verified new evidence of byzantine behavior", "evidence", dve)
	}
	// reset consensus buffer
	evpool.consensusBuffer = make([]duplicateVoteSet, 0)
}

type duplicateVoteSet struct {
	VoteA *types.Vote
	VoteB *types.Vote
}

func bytesToEv(evBytes []byte) (types.Evidence, error) {
	var evpb cmtproto.Evidence
	err := evpb.Unmarshal(evBytes)
	if err != nil {
		return &types.DuplicateVoteEvidence{}, err
	}

	return types.EvidenceFromProto(&evpb)
}

func evMapKey(ev types.Evidence) string {
	return string(ev.Hash())
}

// big endian padded hex
func bE(h int64) string {
	return fmt.Sprintf("%0.16X", h)
}

func keyCommitted(evidence types.Evidence) []byte {
	return append([]byte{baseKeyCommitted}, keySuffix(evidence)...)
}

func keyPending(evidence types.Evidence) []byte {
	return append([]byte{baseKeyPending}, keySuffix(evidence)...)
}

func keyCommittedDuplicateVote(dve *types.DuplicateVoteEvidence) []byte {
	return append([]byte{baseKeyCommittedDuplicateVote}, []byte(duplicateVoteKey(dve))...)
}

// duplicateVoteKey identifies the validator and height that dve is evidence
// against, independent of the round, vote type, conflicting votes or encoding
// used to prove it. It starts with the height so that keys sort by height.
func duplicateVoteKey(dve *types.DuplicateVoteEvidence) string {
	return fmt.Sprintf("%s/%X", bE(dve.VoteA.Height), dve.VoteA.ValidatorAddress)
}

// parseCommittedDuplicateVote returns the height and evidence time of a commit
// marker written under keyCommittedDuplicateVote.
func parseCommittedDuplicateVote(key, value []byte) (int64, time.Time, error) {
	const heightLen = 16
	if len(key) < 1+heightLen {
		return 0, time.Time{}, fmt.Errorf("committed duplicate vote key too short: %X", key)
	}
	height, err := strconv.ParseInt(string(key[1:1+heightLen]), 16, 64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parsing committed duplicate vote height: %w", err)
	}
	var ts gogotypes.Timestamp
	if err := proto.Unmarshal(value, &ts); err != nil {
		return 0, time.Time{}, fmt.Errorf("parsing committed duplicate vote time: %w", err)
	}
	evTime, err := gogotypes.TimestampFromProto(&ts)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parsing committed duplicate vote time: %w", err)
	}
	return height, evTime, nil
}

func keySuffix(evidence types.Evidence) []byte {
	return []byte(fmt.Sprintf("%s/%X", bE(evidence.Height()), evidence.Hash()))
}
