// Package logx owns JukaHub's in-memory log ring and the slog handler that
// writes into it. The main package supplies policy (the level, credential
// redaction and the warning file) so this package stays free of app state.
package logx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Ring is a bounded, concurrency-safe ring of log lines. It keeps at most max
// lines and maxBytes bytes, dropping the oldest first; version changes on every
// write and clear, and appended counts every line ever added (a clear does not
// reset it) for LinesSince.
type Ring struct {
	mu       sync.Mutex
	lines    []string
	bytes    int
	max      int
	maxBytes int
	version  uint64
	appended uint64
}

// NewRing makes a ring with the given caps (0 disables the line cap).
func NewRing(max, maxBytes int) *Ring {
	return &Ring{max: max, maxBytes: maxBytes}
}

// DefaultRing is the app's ring unless a caller installs another one. A
// Handler with no Ring uses it.
var DefaultRing = NewRing(2000, 512<<10)

// Write appends one line per newline-separated chunk, so the ring can stand in
// for an io.Writer (an exec's combined output, a test).
func (b *Ring) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		b.Append(line)
	}
	return len(p), nil
}

// Append adds one line, dropping the oldest lines past the line and byte caps.
func (b *Ring) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.version++
	b.appended++
	b.lines = append(b.lines, line)
	b.bytes += len(line) + 1
	if b.max <= 0 {
		b.lines = nil
		b.bytes = 0
		return
	}
	if len(b.lines) > b.max {
		drop := len(b.lines) - b.max
		for i := 0; i < drop; i++ {
			b.bytes -= len(b.lines[i]) + 1
			b.lines[i] = ""
		}
		b.lines = b.lines[drop:]
	}
	for b.maxBytes > 0 && b.bytes > b.maxBytes && len(b.lines) > 1 {
		b.bytes -= len(b.lines[0]) + 1
		b.lines[0] = ""
		b.lines = b.lines[1:]
	}
	if b.maxBytes > 0 && len(b.lines) == 1 && len(b.lines[0]) > b.maxBytes {
		b.lines[0] = b.lines[0][len(b.lines[0])-b.maxBytes:]
		b.bytes = len(b.lines[0]) + 1
	}
}

// String joins the buffered lines with newlines, oldest first.
func (b *Ring) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

// Clear removes every line; appended keeps counting.
func (b *Ring) Clear() {
	b.mu.Lock()
	b.lines = nil
	b.bytes = 0
	b.version++
	b.mu.Unlock()
}

// Version changes whenever the ring does.
func (b *Ring) Version() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

// Mark is the number of lines appended so far, for LinesSince.
func (b *Ring) Mark() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.appended
}

// LinesSince copies the lines appended after mark that the ring still holds,
// oldest first; at most max of them (the newest), when max > 0.
func (b *Ring) LinesSince(mark uint64, max int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if mark >= b.appended {
		return nil
	}
	n := len(b.lines)
	if since := b.appended - mark; since < uint64(n) {
		n = int(since)
	}
	if max > 0 && n > max {
		n = max
	}
	out := make([]string, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// Snapshot copies the buffered lines, oldest first, with the version they were
// read at.
func (b *Ring) Snapshot() ([]string, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out, b.version
}

// Handler renders a record as "15:04:05 WARN  message key=value" into Ring
// and, at or above StderrLevel, onto stderr. Redact is applied before either
// destination; WarnFile receives every line at warning level or above.
type Handler struct {
	Level       slog.Level
	StderrLevel slog.Level
	Ring        *Ring
	Redact      func(string) string
	WarnFile    func(string)
	ForceDebug  func() bool
	attrs       string
	group       string
}

func (h *Handler) ring() *Ring {
	if h.Ring != nil {
		return h.Ring
	}
	return DefaultRing
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.Level || (level >= slog.LevelDebug && h.ForceDebug != nil && h.ForceDebug())
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.Grow(24 + len(r.Message) + len(h.attrs))
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	b.WriteString(t.Format("15:04:05"))
	b.WriteByte(' ')
	b.WriteString(LevelTag(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		AppendAttr(&b, h.group, a)
		return true
	})
	line := b.String()
	if h.Redact != nil {
		line = h.Redact(line)
	}
	h.ring().Append(line)
	if r.Level >= h.StderrLevel {
		_, _ = os.Stderr.WriteString(line + "\n")
	}
	if r.Level >= slog.LevelWarn && h.WarnFile != nil {
		h.WarnFile(line)
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		AppendAttr(&b, h.group, a)
	}
	c := *h
	c.attrs = b.String()
	return &c
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.group += name + "."
	return &c
}

// LevelTag is the fixed-width level column the Logs screen reads back
// (sysToolParseLogLine).
func LevelTag(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN "
	case level >= slog.LevelInfo:
		return "INFO "
	}
	return "DEBUG"
}

// AppendAttr writes " key=value", quoting a value with spaces so a line stays
// one parsable row.
func AppendAttr(b *strings.Builder, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		prefix := group
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			AppendAttr(b, prefix, ga)
		}
		return
	}
	b.WriteByte(' ')
	b.WriteString(group)
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \t\n\"=") {
		fmt.Fprintf(b, "%q", v)
		return
	}
	b.WriteString(v)
}

var defaultLogger atomic.Pointer[slog.Logger]

// SetLogger installs the slog logger the printf helpers use.
func SetLogger(l *slog.Logger) { defaultLogger.Store(l) }

// Logger returns the installed logger, setting up a default-ring handler when
// none was installed yet (package initialisers and tests log before main).
func Logger() *slog.Logger {
	if l := defaultLogger.Load(); l != nil {
		return l
	}
	l := slog.New(&Handler{Level: slog.LevelInfo, StderrLevel: slog.LevelWarn})
	if defaultLogger.CompareAndSwap(nil, l) {
		return l
	}
	return defaultLogger.Load()
}

func printf(level slog.Level, sys, format string, args ...any) {
	l := Logger()
	if !l.Enabled(context.Background(), level) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if sys != "" {
		msg = sys + ": " + msg
	}
	_ = l.Handler().Handle(context.Background(), slog.NewRecord(time.Now(), level, msg, 0))
}

// Debug, Info, Warn and Error are the printf-style entry points; sys names the
// subsystem and leads the message, and the format runs only when enabled.
func Debug(sys, format string, args ...any) { printf(slog.LevelDebug, sys, format, args...) }
func Info(sys, format string, args ...any)  { printf(slog.LevelInfo, sys, format, args...) }
func Warn(sys, format string, args ...any)  { printf(slog.LevelWarn, sys, format, args...) }
func Error(sys, format string, args ...any) { printf(slog.LevelError, sys, format, args...) }
