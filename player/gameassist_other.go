//go:build !linux

package main

// gameassist_other.go — the in-game half of the game assistant is a Linux
// handheld feature (gameassist_linux.go): it reads the pad beside the game
// through evdev and draws over a paused game, which a PC's own game windows
// rule out. Here only the first half works — the orb (F4) and Chat know the
// game after it — so the watcher and the paused-game note are no-ops.

import "os/exec"

// gameAssistSupported reports whether the assistant can come up over a game
// here: never.
func gameAssistSupported() bool { return false }

// gaPadSync would start the pad watcher: there is none here.
func gaPadSync(*Config, bool, bool) {}

// gaPadStop would stop the pad watcher.
func gaPadStop() {}

// gaPausedNote would write down the processes about to be paused.
func gaPausedNote([]*exec.Cmd) {}

// gaPausedClear would forget them.
func gaPausedClear() {}

// gaRecoverPaused would let go of a game a killed JukaHub left paused.
func gaRecoverPaused() {}

// gmInterruptGame asks a game to quit from the in-game menu: the exit path's
// request (the menu itself only opens on a Linux handheld).
func gmInterruptGame(cmd *exec.Cmd) { _ = screenChildSignal(cmd, screenSignalInterrupt) }
