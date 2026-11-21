package consensus

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtpubsub "github.com/cometbft/cometbft/libs/pubsub"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// missedBlockFilter drops, at the receiving node, the messages that keep node d
// from ever receiving the round-0 block and keep round 0 from committing.
type missedBlockFilter struct {
	p2p.Reactor
	node, d, b, c int
	target        int64
	valIdx        map[int32]int
}

func (f *missedBlockFilter) QueueUnprocessedEnvelope(e p2p.UnprocessedEnvelope) {
	if !f.drop(e) {
		f.Reactor.QueueUnprocessedEnvelope(e)
	}
}

func (f *missedBlockFilter) drop(e p2p.UnprocessedEnvelope) bool {
	switch e.ChannelID {
	case VoteChannel, DataChannel:
		m := &cmtcons.Message{}
		if proto.Unmarshal(e.Message, m) != nil {
			return false
		}
		// every node sees three round-0 precommits but only two for the block.
		if v := m.GetVote(); v != nil && v.Vote != nil && v.Vote.Height == f.target &&
			v.Vote.Round == 0 && v.Vote.Type == cmtproto.PrecommitType {
			val := f.valIdx[v.Vote.ValidatorIndex]
			return (val == f.c && f.node != f.c) || (val == f.b && f.node == f.c)
		}
		if p := m.GetProposal(); p != nil {
			return f.hidden(p.Proposal.Height, p.Proposal.Round)
		}
		if bp := m.GetBlockPart(); bp != nil {
			return f.hidden(bp.Height, bp.Round)
		}
	case propagation.DataChannel, propagation.WantChannel:
		m := &propproto.Message{}
		if f.node != f.d || proto.Unmarshal(e.Message, m) != nil {
			return false
		}
		if cb := m.GetCompactBlock(); cb != nil && cb.Proposal != nil {
			return f.hidden(cb.Proposal.Height, cb.Proposal.Round)
		}
		if hp := m.GetHaveParts(); hp != nil {
			return f.hidden(hp.Height, hp.Round)
		}
		if rp := m.GetRecoveryPart(); rp != nil {
			return f.hidden(rp.Height, rp.Round)
		}
	}
	return false
}

// hidden reports whether node d must not see this round-0 block data.
func (f *missedBlockFilter) hidden(height int64, round int32) bool {
	return f.node == f.d && height == f.target && round == 0
}

// configTimeoutsApp leaves the round timeouts to the consensus config.
type configTimeoutsApp struct{ *kvstore.Application }

func (a configTimeoutsApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	res, err := a.Application.FinalizeBlock(ctx, req)
	if res != nil {
		res.TimeoutInfo = abci.TimeoutInfo{}
	}
	return res, err
}

// TestReactorCommitsBlockNeverReceived checks that a validator that never receives
// the block the others lock on at round 0 and commit at round 1 still reaches the
// next height.
func TestReactorCommitsBlockNeverReceived(t *testing.T) {
	const n = 4
	const target = int64(3)
	css, cleanup := randConsensusNet(t, n, "consensus_missed_block_test", NewTimeoutTicker,
		func() abci.Application { return configTimeoutsApp{kvstore.NewInMemoryApplication()} },
		func(c *cfg.Config) {
			c.Consensus.TimeoutPropose = 2 * time.Second
			c.Consensus.TimeoutProposeDelta = 500 * time.Millisecond
			c.Consensus.TimeoutPrevote = 500 * time.Millisecond
			c.Consensus.TimeoutPrecommit = 500 * time.Millisecond
			c.Consensus.TimeoutCommit = 200 * time.Millisecond
			c.Consensus.SkipTimeoutCommit = false
		})
	t.Cleanup(cleanup)

	// roles at the target height: proposer priorities advance once per height.
	vals := css[0].GetState().Validators.CopyIncrementProposerPriority(int32(target - 1))
	p0 := vals.GetProposer().Address
	p1 := vals.CopyIncrementProposerPriority(1).GetProposer().Address
	valIdx := map[int32]int{}
	d, b, c := -1, -1, -1
	for i, cs := range css {
		addr := cs.privValidatorPubKey.Address()
		idx, _ := vals.GetByAddress(addr)
		valIdx[idx] = i
		switch {
		case bytes.Equal(addr, p0):
		case bytes.Equal(addr, p1):
			d = i
		case b < 0:
			b = i
		default:
			c = i
		}
	}

	reactors := make([]*Reactor, n)
	buses := make([]*types.EventBus, n)
	for i := range css {
		reactors[i] = NewReactor(css[i], css[i].propagator, true, WithGossipDataEnabled(false))
		reactors[i].SetLogger(css[i].Logger)
		buses[i] = css[i].eventBus
		reactors[i].SetEventBus(buses[i])
		require.NoError(t, css[i].blockExec.Store().Save(css[i].state))
	}
	p2p.MakeConnectedSwitches(config.P2P, n, func(i int, s *p2p.Switch) *p2p.Switch {
		prop := css[i].propagator.(*propagation.Reactor)
		prop.SetLogger(css[i].Logger.With("module", "propagation"))
		filter := func(r p2p.Reactor) p2p.Reactor {
			return &missedBlockFilter{Reactor: r, node: i, d: d, b: b, c: c, target: target, valIdx: valIdx}
		}
		s.AddReactor("CONSENSUS", filter(reactors[i]))
		s.AddReactor("RECOVERY", filter(prop))
		s.SetLogger(css[i].Logger.With("module", "p2p"))
		return s
	}, p2p.Connect2Switches)
	t.Cleanup(func() { stopConsensusNet(css[0].Logger, reactors, buses) })
	for i := range reactors {
		reactors[i].SwitchToConsensus(reactors[i].conS.GetState(), false)
	}

	require.Eventually(t, func() bool {
		for _, cs := range css {
			if cs.GetRoundState().Height <= target {
				return false
			}
		}
		return true
	}, 30*time.Second, 50*time.Millisecond, "node %d never received the block and stalled at height %d", d, target)

	// the scenario must have played out: the block was committed at round 1.
	commit := css[d].blockStore.LoadSeenCommit(target)
	require.NotNil(t, commit)
	require.EqualValues(t, 1, commit.Round)
}

// partFeeder hands consensus the parts a test sends instead of the propagator's, and
// holds no block of its own.
type partFeeder struct {
	propagation.Propagator
	parts chan types.PartInfo
}

func (p partFeeder) GetPartChan() <-chan types.PartInfo { return p.parts }

func (p partFeeder) GetProposal(int64, int32) (*types.Proposal, *types.PartSet, bool) {
	return nil, nil, false
}

// commitEarlierRound starts cs1 (round 0's proposer), moves it to round 1 on a nil polka
// and has the round-0 precommits for its block X commit X while it is in round 1.
func commitEarlierRound(t *testing.T, cs1 *State, others []*validatorStub) (*types.PartSet, <-chan cmtpubsub.Message) {
	_, blockX := decideProposal(context.Background(), t, cs1, others[0], 1, 0)
	partsX, err := blockX.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	newBlockCh := subscribe(cs1.eventBus, types.EventQueryNewBlock)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	require.NoError(t, cs1.Start())
	t.Cleanup(func() { _ = cs1.Stop(); cs1.Wait() })
	ensureNewRound(newRoundCh, 1, 0)
	// own parts still queued after the round skip would complete X in the commit step.
	ensureNewProposal(proposalCh, 1, 0)

	incrementRound(others...)
	signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, others...)
	ensureNewRound(newRoundCh, 1, 1)
	for _, vs := range others {
		vs.Round = 0
	}
	signAddVotes(cs1, cmtproto.PrecommitType, blockX.Hash(), partsX.Header(), true, others...)
	return partsX, newBlockCh
}

// A node that enters the commit step for an earlier round than its current one stays
// there on its current round's precommits, takes the commit round's parts and commits.
func TestCommitForEarlierRoundTakesItsParts(t *testing.T) {
	cs1, vss := randState(4)
	others := vss[1:]
	feeder := partFeeder{Propagator: cs1.propagator, parts: make(chan types.PartInfo, 16)}
	cs1.propagator = feeder
	partsX, newBlockCh := commitEarlierRound(t, cs1, others)
	require.Eventually(t, func() bool {
		rs := cs1.GetRoundState()
		return rs.Step == cstypes.RoundStepCommit && rs.CommitRound == 0 && rs.Round == 1
	}, 5*time.Second, 10*time.Millisecond)

	for _, vs := range others {
		vs.Round = 1
	}
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, others...)
	require.Eventually(t, func() bool {
		return cs1.GetRoundState().Votes.Precommits(1).HasTwoThirdsAny()
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, cstypes.RoundStepCommit, cs1.GetRoundState().Step)

	for i := 0; i < int(partsX.Total()); i++ {
		feeder.parts <- types.PartInfo{Part: partsX.GetPart(i), Height: 1, Round: 0}
	}
	ensureNewBlock(newBlockCh, 1)
}

// The proposer's propagator still holds its round-0 block, complete, after consensus
// dropped it for round 1: the late commit of that block takes it from there.
func TestCommitForEarlierRoundTakesHeldBlock(t *testing.T) {
	cs1, vss := randState(4)
	_, newBlockCh := commitEarlierRound(t, cs1, vss[1:])
	ensureNewBlock(newBlockCh, 1)
}

// proposalFeeder hands consensus the proposal and parts a test sends, and holds that
// proposal's parts like the propagator does once it has them.
type proposalFeeder struct {
	propagation.Propagator
	proposals chan propagation.ProposalAndSrc
	parts     chan types.PartInfo
	proposal  *types.Proposal
	partSet   *types.PartSet
}

func (p *proposalFeeder) GetProposalChan() <-chan propagation.ProposalAndSrc { return p.proposals }

func (p *proposalFeeder) GetPartChan() <-chan types.PartInfo { return p.parts }

func (p *proposalFeeder) GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool) {
	if p.proposal == nil || p.proposal.Height != height || p.proposal.Round != round {
		return nil, nil, false
	}
	return p.proposal, p.partSet, true
}

// partRecordingWAL counts the block parts consensus writes to its WAL.
type partRecordingWAL struct {
	nilWAL
	mtx   sync.Mutex
	parts int
}

func (w *partRecordingWAL) Write(m WALMessage) error {
	if mi, ok := m.(msgInfo); ok {
		if _, ok := mi.Msg.(*BlockPartMessage); ok {
			w.mtx.Lock()
			w.parts++
			w.mtx.Unlock()
		}
	}
	return nil
}

func (w *partRecordingWAL) WriteSync(m WALMessage) error { return w.Write(m) }

func (w *partRecordingWAL) partWrites() int {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	return w.parts
}

// earlyPartsSetup holds a node waiting in round 1's propose step, round 1's proposal and
// its block, and the feeder that stands in for the node's propagator.
type earlyPartsSetup struct {
	cs1      *State
	others   []*validatorStub
	feeder   *proposalFeeder
	wal      *partRecordingWAL
	proposal *types.Proposal
	block    *types.Block
	parts    *types.PartSet
}

// startEarlyParts moves cs1 to round 1, where another validator proposes, and hands
// consensus that proposal's parts before anything else about the proposal.
func startEarlyParts(t *testing.T) earlyPartsSetup {
	cs1, vss := randState(4)
	others := vss[1:]
	height := cs1.rs.Height
	cs1.state.Timeouts.TimeoutPropose = time.Minute
	cs1.config.OnlyInternalWal = true
	wal := &partRecordingWAL{}
	cs1.wal = wal
	cs1.doWALCatchup = false

	// round 1's proposer signs a block built by cs1.
	proposerAddr := cs1.rs.Validators.CopyIncrementProposerPriority(1).GetProposer().Address
	var proposer *validatorStub
	for _, vs := range others {
		pubKey, err := vs.GetPubKey()
		require.NoError(t, err)
		if bytes.Equal(pubKey.Address(), proposerAddr) {
			proposer = vs
		}
	}
	require.NotNil(t, proposer)
	proposal, block := decideProposal(context.Background(), t, cs1, proposer, height, 1)
	parts, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	feeder := &proposalFeeder{
		Propagator: cs1.propagator,
		proposals:  make(chan propagation.ProposalAndSrc, 1),
		parts:      make(chan types.PartInfo, parts.Total()+1),
		proposal:   proposal,
		partSet:    parts,
	}
	cs1.propagator = feeder

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	require.NoError(t, cs1.Start())
	t.Cleanup(func() { _ = cs1.Stop(); cs1.Wait() })
	ensureNewRound(newRoundCh, height, 0)

	// nil precommits at round 0 move cs1 to round 1 and leave the others' round-1
	// votes free.
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, false, others...)
	ensureNewRound(newRoundCh, height, 1)
	incrementRound(others...)

	// wait out the round-step wakes: one still pending could hand over the parts
	// later, without the wake under test. syncData finishes a wake it took before
	// it reads anything else.
	require.Eventually(t, func() bool {
		rs := cs1.GetRoundState()
		return rs.Round == 1 && rs.Step == cstypes.RoundStepPropose && len(cs1.newHeightOrRoundChan) == 0
	}, time.Second, time.Millisecond)
	walParts := wal.partWrites()

	// the parts go first: syncData forwards them while cs1 has no part set. A part
	// for another round follows; syncData skips it, and takes it only once it has
	// queued the real parts.
	for i := 0; i < int(parts.Total()); i++ {
		feeder.parts <- types.PartInfo{Part: parts.GetPart(i), Height: height, Round: 1}
	}
	feeder.parts <- types.PartInfo{Part: parts.GetPart(0), Height: height, Round: 99}
	require.Eventually(t, func() bool { return len(feeder.parts) == 0 }, time.Second, time.Millisecond)
	require.Equal(t, walParts, wal.partWrites(), "parts from the propagator must not be written to the WAL")

	return earlyPartsSetup{cs1: cs1, others: others, feeder: feeder, wal: wal, proposal: proposal, block: block, parts: parts}
}

// requireBlockCompletes waits for cs1 to hold round 1's block, well before the propose
// timeout, and checks that no part handed over again was written to the WAL.
func requireBlockCompletes(t *testing.T, s earlyPartsSetup, walParts int) {
	t.Helper()
	require.Eventually(t, func() bool {
		rs := s.cs1.GetRoundState()
		return rs.Round == 1 && rs.ProposalBlock != nil && bytes.Equal(rs.ProposalBlock.Hash(), s.block.Hash())
	}, 5*time.Second, 10*time.Millisecond, "the block never completed")
	require.Equal(t, walParts, s.wal.partWrites(), "parts from the propagator must not be written to the WAL")
}

// A part that reaches consensus before its proposal is dropped. Once the proposal
// arrives, consensus must still take the parts the propagator holds and complete the
// block before the propose timeout.
func TestProposalAfterItsPartsCompletesBlock(t *testing.T) {
	s := startEarlyParts(t)
	walParts := s.wal.partWrites()
	s.feeder.proposals <- propagation.ProposalAndSrc{Proposal: *s.proposal, From: "peer"}
	requireBlockCompletes(t, s, walParts)
}

// A part dropped before a polka creates the part set must also reach consensus then,
// not only at the propose timeout.
func TestPolkaAfterItsPartsCompletesBlock(t *testing.T) {
	s := startEarlyParts(t)
	walParts := s.wal.partWrites()
	signAddVotes(s.cs1, cmtproto.PrevoteType, s.block.Hash(), s.parts.Header(), false, s.others...)
	requireBlockCompletes(t, s, walParts)
}
