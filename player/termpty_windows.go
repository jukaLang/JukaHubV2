//go:build windows

package main

// termpty_windows.go — a program on a Windows pseudo console (ConPTY,
// Windows 10 1809 and newer): two pipes, CreatePseudoConsole, and
// CreateProcess with the console in its attribute list. ConPTY turns the
// console the program writes to into VT sequences on the output pipe and
// reads keys as VT from the input pipe, which is exactly what the emulator
// (termvt.go) and the key encoder (termpty.go) speak. The child is put in a
// job object that dies with JukaHub, so nothing outlives the app.

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type termPtyWin struct {
	hpc        windows.Handle
	process    windows.Handle
	thread     windows.Handle
	job        windows.Handle
	in         *os.File // our end of the program's input
	out        *os.File // our end of its output
	once       sync.Once
	closeMu    sync.Mutex
	closed     bool
	procClosed bool
}

// termPtyConPTYAvailable reports whether kernel32 has CreatePseudoConsole:
// calling it on an older Windows would panic in the lazy DLL loader.
func termPtyConPTYAvailable() bool {
	return windows.NewLazySystemDLL("kernel32.dll").NewProc("CreatePseudoConsole").Find() == nil
}

// termPtyEnvBlock is env as CreateProcess wants it: UTF-16 "k=v" strings,
// each NUL-terminated, sorted, and a final NUL.
func termPtyEnvBlock(env []string) *uint16 {
	var b []uint16
	for _, kv := range termPtySortedEnv(env) {
		if strings.IndexByte(kv, 0) >= 0 || !strings.Contains(kv, "=") {
			continue
		}
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			continue
		}
		b = append(b, u...)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	b = append(b, 0)
	return &b[0]
}

// termPtySpawn starts cmd on a new pseudo console.
func termPtySpawn(c termPtyCommand, env []string, dir string, cols, rows int) (termPtyProc, error) {
	if len(c.argv) == 0 {
		return nil, errors.New("nothing to run")
	}
	if !termPtyConPTYAvailable() {
		return nil, errTermPtyUnsupported
	}
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, err
	}
	closeAll := func(hs ...windows.Handle) {
		for _, h := range hs {
			if h != 0 && h != windows.InvalidHandle {
				windows.CloseHandle(h)
			}
		}
	}
	var hpc windows.Handle
	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	if err := windows.CreatePseudoConsole(size, inRead, outWrite, 0, &hpc); err != nil {
		closeAll(inRead, inWrite, outRead, outWrite)
		return nil, err
	}
	// The pseudo console keeps its own copies of these two ends.
	closeAll(inRead, outWrite)

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		closeAll(inWrite, outRead)
		return nil, err
	}
	defer attrs.Delete()
	// The attribute's value is the HPCON itself, not a pointer to it; it is
	// reinterpreted rather than converted so the handle is never treated as
	// a Go pointer (go vet's unsafeptr).
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		windows.ClosePseudoConsole(hpc)
		closeAll(inWrite, outRead)
		return nil, err
	}
	var si windows.StartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	// No standard handles of JukaHub's own (its log, a debugger's pipe) may
	// stand in for the console's: explicitly invalid ones make the program
	// use the pseudo console (as wezterm's portable-pty does).
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle
	si.ProcThreadAttributeList = attrs.List()

	cmdLine := c.winCmdLine
	if cmdLine == "" {
		cmdLine = windows.ComposeCommandLine(c.argv)
	}
	cmdLine16, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		closeAll(inWrite, outRead)
		return nil, err
	}
	var dir16 *uint16
	if dir != "" {
		if st, statErr := os.Stat(dir); statErr == nil && st.IsDir() {
			dir16, _ = windows.UTF16PtrFromString(dir)
		}
	}
	var pi windows.ProcessInformation
	// Suspended until it is in the job below, so nothing it starts can be
	// created outside the job before the assignment.
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(nil, cmdLine16, nil, nil, false, flags, termPtyEnvBlock(env), dir16, &si.StartupInfo, &pi); err != nil {
		windows.ClosePseudoConsole(hpc)
		closeAll(inWrite, outRead)
		return nil, err
	}
	p := &termPtyWin{
		hpc:     hpc,
		process: pi.Process,
		thread:  pi.Thread,
		in:      os.NewFile(uintptr(inWrite), "conpty-in"),
		out:     os.NewFile(uintptr(outRead), "conpty-out"),
	}
	// A job that kills its processes when its last handle closes — JukaHub's
	// own exit included — so a shell and what it started never outlive it.
	// Without one (a job JukaHub runs in that forbids nesting) the program
	// still runs; End session then reaches only the program itself.
	if job, jobErr := windows.CreateJobObject(nil, nil); jobErr == nil {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, setErr := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); setErr == nil &&
			windows.AssignProcessToJobObject(job, pi.Process) == nil {
			p.job = job
		} else {
			windows.CloseHandle(job)
			logWarn("terminal", "session not in a job object; End session stops only the program itself")
		}
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		// It never ran: stop it and give everything back — our pipe ends
		// first, so ClosePseudoConsole has no unread output to wait on.
		if p.job != 0 {
			_ = windows.TerminateJobObject(p.job, 1)
			windows.CloseHandle(p.job)
		}
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = p.in.Close()
		_ = p.out.Close()
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		return nil, err
	}
	return p, nil
}

func (p *termPtyWin) Read(b []byte) (int, error) { return p.out.Read(b) }

func (p *termPtyWin) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *termPtyWin) Resize(cols, rows int) error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return nil
	}
	return windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

// EchoOff: a pseudo console does not say whether the program shows what is
// typed.
func (p *termPtyWin) EchoOff() bool { return false }

// Kill ends the program and its tree (the job) at once — Windows has no
// hang-up to ask with, so grace is not used — then closes the pseudo
// console. ClosePseudoConsole can wait until its output is read, which the
// session's reader keeps doing until the pipe closes.
func (p *termPtyWin) Kill(grace time.Duration) {
	p.once.Do(func() {
		p.closeMu.Lock()
		if p.job != 0 {
			_ = windows.TerminateJobObject(p.job, 1)
		}
		if !p.procClosed {
			_ = windows.TerminateProcess(p.process, 1)
		}
		p.closed = true
		hpc := p.hpc
		p.closeMu.Unlock()
		windows.ClosePseudoConsole(hpc)
		_ = p.in.Close()
		_ = p.out.Close()
		if p.job != 0 {
			windows.CloseHandle(p.job)
		}
	})
}

// Wait blocks until the program exits. Its handles are closed here, under
// the lock Kill takes, so Kill never terminates a handle value Windows has
// already handed to something else.
func (p *termPtyWin) Wait() (int, error) {
	if _, err := windows.WaitForSingleObject(p.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	p.closeMu.Lock()
	err := windows.GetExitCodeProcess(p.process, &code)
	windows.CloseHandle(p.thread)
	windows.CloseHandle(p.process)
	p.procClosed = true
	p.closeMu.Unlock()
	return int(code), err
}
