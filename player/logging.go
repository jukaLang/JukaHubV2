package main

// logging.go — the app's one log.
//
// Everything JukaHub reports goes through log/slog into one handler
// (logRingHandler). Each record becomes a compact line in the in-memory ring
// (appLog, hardware.go) that the Logs screen, the bug report and the
// diagnostics export read; records at warning level and above are also copied
// to stderr, which launch.sh appends to errors.txt on the card. Debug lines on
// a handheld cost one level compare: the printf helpers below do not format a
// message nobody will see, and the card is never written for routine chatter.
//
// The level is JUKAHUB_LOG_LEVEL (debug, info, warn, error, off); JUKAHUB_DEBUG=1
// is shorthand for debug. The default is info on a handheld and debug on a
// desktop, where stderr also receives everything at the chosen level.
//
// The standard library's log package is not used by the app. slog.SetDefault
// routes it into this handler, so a dependency that still prints through it
// lands in the ring at debug level instead of on the card.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

var logger *slog.Logger

// logLevelFromEnv reads JUKAHUB_LOG_LEVEL (case-insensitive) and
// JUKAHUB_DEBUG. Valid values: debug, info, warn, error, off.
func logLevelFromEnv() slog.Level {
	if env := os.Getenv("JUKAHUB_LOG_LEVEL"); env != "" {
		switch strings.ToLower(strings.TrimSpace(env)) {
		case "debug":
			return slog.LevelDebug
		case "info":
			return slog.LevelInfo
		case "warn", "warning":
			return slog.LevelWarn
		case "error":
			return slog.LevelError
		case "none", "off":
			// A level above Error silences everything.
			return slog.Level(12)
		}
	}
	if os.Getenv("JUKAHUB_DEBUG") != "" {
		return slog.LevelDebug
	}
	if IsTrimuiDevice() {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// InitLogging installs the ring handler as the default logger. It is called
// once, first thing in main; Log() calls it lazily for code that runs earlier
// (package initialisers, tests, the secret CLI).
func InitLogging() {
	if logger == nil {
		// A bootstrap logger first. Choosing the real level asks which device
		// this is, and device detection logs what it found from inside its
		// sync.Once: without a logger already in place that line would call
		// back into InitLogging, re-enter the Once on the same goroutine and
		// hang the boot on every Linux device.
		logger = slog.New(&logRingHandler{level: slog.LevelInfo, stderr: slog.LevelWarn})
	}
	level := logLevelFromEnv()
	stderrLevel := slog.LevelWarn
	if !IsTrimuiDevice() && level < stderrLevel {
		// A desktop run has a console a developer is looking at.
		stderrLevel = level
	}
	logger = slog.New(&logRingHandler{level: level, stderr: stderrLevel})
	slog.SetDefault(logger)
	// Anything a dependency still prints through the log package is chatter.
	slog.SetLogLoggerLevel(slog.LevelDebug)
}

// Log returns the structured logger.
func Log() *slog.Logger {
	if logger == nil {
		InitLogging()
	}
	return logger
}

// LogScene returns a logger with the scene attached.
func LogScene(scene string) *slog.Logger {
	return Log().With("scene", scene)
}

// LogSceneOp returns a logger with both scene and operation attached.
func LogSceneOp(scene, op string) *slog.Logger {
	return LogScene(scene).With("op", op)
}

// logDebug, logInfo, logWarn and logError are the printf-style entry points;
// sys names the subsystem ("apps", "wifi", ...) and leads the message. The
// message is only formatted when the level is enabled.
func logDebug(sys, format string, args ...any) { logPrintf(slog.LevelDebug, sys, format, args...) }
func logInfo(sys, format string, args ...any)  { logPrintf(slog.LevelInfo, sys, format, args...) }
func logWarn(sys, format string, args ...any)  { logPrintf(slog.LevelWarn, sys, format, args...) }
func logError(sys, format string, args ...any) { logPrintf(slog.LevelError, sys, format, args...) }

// logFatal reports a startup failure the app cannot continue past and exits.
// The line goes to stderr even if logging is off, because it is the only
// evidence a black screen leaves behind.
func logFatal(sys, format string, args ...any) {
	msg := sys + ": " + fmt.Sprintf(format, args...)
	logPrintf(slog.LevelError, "", "%s", msg)
	if !Log().Enabled(context.Background(), slog.LevelError) {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(1)
}

func logPrintf(level slog.Level, sys, format string, args ...any) {
	l := Log()
	if !l.Enabled(context.Background(), level) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if sys != "" {
		msg = sys + ": " + msg
	}
	_ = l.Handler().Handle(context.Background(), slog.NewRecord(time.Now(), level, msg, 0))
}

// logRingHandler renders a record as "15:04:05 WARN  message key=value" into
// appLog and, at or above the stderr level, onto stderr.
type logRingHandler struct {
	level  slog.Level
	stderr slog.Level
	attrs  string // pre-rendered " key=value" pairs from WithAttrs
	group  string // dotted group prefix from WithGroup
}

func (h *logRingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *logRingHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.Grow(24 + len(r.Message) + len(h.attrs))
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	b.WriteString(t.Format("15:04:05"))
	b.WriteByte(' ')
	b.WriteString(logLevelTag(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		appendLogAttr(&b, h.group, a)
		return true
	})
	line := b.String()
	appLog.Append(line)
	if r.Level >= h.stderr {
		_, _ = os.Stderr.WriteString(line + "\n")
	}
	return nil
}

func (h *logRingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		appendLogAttr(&b, h.group, a)
	}
	c := *h
	c.attrs = b.String()
	return &c
}

func (h *logRingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.group = c.group + name + "."
	return &c
}

// logLevelTag is the fixed-width level column the Logs screen reads back
// (sysToolParseLogLine).
func logLevelTag(level slog.Level) string {
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

// appendLogAttr writes " key=value", quoting a value with spaces so a line
// stays one parsable row.
func appendLogAttr(b *strings.Builder, group string, a slog.Attr) {
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
			appendLogAttr(b, prefix, ga)
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
