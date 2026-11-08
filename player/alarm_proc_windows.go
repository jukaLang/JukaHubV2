//go:build windows

package main

// alarm_proc_windows.go — the Windows half of an alarm ringing over a game.
//
// A desktop game has its own window, so nothing needs pausing: the alarm raises
// JukaHub's window over it instead (alarmTrigger). The music decoder for a
// ringtone runs without a console window, like the emulator installer's helpers.

import (
	"os/exec"
	"syscall"
)

// alarmCreateNoWindow is CREATE_NO_WINDOW: the decoder gets no console at all.
const alarmCreateNoWindow = 0x08000000

// alarmSignalChild has no portable pause on Windows; a game's window simply
// goes behind the alarm.
func alarmSignalChild(cmd *exec.Cmd, stop bool) error { return nil }

// alarmCanPauseChildren reports whether a running game can be paused under the
// alarm on this platform.
func alarmCanPauseChildren() bool { return false }

// alarmHideConsole runs cmd without a console window.
func alarmHideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= alarmCreateNoWindow
}
