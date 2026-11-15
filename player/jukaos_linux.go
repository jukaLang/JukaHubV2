//go:build linux

package main

// jukaos_linux.go — JukaHub OS on the handheld: the --os-session start (a
// private mount namespace with the layers' binds, then an exec of JukaHub
// inside it), the global steps (kernel modules, sysfs values) with their RAM
// journal, and putting them back. See jukaos.go for the design.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// jukaosJournalLock serialises the apply goroutine and every revert: a
// channel, so a revert at exit can give up waiting for a module that hangs in
// its init instead of hanging the quit.
var jukaosJournalLock = make(chan struct{}, 1)

// jukaosLockJournal takes the journal lock within wait.
func jukaosLockJournal(wait time.Duration) bool {
	select {
	case jukaosJournalLock <- struct{}{}:
		return true
	case <-time.After(wait):
		return false
	}
}

func jukaosUnlockJournal() { <-jukaosJournalLock }

// jukaosSupported reports whether this device can run JukaHub OS: a TrimUI,
// whose launch.sh starts the session.
func jukaosSupported() bool { return IsTrimuiDevice() }

// jukaosUname is the machine and the kernel release (uname -m, uname -r).
func jukaosUname() (machine, release string) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", ""
	}
	return unix.ByteSliceToString(u.Machine[:]), unix.ByteSliceToString(u.Release[:])
}

// jukaosHostFor is the host the layers are checked against.
func jukaosHostFor(appDir string) jukaosHost {
	machine, release := jukaosUname()
	return jukaosHost{Machine: machine, Release: release, AppDir: filepath.ToSlash(appDir), SDRoot: jukaosSDRootOf(appDir)}
}

// jukaosKernel is the kernel release for the Settings status.
func jukaosKernel() string {
	_, release := jukaosUname()
	return release
}

// jukaosSay writes one line to stderr, which launch.sh appends to errors.txt;
// the --os-session start runs before JukaHub's logging exists.
func jukaosSay(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%s] jukaos: %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// jukaosSessionEntry is the --os-session start, the first thing main does.
// launch.sh passes the flag when OS mode is on. Whatever happens, the flag
// leaves os.Args, and when OS mode cannot or should not run (off, the stock
// system chosen for this boot, not root) JukaHub carries on as the app. In a
// session it sets JUKAHUB_OS_* for this process and, when the layers bind
// files or bring libraries for JukaHub itself, re-executes JukaHub inside a
// private mount namespace with them. It never blocks a start: a part that
// fails is skipped and written down.
func jukaosSessionEntry() {
	if len(os.Args) < 2 || os.Args[1] != jukaosSessionArg {
		return
	}
	os.Args = append(os.Args[:1:1], os.Args[2:]...)
	// No variable of an earlier session (a JukaHub started from inside one)
	// carries over into this one.
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); strings.HasPrefix(key, jukaosEnvPrefix) {
			_ = os.Unsetenv(key)
		}
	}
	appDir := jukaosAppDir()
	if os.Geteuid() != 0 {
		jukaosSay("JukaHub OS needs root; starting as the app")
		return
	}
	state := jukaosLoadState(appDir)
	if !state.Enabled {
		jukaosSay("OS mode is off; starting as the app")
		return
	}
	if _, err := os.Stat(filepath.Join(jukaosRunDir, jukaosStockOnceName)); err == nil {
		jukaosSay("the stock system was chosen for this boot; starting as the app")
		return
	}
	h := jukaosHostFor(appDir)
	all := append(jukaosLoadLayers(h, jukaosLayersDir(appDir)), jukaosBuiltinLayer(appDir))
	layers := jukaosEnabledLayers(all, state)
	info := jukaosSessionInfo{PID: os.Getpid()}
	for _, l := range all {
		if state.Layers[l.ID] && l.Problem != "" {
			info.Skipped = append(info.Skipped, l.ID+": "+l.Problem)
		}
	}
	// Safe mode (--safe-mode, JUKAHUB_SAFE_MODE, safe-mode.flag) is for
	// getting a broken setup going: no layers then either.
	_, flagErr := os.Stat(filepath.Join(appDir, safeModeFlagPath))
	if len(layers) > 0 && resolveSafeMode(os.Args[1:], os.Getenv, flagErr == nil).Active {
		jukaosSay("safe mode: starting without layers")
		info.Skipped = append(info.Skipped, "safe mode: all layers left out")
		layers = nil
	}
	if len(layers) > 0 {
		guard := jukaosLoadGuard(appDir)
		if guard.Failed >= jukaosGuardLimit {
			jukaosSay("the last %d OS starts with layers did not get going; starting without layers", guard.Failed)
			info.Guarded = true
			layers = nil
		} else {
			// Counted before anything is applied: a start the layers break
			// may never get further than this.
			guard.Failed++
			if err := jukaosSaveGuard(appDir, guard); err != nil {
				jukaosSay("OS guard not saved: %v", err)
			}
		}
	}
	var binds []jukaosBind
	var selfLibs, childLibs []string
	for _, l := range layers {
		info.Layers = append(info.Layers, l.ID)
		binds = append(binds, l.Binds...)
		if !l.Builtin {
			selfLibs = append(selfLibs, l.LibDirs...)
		}
		if l.Spec.Children {
			childLibs = append(childLibs, l.LibDirs...)
		}
		for _, note := range l.Notes {
			info.Skipped = append(info.Skipped, l.ID+": "+note)
		}
	}
	needExec, unshared := false, false
	// The namespace this thread leaves, to go back to should the exec fail.
	var homeNS *os.File
	if len(binds) > 0 {
		// The namespace belongs to this thread only; the thread stays locked
		// and executes JukaHub, which makes it the whole process's.
		runtime.LockOSThread()
		homeNS, _ = os.Open("/proc/self/ns/mnt")
		var err error
		unshared, err = jukaosPrivateMounts()
		if unshared {
			needExec = true
		} else {
			runtime.UnlockOSThread()
		}
		if err != nil {
			info.NamespaceErr = err.Error()
			jukaosSay("no private mount namespace (%v); the layers' file binds are skipped", err)
		} else {
			info.Namespace = true
			for _, b := range binds {
				if err := jukaosMountBind(b); err != nil {
					info.Skipped = append(info.Skipped, fmt.Sprintf("bind %s: %v", b.Dst, err))
					jukaosSay("bind %s skipped: %v", b.Dst, err)
					continue
				}
				info.Binds++
			}
		}
	}
	_ = os.Setenv(jukaosEnvSession, "1")
	if len(info.Layers) > 0 {
		_ = os.Setenv(jukaosEnvLayers, strings.Join(info.Layers, ","))
	}
	if len(childLibs) > 0 {
		_ = os.Setenv(jukaosEnvChildLibs, strings.Join(childLibs, ":"))
	}
	if info.Guarded {
		_ = os.Setenv(jukaosEnvGuard, "1")
	}
	oldLibPath, hadLibPath := os.LookupEnv("LD_LIBRARY_PATH")
	if len(selfLibs) > 0 {
		// The loader reads the path once, at exec: JukaHub itself runs with
		// the layers' libraries only when it is executed again.
		needExec = true
		list := strings.Join(selfLibs, ":")
		if old := os.Getenv("LD_LIBRARY_PATH"); old != "" {
			list += ":" + old
		}
		_ = os.Setenv("LD_LIBRARY_PATH", list)
	}
	jukaosWriteSessionInfo(info)
	jukaosSay("OS session: %d layer(s), %d bind(s), %d skipped", len(info.Layers), info.Binds, len(info.Skipped))
	if !needExec {
		if homeNS != nil {
			homeNS.Close()
		}
		return
	}
	self, err := os.Executable()
	if err == nil {
		err = syscall.Exec(self, os.Args, os.Environ())
	}
	// Only a failed exec gets here (a "core" layer's broken loader, say).
	jukaosSay("could not start JukaHub inside the session: %v", err)
	if unshared && !jukaosLeaveNamespace(homeNS) {
		// This thread stays in the new namespace while the others are not:
		// carrying on would give JukaHub two views of the files. Stop, and
		// let the OS guard count it.
		os.Exit(1)
	}
	if homeNS != nil {
		homeNS.Close()
	}
	// Back where the start began: JukaHub carries on as a session without
	// the layers (the guard has counted this start).
	if hadLibPath {
		_ = os.Setenv("LD_LIBRARY_PATH", oldLibPath)
	} else {
		_ = os.Unsetenv("LD_LIBRARY_PATH")
	}
	_ = os.Unsetenv(jukaosEnvLayers)
	_ = os.Unsetenv(jukaosEnvChildLibs)
	info.Layers, info.Binds, info.Namespace = nil, 0, false
	info.Skipped = append(info.Skipped, fmt.Sprintf("layers left out: JukaHub could not start with them (%v)", err))
	jukaosWriteSessionInfo(info)
}

// jukaosLeaveNamespace puts this (locked) thread back into the mount
// namespace home names, in the folder it worked in, and unlocks it.
func jukaosLeaveNamespace(home *os.File) bool {
	if home == nil {
		return false
	}
	wd, _ := os.Getwd()
	if err := unix.Setns(int(home.Fd()), unix.CLONE_NEWNS); err != nil {
		jukaosSay("could not leave the session's namespace: %v", err)
		return false
	}
	// setns moves this thread's working folder to "/"; the other threads
	// never left theirs.
	if wd != "" {
		_ = os.Chdir(wd)
	}
	runtime.UnlockOSThread()
	return true
}

// jukaosPrivateMounts gives this thread its own mount namespace whose mounts
// never propagate out: "/" becomes a slave (new SD and USB mounts still come
// in), or private where the kernel refuses that. unshared says whether the
// thread is in a new namespace now; an error means no bind may be made.
func jukaosPrivateMounts() (unshared bool, err error) {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return false, fmt.Errorf("unshare: %w", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SLAVE, ""); err != nil {
		if err2 := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err2 != nil {
			return true, fmt.Errorf("mount propagation: %v / %v", err, err2)
		}
	}
	return true, nil
}

// jukaosMountBind binds one layer path over a system path, read-only unless
// the layer asked for a writable folder.
func jukaosMountBind(b jukaosBind) error {
	if err := unix.Mount(b.Src, b.Dst, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	if !b.RW {
		if err := unix.Mount("", b.Dst, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			// A writable bind would let a game write through it into the
			// layer on the card: no bind rather than that.
			_ = unix.Unmount(b.Dst, unix.MNT_DETACH)
			return fmt.Errorf("could not make it read-only: %w", err)
		}
	}
	return nil
}

// jukaosWriteSessionInfo leaves what the start did for the JukaHub it runs.
func jukaosWriteSessionInfo(info jukaosSessionInfo) {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(jukaosRunDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(jukaosRunDir, jukaosSessionName), data, 0o644)
}

// ── The running JukaHub ────────────────────────────────────────────────────

// jukaosInit runs once logging is up (main.go): it reads the session the
// --os-session start set up, undoes a journal a crashed session left, and in
// a session loads the layers' drivers and values off the main goroutine.
func jukaosInit() {
	session := os.Getenv(jukaosEnvSession) == "1"
	var info jukaosSessionInfo
	if session {
		// Only the process the --os-session start set up is the session: a
		// JukaHub started from inside one (a terminal, a script) inherits the
		// variables but must not apply layers or write over its journal.
		if info = jukaosReadSessionInfo(); info.PID != os.Getpid() {
			session = false
		}
	}
	if !session {
		for _, key := range []string{jukaosEnvSession, jukaosEnvLayers, jukaosEnvChildLibs, jukaosEnvGuard} {
			_ = os.Unsetenv(key)
		}
	}
	ids := jukaosSplitIDs(os.Getenv(jukaosEnvLayers))
	jukaosLive.mu.Lock()
	jukaosLive.session = session
	jukaosLive.guarded = os.Getenv(jukaosEnvGuard) == "1"
	jukaosLive.layers = ids
	jukaosLive.info = info
	jukaosLive.mu.Unlock()
	// A journal a killed or crashed session left behind is put back first,
	// in App mode too: its changes are still in force until a reboot.
	jukaosRevertStale()
	Cleanup = append(Cleanup, func() { jukaosRevert("exit") })
	if !session {
		return
	}
	logInfo("jukaos", "JukaHub OS session: layers %v, %d bind(s), namespace %v %s", ids, jukaosLive.info.Binds, jukaosLive.info.Namespace, jukaosLive.info.NamespaceErr)
	for _, s := range jukaosLive.info.Skipped {
		logWarn("jukaos", "skipped: %s", s)
	}
	if len(ids) > 0 {
		go jukaosApplyGlobal(jukaosAppDir(), ids)
	}
}

// jukaosReadSessionInfo is session.json when this process wrote it.
func jukaosReadSessionInfo() jukaosSessionInfo {
	var info jukaosSessionInfo
	data, err := jukaosReadBounded(filepath.Join(jukaosRunDir, jukaosSessionName), 256<<10)
	if err != nil || json.Unmarshal(data, &info) != nil || info.PID != os.Getpid() {
		return jukaosSessionInfo{}
	}
	return info
}

// jukaosJournalPath is /tmp/jukaos/journal.json.
func jukaosJournalPath() string { return filepath.Join(jukaosRunDir, jukaosJournalName) }

// jukaosSaveJournal writes the journal. Caller holds the journal lock. A
// change whose journal could not be written is not made (or undone at once):
// nothing could put it back.
func jukaosSaveJournal(j jukaosJournal) error {
	data, err := jukaosEncodeJournal(j)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(jukaosRunDir, 0o755); err != nil {
		logWarn("jukaos", "journal folder: %v", err)
		return err
	}
	tmp := jukaosJournalPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		logWarn("jukaos", "journal not written: %v", err)
		return err
	}
	if err := os.Rename(tmp, jukaosJournalPath()); err != nil {
		logWarn("jukaos", "journal not written: %v", err)
		return err
	}
	return nil
}

// jukaosStopping is set once a revert was asked for: the apply goroutine
// makes no further change (it may be waiting for the lock, or not have
// started yet).
var jukaosStopping atomic.Bool

// jukaosNewModule is the name a module just loaded is listed under: the
// expected name when it newly appears in /proc/modules, else the one name
// that newly appeared; "" when that is unclear (then it is not journaled, and
// so never unloaded: somebody else's driver of that name must stay).
func jukaosNewModule(before map[string]bool, expected string) string {
	after := jukaosLoadedModules()
	if after[expected] && !before[expected] {
		return expected
	}
	found := ""
	for name := range after {
		if !before[name] {
			if found != "" {
				return ""
			}
			found = name
		}
	}
	return found
}

// jukaosLoadedModules is the set of module names /proc/modules lists.
func jukaosLoadedModules() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			out[f[0]] = true
		}
	}
	return out
}

// jukaosLoadModule loads a .ko: finit_module, or init_module on a kernel
// without it.
func jukaosLoadModule(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	err = unix.FinitModule(int(f.Fd()), "", 0)
	if errors.Is(err, unix.ENOSYS) {
		data, rerr := jukaosReadBounded(p, jukaosModuleMax)
		if rerr != nil {
			return rerr
		}
		err = unix.InitModule(data, "")
	}
	return err
}

// jukaosReadValue reads a sysfs/proc value.
func jukaosReadValue(p string) (string, error) {
	data, err := jukaosReadSmall(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// jukaosReadSmall reads a pseudo-file (its size is reported as 0 or 4096,
// so the bound is on what is read, not on the size).
func jukaosReadSmall(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n > 0 {
		return buf[:n], nil
	}
	return nil, err
}

// jukaosWriteValue writes a sysfs/proc value; the file must exist.
func jukaosWriteValue(p, v string) error {
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(v)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// jukaosDeviceOps are the revert's effects on the device.
func jukaosDeviceOps() jukaosRevertOps {
	return jukaosRevertOps{
		ReadValue:  jukaosReadValue,
		WriteValue: jukaosWriteValue,
		UnloadModule: func(name string) error {
			return unix.DeleteModule(name, unix.O_NONBLOCK)
		},
	}
}

// jukaosApplyGlobal loads the drivers and writes the values of the layers
// this session applied, journaling each change. A driver is journaled once it
// is in (an entry for one that never loaded could unload somebody else's of
// the same name); a value before it is written (an entry whose write never
// happened reads as "changed since" and is left alone).
func jukaosApplyGlobal(appDir string, ids []string) {
	if !jukaosLockJournal(10 * time.Second) {
		logWarn("jukaos", "journal busy; drivers and values not applied")
		return
	}
	defer jukaosUnlockJournal()
	if jukaosStopping.Load() {
		return
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	h := jukaosHostFor(appDir)
	j := jukaosJournal{PID: os.Getpid(), BootID: bootOSReadBootID()}
	loaded := jukaosLoadedModules()
	modules := 0
	layers := jukaosLoadLayers(h, jukaosLayersDir(appDir))
	for _, l := range layers {
		if !want[l.ID] || l.Problem != "" {
			continue
		}
		for _, m := range l.Modules {
			if jukaosStopping.Load() {
				return
			}
			if loaded[m.Name] {
				logInfo("jukaos", "driver %s is loaded already; left to the system", m.Name)
				continue
			}
			if err := jukaosLoadModule(m.Path); err != nil {
				if !errors.Is(err, unix.EEXIST) {
					logWarn("jukaos", "driver %s (%s) not loaded: %v", m.Name, l.ID, err)
				}
				continue
			}
			name := jukaosNewModule(loaded, m.Name)
			loaded = jukaosLoadedModules()
			if name == "" {
				logWarn("jukaos", "driver %s (%s) loaded under an unclear name; it stays until a reboot", m.Name, l.ID)
				continue
			}
			modules++
			j.Entries = append(j.Entries, jukaosJournalEntry{Kind: jukaosKindModule, Name: name, Path: m.Path})
			if err := jukaosSaveJournal(j); err != nil {
				j.Entries = j.Entries[:len(j.Entries)-1]
				modules--
				if uerr := unix.DeleteModule(name, unix.O_NONBLOCK); uerr != nil {
					logWarn("jukaos", "driver %s: no journal and not unloaded (%v); it stays until a reboot", name, uerr)
				}
				continue
			}
			logInfo("jukaos", "driver %s loaded (%s)", name, l.ID)
		}
	}
	for _, l := range layers {
		if !want[l.ID] || l.Problem != "" {
			continue
		}
		for _, w := range l.Sysfs {
			if jukaosStopping.Load() {
				return
			}
			old, err := jukaosReadValue(w.Path)
			if err != nil {
				logWarn("jukaos", "%s (%s): %v", w.Path, l.ID, err)
				continue
			}
			old = jukaosSysfsCurrent(old)
			if jukaosSysfsSame(old, w.Value) {
				continue
			}
			if old == "" || len(old) > jukaosSysfsValueMax || strings.ContainsAny(old, "\n\r\x00") {
				// Not a value that could be written back as it was.
				logWarn("jukaos", "%s (%s): current value cannot be put back; left alone", w.Path, l.ID)
				continue
			}
			j.Entries = append(j.Entries, jukaosJournalEntry{Kind: jukaosKindSysfs, Path: w.Path, Old: old, New: w.Value})
			if err := jukaosSaveJournal(j); err != nil {
				j.Entries = j.Entries[:len(j.Entries)-1]
				continue
			}
			if err := jukaosWriteValue(w.Path, w.Value); err != nil {
				logWarn("jukaos", "%s (%s) not set: %v", w.Path, l.ID, err)
				j.Entries = j.Entries[:len(j.Entries)-1]
				_ = jukaosSaveJournal(j)
				continue
			}
			logInfo("jukaos", "%s: %s -> %s (%s)", w.Path, old, w.Value, l.ID)
		}
	}
	jukaosLive.mu.Lock()
	jukaosLive.modules = modules
	jukaosLive.mu.Unlock()
}

// jukaosRevert undoes this process's journal: at exit, on "Return to …",
// and from the escape hatch. Idempotent; it waits at most three seconds for
// a driver still loading.
func jukaosRevert(reason string) []string {
	// Only the quit reverts: from here on nothing new is applied.
	jukaosStopping.Store(true)
	if !jukaosLockJournal(3 * time.Second) {
		logWarn("jukaos", "%s: journal busy; the changes stay until the next start or a reboot", reason)
		return nil
	}
	defer jukaosUnlockJournal()
	return jukaosRevertLocked(reason, func(j jukaosJournal) bool { return j.PID == os.Getpid() })
}

// jukaosRevertStale undoes a journal a session that is gone left behind. One
// from an earlier boot (a /tmp that survived it) is only removed: the reboot
// undid it. One whose JukaHub still runs is left alone.
func jukaosRevertStale() {
	if !jukaosLockJournal(time.Second) {
		return
	}
	defer jukaosUnlockJournal()
	jukaosRevertLocked("stale journal", func(j jukaosJournal) bool {
		if j.PID == os.Getpid() {
			return false
		}
		if j.PID > 0 && jukaosSameProgramRuns(j.PID) {
			return false
		}
		// Only a journal provably of this boot is undone: one of an earlier
		// boot (or one whose boot cannot be told) names drivers and values a
		// reboot already put back, and undoing it again could unload the
		// stock system's driver of the same name.
		if boot := bootOSReadBootID(); boot == "" || j.BootID == "" || boot != j.BootID {
			_ = os.Remove(jukaosJournalPath())
			return false
		}
		return true
	})
}

// jukaosSameProgramRuns reports whether pid is a live JukaHub.
func jukaosSameProgramRuns(pid int) bool {
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == filepath.Base(self)
}

// jukaosRevertLocked reads the journal and, when mine says it is this
// caller's to undo, undoes it and removes it. Caller holds the lock.
func jukaosRevertLocked(reason string, mine func(jukaosJournal) bool) []string {
	data, err := jukaosReadBounded(jukaosJournalPath(), jukaosJournalMax)
	if err != nil {
		return nil
	}
	j, err := jukaosDecodeJournal(data)
	if err != nil {
		logWarn("jukaos", "%s: unreadable journal removed: %v", reason, err)
		_ = os.Remove(jukaosJournalPath())
		return nil
	}
	if !mine(j) {
		return nil
	}
	report := jukaosRevertEntries(j.Entries, jukaosDeviceOps())
	_ = os.Remove(jukaosJournalPath())
	for _, line := range report {
		logInfo("jukaos", "%s: %s", reason, line)
	}
	jukaosLive.mu.Lock()
	jukaosLive.modules = 0
	jukaosLive.mu.Unlock()
	return report
}

// jukaosDropCaches frees the page cache once the first screen is up: the
// assets are loaded by then, and the games get the memory. Dentries and
// inodes stay (1, not 3), so finding files stays quick.
func jukaosDropCaches() {
	unix.Sync()
	if err := jukaosWriteValue("/proc/sys/vm/drop_caches", "1"); err != nil {
		logWarn("jukaos", "page cache not freed: %v", err)
		return
	}
	logInfo("jukaos", "page cache freed for the games")
}

// jukaosMarkStockOnce makes the rest of this boot the stock system's: a
// JukaHub started from the menu again runs as the app. /tmp is RAM, so the
// next boot is JukaHub OS again.
func jukaosMarkStockOnce() {
	if err := os.MkdirAll(jukaosRunDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(jukaosRunDir, jukaosStockOnceName), []byte("stock system chosen at boot\n"), 0o644)
}

// jukaosStockOnce reports whether the stock system was chosen for this boot.
func jukaosStockOnce() bool {
	_, err := os.Stat(filepath.Join(jukaosRunDir, jukaosStockOnceName))
	return err == nil
}

// jukaosClearStockOnce forgets that choice ("Start OS session now").
func jukaosClearStockOnce() { _ = os.Remove(filepath.Join(jukaosRunDir, jukaosStockOnceName)) }
