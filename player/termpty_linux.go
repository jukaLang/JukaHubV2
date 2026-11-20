//go:build linux

package main

// termpty_linux.go — a program on a pseudo-terminal: /dev/ptmx opened the
// way bzOpenPTY does (bluetooth_devices_pty_linux.go: the ioctls through
// SyscallConn, so the master stays in Go's poller and Close ends a blocked
// Read), the child in a session of its own with the PTY as its controlling
// terminal, and the whole session stopped on Kill — a shell's background
// jobs included, which sit in process groups of their own.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type termPtyLinux struct {
	master    *os.File
	cmd       *exec.Cmd
	hupOnce   sync.Once
	closeOnce sync.Once
}

// termPtyOpen opens a PTY pair sized cols x rows.
func termPtyOpen(cols, rows int) (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	rc, err := master.SyscallConn()
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	n := -1
	var ioErr error
	ctlErr := rc.Control(func(fd uintptr) {
		if ioErr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioErr != nil {
			return
		}
		if n, ioErr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN); ioErr != nil {
			return
		}
		ioErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	})
	if ctlErr == nil {
		ctlErr = ioErr
	}
	if ctlErr != nil {
		_ = master.Close()
		return nil, nil, ctlErr
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// termPtySpawn starts cmd on a new PTY.
func termPtySpawn(c termPtyCommand, env []string, dir string, cols, rows int) (termPtyProc, error) {
	if len(c.argv) == 0 {
		return nil, errors.New("nothing to run")
	}
	master, slave, err := termPtyOpen(cols, rows)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(c.argv[0], c.argv[1:]...)
	cmd.Env = env
	if dir != "" {
		if st, statErr := os.Stat(dir); statErr == nil && st.IsDir() {
			cmd.Dir = dir
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own, with the PTY (its standard input) as the
	// controlling terminal: Ctrl+C, Ctrl+Z and job control work, and a
	// program's process group is stopped with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, err
	}
	// Only the child keeps the terminal's other end: once it (and whatever it
	// started) is gone, reading the master ends.
	_ = slave.Close()
	return &termPtyLinux{master: master, cmd: cmd}, nil
}

func (p *termPtyLinux) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	if err != nil && errors.Is(err, syscall.EIO) {
		// The last holder of the other end closed it: the program ended.
		err = errors.New("terminal closed")
	}
	return n, err
}

func (p *termPtyLinux) Write(b []byte) (int, error) { return p.master.Write(b) }

func (p *termPtyLinux) Resize(cols, rows int) error {
	rc, err := p.master.SyscallConn()
	if err != nil {
		return err
	}
	var ioErr error
	if err := rc.Control(func(fd uintptr) {
		// The kernel tells the foreground programs (SIGWINCH).
		ioErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	}); err != nil {
		return err
	}
	return ioErr
}

// Kill hangs up the session — the shell, its jobs, a program it started —
// then, after grace, kills what is left, and closes the terminal. Each call
// waits its own grace, so JukaHub quitting during an End session still
// kills within its shorter one (the hang-up is sent once).
func (p *termPtyLinux) Kill(grace time.Duration) {
	if p.cmd.Process != nil {
		sid := p.cmd.Process.Pid
		p.hupOnce.Do(func() {
			termPtySignalSession(sid, syscall.SIGHUP)
			_ = syscall.Kill(-sid, syscall.SIGCONT)
		})
		deadline := time.Now().Add(grace)
		for time.Now().Before(deadline) && termPtySessionAlive(sid) {
			time.Sleep(50 * time.Millisecond)
		}
		termPtySignalSession(sid, syscall.SIGKILL)
	}
	p.closeOnce.Do(func() { _ = p.master.Close() })
}

func (p *termPtyLinux) Wait() (int, error) {
	err := p.cmd.Wait()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
		err = nil
		// Killed by a signal: ExitCode says -1; the signal says what happened.
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sig := ws.Signal()
			code = 128 + int(sig)
			err = fmt.Errorf("stopped by %s (%s)", unix.SignalName(sig), sig.String())
		}
	}
	return code, err
}

// EchoOff reports the program reading a line without showing it — echo off
// while the line discipline still collects lines, which is what getpass,
// sudo, su and ssh's password prompts set. A shell's line editor, vi or
// htop turn echo off too, but read keys one by one (no ICANON), so they do
// not count. On a PTY master TCGETS reads the terminal's (slave's) modes.
func (p *termPtyLinux) EchoOff() bool {
	rc, err := p.master.SyscallConn()
	if err != nil {
		return false
	}
	var lflag uint32
	var ioErr error
	if err := rc.Control(func(fd uintptr) {
		var tio *unix.Termios
		if tio, ioErr = unix.IoctlGetTermios(int(fd), unix.TCGETS); ioErr == nil {
			lflag = tio.Lflag
		}
	}); err != nil || ioErr != nil {
		return false
	}
	return lflag&unix.ECHO == 0 && lflag&unix.ICANON != 0
}

// termPtySessionPids lists the processes of session sid (its leader
// included), from /proc.
func termPtySessionPids(sid int) []int {
	if sid <= 1 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return []int{sid}
	}
	var out []int
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil || pid <= 1 {
			continue
		}
		data, readErr := os.ReadFile("/proc/" + e.Name() + "/stat")
		if readErr != nil {
			continue
		}
		// pid (comm) state ppid pgrp session …; comm may hold spaces and
		// parentheses, so the fields start after the last ')'.
		s := string(data)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 4 {
			continue
		}
		if v, convErr := strconv.Atoi(f[3]); convErr == nil && v == sid {
			out = append(out, pid)
		}
	}
	return out
}

func termPtySignalSession(sid int, sig syscall.Signal) {
	for _, pid := range termPtySessionPids(sid) {
		_ = syscall.Kill(pid, sig)
	}
}

func termPtySessionAlive(sid int) bool {
	for _, pid := range termPtySessionPids(sid) {
		// A zombie still listed is gone for this purpose.
		if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
			s := string(data)
			if i := strings.LastIndexByte(s, ')'); i >= 0 {
				if f := strings.Fields(s[i+1:]); len(f) > 0 && f[0] != "Z" {
					return true
				}
			}
		}
	}
	return false
}
