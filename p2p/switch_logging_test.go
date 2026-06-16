package p2p

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
)

// logEntry records a single emitted log line's level and message.
type logEntry struct {
	level string
	msg   string
}

// capturingLogger is a minimal log.Logger that records the level and message
// of each log call so tests can assert on the level used.
type capturingLogger struct {
	entries []logEntry
}

func (l *capturingLogger) Trace(msg string, _ ...interface{}) {
	l.entries = append(l.entries, logEntry{level: "trace", msg: msg})
}

func (l *capturingLogger) Debug(msg string, _ ...interface{}) {
	l.entries = append(l.entries, logEntry{level: "debug", msg: msg})
}

func (l *capturingLogger) Info(msg string, _ ...interface{}) {
	l.entries = append(l.entries, logEntry{level: "info", msg: msg})
}

func (l *capturingLogger) Error(msg string, _ ...interface{}) {
	l.entries = append(l.entries, logEntry{level: "error", msg: msg})
}

func (l *capturingLogger) With(_ ...interface{}) log.Logger { return l }

// levelFor returns the level recorded for the first entry whose message equals
// msg, or "" if no such entry was recorded.
func (l *capturingLogger) levelFor(msg string) string {
	for _, e := range l.entries {
		if e.msg == msg {
			return e.level
		}
	}
	return ""
}

// TestAsError verifies that asError coerces StopPeerForError reasons into an
// error so they can be inspected with errors.Is.
func TestAsError(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		require.NoError(t, asError(nil))
	})
	t.Run("error EOF", func(t *testing.T) {
		require.True(t, errors.Is(asError(io.EOF), io.EOF))
	})
	t.Run("non-EOF error", func(t *testing.T) {
		require.False(t, errors.Is(asError(io.ErrUnexpectedEOF), io.EOF))
		require.False(t, errors.Is(asError(errors.New("boom")), io.EOF))
	})
	t.Run("string EOF", func(t *testing.T) {
		require.True(t, errors.Is(asError("EOF"), io.EOF))
	})
	t.Run("other string", func(t *testing.T) {
		err := asError("some reason")
		require.Error(t, err)
		require.False(t, errors.Is(err, io.EOF))
	})
	t.Run("non-string stringifies to EOF", func(t *testing.T) {
		require.True(t, errors.Is(asError(stringerEOF{}), io.EOF))
	})
	t.Run("other non-string", func(t *testing.T) {
		err := asError(42)
		require.Error(t, err)
		require.False(t, errors.Is(err, io.EOF))
	})
}

type stringerEOF struct{}

func (stringerEOF) String() string { return "EOF" }

// TestShouldLogStopError verifies that the idempotent-stop sentinel is not
// logged while genuine errors are.
func TestShouldLogStopError(t *testing.T) {
	assert.False(t, shouldLogStopError(nil))
	assert.False(t, shouldLogStopError(service.ErrAlreadyStopped))
	assert.True(t, shouldLogStopError(errors.New("real error")))
}

// TestStopPeerForErrorLogLevel exercises the StopPeerForError seam end-to-end
// with a real running peer and asserts the log level chosen for the
// "Stopping peer for error" message: EOF reasons log at Info, others at Error.
func TestStopPeerForErrorLogLevel(t *testing.T) {
	const msg = "Stopping peer for error"

	tests := []struct {
		name      string
		reason    interface{}
		wantLevel string
	}{
		{name: "EOF reason logs at info", reason: io.EOF, wantLevel: "info"},
		{name: "non-EOF reason logs at error", reason: errors.New("some err"), wantLevel: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw1, sw2 := MakeSwitchPair(initSwitchFunc)
			t.Cleanup(func() {
				_ = sw1.Stop()
				_ = sw2.Stop()
			})

			require.Len(t, sw1.Peers().List(), 1)
			p := sw1.Peers().List()[0]
			require.True(t, p.IsRunning())

			capture := &capturingLogger{}
			sw1.SetLogger(capture)

			sw1.StopPeerForError(p, tt.reason, "test")

			require.Equal(t, tt.wantLevel, capture.levelFor(msg))
		})
	}
}
