package consensus

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
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
