package main

// childenv.go — the environment a game, emulator or app starts with.
//
// launch.sh sets JukaHub up for itself: its own folder first on the library
// path (the libraries it carries, then /usr/trimui/lib, /usr/lib and /lib, all
// ahead of the firmware's own list), its folder and required/ first on PATH
// (its ffmpeg and tools), and HOME pointed at its folder. Every child used to
// inherit all of that. The stock and CrossMix launch scripts set HOME for
// RetroArch but leave the library path and PATH alone, so RetroArch started
// from JukaHub ran against different libraries than the same game started from
// the firmware's menu: CrossMix puts its own /mnt/SDCARD/System/lib first in
// the path MainUI hands a game, and under JukaHub the stock /usr/lib copies
// came first instead, while the helpers a launcher calls resolved to JukaHub's.
//
// launch.sh now records the originals before it changes anything:
// JUKAHUB_ORIG_ENV lists the names, and JUKAHUB_ORIG_<NAME> carries a value
// only when that variable was set, so a missing record means "was unset".
// screenChildEnv puts them back for every screen-taking child
// (screenChildGroupConfigure). An older launch.sh leaves no record; then only
// the entries that point into JukaHub's own folder are taken out of PATH and
// LD_LIBRARY_PATH, and HOME is left alone — the launchers that care set their
// own.
//
// TZ is deliberately inherited: the zone chosen in Settings → Date & time is
// meant for every emulator JukaHub starts (launch.sh exports it for that).

import (
	"path/filepath"
	"strings"
	"time"
)

const (
	// childEnvOrigList names the variables launch.sh recorded.
	childEnvOrigList = "JUKAHUB_ORIG_ENV"
	// childEnvOrigPrefix starts each recorded value: JUKAHUB_ORIG_HOME, ...
	childEnvOrigPrefix = "JUKAHUB_ORIG_"
	// childEnvListSep separates PATH and LD_LIBRARY_PATH entries. The lists
	// this file edits are POSIX ones, whatever host runs the tests.
	childEnvListSep = ":"
)

// childEnvMarkers are JukaHub's own launcher markers. They describe launch.sh
// to JukaHub; a child that saw them could take itself for a JukaHub that
// launch.sh started and wait for a restart protocol nobody runs.
var childEnvMarkers = []string{"JUKAHUB_LAUNCHED_BY", "JUKAHUB_SCREEN_HANDOFF"}

// childEnvAppDir is the folder whose PATH and library entries the fallback
// removes; a variable so the tests can name one.
var childEnvAppDir = exeDir

// screenChildEnv is the environment for a screen-taking child: environ (the
// app's own, os.Environ()) with the firmware's HOME, PATH and LD_LIBRARY_PATH
// put back and JukaHub's markers removed. See the file comment.
func screenChildEnv(environ []string) []string {
	return childEnvWithTZ(screenChildEnvFor(environ, childEnvAppDir()), gameChildZone())
}

// gameChildZone is the zone a game child should run in: the one JukaHub uses
// now. A zone picked in Settings updates time.Local, so the child gets it
// without waiting for JukaHub to restart (NET-14). "Local" is the system's
// zone, which a child already inherits, so it is not written as TZ.
func gameChildZone() string {
	zone := dateTimeTZName(time.Local.String())
	if zone == "Local" {
		return ""
	}
	return zone
}

// childEnvWithTZ returns environ with every TZ replaced by zone. An empty or
// "Local" zone removes TZ, leaving the child on the system's zone; no other
// variable is touched.
func childEnvWithTZ(environ []string, zone string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if key == "TZ" {
			continue
		}
		out = append(out, kv)
	}
	if zone != "" && zone != "Local" {
		out = append(out, "TZ="+zone)
	}
	return out
}

// screenChildEnvFor is screenChildEnv with the app folder given, so the rule
// is testable without the running executable.
func screenChildEnvFor(environ []string, appDir string) []string {
	if list, recorded := childEnvLookup(environ, childEnvOrigList); recorded {
		return childEnvRestore(environ, list)
	}
	return childEnvStripApp(environ, appDir)
}

// childEnvLookup returns the value a child would see for name: the last
// occurrence wins, the rule os/exec applies to a duplicated key.
func childEnvLookup(environ []string, name string) (string, bool) {
	value, found := "", false
	for _, kv := range environ {
		if key, v, ok := strings.Cut(kv, "="); ok && key == name {
			value, found = v, true
		}
	}
	return value, found
}

// childEnvMarker reports whether name is one of JukaHub's launcher variables,
// which no child is given.
func childEnvMarker(name string) bool {
	if strings.HasPrefix(name, childEnvOrigPrefix) {
		return true
	}
	for _, marker := range childEnvMarkers {
		if name == marker {
			return true
		}
	}
	return false
}

// childEnvRestorable reports whether a name from JUKAHUB_ORIG_ENV may be put
// back. Only plain variable names count. TZ is JukaHub's choice for every
// child, not something to undo; ENV's record would be JUKAHUB_ORIG_ENV, the
// list itself; and JukaHub's own markers are never restored.
func childEnvRestorable(name string) bool {
	if name == "" || name == "TZ" || name == "ENV" || strings.HasPrefix(name, "JUKAHUB_") {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// childEnvRestore rebuilds environ from launch.sh's record: every listed
// variable is removed, then given back its recorded value when it had one, so
// a variable that was unset before launch.sh ran is unset in the child too.
// Each restored variable appears exactly once, whatever duplicates environ
// held. Everything else keeps its place.
func childEnvRestore(environ []string, list string) []string {
	var names []string
	managed := map[string]bool{}
	for _, name := range strings.Fields(list) {
		if !childEnvRestorable(name) || managed[name] {
			continue
		}
		managed[name] = true
		names = append(names, name)
	}
	recorded := map[string]string{}
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if name, isRecord := strings.CutPrefix(key, childEnvOrigPrefix); isRecord && managed[name] {
			recorded[name] = value
		}
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if managed[key] || childEnvMarker(key) {
			continue
		}
		out = append(out, kv)
	}
	for _, name := range names {
		if value, ok := recorded[name]; ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

// childEnvStripApp is the fallback for a launch.sh that records nothing: the
// app folder, its libs/ and its required/ leave PATH and LD_LIBRARY_PATH
// (every occurrence), as do the empty entries a "$A:$UNSET" join leaves
// behind, which the dynamic loader and the shell both read as "the current
// folder". A list left with nothing is unset. HOME stays as it is.
func childEnvStripApp(environ []string, appDir string) []string {
	drop := map[string]bool{}
	if appDir = strings.TrimSpace(appDir); appDir != "" && appDir != "." {
		for _, dir := range []string{appDir, filepath.Join(appDir, "libs"), filepath.Join(appDir, "required")} {
			drop[filepath.Clean(dir)] = true
		}
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if childEnvMarker(key) {
			continue
		}
		if ok && (key == "PATH" || key == "LD_LIBRARY_PATH") {
			kept := childEnvFilterList(value, drop)
			if kept == "" {
				continue
			}
			out = append(out, key+"="+kept)
			continue
		}
		out = append(out, kv)
	}
	return out
}

// childEnvFilterList removes the dropped folders and the empty entries from a
// colon-separated list, keeping the order of the rest.
func childEnvFilterList(list string, drop map[string]bool) string {
	parts := strings.Split(list, childEnvListSep)
	kept := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || drop[filepath.Clean(part)] {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, childEnvListSep)
}
