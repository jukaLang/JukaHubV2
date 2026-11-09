//go:build windows

package main

// terminal_windows.go — the Terminal's shell on Windows: cmd.exe, started
// without a console window, with output in UTF-8 and stopped as a whole tree.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// termCreateNoWindow is CREATE_NO_WINDOW: a helper the terminal starts on its
// own (taskkill) must not flash a console over the UI.
const termCreateNoWindow = 0x08000000

// termShellCommand runs line through cmd.exe. The command line is passed raw
// (SysProcAttr.CmdLine): Go's argument quoting follows the C runtime's rules,
// which cmd.exe does not use, so `echo "a b" | find "a"` would arrive mangled.
// /s keeps the quotes inside line intact, /d skips AutoRun scripts, and
// `chcp 65001` switches the hidden console to UTF-8 so names with accents come
// through instead of OEM code-page bytes. The window is hidden, not absent, so
// that console exists for chcp to change.
//
// Trust boundary (SEC-12): line is what the user typed at the Terminal prompt
// (or picked from their own history), spliced into the command line on
// purpose — cmd.exe syntax is what the user is typing. No other code path
// builds a line for it; batch files the app launches use winBatchCommand.
func termShellCommand(ctx context.Context, line string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CmdLine:    `"` + comspec + `" /d /s /c "chcp 65001>nul 2>&1 & ` + line + `"`,
	}
	return cmd
}

// termKillTree stops cmd.exe and everything it started: killing only the shell
// leaves a `ping -t` it launched running on, invisible. taskkill /T walks the
// tree; the direct Kill is the fallback when taskkill is unavailable.
func termKillTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: termCreateNoWindow}
	_ = kill.Run()
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
