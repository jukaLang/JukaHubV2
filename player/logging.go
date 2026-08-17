package main

import (
	"log"
	"log/slog"
	"os"
	"runtime"
)

var logger *slog.Logger

// InitLogging configures the structured logger.
// On TSP it writes compact text; on Windows it writes JSON for tooling.
func InitLogging() {
	var handler slog.Handler
	if IsTSP() {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	}
	logger = slog.New(handler)
	slog.SetDefault(logger)
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
