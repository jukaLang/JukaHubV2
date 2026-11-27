package main

// portmaster_runtime.go — what PortMaster needs from a TrimUI firmware, and
// what JukaHub supplies when the firmware does not have it.
//
// PortMaster's TrimUI package (trimui.portmaster.zip; its launch.sh is
// PortMaster-GUI's trimui/PortMaster.txt) is written for firmware with
// TRIMUI_EX, kloptops's bootstrap that CrossMix includes. On a stock TrimUI
// (Smart Pro 1.1.x, Brick) or under JukaHub OS it closes the moment it opens:
//
//   - launch.sh sources /mnt/SDCARD/System/etc/ex_config, TRIMUI_EX's file;
//     BusyBox sh stops the script there (exit 2);
//   - pugwash and harbourmaster are `#!/usr/bin/env python3`, and the
//     firmware has no python3 (TRIMUI_EX brings Python 3.11);
//   - /bin/bash is BusyBox, and PortMaster's scripts use bash arrays since
//     autumn 2026;
//   - pysdl2 finds SDL2 only through PYSDL2_DLL_PATH (TRIMUI_EX links
//     /usr/trimui/lib/libSDL* into /usr/lib, which JukaHub never does).
//
// So on an ARM64 TrimUI, before PortMaster or a port starts, JukaHub checks —
// once a session, in the environment the port gets — whether the firmware's
// bash runs an array and a process substitution and whether its python3
// imports what PortMaster needs. What fails is supplied for these child
// processes only, first on their PATH: a real bash the firmware has further
// down its PATH (CrossMix's System/bin) through a one-line shim, else Ubuntu's
// bash on the private C runtime (glibc_runtime.go); Python from
// portmaster_python.go. A missing piece is downloaded first (Wi-Fi) with the
// install card up, and the launch then carries on by itself. With JukaHub's
// Python, PYSDL2_DLL_PATH points at a tmpfs folder of links to the firmware's
// SDL2 libraries under the names pysdl2 looks for and SSL_CERT_FILE names the
// firmware's CA bundle; /usr/trimui/lib is appended to LD_LIBRARY_PATH when it
// is missing, and /dev/fd is linked to /proc/self/fd when mdev did not make it
// (bash's process substitution needs it; devtmpfs, gone at the next boot).
// PortMaster's launch.sh runs as a copy beside it (.jukahub-launch.sh) only
// when it reads ex_config the card does not have: in the copy that line is
// skipped, and TRIMUI_EX's python3 path names the Python in use. Nothing in
// PortMaster's folder is edited and nothing in the firmware is replaced; a
// firmware whose bash and Python work is left as it is. A script started by
// its own `#!/bin/bash` line gets a real bash too, in the launch's own mount
// namespace (portmaster_binbash.go), and JukaHub's Python corrects a
// PYSDL2_DLL_PATH that names a folder without SDL2 (portmaster_python.go).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// pmRuntimeRetryAfter spaces download attempts after a failed one.
	pmRuntimeRetryAfter = 30 * time.Second
	// pmShadowLauncherName is the copy of PortMaster's launch.sh JukaHub runs
	// when the card has no TRIMUI_EX.
	pmShadowLauncherName = ".jukahub-launch.sh"
	// pmExPython is TRIMUI_EX's Python, which PortMaster's launcher may name.
	pmExPython = "/mnt/SDCARD/System/bin/python3"
	// pmFirmwareCA is the stock TrimUI CA bundle (docs/HARDWARE_VALIDATION.md).
	pmFirmwareCA = "/etc/ssl/certs/ca-certificates.crt"
	// pmTrimuiLibDir is where stock TrimUI keeps SDL2 and its own libraries.
	pmTrimuiLibDir = "/usr/trimui/lib"
	// pmBashProbe needs a bash array and a process substitution, the two
	// things PortMaster's scripts use that BusyBox's sh has not.
	pmBashProbe = `a=(jukahub bash); cat <(echo "${a[1]}-${#a[@]}")`
	// pmBashProbeOK is what pmBashProbe prints in a real bash.
	pmBashProbeOK = "bash-2"
	// pmTmpDirName is the tmpfs folder for the bash shim and the SDL2 links
	// (not the udevadm shim folder, which is on the port's PATH).
	pmTmpDirName = "jukahub-port-runtime"
	// pmBarWaitMax bounds the wait for another install to free the card
	// (pmBarWaitMax × 2 s).
	pmBarWaitMax = 300
)

// errPortPreparing says a port's start waits for the firmware check or a
// download; it starts by itself afterwards. Callers do not show it.
var errPortPreparing = errors.New("getting PortMaster ready")

// pmRuntimeCheck is what the firmware check found.
type pmRuntimeCheck struct {
	Done       bool
	BashOK     bool   // the bash a port script gets (first on PATH) works
	BashPath   string // that bash
	BashGood   string // the first bash on PATH that works, "" when none
	BashNote   string
	PythonOK   bool
	PythonPath string
	PythonNote string
	PythonByEx bool // TRIMUI_EX's Python, which ex_config puts on PATH
	// BinBashOK: /bin/bash itself is a real bash, which a script's own
	// `#!/bin/bash` line gets (portmaster_binbash.go). BinBashNote says why not.
	BinBashOK   bool
	BinBashNote string
}

var pmRuntime struct {
	mu        sync.Mutex
	check     pmRuntimeCheck
	running   bool
	retry     func()
	attempted time.Time // when the last download attempt ended
	pyTried   bool      // a download included Python this session
	err       string
	warned    bool
}

// pmSDLLogged makes pmSDLDir log the links it made once a session.
var pmSDLLogged atomic.Bool

// pmRuntimeApplies is true on an ARM64 TrimUI, the firmware this is for.
func pmRuntimeApplies() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && IsTrimuiDevice()
}

func pmRuntimeSnapshot() pmRuntimeCheck {
	pmRuntime.mu.Lock()
	defer pmRuntime.mu.Unlock()
	return pmRuntime.check
}

// pmFirmwareHasNoBash reports a TrimUI whose session check found no working
// bash anywhere on the firmware's PATH (only BusyBox's), so a script the
// firmware itself starts runs under BusyBox (ports_env.go).
func pmFirmwareHasNoBash() bool {
	if !pmRuntimeApplies() {
		return false
	}
	c := pmRuntimeSnapshot()
	return c.Done && !c.BashOK && c.BashGood == ""
}

// pmRuntimeForget makes the next launch check the firmware again (after a
// PortMaster repair).
func pmRuntimeForget() {
	pmRuntime.mu.Lock()
	if !pmRuntime.running {
		pmRuntime.check = pmRuntimeCheck{}
		pmRuntime.attempted = time.Time{}
		pmRuntime.pyTried = false
		pmRuntime.warned = false
	}
	pmRuntime.mu.Unlock()
}

// pmLookPathAll is every executable name on env's PATH, in order, without
// repeats.
func pmLookPathAll(env []string, name string) []string {
	p, _ := childEnvLookup(env, "PATH")
	var out []string
	seen := map[string]bool{}
	for _, dir := range strings.Split(p, ":") {
		if dir = strings.TrimSpace(dir); dir == "" {
			continue
		}
		c := filepath.Join(dir, name)
		if !seen[c] && executableFile(c) {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// pmLookPath finds name on env's PATH, else "".
func pmLookPath(env []string, name string) string {
	if all := pmLookPathAll(env, name); len(all) > 0 {
		return all[0]
	}
	return ""
}

// pmLauncherReadsExConfig reports whether PortMaster's launcher sources
// TRIMUI_EX's ex_config.
func pmLauncherReadsExConfig(launcher string) bool {
	if launcher == "" {
		return false
	}
	head, err := gameDiagReadHead(launcher)
	return err == nil && strings.Contains(string(head), portMasterExConfig)
}

// pmExConfigPresent reports whether this card has TRIMUI_EX's ex_config.
func pmExConfigPresent() bool {
	return regularFile(filepath.Join(portMasterSDRoot, filepath.FromSlash(portMasterExConfig)))
}

// pmEnsureDevFD links /dev/fd (and /dev/stdin…) to /proc/self/fd when mdev
// did not: bash's process substitution opens /dev/fd/N. Off the main thread.
func pmEnsureDevFD() {
	if _, err := os.Lstat("/dev/fd"); err == nil || !dirExists("/proc/self/fd") {
		return
	}
	if err := os.Symlink("/proc/self/fd", "/dev/fd"); err != nil {
		logDebug("portmaster", "could not link /dev/fd: %v", err)
		return
	}
	logInfo("portmaster", "linked /dev/fd to /proc/self/fd (bash process substitution)")
	for i, n := range []string{"stdin", "stdout", "stderr"} {
		if _, err := os.Lstat("/dev/" + n); os.IsNotExist(err) {
			_ = os.Symlink(fmt.Sprintf("/proc/self/fd/%d", i), "/dev/"+n)
		}
	}
}

// pmBashWorks runs the bash probe with shell in env.
func pmBashWorks(ctx context.Context, shell string, env []string) bool {
	bctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := hiddenCommandContext(bctx, shell, "-c", pmBashProbe)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return err == nil && bytes.Contains(out, []byte(pmBashProbeOK))
}

// pmRuntimeProbe checks the firmware's bash and python3 in env (the
// environment a port gets, before JukaHub's own additions). Off the main
// thread: it starts the programs.
func pmRuntimeProbe(env []string, launcher string) pmRuntimeCheck {
	c := pmRuntimeCheck{Done: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pmEnsureDevFD()
	candidates := pmLookPathAll(env, "bash")
	if shell := portShell(env); filepath.Base(shell) == "bash" {
		c.BashPath = shell
		if len(candidates) == 0 || candidates[0] != shell {
			candidates = append([]string{shell}, candidates...)
		}
	}
	for i, shell := range candidates {
		if i >= 4 {
			break
		}
		if pmBashWorks(ctx, shell, env) {
			c.BashGood = shell
			c.BashOK = shell == c.BashPath
			break
		}
	}
	switch {
	case c.BashOK:
	case c.BashPath == "":
		c.BashNote = "the firmware has no bash"
	default:
		c.BashNote = c.BashPath + " cannot run bash arrays (BusyBox's sh)"
		if resolved, err := filepath.EvalSymlinks(c.BashPath); err == nil && resolved != c.BashPath {
			c.BashNote += "; it is " + resolved
		}
	}
	c.BinBashOK, c.BinBashNote = pmBinBashProbe(ctx, env, c)
	switch py := pmLookPath(env, "python3"); {
	case pmLauncherReadsExConfig(launcher) && pmExConfigPresent() && executableFile(pmExPython):
		c.PythonOK, c.PythonByEx, c.PythonPath = true, true, pmExPython
		c.PythonNote = "TRIMUI_EX's Python, set up by ex_config"
	case py == "":
		c.PythonNote = "the firmware has no python3"
	default:
		c.PythonPath = py
		if err := pmPythonProbe(ctx, py, env); err != nil {
			c.PythonNote = py + " cannot run PortMaster: " + err.Error()
		} else {
			c.PythonOK = true
		}
	}
	return c
}

// pmRuntimeNeeds is what still has to be downloaded for this check.
func pmRuntimeNeeds(c pmRuntimeCheck, master bool) (python, bash bool) {
	return master && c.Done && !c.PythonOK && !pmPythonReady(),
		c.Done && !c.BashOK && c.BashGood == "" && !privateGlibcBashReady()
}

// pmRuntimeGate runs on the main thread before a port or PortMaster starts.
// nil: go ahead now. errPortPreparing: the start waits for the check or a
// download, and retry runs on the main thread afterwards. Anything else: why
// it cannot start. A retry after a download never starts another one: the
// attempt time is set when a download ends, failed or not.
func pmRuntimeGate(master bool, retry func()) error {
	if !pmRuntimeApplies() {
		return nil
	}
	pmRuntime.mu.Lock()
	if pmRuntime.running {
		pmRuntime.retry = retry // the last request wins
		pmRuntime.mu.Unlock()
		ShowToast("Getting PortMaster ready…", ToastKindInfo)
		return errPortPreparing
	}
	c, attempted, pyTried := pmRuntime.check, pmRuntime.attempted, pmRuntime.pyTried
	pmRuntime.mu.Unlock()
	needPython, needBash := pmRuntimeNeeds(c, master)
	if c.Done && !needPython && !needBash {
		return nil
	}
	online := IsOnline()
	if !c.Done || (online && (time.Since(attempted) > pmRuntimeRetryAfter || (needPython && !pyTried))) {
		pmRuntimeStart(portFirmwareEnv(appConfig), portMasterLauncher(portMasterSDRoot), master, online, retry)
		return errPortPreparing
	}
	if needPython {
		return errors.New(pmRuntimePythonMissing(online))
	}
	// Only bash is missing: the firmware's shell may still do for an older
	// PortMaster or a simple port. Said once a session.
	pmRuntime.mu.Lock()
	warned := pmRuntime.warned
	pmRuntime.warned = true
	pmRuntime.mu.Unlock()
	if !warned {
		msg := "This firmware's bash is BusyBox, which PortMaster's scripts may not run on — connect to Wi-Fi once so JukaHub can add a full bash"
		if online {
			msg = "Could not add a full bash for PortMaster (" + firstLine(pmRuntimeLastError()) + ") — starting with the firmware's"
		}
		ShowToast(msg, ToastKindWarning)
	}
	return nil
}

func pmRuntimeLastError() string {
	pmRuntime.mu.Lock()
	defer pmRuntime.mu.Unlock()
	return setOr(pmRuntime.err, "unknown error")
}

// pmRuntimePythonMissing says why PortMaster cannot open without Python.
func pmRuntimePythonMissing(online bool) string {
	if !online {
		return "PortMaster needs Python 3, which this firmware does not have — connect to Wi-Fi so JukaHub can add it (about 30 MB, once)"
	}
	return "PortMaster needs Python 3 and JukaHub could not add it: " + firstLine(pmRuntimeLastError())
}

// pmRuntimeStart checks the firmware in the background and, when something
// is missing and the handheld is online, has the main thread start the
// downloads (Python only for PortMaster itself); retry runs on the main thread
// once all of it is over. env and launcher are read on the main thread.
func pmRuntimeStart(env []string, launcher string, master, online bool, retry func()) {
	pmRuntime.mu.Lock()
	if pmRuntime.running {
		if retry != nil {
			pmRuntime.retry = retry
		}
		pmRuntime.mu.Unlock()
		return
	}
	pmRuntime.running = true
	pmRuntime.retry = retry
	pmRuntime.mu.Unlock()
	go func() {
		handedOver := false
		defer func() {
			if r := recover(); r != nil {
				logWarn("portmaster", "checking the firmware for PortMaster failed: %v", r)
				pmRuntime.mu.Lock()
				if !pmRuntime.check.Done {
					// Counted as checked, so the next launch does not probe again.
					pmRuntime.check = pmRuntimeCheck{Done: true, BashNote: "the check failed", PythonNote: "the check failed"}
				}
				// And as an attempt, so the retry does not start another at once.
				pmRuntime.attempted, pmRuntime.pyTried, pmRuntime.err = time.Now(), true, fmt.Sprint(r)
				pmRuntime.mu.Unlock()
				handedOver = false
			}
			if !handedOver {
				pmRuntimeFinish(nil, false, false)
			}
		}()
		c := pmRuntimeProbe(env, launcher)
		pmRuntime.mu.Lock()
		pmRuntime.check = c
		pmRuntime.mu.Unlock()
		logInfo("portmaster", "firmware check: bash ok=%v %s; python3 ok=%v %s", c.BashOK || c.BashGood != "",
			setOr(c.BashGood, c.BashNote), c.PythonOK, setOr(c.PythonNote, c.PythonPath))
		needPython, needBash := pmRuntimeNeeds(c, master)
		if !online || (!needPython && !needBash) {
			return
		}
		handedOver = true
		tbxPost(func() { pmRuntimeBeginDownloads(needPython, needBash, 0) })
	}()
}

// pmRuntimeBeginDownloads shows the install card and downloads what is
// missing. Main thread. It waits while another install has the card, since a
// new run would cancel that one.
func pmRuntimeBeginDownloads(needPython, needBash bool, waited int) {
	if installBarActive() && waited < pmBarWaitMax {
		go func() {
			time.Sleep(2 * time.Second)
			tbxPost(func() { pmRuntimeBeginDownloads(needPython, needBash, waited+1) })
		}()
		return
	}
	gen, barCtx := installBarBeginRun("PortMaster's runtime")
	pmRuntime.mu.Lock()
	pmRuntime.pyTried = pmRuntime.pyTried || needPython
	pmRuntime.err = ""
	pmRuntime.mu.Unlock()
	go func() {
		var failure error
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Errorf("%v", r)
			}
			if failure != nil {
				logWarn("portmaster", "runtime install: %v", failure)
				installBarFailRun(gen, firstLine(failure.Error()))
			} else {
				installBarDoneRun(gen)
			}
			// Stopped from the card's X (or taken over by another install):
			// the player chose not to wait, so nothing starts by itself.
			pmRuntimeFinish(failure, true, failure != nil && barCtx.Err() != nil)
		}()
		ctx, cancel := context.WithTimeout(barCtx, 25*time.Minute)
		defer cancel()
		progress := func(done, total int64) { installBarUpdateRun(gen, installPhaseDownload, done, total) }
		var errs []string
		if needPython {
			installBarUpdateRun(gen, installPhaseResolving, 0, 0)
			if err := pmPythonInstall(ctx, progress, func(phase string) { installBarUpdateRun(gen, phase, 0, 0) }); err != nil {
				errs = append(errs, "Python 3: "+err.Error())
			}
		}
		if needBash && ctx.Err() == nil {
			installBarUpdateRun(gen, installPhaseResolving, 0, 0)
			if err := privateGlibcEnsureBash(ctx, progress); err != nil {
				errs = append(errs, "bash: "+err.Error())
			}
		}
		if len(errs) > 0 {
			failure = errors.New(strings.Join(errs, "; "))
		}
	}()
}

// pmRuntimeFinish ends a check or a download: it records the outcome and runs
// the waiting launch on the main thread — unless it was cancelled, or a game
// started meanwhile (a download can take minutes), when it only says so.
func pmRuntimeFinish(failure error, downloaded, cancelled bool) {
	pmRuntime.mu.Lock()
	pmRuntime.running = false
	if downloaded {
		pmRuntime.attempted = time.Now()
	}
	if failure != nil {
		pmRuntime.err = failure.Error()
	}
	next := pmRuntime.retry
	pmRuntime.retry = nil
	pmRuntime.mu.Unlock()
	if next == nil || cancelled {
		return
	}
	tbxPost(func() {
		if gameScreenSuspended() {
			if failure == nil {
				ShowToast("PortMaster is ready — open it again when you like", ToastKindInfo)
			}
			return
		}
		next()
	})
}

// pmRuntimePrefetch gets what ports need from the firmware (a full bash) ready
// in the background right after PortMaster was installed, repaired or updated,
// so the first port does not wait. Python is PortMaster's own menu's alone —
// JukaHub installs and runs ports without it (ports_native_install.go) — so it
// is fetched only when that menu is opened. Main thread.
func pmRuntimePrefetch() {
	if !pmRuntimeApplies() || !IsOnline() || portMasterLauncher(portMasterSDRoot) == "" {
		return
	}
	pmRuntimeStart(portFirmwareEnv(appConfig), portMasterLauncher(portMasterSDRoot), false, true, nil)
}

// pmBashShimDir is a tmpfs folder whose `bash` runs good — a real bash the
// firmware has further down its PATH than the BusyBox one. "" on failure.
func pmBashShimDir(good string) string {
	dir := filepath.Join(os.TempDir(), pmTmpDirName, "bash")
	p := filepath.Join(dir, "bash")
	text := "#!/bin/sh\n# Written by JukaHub: the firmware's first bash is BusyBox; this one is real.\nexec " + shellQuote(good) + " \"$@\"\n"
	if old, err := os.ReadFile(p); err != nil || string(old) != text {
		if os.MkdirAll(dir, 0o755) != nil || os.WriteFile(p, []byte(text), 0o755) != nil {
			return ""
		}
	}
	if !executableFile(p) && os.Chmod(p, 0o755) != nil {
		return ""
	}
	return dir
}

// pmLooksBusyBox reports a bash that is BusyBox (or missing), for a launch
// before the check ran (the Ports Store's harbourmaster).
func pmLooksBusyBox(shell string) bool {
	if filepath.Base(shell) != "bash" {
		return true
	}
	resolved, err := filepath.EvalSymlinks(shell)
	return err == nil && strings.Contains(filepath.Base(resolved), "busybox")
}

// pmRuntimeEnv adds JukaHub's runtime to a port's environment on a TrimUI
// (see the file comment); env is returned unchanged elsewhere. Before the
// session's check it decides from what is on the card alone.
func pmRuntimeEnv(env []string) []string {
	if !pmRuntimeApplies() {
		return env
	}
	c := pmRuntimeSnapshot()
	set := map[string]string{}
	var front []string
	switch {
	case c.Done && !c.BashOK && c.BashGood != "":
		if d := pmBashShimDir(c.BashGood); d != "" {
			front = append(front, d)
		}
	case (c.Done && !c.BashOK) || (!c.Done && pmLooksBusyBox(portShell(env))):
		if d := privateGlibcBashWrapperDir(); d != "" {
			front = append(front, d)
		}
	}
	ownPython := false
	if (c.Done && !c.PythonOK) || (!c.Done && pmLookPath(env, "python3") == "" && !pmExConfigPresent()) {
		if d := pmPythonBinDir(); d != "" {
			front = append(front, d)
			ownPython = true
			pmPythonSiteCheck(d) // its sitecustomize.py (portmaster_python.go)
		}
	}
	if len(front) > 0 {
		p := strings.Join(front, ":")
		if cur, _ := childEnvLookup(env, "PATH"); cur != "" {
			p += ":" + cur
		}
		set["PATH"] = p
	}
	if ownPython {
		// The downloaded Python's OpenSSL looks for a CA bundle where Debian
		// keeps one; the firmware's is elsewhere.
		if v, _ := childEnvLookup(env, "SSL_CERT_FILE"); strings.TrimSpace(v) == "" && regularFile(pmFirmwareCA) {
			set["SSL_CERT_FILE"] = pmFirmwareCA
		}
	}
	sdl := ""
	if v, _ := childEnvLookup(env, "PYSDL2_DLL_PATH"); strings.TrimSpace(v) == "" && !c.PythonByEx {
		if sdl = pmSDLDir(); sdl != "" {
			set["PYSDL2_DLL_PATH"] = sdl
		}
	}
	if ownPython {
		// For the sitecustomize.py in JukaHub's Python: PortMaster's launcher
		// may export PYSDL2_DLL_PATH=/usr/lib over the value above.
		if sdl == "" {
			sdl = pmSDLDir()
		}
		if sdl != "" {
			set[pmPythonSDLEnv] = sdl
		}
	}
	if dirExists(pmTrimuiLibDir) {
		cur, _ := childEnvLookup(env, "LD_LIBRARY_PATH")
		if !pmPathListHas(cur, pmTrimuiLibDir) {
			if cur == "" {
				set["LD_LIBRARY_PATH"] = pmTrimuiLibDir
			} else {
				set["LD_LIBRARY_PATH"] = cur + ":" + pmTrimuiLibDir
			}
		}
	}
	if len(set) > 0 {
		env = mergeGameProfileEnv(env, set)
	}
	if ownPython {
		// A firmware Python's home or path would point the downloaded one at
		// another standard library.
		env = pmEnvDrop(env, "PYTHONHOME", "PYTHONPATH")
	}
	return env
}

// pmEnvDrop is env without the named variables.
func pmEnvDrop(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, key := range keys {
			if k == key {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

func pmPathListHas(list, dir string) bool {
	for _, d := range strings.Split(list, ":") {
		if filepath.Clean(strings.TrimSpace(d)) == dir {
			return true
		}
	}
	return false
}

// pmSDLDir is a folder where the firmware's SDL2 libraries can be found under
// every name pysdl2 tries (libSDL2.so, libSDL2-2.0.so, libSDL2-2.0.so.0 and
// the same for SDL2_ttf, _image, _mixer, _gfx): links in tmpfs to the files in
// /usr/trimui/lib (or /usr/lib). "" when the firmware has no SDL2.
func pmSDLDir() string {
	src := ""
	for _, d := range []string{pmTrimuiLibDir, "/usr/lib"} {
		if regularFile(filepath.Join(d, "libSDL2-2.0.so.0")) {
			src = d
			break
		}
	}
	if src == "" {
		return ""
	}
	dir := filepath.Join(os.TempDir(), pmTmpDirName, "sdl2")
	if os.MkdirAll(dir, 0o755) != nil {
		return src
	}
	matches, _ := filepath.Glob(filepath.Join(src, "libSDL2*.so*"))
	var names []string
	for _, m := range matches {
		base := filepath.Base(m)
		i := strings.Index(base, ".so")
		if i <= 0 || !regularFile(m) {
			continue
		}
		stem := base[:i]
		short, _, _ := strings.Cut(stem, "-")
		for _, n := range []string{base, stem + ".so", short + ".so"} {
			link := filepath.Join(dir, n)
			if _, err := os.Lstat(link); err == nil || os.Symlink(m, link) == nil {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		return src
	}
	if pmSDLLogged.CompareAndSwap(false, true) {
		logInfo("portmaster", "PYSDL2_DLL_PATH=%s: %s", dir, strings.Join(names, " "))
	}
	return dir
}

// pmRuntimeNotes are the launch log's lines about the runtime.
func pmRuntimeNotes(master bool) []string {
	if !pmRuntimeApplies() {
		return nil
	}
	c := pmRuntimeSnapshot()
	if !c.Done {
		return []string{"the firmware's bash and python3 were not checked"}
	}
	var notes []string
	switch {
	case c.BashOK:
		notes = append(notes, "bash: the firmware's "+c.BashPath)
	case c.BashGood != "":
		notes = append(notes, "bash: the firmware's "+c.BashGood+" through a shim — "+c.BashNote)
	case privateGlibcBashReady():
		notes = append(notes, "bash: JukaHub's (Ubuntu's bash on the private glibc "+privateGlibcVersion+") — "+c.BashNote)
	default:
		notes = append(notes, "bash: none usable — "+c.BashNote)
	}
	if master {
		switch {
		case c.PythonOK:
			notes = append(notes, "python3: "+setOr(c.PythonNote, "the firmware's "+c.PythonPath))
		case pmPythonReady():
			notes = append(notes, "python3: JukaHub's ("+pmPythonBinDir()+") — "+c.PythonNote)
		default:
			notes = append(notes, "python3: none usable — "+c.PythonNote)
		}
	}
	return notes
}

// pmLauncherPython is the Python the patched launcher names instead of
// TRIMUI_EX's: JukaHub's when PortMaster gets it, else the firmware's.
func pmLauncherPython() string {
	c := pmRuntimeSnapshot()
	if c.Done && !c.PythonOK {
		if d := pmPythonBinDir(); d != "" {
			return filepath.Join(d, "python3")
		}
	}
	return c.PythonPath
}

// pmMasterLauncher is the script to run for PortMaster's launch.sh: the file
// itself, or — on a TrimUI card without TRIMUI_EX, when the launcher reads
// ex_config — a patched copy beside it, also returned as the file to delete
// after PortMaster exits. Notes go to the launch log.
func pmMasterLauncher(launcher string) (run, cleanup string, notes []string) {
	if !pmRuntimeApplies() || !pmLauncherReadsExConfig(launcher) || pmExConfigPresent() {
		return launcher, "", nil
	}
	src, err := os.ReadFile(launcher)
	if err != nil || len(src) > 256<<10 {
		return launcher, "", nil
	}
	python := ""
	if !executableFile(pmExPython) {
		python = pmLauncherPython()
	}
	patched, changed := pmPatchLauncher(src, python)
	if !changed {
		return launcher, "", []string{filepath.Base(launcher) + " reads " + portMasterExConfig + " in a form JukaHub does not change"}
	}
	shadow := filepath.Join(filepath.Dir(launcher), pmShadowLauncherName)
	if err := os.WriteFile(shadow, patched, 0o755); err != nil {
		return launcher, "", []string{"could not write " + shadow + ": " + err.Error()}
	}
	return shadow, shadow, []string{"running a copy of " + filepath.Base(launcher) + " (" + pmShadowLauncherName +
		") that skips " + portMasterExConfig + ", which this card does not have (no TRIMUI_EX)"}
}

// pmWordEnd reports whether s[i:] starts where a path word ends.
func pmWordEnd(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	ch := s[i]
	return !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-' || ch == '/')
}

// pmPatchLauncher makes PortMaster's launcher run without TRIMUI_EX: a line
// that sources ex_config runs only when the file exists, and, with python
// set, TRIMUI_EX's python3 path (the whole word only) becomes python. Lines
// with quotes, command substitution, a trailing `&` or a continuation in the
// command are left alone. Pure.
func pmPatchLauncher(src []byte, python string) ([]byte, bool) {
	lines := strings.SplitAfter(string(src), "\n")
	changed := false
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		ending := line[len(body):]
		trimmed := strings.TrimSpace(body)
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		if strings.Contains(trimmed, portMasterExConfig) && (strings.HasPrefix(trimmed, "source ") || strings.HasPrefix(trimmed, ". ")) {
			cmd := trimmed
			if j := strings.Index(cmd, " #"); j > 0 {
				cmd = strings.TrimSpace(cmd[:j])
			}
			cmd = strings.TrimSpace(strings.TrimSuffix(cmd, ";"))
			if strings.HasSuffix(cmd, "\\") || (strings.HasSuffix(cmd, "&") && !strings.HasSuffix(cmd, "&&")) {
				continue
			}
			fields := strings.Fields(cmd)
			if len(fields) < 2 {
				continue
			}
			file := strings.Trim(fields[1], `"'`)
			if file == "" || strings.ContainsAny(file, "\"'`\\;&|()<>") || strings.Contains(file, "$(") {
				continue
			}
			lines[i] = indent + `if [ -f "` + file + `" ]; then ` + cmd + "; fi" + ending
			changed = true
			continue
		}
		if python == "" || !strings.Contains(body, pmExPython) {
			continue
		}
		var b strings.Builder
		rest := body
		for {
			j := strings.Index(rest, pmExPython)
			if j < 0 {
				b.WriteString(rest)
				break
			}
			end := j + len(pmExPython)
			b.WriteString(rest[:j])
			if pmWordEnd(rest, end) {
				b.WriteString(python)
				changed = true
			} else {
				b.WriteString(pmExPython)
			}
			rest = rest[end:]
		}
		lines[i] = b.String() + ending
	}
	return []byte(strings.Join(lines, "")), changed
}

// pmRuntimeIssues are the diagnosis lines about the runtime: bash for every
// port, Python for PortMaster itself.
func pmRuntimeIssues(master bool) []launchIssue {
	if !pmRuntimeApplies() {
		return nil
	}
	c := pmRuntimeSnapshot()
	if !c.Done {
		return nil
	}
	var issues []launchIssue
	if !c.BashOK && c.BashGood == "" && !privateGlibcBashReady() {
		issues = append(issues, launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "This firmware has no full bash",
			Detail: c.BashNote + ". PortMaster's scripts use bash arrays. JukaHub adds Ubuntu's bash on its private C runtime when it opens PortMaster with Wi-Fi on" + pmRuntimeErrSuffix() + ".",
		})
	}
	if master && !c.PythonOK && !pmPythonReady() {
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "PortMaster needs Python 3",
			Detail: c.PythonNote + ". JukaHub downloads a Python 3 for PortMaster (about 30 MB) when you open PortMaster with Wi-Fi on" + pmRuntimeErrSuffix() + ".",
		})
	}
	return issues
}

func pmRuntimeErrSuffix() string {
	pmRuntime.mu.Lock()
	defer pmRuntime.mu.Unlock()
	if pmRuntime.err == "" {
		return ""
	}
	return " (last try: " + firstLine(pmRuntime.err) + ")"
}

// pmRuntimeQuickExit is the likeliest reason for a PortMaster that closed at
// once, or "".
func pmRuntimeQuickExit() string {
	issues := pmRuntimeIssues(true)
	if len(issues) == 0 {
		return ""
	}
	return "PortMaster closed at once: " + issues[0].Title + " — " + hintKeyFor("Y", "Options") + " on its row, then Why didn't it open?"
}

