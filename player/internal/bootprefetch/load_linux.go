//go:build linux

package bootprefetch

// load_linux.go — the loader: this package's init starts it on a goroutine,
// before the main package's own (large) initialisation begins.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// coldStartWithin is how soon after the kernel's start a start counts as the
// boot's: the list is for those (the same minute startup.go uses).
const coldStartWithin = 60 * time.Second

// active is set by init only: this start uses the list, and the recorder
// records the next one.
var active bool

// loaded is closed when the loader has finished, or never started.
var loaded = make(chan struct{})

// progress is the bytes asked for so far.
var progress atomic.Int64

var status struct {
	mu       sync.Mutex
	started  time.Time
	listed   int64
	stats    Stats
	previous *List
}

func init() {
	if testing.Testing() {
		close(loaded)
		return
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvName))) {
	case "0", "off", "false", "no":
		status.stats = Stats{Off: true, Why: "off (" + EnvName + "=0)"}
		close(loaded)
		return
	case "1", "on", "true", "yes":
		// Every start, cold or not: a bench after dropping the caches. The
		// helpers JukaHub starts inherit the variable and stay out.
		if startedBySelf() {
			status.stats.Why = "not a cold start"
			close(loaded)
			return
		}
	default:
		if !coldStart() || startedBySelf() {
			status.stats.Why = "not a cold start"
			close(loaded)
			return
		}
	}
	active = true
	status.started = time.Now()
	go func() {
		defer close(loaded)
		defer func() {
			if recover() != nil {
				finish(Stats{Why: "stopped by an error"})
			}
		}()
		load()
	}()
}

// Active reports whether this start runs the list (a cold start, or one the
// environment asked for) and records the next one. It is decided before the
// main package initialises; a boot-prefetch.off file is only seen by the
// loader, which then reports Off.
func Active() bool { return active }

// Result is this start's prefetch so far, and done once the loader has
// finished.
func Result() (Stats, bool) {
	select {
	case <-loaded:
		status.mu.Lock()
		defer status.mu.Unlock()
		return status.stats, true
	default:
	}
	status.mu.Lock()
	defer status.mu.Unlock()
	st := Stats{Listed: status.listed, Bytes: progress.Load()}
	if !status.started.IsZero() {
		st.Took = time.Since(status.started)
	}
	return st, false
}

// Wait waits up to d for the loader to finish, then reports as Result.
func Wait(d time.Duration) (Stats, bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-loaded:
	case <-t.C:
	}
	return Result()
}

// Previous is the list this start read, nil when there was none or it was
// refused.
func Previous() *List {
	status.mu.Lock()
	defer status.mu.Unlock()
	return status.previous
}

func finish(st Stats) {
	status.mu.Lock()
	st.Took = time.Since(status.started)
	status.stats = st
	status.mu.Unlock()
}

func load() {
	exe, err := os.Executable()
	if err != nil {
		finish(Stats{Why: "executable not found"})
		return
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(exe), "data", OffName)); err == nil {
		finish(Stats{Off: true, Why: "off (data/" + OffName + ")"})
		return
	}
	data, ok := readList(ListPath(exe))
	if !ok {
		finish(Stats{Why: "no list yet"})
		return
	}
	l, err := Parse(data)
	if err != nil {
		finish(Stats{Why: "list refused: " + err.Error()})
		return
	}
	status.mu.Lock()
	status.previous = l
	status.listed = l.Bytes()
	status.mu.Unlock()
	k := &kernelIO{buf: make([]byte, Chunk)}
	finish(run(l, exe, k))
}

// coldStart reports whether this process was started within coldStartWithin
// of the kernel's start: field 22 of /proc/self/stat, in clock ticks (USER_HZ,
// 100 on every architecture JukaHub runs on). An exec keeps the process, so
// JukaHub OS's re-exec into its session counts from the first start.
func coldStart() bool {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return false
	}
	stat := string(data)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(stat[end+1:])
	const startField = 22 - 3 // fields[0] is field 3
	if len(fields) <= startField {
		return false
	}
	ticks, err := strconv.ParseUint(fields[startField], 10, 64)
	return err == nil && ticks > 0 && ticks <= uint64(coldStartWithin/time.Second)*100
}

// startedBySelf reports whether the parent process runs the same executable:
// JukaHub's helpers (the VPN daemon, the audio probe) are JukaHub started by
// JukaHub, after the start the list is for.
func startedBySelf() bool {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return false
	}
	parent, err := os.Readlink("/proc/" + strconv.Itoa(os.Getppid()) + "/exe")
	return err == nil && parent == self
}

// openRegular opens path for reading only when it is a regular file of a
// filesystem that holds files. A device, pipe or socket is never opened
// (opening one can block or do something), nor a symbolic link at the end of
// the path, nor a kernel file (proc, sysfs, debugfs and the like) reached
// through a link earlier in the path, which ValidPath's prefixes cannot see.
// The open does not wait, and fstat confirms what was opened.
func openRegular(path string) (int, unix.Stat_t, bool) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return -1, st, false
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil || kernelFS(uint32(fs.Type)) {
		return -1, st, false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, st, false
	}
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return -1, st, false
	}
	return fd, st, true
}

// kernelFS reports whether a filesystem type is one of the kernel's own,
// whose files are not data on a disk.
func kernelFS(magic uint32) bool {
	switch magic {
	case unix.PROC_SUPER_MAGIC, unix.SYSFS_MAGIC, unix.DEBUGFS_MAGIC, unix.TRACEFS_MAGIC,
		unix.SECURITYFS_MAGIC, unix.CGROUP_SUPER_MAGIC, unix.CGROUP2_SUPER_MAGIC, unix.BPF_FS_MAGIC,
		unix.PSTOREFS_MAGIC, unix.EFIVARFS_MAGIC, unix.DEVPTS_SUPER_MAGIC:
		return true
	}
	return false
}

// readList reads the list: a regular file (openRegular), and not larger than
// a list can be.
func readList(path string) ([]byte, bool) {
	fd, st, ok := openRegular(path)
	if !ok {
		return nil, false
	}
	defer unix.Close(fd)
	if st.Size <= 0 || st.Size > maxListSize {
		return nil, false
	}
	buf := make([]byte, st.Size)
	for got := 0; got < len(buf); {
		n, err := unix.Read(fd, buf[got:])
		if err != nil || n <= 0 {
			return nil, false
		}
		got += n
	}
	return buf, true
}

// ── The kernel's read-ahead ───────────────────────────────────────────────

// kernelIO asks with POSIX_FADV_WILLNEED, which queues the read and returns,
// and keeps at most Window bytes asked for and not yet confirmed. A range
// leaves the window when it is read into buf: that waits until the kernel has
// it (a page still on its way keeps the reader waiting until it arrives), and
// reads what the hint did not cover (a device whose read-ahead window is
// smaller than Chunk). On squashfs the hint itself decompresses, here rather
// than on the thread that would have faulted.
type kernelIO struct {
	pending  []pendingRange
	inFlight int64
	buf      []byte
}

type pendingRange struct {
	fd     int
	off, n int64
}

func (k *kernelIO) open(path string) (int, Info, bool) {
	// Only a regular file is opened at all: opening a device can do things.
	fd, st, ok := openRegular(path)
	if !ok {
		return -1, Info{}, false
	}
	// The confirming reads below must not start read-ahead of their own
	// past the range.
	_ = unix.Fadvise(fd, 0, 0, unix.FADV_RANDOM)
	return fd, Info{Regular: true, Size: st.Size, MtimeNs: st.Mtim.Nano()}, true
}

func (k *kernelIO) queue(fd int, off, n int64) {
	_ = unix.Fadvise(fd, off, n, unix.FADV_WILLNEED)
	k.pending = append(k.pending, pendingRange{fd: fd, off: off, n: n})
	k.inFlight += n
	for k.inFlight > Window && len(k.pending) > 0 {
		k.settle()
	}
	progress.Add(n)
}

// settle confirms the oldest range asked for.
func (k *kernelIO) settle() {
	p := k.pending[0]
	k.pending = k.pending[1:]
	k.inFlight -= p.n
	for got := int64(0); got < p.n; {
		n, err := unix.Pread(p.fd, k.buf[:int(p.n-got)], p.off+got)
		if err != nil || n <= 0 {
			return
		}
		got += int64(n)
	}
}

func (k *kernelIO) finish() {
	for len(k.pending) > 0 {
		k.settle()
	}
}

func (k *kernelIO) close(fd int) { _ = unix.Close(fd) }
