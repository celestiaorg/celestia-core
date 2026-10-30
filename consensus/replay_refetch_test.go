package consensus

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/crypto"
	cmtpubsub "github.com/cometbft/cometbft/libs/pubsub"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	mempl "github.com/cometbft/cometbft/mempool"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// propagatorCall is one AddCommitment, AddBlock or SetConsensusState call.
type propagatorCall struct {
	commitment bool
	block      bool
	height     int64
	round      int32
	psh        types.PartSetHeader
}

// callRecorder records, in order, the calls that point the propagator at a block or move its round.
type callRecorder struct {
	propagation.Propagator

	mtx   cmtsync.Mutex
	calls []propagatorCall
}

func (r *callRecorder) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	r.mtx.Lock()
	r.calls = append(r.calls, propagatorCall{commitment: true, height: height, round: round, psh: *psh})
	r.mtx.Unlock()
	r.Propagator.AddCommitment(height, round, psh)
}

func (r *callRecorder) AddBlock(height int64, round int32, parts *types.PartSet) {
	r.mtx.Lock()
	r.calls = append(r.calls, propagatorCall{block: true, height: height, round: round, psh: parts.Header()})
	r.mtx.Unlock()
	r.Propagator.AddBlock(height, round, parts)
}

func (r *callRecorder) SetConsensusState(height int64, round int32, proposer crypto.PubKey) {
	r.mtx.Lock()
	r.calls = append(r.calls, propagatorCall{height: height, round: round})
	r.mtx.Unlock()
	r.Propagator.SetConsensusState(height, round, proposer)
}

func (r *callRecorder) snapshot() []propagatorCall {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return append([]propagatorCall(nil), r.calls...)
}

func TestWALPeerMsg(t *testing.T) {
	prop := &types.Proposal{Height: 2, Round: 1}
	mi := msgInfo{Msg: &ProposalMessage{prop}}
	require.True(t, walPeerMsg(nil, prop, mi))
	require.False(t, walPeerMsg(nil, nil, mi), "rejected")
	require.False(t, walPeerMsg(prop, prop, mi), "already set")
	other := &types.Proposal{Height: 2, Round: 1}
	require.False(t, walPeerMsg(other, other, mi), "another proposal already set")
	require.False(t, walPeerMsg(nil, prop, msgInfo{Msg: &BlockPartMessage{Height: 2, Round: 1}}))
	require.False(t, walPeerMsg(nil, prop, msgInfo{Msg: &VoteMessage{&types.Vote{Height: 2, Round: 1}}}))
}

// A peer cannot make the node journal proposals it rejects.
func TestWALJournalsOnlyAcceptedProposal(t *testing.T) {
	f := newLockFixture(t, true, false)
	cs := f.newState()
	require.NoError(t, cs.Start())
	prop, _ := decideProposal(context.Background(), t, cs, f.vss[f.p0], 1, 0)
	for i := 0; i < 5; i++ {
		bad := *prop
		bad.Signature = append([]byte(nil), prop.Signature...)
		bad.Signature[i] ^= 1
		require.NoError(t, cs.SetProposal(&bad, "peer"))
	}
	require.NoError(t, cs.SetProposal(prop, "peer"))
	require.Eventually(t, func() bool { return cs.GetRoundState().Proposal != nil }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, cs.Stop())
	cs.Wait()

	in, err := os.Open(f.config.Consensus.WalFile())
	require.NoError(t, err)
	defer in.Close()
	dec := NewWALDecoder(in)
	var journaled []*types.Proposal
	for {
		msg, err := dec.Decode()
		if err != nil {
			break
		}
		if mi, ok := msg.Msg.(msgInfo); ok {
			if pm, ok := mi.Msg.(*ProposalMessage); ok {
				journaled = append(journaled, pm.Proposal)
			}
		}
	}
	require.Len(t, journaled, 1)
	require.Equal(t, prop.Signature, journaled[0].Signature)
}

// Replaying a lock without its bytes prepares the part set and asks the propagator for them.
func TestReplayLockInfoRefetchesBytes(t *testing.T) {
	cs, _ := randState(1)
	rec := &callRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	psh := types.PartSetHeader{Total: 2, Hash: cmtrand.Bytes(32)}
	li := LockInfo{Height: 1, Round: 0, BlockID: types.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: psh}}

	require.NoError(t, cs.readReplayMessage(&TimedWALMessage{Msg: li}, nil))
	require.True(t, cs.GetRoundState().ProposalBlockParts.HasHeader(psh))
	require.Equal(t, []propagatorCall{{commitment: true, height: 1, round: 0, psh: psh}}, rec.snapshot())
}

// A validator that prevoted a peer's block restarts with the proposal, so the
// block's parts are accepted again and it precommits the block on the polka.
func TestRestartAfterPrevoteRefetchesProposalBlock(t *testing.T) {
	f := newLockFixture(t, true, false)
	cs1 := f.newState()
	require.NoError(t, cs1.Start())
	voteCh := subscribeToVoter(cs1, cs1.privValidatorPubKey.Address())
	prop, block := decideProposal(context.Background(), t, cs1, f.vss[f.p0], 1, 0)
	parts, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NoError(t, cs1.SetProposalAndBlock(prop, block, parts, "peer"))
	nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	require.NoError(t, cs1.Stop())
	cs1.Wait()

	cs := f.newState()
	_, voteCh = startRestarted(t, cs)
	rs := cs.GetRoundState()
	require.NotNil(t, rs.Proposal, "the peer's proposal was not journaled")
	require.Equal(t, prop.BlockID, rs.Proposal.BlockID)
	require.Nil(t, rs.ProposalBlock)

	addParts(t, cs, parts, 0)
	signAddVotes(cs, cmtproto.PrevoteType, block.Hash(), parts.Header(), false, f.others...)
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.Equal(t, prop.BlockID, v.BlockID)
}

// A node locked by ID refetches the block, fills the lock bytes once the parts
// arrive and, as a later proposer, re-proposes the locked block.
func TestRestartedLockRefetchesAndReproposes(t *testing.T) {
	f := newLockFixture(t, true, false)
	_, block, parts := f.lockAndStop(t)
	xID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

	cs := f.newState()
	rec := &callRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	newRoundCh, _ := startRestarted(t, cs)
	calls := rec.snapshot()
	require.Contains(t, calls, propagatorCall{commitment: true, height: 1, round: 0, psh: parts.Header()})
	require.Equal(t, propagatorCall{height: 1, round: 0}, calls[len(calls)-1], "SetConsensusState must follow the refetch")
	require.Nil(t, cs.GetRoundState().LockedBlock)

	addParts(t, cs, parts, 0)
	require.Eventually(t, func() bool {
		rs := cs.GetRoundState()
		return blockMatchesBlockID(rs.LockedBlock, rs.LockedBlockParts, xID)
	}, 5*time.Second, 10*time.Millisecond)
	rs := cs.GetRoundState()
	require.EqualValues(t, 0, rs.ValidRound)
	require.True(t, blockMatchesBlockID(rs.ValidBlock, rs.ValidBlockParts, xID))
	require.Contains(t, rec.snapshot(), propagatorCall{block: true, height: 1, round: 0, psh: parts.Header()})

	p := f.nextOwnProposal(t, cs, newRoundCh)
	require.Equal(t, xID, p.BlockID)
	require.EqualValues(t, 0, p.POLRound)
}

// A restarted node hands its locked block X to the propagator for the lock round, and
// adopts and hands it over for round 1 on a polka for X while Y is proposed there.
func TestLockedBlockSharedWithPropagator(t *testing.T) {
	f := newLockFixture(t, true, true)
	cs1 := f.newState()
	voteCh := subscribeToVoter(cs1, cs1.privValidatorPubKey.Address())
	require.NoError(t, cs1.Start())
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	signAddVotes(cs1, cmtproto.PrevoteType, v.BlockID.Hash, v.BlockID.PartSetHeader, false, f.others...)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.NoError(t, cs1.Stop())
	cs1.Wait()

	cs := f.newState()
	rec := &callRecorder{Propagator: cs.propagator}
	cs.propagator = rec
	newRoundCh, voteCh := startRestarted(t, cs)
	psh := v.BlockID.PartSetHeader
	require.Contains(t, rec.snapshot(), propagatorCall{block: true, height: 1, round: 0, psh: psh})

	f.moveToRound1(t, cs, newRoundCh)
	f.proposeFreshY(t, cs)
	nextVote(t, voteCh, 1, 1, cmtproto.PrevoteType)
	signAddVotes(cs, cmtproto.PrevoteType, v.BlockID.Hash, psh, false, f.others...)
	nextVote(t, voteCh, 1, 1, cmtproto.PrecommitType)
	calls := rec.snapshot()
	require.Contains(t, calls, propagatorCall{block: true, height: 1, round: 1, psh: psh})
	require.NotContains(t, calls, propagatorCall{commitment: true, height: 1, round: 1, psh: psh})
	rs := cs.GetRoundState()
	require.EqualValues(t, 1, rs.ValidRound)
	require.True(t, blockMatchesBlockID(rs.ValidBlock, rs.ValidBlockParts, v.BlockID))
}

// A round-0 proposer locked on its own block restarts with the bytes from its
// WAL and re-proposes that block, with its lock round as POL, when it proposes again.
func TestRestartedProposerReproposesLock(t *testing.T) {
	f := newLockFixture(t, true, true)
	cs1 := f.newState()
	voteCh := subscribeToVoter(cs1, cs1.privValidatorPubKey.Address())
	require.NoError(t, cs1.Start())
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	signAddVotes(cs1, cmtproto.PrevoteType, v.BlockID.Hash, v.BlockID.PartSetHeader, false, f.others...)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	require.NoError(t, cs1.Stop())
	cs1.Wait()

	cs := f.newState()
	newRoundCh, _ := startRestarted(t, cs)
	rs := cs.GetRoundState()
	require.True(t, blockMatchesBlockID(rs.LockedBlock, rs.LockedBlockParts, v.BlockID))
	require.EqualValues(t, 0, rs.ValidRound)

	p := f.nextOwnProposal(t, cs, newRoundCh)
	require.Equal(t, v.BlockID, p.BlockID)
	require.EqualValues(t, 0, p.POLRound)
}

// A proposer that relocks its own block at a later round restarts with Valid*
// at the relock round, as before the crash, and uses it as POL when it proposes again.
func TestRestartedProposerKeepsRelockRound(t *testing.T) {
	f := newLockFixture(t, true, true)
	cs1 := f.newState()
	newRoundCh, voteCh := startRestarted(t, cs1)
	v := nextVote(t, voteCh, 1, 0, cmtproto.PrevoteType)
	signAddVotes(cs1, cmtproto.PrevoteType, v.BlockID.Hash, v.BlockID.PartSetHeader, false, f.others...)
	nextVote(t, voteCh, 1, 0, cmtproto.PrecommitType)
	// Two nil precommits: round 1 starts on the precommit timeout, which is journaled.
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, false, f.others[:2]...)
	waitRound(t, newRoundCh, 1, 1)
	incrementRound(f.vss...)

	rs := cs1.GetRoundState()
	prop := types.NewProposal(1, 1, 0, v.BlockID)
	pp := prop.ToProto()
	require.NoError(t, f.vss[f.p1].SignProposal(cs1.state.ChainID, pp))
	prop.Signature = pp.Signature
	require.NoError(t, cs1.SetProposalAndBlock(prop, rs.LockedBlock, rs.LockedBlockParts, "peer"))
	nextVote(t, voteCh, 1, 1, cmtproto.PrevoteType)
	signAddVotes(cs1, cmtproto.PrevoteType, v.BlockID.Hash, v.BlockID.PartSetHeader, false, f.others...)
	nextVote(t, voteCh, 1, 1, cmtproto.PrecommitType)
	rs = cs1.GetRoundState()
	require.EqualValues(t, 1, rs.LockedRound)
	require.EqualValues(t, 1, rs.ValidRound)
	require.NoError(t, cs1.Stop())
	cs1.Wait()

	cs := f.newState()
	newRoundCh, _ = startRestarted(t, cs)
	rs = cs.GetRoundState()
	require.EqualValues(t, 1, rs.Round)
	require.EqualValues(t, 1, rs.LockedRound)
	require.EqualValues(t, 1, rs.ValidRound)

	p := f.nextOwnProposal(t, cs, newRoundCh)
	require.Equal(t, v.BlockID, p.BlockID)
	require.EqualValues(t, 1, p.POLRound)
}

// nextOwnProposal skips rounds with nil precommits until our node proposes again
// and returns its proposal; a mempool tx makes a fresh block differ from the lock.
func (f *lockFixture) nextOwnProposal(t *testing.T, cs *State, newRoundCh <-chan cmtpubsub.Message) *types.Proposal {
	t.Helper()
	require.NoError(t, assertMempool(cs.txNotifier).CheckTx(kvstore.NewTx("fresh", "z"), nil, mempl.TxInfo{}))
	vals := f.state.Validators.Copy()
	ours := cs.privValidatorPubKey.Address()
	from := cs.GetRoundState().Round
	r := int32(0)
	for r <= from || !bytes.Equal(vals.GetProposer().Address, ours) {
		vals.IncrementProposerPriority(1)
		r++
	}
	for rr := from; rr < r; rr++ {
		signAddVotes(cs, cmtproto.PrecommitType, nil, types.PartSetHeader{}, false, f.others...)
		waitRound(t, newRoundCh, 1, rr+1)
		incrementRound(f.vss...)
	}
	require.Eventually(t, func() bool {
		p := cs.GetRoundState().Proposal
		return p != nil && p.Round == r
	}, 5*time.Second, 10*time.Millisecond)
	return cs.GetRoundState().Proposal
}

func addParts(t *testing.T, cs *State, parts *types.PartSet, round int32) {
	t.Helper()
	for i := 0; i < int(parts.Total()); i++ {
		require.NoError(t, cs.AddProposalBlockPart(1, round, parts.GetPart(i), "peer"))
	}
}
