package propagation

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

// captureLogger records every log call so tests can assert log levels.
type captureLogger struct {
	mtx     sync.Mutex
	entries []capturedEntry
}

type capturedEntry struct {
	level string
	msg   string
}

var _ log.Logger = (*captureLogger)(nil)

func (l *captureLogger) record(level, msg string) {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	l.entries = append(l.entries, capturedEntry{level: level, msg: msg})
}

func (l *captureLogger) Trace(msg string, _ ...any) { l.record("trace", msg) }
func (l *captureLogger) Debug(msg string, _ ...any) { l.record("debug", msg) }
func (l *captureLogger) Info(msg string, _ ...any)  { l.record("info", msg) }
func (l *captureLogger) Error(msg string, _ ...any) { l.record("error", msg) }
func (l *captureLogger) With(_ ...any) log.Logger   { return l }

// levelOf returns the level of the first recorded entry with the given message.
func (l *captureLogger) levelOf(msg string) (string, bool) {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	for _, e := range l.entries {
		if e.msg == msg {
			return e.level, true
		}
	}
	return "", false
}

// TestUnknownProposalWantLogsAtDebug asserts that a Want for a proposal this
// node does not have is logged at Debug, not Error: it is routine when the
// requester is ahead of us or the compact block has not arrived yet.
func TestUnknownProposalWantLogsAtDebug(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	reactor := reactors[0]
	logger := &captureLogger{}
	reactor.SetLogger(logger)

	wantParts := bits.NewBitArray(4)
	wantParts.SetIndex(0, true)
	reactor.handleWants(reactors[1].self, &proptypes.WantParts{
		Height: reactor.ProposalCache.height + 3,
		Round:  0,
		Parts:  wantParts,
		Prove:  true,
	})

	level, found := logger.levelOf("received part state request for unknown proposal")
	require.True(t, found, "expected the unknown-proposal Want log, got %+v", logger.entries)
	require.Equal(t, "debug", level)
}
