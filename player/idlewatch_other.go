//go:build !linux

package main

// idlewatch_other.go — idle in a game is a Linux handheld feature: a desktop
// has no input devices to read beside the game, no backlight JukaHub moves
// and no /bin/sh hooks (idlewatch.go, idlewatch_linux.go).

// idleWatchRun has nothing to watch here.
func idleWatchRun(*idleWatchSession) {}

// idleWatchDim and idleWatchUndim have no backlight to move here.
func idleWatchDim() {}

func idleWatchUndim(string) {}

// idleHookRunScripts runs no scripts here.
func idleHookRunScripts(state, where, source string) {}
