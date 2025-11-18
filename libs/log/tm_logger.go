package log

import (
	"io"

	kitlog "github.com/go-kit/log"
	kitlevel "github.com/go-kit/log/level"
	"github.com/go-kit/log/term"
)

const (
	msgKey    = "_msg" // "_" prefixed to avoid collisions
	moduleKey = "module"
	levelKey  = "level"
)

type tmLogger struct {
	srcLogger kitlog.Logger
}

// Interface assertions
var _ Logger = (*tmLogger)(nil)

// NewTMLogger returns a logger that encodes msg and keyvals to the Writer
// using go-kit's log as an underlying logger and our custom formatter. Note
// that underlying logger could be swapped with something else.
func NewTMLogger(w io.Writer) Logger {
	colorFn := func(keyvals ...interface{}) term.FgBgColor {
		if len(keyvals) < 2 {
			return term.FgBgColor{}
		}

		var levelStr string

		// Handle custom trace level
		if keyvals[0] == levelKey {
			// Safely handle this case
			if strVal, ok := keyvals[1].(string); ok {
				levelStr = strVal
			} else {
				return term.FgBgColor{}
			}
			// Handle standard go-kit levels
		} else if keyvals[0] == kitlevel.Key() {
			// Type-safe extraction for current go-kit/log (pointer and value types)
			if stringer, ok := keyvals[1].(interface{ String() string }); ok {
				levelStr = stringer.String()
			} else if strVal, ok := keyvals[1].(string); ok {
				levelStr = strVal
			} else {
				return term.FgBgColor{}
			}
		} else {
			// Never panic: just return default color for unknown key types
			return term.FgBgColor{}
		}

		switch levelStr {
		case "trace":
			return term.FgBgColor{Fg: term.DarkGray}
		case "debug":
			return term.FgBgColor{Fg: term.Gray}
		case "error":
			return term.FgBgColor{Fg: term.Red}
		default:
			return term.FgBgColor{}
		}
	}

	return &tmLogger{term.NewLogger(w, NewTMFmtLogger, colorFn)}
}

// NewTMLoggerWithColorFn allows you to provide your own color function. See
// NewTMLogger for documentation.
func NewTMLoggerWithColorFn(w io.Writer, colorFn func(keyvals ...interface{}) term.FgBgColor) Logger {
	return &tmLogger{term.NewLogger(w, NewTMFmtLogger, colorFn)}
}

// Trace logs a message at level Trace.
func (l *tmLogger) Trace(msg string, keyvals ...interface{}) {
	lWithLevel := kitlog.WithPrefix(l.srcLogger, levelKey, "trace")

	if err := kitlog.With(lWithLevel, msgKey, msg).Log(keyvals...); err != nil {
		errLogger := kitlevel.Error(l.srcLogger)
		kitlog.With(errLogger, msgKey, msg).Log("err", err) //nolint:errcheck // no need to check error again
	}
}

// Info logs a message at level Info.
func (l *tmLogger) Info(msg string, keyvals ...interface{}) {
	lWithLevel := kitlevel.Info(l.srcLogger)

	if err := kitlog.With(lWithLevel, msgKey, msg).Log(keyvals...); err != nil {
		errLogger := kitlevel.Error(l.srcLogger)
		kitlog.With(errLogger, msgKey, msg).Log("err", err) //nolint:errcheck // no need to check error again
	}
}

// Debug logs a message at level Debug.
func (l *tmLogger) Debug(msg string, keyvals ...interface{}) {
	lWithLevel := kitlevel.Debug(l.srcLogger)

	if err := kitlog.With(lWithLevel, msgKey, msg).Log(keyvals...); err != nil {
		errLogger := kitlevel.Error(l.srcLogger)
		kitlog.With(errLogger, msgKey, msg).Log("err", err) //nolint:errcheck // no need to check error again
	}
}

// Error logs a message at level Error.
func (l *tmLogger) Error(msg string, keyvals ...interface{}) {
	lWithLevel := kitlevel.Error(l.srcLogger)

	lWithMsg := kitlog.With(lWithLevel, msgKey, msg)
	if err := lWithMsg.Log(keyvals...); err != nil {
		lWithMsg.Log("err", err) //nolint:errcheck // no need to check error again
	}
}

// With returns a new contextual logger with keyvals prepended to those passed
// to calls to Trace, Info, Debug or Error.
func (l *tmLogger) With(keyvals ...interface{}) Logger {
	return &tmLogger{kitlog.With(l.srcLogger, keyvals...)}
}
