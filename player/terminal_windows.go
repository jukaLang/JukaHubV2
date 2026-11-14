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
	"strings"
	"syscall"
	"time"
)

// termCreateNoWindow is CREATE_NO_WINDOW: a helper the terminal starts on its
// own (taskkill) must not flash a console over the UI.
const termCreateNoWindow = 0x08000000

// termShellCommand feeds line to cmd.exe on stdin. The command line is fixed
// (/d /q /s), so the user's text is never reparsed as an argv command; the
// hidden console still runs `chcp 65001` first so names with accents come
// through instead of OEM code-page bytes. The window is hidden, not absent, so
// that console exists for chcp to change.
//
// Trust boundary (SEC-12): line is what the user typed at the Terminal prompt
// (or picked from their own history), and it is executed as cmd.exe syntax on
// purpose. No other code path builds a line for it; batch files the app
// launches use winBatchCommand.
func termShellCommand(ctx context.Context, line string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CmdLine:    `"` + comspec + `" /d /q /s`,
	}
	cmd.Stdin = strings.NewReader("chcp 65001>nul 2>&1\r\n" + line + "\r\n")
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
