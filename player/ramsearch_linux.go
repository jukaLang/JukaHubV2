//go:build linux

package main

// ramsearch_linux.go — the Linux ramAccess for ramsearch.go and the Memory
// panel. It lists the writable mappings from /proc/<pid>/maps and reads and
// writes them through /proc/<pid>/mem.
//
// Reading another process needs ptrace permission, the same as any debugger,
// so the session attaches to the target while it is open and detaches when it
// closes. Attaching stops the process: a scan runs against a frozen game, and
// closing the panel lets it run again. Only the main thread is attached, which
// is enough for /proc/<pid>/mem access but means a multi-threaded game keeps
// its other threads running.
//
// Nothing here needs root. A process that refuses the attach (another user's,
// or one protected by yama) yields an error the panel shows.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// procMemReader reads and edits one process's writable memory.
type procMemReader struct {
	pid      int
	mem      *os.File
	attached bool
}

// ramOpenProcess attaches to pid and opens its memory for reading and writing.
// The returned access must be closed: that detaches and resumes the process.
func ramOpenProcess(pid int) (ramAccess, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid %d", pid)
	}
	attached, err := attachProcess(pid)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(fmt.Sprintf("/proc/%d/mem", pid), os.O_RDWR, 0)
	if err != nil {
		if attached {
			detachProcess(pid)
		}
		return nil, err
	}
	return &procMemReader{pid: pid, mem: f, attached: attached}, nil
}

// attachProcess stops pid so its memory can be read. A refusal from the kernel
// is reported as-is: the panel turns it into "cannot read that process", and
// the reason (permission, already traced) matters to the user.
func attachProcess(pid int) (bool, error) {
	if err := syscall.PtraceAttach(pid); err != nil {
		if errors.Is(err, syscall.EPERM) {
			return false, fmt.Errorf("permission denied attaching to pid %d", pid)
		}
		return false, err
	}
	// PtraceAttach returns before the kernel reports the stop. Reading too
	// early fails, so wait for it. EINTR is retried; the wait has no timeout
	// because a ptrace stop is the one thing that always arrives.
	var status syscall.WaitStatus
	for {
		_, err := syscall.Wait4(pid, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			detachProcess(pid)
			return false, err
		}
		if status.Stopped() || status.Exited() {
			return true, nil
		}
	}
}

// detachProcess lets pid continue after a scan.
func detachProcess(pid int) {
	_ = syscall.PtraceDetach(pid)
}

// Close detaches from the process and releases the memory handle. The process
// resumes; a game that was paused for a scan keeps playing.
func (p *procMemReader) Close() error {
	err := p.mem.Close()
	if p.attached {
		detachProcess(p.pid)
		p.attached = false
	}
	return err
}

// Regions returns the writable, readable mappings of the process. Only
// read-write mappings are kept; code and read-only data are skipped because a
// value search targets game state, not program text.
func (p *procMemReader) Regions() ([]memRegion, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/maps", p.pid))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseWritableMaps(f)
}

// ReadAt reads from the process at addr. A short read is returned with the
// count, not as an error, so callers can keep the bytes they did get.
func (p *procMemReader) ReadAt(addr uint64, buf []byte) (int, error) {
	n, err := p.mem.ReadAt(buf, int64(addr))
	if err == io.EOF {
		err = nil
	}
	return n, err
}

// WriteAt stores buf at addr. The mapping has to be writable, which is true of
// every region a scan found.
func (p *procMemReader) WriteAt(addr uint64, buf []byte) error {
	n, err := p.mem.WriteAt(buf, int64(addr))
	if err == io.EOF && n == len(buf) {
		return nil
	}
	if err != nil {
		return err
	}
	if n != len(buf) {
		return fmt.Errorf("short write at %s", ramFormatAddr(addr))
	}
	return nil
}
