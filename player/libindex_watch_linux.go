package main

// libindex_watch_linux.go — the library index's live view of the ROM
// folders (LIB-1): one inotify instance with a watch on each folder the scan
// read (up to libIndexMaxWatches, console folders first), marking a folder in
// the index the moment a file in it is created, deleted, moved or finished
// writing — an MTP or FTP copy, the Files app, an emulator's save — so the
// next look reads only that folder, and an open library refreshes itself once
// the changes settle (libIndexMarkDirs). inotify reports what this kernel
// does, whatever the file system keeps of folder times. A full event queue
// makes the next look list every folder. When a watch cannot be added (the
// kernel's limit), libIndexWatch reports false and the minute-by-minute time
// check covers the folders (libindex.go).
//
// Also the CRC pass's thread priority: nice 19 and the idle I/O class.

import (
	"encoding/binary"
	"path/filepath"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

// libIndexWatchMask is what a folder's watch reports.
const libIndexWatchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
	unix.IN_CLOSE_WRITE | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ONLYDIR

var libIndexWatcher struct {
	mu      sync.Mutex
	fd      int
	started bool
	failed  bool
	byWD    map[int32]string
	byDir   map[string]int32
}

// libIndexWatch watches exactly dirs (the first libIndexMaxWatches of them)
// and reports whether every one is watched.
func libIndexWatch(dirs []string) bool {
	w := &libIndexWatcher
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed {
		return false
	}
	if !w.started {
		fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
		if err != nil {
			logWarn("library", "no live view of the ROM folders (inotify: %v); checking their times instead", err)
			w.failed = true
			return false
		}
		w.fd, w.started = fd, true
		w.byWD = make(map[int32]string)
		w.byDir = make(map[string]int32)
		go libIndexWatchRead(fd)
	}
	complete := len(dirs) <= libIndexMaxWatches
	if !complete {
		dirs = dirs[:libIndexMaxWatches]
	}
	want := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		want[d] = true
	}
	for dir, wd := range w.byDir {
		if want[dir] {
			continue
		}
		delete(w.byDir, dir)
		// A folder renamed keeps its watch under its new name: only the
		// watch's own name lets it go.
		if w.byWD[wd] == dir {
			delete(w.byWD, wd)
			_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
		}
	}
	for _, dir := range dirs {
		if _, ok := w.byDir[dir]; ok {
			continue
		}
		wd, err := unix.InotifyAddWatch(w.fd, dir, libIndexWatchMask)
		if err != nil {
			complete = false
			if err == unix.ENOSPC {
				break
			}
			continue
		}
		if old, ok := w.byWD[int32(wd)]; ok && old != dir {
			delete(w.byDir, old)
		}
		w.byWD[int32(wd)] = dir
		w.byDir[dir] = int32(wd)
	}
	return complete
}

// libIndexWatchRead reads the events for as long as JukaHub runs.
func libIndexWatchRead(fd int) {
	buf := make([]byte, 64<<10)
	for {
		n, err := unix.Read(fd, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			logWarn("library", "the live view of the ROM folders stopped: %v", err)
			w := &libIndexWatcher
			w.mu.Lock()
			w.failed = true
			w.byWD, w.byDir = nil, nil
			_ = unix.Close(fd)
			w.mu.Unlock()
			libIndexWatchState.Lock()
			libIndexWatchState.complete = false
			libIndexWatchState.Unlock()
			return
		}
		var dirs []string
		overflow := false
		w := &libIndexWatcher
		w.mu.Lock()
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			wd := int32(binary.NativeEndian.Uint32(buf[off:]))
			mask := binary.NativeEndian.Uint32(buf[off+4:])
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			off += unix.SizeofInotifyEvent + nameLen
			if mask&unix.IN_Q_OVERFLOW != 0 {
				overflow = true
				continue
			}
			dir, ok := w.byWD[wd]
			if !ok {
				continue
			}
			if mask&unix.IN_IGNORED != 0 {
				// The folder went (or was unmounted): its watch is gone.
				delete(w.byWD, wd)
				if w.byDir[dir] == wd {
					delete(w.byDir, dir)
				}
				dirs = append(dirs, dir, filepath.Dir(dir))
				continue
			}
			dirs = append(dirs, dir)
			if mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
				dirs = append(dirs, filepath.Dir(dir))
			}
		}
		w.mu.Unlock()
		libIndexMarkDirs(dirs, overflow)
	}
}

// libIndexLowPriority puts the calling goroutine on a thread of its own at
// nice 19 in the idle I/O class, so the CRC pass gives way to everything
// else. The thread ends with the goroutine (it stays locked).
func libIndexLowPriority() {
	runtime.LockOSThread()
	tid := unix.Gettid()
	_ = unix.Setpriority(unix.PRIO_PROCESS, tid, 19)
	// ioprio_set(IOPRIO_WHO_PROCESS, tid, IOPRIO_CLASS_IDLE << 13).
	_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), uintptr(3<<13))
}
