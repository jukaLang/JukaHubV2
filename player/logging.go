package main

// logging.go — the app's one log.
//
// Everything JukaHub reports goes through log/slog into one handler
// (internal/logx.Handler). Each record becomes a compact line in the in-memory
// ring (appLog, internal/logx) that the Logs screen, the bug report and the
// diagnostics export read; records at warning level and above are also copied
// to stderr, which launch.sh appends to errors.txt on the card. Both copies
// have credentials removed first (redactLogSecrets, logredact.go). Debug lines on
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

	"jukahub/JukaHub/internal/logx"
)

var logger *slog.Logger

// logRingHandler is kept as the name the package's tests use; the handler
// itself lives in internal/logx.
type logRingHandler = logx.Handler

// newLogHandler wires the app's policy into the logx handler: redaction, the
// Windows warning file, and the runtime Debug logging switch.
func newLogHandler(level, stderr slog.Level) *logx.Handler {
	return &logx.Handler{
		Level:       level,
		StderrLevel: stderr,
		Ring:        appLog,
		Redact:      redactLogSecrets,
		WarnFile:    logFileAppend,
		ForceDebug:  logDebugForced.Load,
	}
}

// Package initialisers log before main installs the real level. Install a
// redacting handler at once so none of those early lines can miss redaction.
func init() {
	logger = slog.New(newLogHandler(slog.LevelInfo, slog.LevelWarn))
	logx.SetLogger(logger)
}

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
	level := logLevelFromEnv()
	stderrLevel := slog.LevelWarn
	if !IsTrimuiDevice() && level < stderrLevel {
		// A desktop run has a console a developer is looking at.
		stderrLevel = level
	}
	logger = slog.New(newLogHandler(level, stderrLevel))
	logx.SetLogger(logger)
	slog.SetDefault(logger)
	// Anything a dependency still prints through the log package is chatter.
	slog.SetLogLoggerLevel(slog.LevelDebug)
	logDetectedDeviceIdentity()
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
var (
	logDebug = logx.Debug
	logInfo  = logx.Info
	logWarn  = logx.Warn
	logError = logx.Error
)

// logFatal reports a startup failure the app cannot continue past and exits.
// The line goes to stderr even if logging is off, because it is the only
// evidence a black screen leaves behind.
func logFatal(sys, format string, args ...any) {
	msg := sys + ": " + fmt.Sprintf(format, args...)
	logError("", "%s", msg)
	if !Log().Enabled(context.Background(), slog.LevelError) {
		fmt.Fprintln(os.Stderr, redactLogSecrets(msg))
	}
	os.Exit(1)
}
