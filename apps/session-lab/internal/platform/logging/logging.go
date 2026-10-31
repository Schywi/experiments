// Package logging is the runner's logger: one line per event, key=value fields,
// a level, and a phase. It exists because the alternative — ad-hoc Printf — made a
// two-boolean return value look fine until a real pod turned it into 30 false
// failures with no way to tell what the runner thought was happening.
package logging

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is how loud a line is.
type Level int

const (
	// LevelDebug is for per-session detail, off unless asked for.
	LevelDebug Level = iota
	// LevelInfo is run progress.
	LevelInfo
	// LevelWarn is something unexpected that the run survived.
	LevelWarn
	// LevelError is a failure that changes an outcome.
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// ParseLevel reads a --log-level value.
func ParseLevel(value string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return LevelDebug, nil
	case "", "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	}
	return LevelInfo, fmt.Errorf("unknown log level %q: want debug, info, warn or error", value)
}

// Field is one key/value pair.
type Field struct {
	Key   string
	Value any
}

// F builds a field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// Logger writes one line per event.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	level Level
	clock func() time.Time
}

// New builds a logger at the given level.
func New(out io.Writer, level Level) *Logger {
	if out == nil {
		out = os.Stderr
	}
	return &Logger{out: out, level: level, clock: time.Now}
}

// Discard is a logger that writes nothing, for tests.
func Discard() *Logger { return New(io.Discard, LevelError+1) }

// Enabled reports whether a level is loud enough to write.
func (l *Logger) Enabled(level Level) bool { return level >= l.level }

// Debug writes a per-session line.
func (l *Logger) Debug(msg string, fields ...Field) { l.log(LevelDebug, msg, fields...) }

// Info writes a progress line.
func (l *Logger) Info(msg string, fields ...Field) { l.log(LevelInfo, msg, fields...) }

// Warn writes a survivable problem.
func (l *Logger) Warn(msg string, fields ...Field) { l.log(LevelWarn, msg, fields...) }

// Error writes a failure.
func (l *Logger) Error(msg string, fields ...Field) { l.log(LevelError, msg, fields...) }

func (l *Logger) log(level Level, msg string, fields ...Field) {
	if !l.Enabled(level) {
		return
	}
	var builder strings.Builder
	builder.WriteString(l.clock().UTC().Format("15:04:05.000000"))
	builder.WriteString(" level=")
	builder.WriteString(level.String())
	builder.WriteString(" msg=")
	builder.WriteString(quote(msg))
	for _, field := range sorted(fields) {
		builder.WriteByte(' ')
		builder.WriteString(field.Key)
		builder.WriteByte('=')
		builder.WriteString(quote(fmt.Sprint(field.Value)))
	}
	builder.WriteByte('\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	io.WriteString(l.out, builder.String())
}

// quote keeps a value greppable: no spaces, no accidental line breaks.
func quote(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\n\"=") {
		return value
	}
	return fmt.Sprintf("%q", value)
}

// sorted makes output deterministic, so two runs diff cleanly.
func sorted(fields []Field) []Field {
	out := make([]Field, len(fields))
	copy(out, fields)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
