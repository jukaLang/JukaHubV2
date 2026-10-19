package main

// Safe mode
//
// A deliberately small escape hatch for a handheld that misbehaves after an
// update, a plugin install or a bad network setup. Safe mode never edits the
// user's config: it only suppresses background work for this launch.
//
// Any one of these turns it on:
//   * --safe-mode / -safe-mode on the command line
//   * JUKAHUB_SAFE_MODE=1 (also true/yes/on)
//   * a "safe-mode.flag" file next to the app (easy to create over USB/FTP)
//
// While active, plugins are not loaded, the updater and tool installer do not
// start, and background network work (weather, connectivity probes) is skipped.
// Scenes, navigation, local media and settings keep working.

import (
	"os"
	"strings"
)

// SafeFeature names a capability safe mode can suppress.
type SafeFeature int

const (
	SafeFeatureNetwork SafeFeature = iota
	SafeFeaturePlugins
	SafeFeatureUpdates
	SafeFeatureToolInstall
)

type safeModeState struct {
	Active bool
	Reason string
}

var safeMode safeModeState

// safeModeFlagPath is the marker a user can create without editing config.
const safeModeFlagPath = "safe-mode.flag"

// resolveSafeMode decides the mode from explicit inputs. It is pure so the
// precedence rules are testable without touching the environment.
func resolveSafeMode(args []string, getenv func(string) string, markerExists bool) safeModeState {
	for _, a := range args {
		if a == "--safe-mode" || a == "-safe-mode" {
			return safeModeState{Active: true, Reason: "command line"}
		}
	}
	switch strings.ToLower(strings.TrimSpace(getenv("JUKAHUB_SAFE_MODE"))) {
	case "1", "true", "yes", "on":
		return safeModeState{Active: true, Reason: "JUKAHUB_SAFE_MODE"}
	}
	if markerExists {
		return safeModeState{Active: true, Reason: safeModeFlagPath}
	}
	return safeModeState{}
}

// initSafeMode resolves and records the mode for this run.
func initSafeMode() {
	_, statErr := os.Stat(safeModeFlagPath)
	safeMode = resolveSafeMode(os.Args[1:], os.Getenv, statErr == nil)
	if safeMode.Active {
		Log().Warn("safe mode enabled: plugins, updates and network are disabled",
			"reason", safeMode.Reason)
	}
}

// safeModeActive reports whether this launch is running in safe mode.
func safeModeActive() bool { return safeMode.Active }

// safeModeAllows reports whether a capability may run this launch.
func safeModeAllows(f SafeFeature) bool {
	if !safeMode.Active {
		return true
	}
	switch f {
	case SafeFeatureNetwork, SafeFeaturePlugins, SafeFeatureUpdates, SafeFeatureToolInstall:
		return false
	}
	return true
}
