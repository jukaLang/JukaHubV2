package main

import (
	"log"
	"log/slog"
	"os"
	"runtime"
	"strings"
)

var logger *slog.Logger

// logLevelFromEnv reads the JUKAHUB_LOG_LEVEL env var (case-insensitive).
// Valid values: debug, info, warn, error. Defaults to "info" on TSP, "debug" elsewhere.
func logLevelFromEnv() slog.Level {
	if env := os.Getenv("JUKAHUB_LOG_LEVEL"); env != "" {
		switch strings.ToLower(env) {
		case "debug":
			return slog.LevelDebug
		case "info":
			return slog.LevelInfo
		case "warn":
			return slog.LevelWarn
		case "error":
			return slog.LevelError
		case "none", "off":
			return slog.LevelCrit
		}
	}
	// Default: debug on desktop, info on TSP
	if IsTSP() {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// InitLogging configures the structured logger.
// On TSP it writes compact text; on Windows it writes JSON for tooling.
// Log level can be controlled via JUKAHUB_LOG_LEVEL env var.
func InitLogging() {
	var handler slog.Handler
	
	// Choose format based on platform
	if IsTSP() {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevelFromEnv(),
		})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevelFromEnv(),
		})
	}
	logger = slog.New(handler)
	slog.SetDefault(logger)
	
	// Log the configured level
	var levelName string
	switch logLevelFromEnv() {
	case slog.LevelDebug:
		levelName = "debug"
	case slog.LevelInfo:
		levelName = "info"
	case slog.LevelWarn:
		levelName = "warn"
	case slog.LevelError:
		levelName = "error"
	default:
		levelName = "crit"
	}
	log.Printf("[LOG] Initializing logger: level=%s format=%s", levelName, map[bool]string{true: "JSON", false: "text"}[!IsTSP()])
}

// Log returns the structured logger.
func Log() *slog.Logger {
	if logger == nil {
		InitLogging()
	}
	return logger
}

// LogScene returns a logger with scene name attached.
func LogScene(scene string) *slog.Logger {
	return Log().With(
		"subsystem", "scene",
		"scene", scene,
		"platform", P().Name(),
		"goos", runtime.GOOS,
		"arch", runtime.GOARCH,
	)
}

// LogOp returns a logger with operation name attached.
func LogOp(op string) *slog.Logger {
	return Log().With("op", op)
}

// LogSceneOp returns a logger with both scene and operation attached.
func LogSceneOp(scene, op string) *slog.Logger {
	return LogScene(scene).With("op", op)
}

// Ensure backward compatibility with existing log.Printf calls.
func init() {
	log.SetPrefix("[legacy] ")
	log.SetFlags(log.Ltime | log.Lshortfile)
}
