package consensus

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// capturingLogger records every log call so tests can assert that consensus
// message handlers log failures in place at the expected level.
type capturingLogger struct {
	mtx     sync.Mutex
	entries []logEntry
}

type logEntry struct {
	level string
	msg   string
}

var _ log.Logger = (*capturingLogger)(nil)

func (l *capturingLogger) record(level, msg string) {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg})
}

func (l *capturingLogger) Trace(msg string, _ ...any) { l.record("trace", msg) }
func (l *capturingLogger) Debug(msg string, _ ...any) { l.record("debug", msg) }
func (l *capturingLogger) Info(msg string, _ ...any)  { l.record("info", msg) }
func (l *capturingLogger) Error(msg string, _ ...any) { l.record("error", msg) }
func (l *capturingLogger) With(_ ...any) log.Logger   { return l }

// levelOf returns the level of the first recorded entry with the given
// message.
func (l *capturingLogger) levelOf(msg string) (string, bool) {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	for _, e := range l.entries {
		if e.msg == msg {
			return e.level, true
		}
	}
	return "", false
}

func requireLogged(t *testing.T, logger *capturingLogger, msg, level string) {
	t.Helper()
	got, ok := logger.levelOf(msg)
	require.True(t, ok, "expected a log with message %q, got %+v", msg, logger.entries)
	require.Equal(t, level, got, "level of log %q", msg)
}

func TestSetProposalLogsStaleProposalAtDebug(t *testing.T) {
	cs, _ := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	proposal := &types.Proposal{Height: cs.rs.Height + 1, Round: cs.rs.Round}

	err := cs.defaultSetProposal(proposal)
	require.ErrorIs(t, err, errInvalidProposalHeightRound)
	requireLogged(t, logger, "ignoring proposal from different height or round", "debug")
}

func TestSetProposalLogsInvalidPOLRoundAtInfo(t *testing.T) {
	cs, _ := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	// POLRound must be -1 or in [0, proposal.Round); POLRound == Round is invalid.
	proposal := types.NewProposal(cs.rs.Height, cs.rs.Round, cs.rs.Round, types.BlockID{})

	err := cs.defaultSetProposal(proposal)
	require.ErrorIs(t, err, ErrInvalidProposalPOLRound)
	requireLogged(t, logger, "rejecting proposal with invalid POL round", "info")
}

func TestSetProposalLogsInvalidSignatureAtInfo(t *testing.T) {
	cs, _ := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)},
	}
	proposal := types.NewProposal(cs.rs.Height, cs.rs.Round, -1, blockID)
	proposal.Signature = cmtrand.Bytes(64)

	err := cs.defaultSetProposal(proposal)
	require.ErrorIs(t, err, ErrInvalidProposalSignature)
	requireLogged(t, logger, "rejecting proposal with invalid signature", "info")
}

func TestSetProposalLogsTooManyPartsAtInfo(t *testing.T) {
	cs, vss := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	// 2049 is the largest signable total (MaxBlockPartsCount) and exceeds the
	// limit derived from the test consensus params' Block.MaxBytes.
	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: types.PartSetHeader{Total: 2049, Hash: cmtrand.Bytes(32)},
	}
	proposal := types.NewProposal(cs.rs.Height, cs.rs.Round, -1, blockID)
	p := proposal.ToProto()
	require.NoError(t, vss[0].SignProposal(cs.state.ChainID, p))
	proposal.Signature = p.Signature

	err := cs.defaultSetProposal(proposal)
	require.ErrorIs(t, err, ErrProposalTooManyParts)
	requireLogged(t, logger, "rejecting proposal with too many block parts", "info")
}

func TestAddProposalBlockPartLogsInvalidPartAtInfo(t *testing.T) {
	cs, _ := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(
		types.PartSetHeader{Total: 2, Hash: cmtrand.Bytes(32)},
		types.BlockPartSizeBytes,
	)
	part := &types.Part{
		Index: 0,
		Bytes: cmtrand.Bytes(16),
		Proof: merkle.Proof{Total: 2, Index: 0, LeafHash: cmtrand.Bytes(32)},
	}
	msg := &BlockPartMessage{Height: cs.rs.Height, Round: cs.rs.Round, Part: part}

	added, err := cs.addProposalBlockPart(msg, "peer1")
	require.Error(t, err)
	require.False(t, added)
	requireLogged(t, logger, "failed to add proposal block part", "info")
}

func TestAddProposalBlockPartLogsWrongRoundAtTrace(t *testing.T) {
	cs, _ := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(
		types.PartSetHeader{Total: 2, Hash: cmtrand.Bytes(32)},
		types.BlockPartSizeBytes,
	)
	part := &types.Part{
		Index: 0,
		Bytes: cmtrand.Bytes(16),
		Proof: merkle.Proof{Total: 2, Index: 0, LeafHash: cmtrand.Bytes(32)},
	}
	// Same height but a different round: a failure to add is routine because
	// blocks may be reused across rounds.
	msg := &BlockPartMessage{Height: cs.rs.Height, Round: cs.rs.Round + 1, Part: part}

	added, err := cs.addProposalBlockPart(msg, "peer1")
	require.Error(t, err)
	require.False(t, added)
	requireLogged(t, logger, "received block part from wrong round", "trace")
}

func TestTryAddVoteLogsAddVoteFailureAtDebug(t *testing.T) {
	cs, vss := randState(2)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	vote := signVote(
		vss[1],
		cmtproto.PrecommitType,
		cmtrand.Bytes(32),
		types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)},
		false,
	)
	vote.Signature = cmtrand.Bytes(64)

	_, err := cs.tryAddVote(vote, "peer1")
	require.ErrorIs(t, err, ErrAddingVote)
	requireLogged(t, logger, "failed attempting to add vote", "debug")
}

func TestTryAddVoteLogsConflictingVotesAtInfo(t *testing.T) {
	cs, vss := randState(2)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	psh := types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)}
	vote1 := signVote(vss[1], cmtproto.PrevoteType, cmtrand.Bytes(32), psh, false)
	_, err := cs.tryAddVote(vote1, "peer1")
	require.NoError(t, err)

	// Same validator, same height/round/type, different block: equivocation.
	vote2 := signVote(vss[1], cmtproto.PrevoteType, cmtrand.Bytes(32), psh, false)
	_, err = cs.tryAddVote(vote2, "peer1")
	require.Error(t, err)
	requireLogged(t, logger, "found and sent conflicting votes to the evidence pool", "info")
}

func TestTryAddVoteLogsMissingPubKeyAtError(t *testing.T) {
	cs, vss := randState(2)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	psh := types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)}
	vote1 := signVote(vss[1], cmtproto.PrevoteType, cmtrand.Bytes(32), psh, false)
	_, err := cs.tryAddVote(vote1, "peer1")
	require.NoError(t, err)

	cs.privValidatorPubKey = nil
	vote2 := signVote(vss[1], cmtproto.PrevoteType, cmtrand.Bytes(32), psh, false)
	_, err = cs.tryAddVote(vote2, "peer1")
	require.ErrorIs(t, err, errPubKeyIsNotSet)
	requireLogged(t, logger, "failed to process conflicting vote; private validator public key is not set", "error")
}

// TestHandleMsgDoesNotLogFailedToProcessMessage asserts that handleMsg no
// longer emits the generic "failed to process message" log: failures are
// logged in place by the message handlers instead.
func TestHandleMsgDoesNotLogFailedToProcessMessage(t *testing.T) {
	cs, _ := randState(1)
	logger := &capturingLogger{}
	cs.SetLogger(logger)

	proposal := &types.Proposal{Height: cs.rs.Height + 1, Round: cs.rs.Round}
	cs.handleMsg(msgInfo{Msg: &ProposalMessage{Proposal: proposal}, PeerID: "peer1"})

	_, found := logger.levelOf("failed to process message")
	require.False(t, found, "handleMsg should not emit the generic catch-all log")
	requireLogged(t, logger, "ignoring proposal from different height or round", "debug")
}
