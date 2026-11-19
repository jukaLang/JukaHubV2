//go:build linux

package main

// Process 1's duties: start children with fork/exec, reap every one of them
// and every orphan (SIGCHLD and a wait4(-1) loop), and turn the signals
// busybox's halt (SIGUSR1), poweroff (SIGUSR2) and reboot (SIGTERM) send to
// process 1 — and SIGPWR, which busybox init reads as halt — into the shell
// init's clean shutdown. Halt powers off, as in the shell init: a halted
// handheld would only drain its battery.

import (
	"errors"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var errStopping = errors.New("process 1 is shutting down or handing over")

// procTable maps the children whose end someone waits for to that wait.
type procTable struct {
	mu      sync.Mutex
	waiters map[int]chan int
}

// reap collects every child that has ended, without blocking.
func (p *procTable) reap() {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil || pid <= 0 {
			return
		}
		p.mu.Lock()
		if ch, ok := p.waiters[pid]; ok {
			delete(p.waiters, pid)
			ch <- shellStatus(ws)
		}
		p.mu.Unlock()
	}
}

// shellStatus is the exit status as sh's $? gives it: the code, or 128 plus
// the signal that ended it.
func shellStatus(ws unix.WaitStatus) int {
	switch {
	case ws.Exited():
		return ws.ExitStatus()
	case ws.Signaled():
		return 128 + int(ws.Signal())
	}
	return 255
}

type spawnOpts struct {
	dir    string
	env    []string
	out    int  // stdout and stderr; -1: /dev/null
	commit bool // JukaHub or a daemon: no handing the boot to the shell init after it
	wait   bool // the returned channel gets its exit status
}

// spawn starts path with argv. With opts.wait the channel receives the exit
// status once the child ends.
func (b *booter) spawn(path string, argv []string, opts spawnOpts) (int, <-chan int, error) {
	b.fbMu.Lock()
	defer b.fbMu.Unlock()
	if b.fellBack || b.shuttingDown.Load() {
		return 0, nil, errStopping
	}
	in := uintptr(0)
	out := uintptr(1)
	errOut := uintptr(2)
	if b.nullFD >= 0 {
		in, out, errOut = uintptr(b.nullFD), uintptr(b.nullFD), uintptr(b.nullFD)
	}
	if opts.out >= 0 {
		out, errOut = uintptr(opts.out), uintptr(opts.out)
	}
	attr := &syscall.ProcAttr{Dir: opts.dir, Env: opts.env, Files: []uintptr{in, out, errOut}}
	// The table is locked across the fork, so the reaper cannot collect a
	// child that ends at once before its waiter is in the table.
	b.procs.mu.Lock()
	defer b.procs.mu.Unlock()
	pid, err := syscall.ForkExec(path, argv, attr)
	if err != nil {
		return 0, nil, err
	}
	var ch chan int
	if opts.wait {
		ch = make(chan int, 1)
		b.procs.waiters[pid] = ch
	}
	if opts.commit {
		b.committed.Store(true)
	}
	return pid, ch, nil
}

// watchSignals: every signal is caught (the Go runtime would end the program,
// and with it process 1, on several of them), SIGINT and SIGHUP ignored as the
// shell init's "trap '' INT HUP" (children keep them ignored, as they did).
func (b *booter) watchSignals() {
	all := make(chan os.Signal, 64)
	signal.Notify(all)
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP)
	// The shutdown signals have a channel of their own: signal.Notify drops
	// what does not fit, and the catch-all one also gets every SIGCHLD and
	// the runtime's own SIGURG, which must never crowd out a poweroff.
	stop := make(chan os.Signal, 4)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGPWR)
	chld := make(chan os.Signal, 1)
	signal.Notify(chld, syscall.SIGCHLD)
	go func() {
		for range all {
		}
	}()
	go func() {
		tick := time.NewTicker(time.Second)
		for {
			select {
			case <-chld:
			case <-tick.C:
			}
			b.procs.reap()
		}
	}()
	go func() {
		for s := range stop {
			reason := ""
			switch s {
			case syscall.SIGTERM:
				reason = "reboot"
			case syscall.SIGUSR2:
				reason = "poweroff"
			case syscall.SIGUSR1, syscall.SIGPWR:
				reason = "halt"
			}
			if reason != "" {
				go b.safe("the shutdown", func() { b.shutdown(reason) })
			}
		}
	}()
}

// startDaemons starts the stock daemons of a group as they were running: in
// their folder, with their LD_LIBRARY_PATH, stdin and output on /dev/null.
func (b *booter) startDaemons(group string) {
	if b.rwDone != nil {
		select {
		case <-b.rwDone:
		case <-time.After(5 * time.Second):
		}
	}
	done := b.tl.begin(group + " daemons started")
	defer done()
	for _, d := range b.daemons {
		if d.Group != group {
			continue
		}
		dir := d.Dir
		if !isDir(dir) {
			dir = "/"
		}
		env := b.env
		if d.LD != "" {
			env = setEnv(b.env, "LD_LIBRARY_PATH", d.LD)
		}
		path, argv := daemonArgv(d.Cmd)
		pid, _, err := b.spawn(path, argv, spawnOpts{dir: dir, env: env, out: -1, commit: true})
		if err != nil {
			b.log("not started: %s (%v)", d.Cmd, err)
			continue
		}
		b.tl.mark("started " + strings.Join(argv, " ") + " (pid " + strconv.Itoa(pid) + ")")
	}
}

// daemonArgv is how sh's eval "exec CMD" would run a daemons.list command:
// split here when the quoting is plain (always, for what build_image.py
// writes), else by sh itself.
func daemonArgv(cmd string) (string, []string) {
	argv, ok := shellWords(cmd)
	if ok && len(argv) > 0 {
		if strings.Contains(argv[0], "/") {
			return argv[0], argv
		}
		if path := lookPath(argv[0], initPath); path != "" {
			return path, argv
		}
	}
	return "/bin/sh", []string{"sh", "-c", "exec " + cmd}
}

// usersLeft reports a program still running besides process 1. Kernel
// threads have no executable; finished children (zombies) neither.
func usersLeft() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if name == "1" || !allDigits(name) {
			continue
		}
		if _, err := os.Stat("/proc/" + name + "/exe"); err == nil {
			return true
		}
	}
	return false
}

// shutdown is the shell init's: TERM to everything, up to three seconds for a
// game or app to save and quit, the Wi-Fi networks to the card, sync, KILL,
// the card unmounted (or read-only), sync, reboot(2). Once; it never returns.
func (b *booter) shutdown(reason string) {
	b.shutOnce.Do(func() {
		b.shuttingDown.Store(true)
		b.log("%s", reason)
		b.saveTimeline()
		_ = unix.Kill(-1, unix.SIGTERM)
		for i := 0; i < 15 && usersLeft(); i++ {
			time.Sleep(200 * time.Millisecond)
		}
		b.wifiConfOut()
		unix.Sync()
		_ = unix.Kill(-1, unix.SIGKILL)
		time.Sleep(time.Second)
		b.oslog.close()
		_ = unix.Unmount(udiskDir, 0)
		if err := unix.Unmount(cardDir, 0); err != nil {
			_ = unix.Mount("", cardDir, "", unix.MS_REMOUNT|unix.MS_RDONLY, "")
		}
		unix.Sync()
		cmd := unix.LINUX_REBOOT_CMD_POWER_OFF
		if reason == "reboot" {
			cmd = unix.LINUX_REBOOT_CMD_RESTART
		}
		if err := unix.Reboot(cmd); err != nil {
			// Only reached if the kernel refused: never let process 1 end.
			kmsg("jukahub-os: the kernel refused to " + reason + ": " + err.Error())
		}
	})
	park()
}
