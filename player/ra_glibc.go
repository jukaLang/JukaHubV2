package main

// ra_glibc.go — RetroArch on JukaHub's private C library, so the newest cores
// load.
//
// A libretro core is a shared object loaded into RetroArch's own process, so a
// core can only use the C library RetroArch started on. The TrimUI firmware's
// is glibc 2.33, and the libretro buildbot builds its linux/aarch64 nightlies on
// a newer Ubuntu: in November 2026 its mGBA needed GLIBC_2.35. JukaHub's updater
// therefore refused those builds (elfcompat.go) and the newest cores never
// reached the handheld.
//
// JukaHub already keeps a private glibc 2.39 runtime for PortMaster and
// GreenOvercast (glibc_runtime.go): Ubuntu's libc6, libstdc++6 and libgcc-s1
// from Ubuntu's signed index. This file starts RetroArch itself through that
// runtime's loader when a launch needs it, so a core built for glibc up to 2.39
// (and libstdc++ up to GCC 14) loads there — and the updater may install it,
// because the load check reads the runtime's libraries first (compat.go,
// coreLoadEnvWith).
//
// How, without changing the firmware or the card's RetroArch: the launch runs
// in a mount namespace of its own (CLONE_NEWNS; its mounts end with the game's
// last process). There a setup script bind-mounts a small wrapper over each
// RetroArch binary, so the firmware's launch.sh — which calls
// $RA_DIR/ra64.trimui by path — starts the wrapper instead. The wrapper execs
// the private loader with the runtime first on the library path and the real
// binary as the program. Two more bind mounts keep RetroArch's view of itself:
// the loader is reached as <ns>/v<i>/ra64.trimui inside a bind of RetroArch's
// folder, so /proc/self/exe names a file called ra64.trimui in a folder holding
// RetroArch's own files (its ":" paths and the process name stay right), and
// the real binary is reached through a second bind of the folder (<ns>/r<i>).
// JukaHub, MainUI and everything else outside the game see the card unchanged.
//
// When — the setting "RetroArch's C library" (retroArchGlibc):
//   - auto (default): only a launch whose core cannot load on the firmware's
//     glibc and can on the runtime (RetroArch's own menu too, which may load
//     any core, once the runtime is installed); every other game starts
//     exactly as before;
//   - always: every RetroArch launch from JukaHub;
//   - off: never, and the load check is the firmware's alone again, so
//     corefix.go puts builds that run on the firmware back.
//
// Safety: a RetroArch starts on the runtime only after a probe. Its libraries
// are listed and bound, as `ldd -r` does, once by the firmware's loader and
// once through the same namespace, wrapper and private loader a launch uses; a
// library or symbol only the second run cannot find, or a namespace that cannot
// be made, keeps that RetroArch on the firmware's glibc. The answer is kept per
// RetroArch build, runtime build and library path (private-glibc/ra/probe.json).
// A launch on the runtime that dies in the loader marks it too
// (raGlibcLauncherIssue, from the game diagnosis).
//
// Not covered: a game started from the firmware's own menu (MainUI) runs
// RetroArch on the firmware's glibc, so a core that needs the runtime does not
// load there.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// raGlibcSetting holds the mode: "auto", "always" or "off".
	raGlibcSetting = "retroArchGlibc"
	raGlibcAuto    = "auto"
	raGlibcAlways  = "always"
	raGlibcOff     = "off"
	// raGlibcProbeVersion is raised when the probe learns to read more, so the
	// answers kept are asked again.
	raGlibcProbeVersion = 1
	// raGlibcProbeTimeout bounds each half of a probe.
	raGlibcProbeTimeout = 8 * time.Second
	// raGlibcRuntimeTTL is how long one look at the installed runtime is used:
	// the Settings row and every core's load check ask for it.
	raGlibcRuntimeTTL = 10 * time.Second
	// raGlibcInstallRetry spaces launch-time downloads after one failed.
	raGlibcInstallRetry = 10 * time.Minute
	// raGlibcMaxRetroArchs bounds the RetroArch binaries one launch wraps.
	raGlibcMaxRetroArchs = 4
	// raGlibcOutputMax bounds what a probe keeps of a loader's listing.
	raGlibcOutputMax = 64 << 10
)

// raGlibcModes are the setting's stops, in the row's order.
var raGlibcModes = []string{raGlibcAuto, raGlibcAlways, raGlibcOff}

// raGlibcModeIdx mirrors the setting for the goroutines (the load check, the
// updater, corefix): the Custom map belongs to the main thread. 0 is auto.
var raGlibcModeIdx atomic.Int32

func raGlibcModeFrom(c *Config) string {
	if c != nil {
		if s, ok := customString(c.Variables.Custom, raGlibcSetting); ok {
			s = strings.ToLower(strings.TrimSpace(s))
			if slices.Contains(raGlibcModes, s) {
				return s
			}
		}
	}
	return raGlibcAuto
}

// raGlibcSyncMode stores c's mode for the goroutines (main thread: at start,
// startCoreAutoUpdate, and when the row changes it).
func raGlibcSyncMode(c *Config) {
	raGlibcModeIdx.Store(int32(slices.Index(raGlibcModes, raGlibcModeFrom(c))))
}

func raGlibcMode() string {
	i := int(raGlibcModeIdx.Load())
	if i < 0 || i >= len(raGlibcModes) {
		return raGlibcAuto
	}
	return raGlibcModes[i]
}

func raGlibcModeLabel(mode string) string {
	switch mode {
	case raGlibcAlways:
		return "JukaHub's glibc " + privateGlibcVersion
	case raGlibcOff:
		return "Firmware's only"
	}
	return "Automatic"
}

// raGlibcSupported reports a device whose RetroArch the runtime can start: the
// 64-bit TrimUI handhelds, the platform the runtime is built for.
func raGlibcSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && IsTrimuiDevice()
}

// raGlibcWanted reports that RetroArch may start on the runtime here.
func raGlibcWanted() bool { return raGlibcSupported() && raGlibcMode() != raGlibcOff }

// ── The installed runtime ────────────────────────────────────────────────

var raGlibcRuntimeMemo struct {
	mu     sync.Mutex
	at     time.Time
	loader string
	libDir string
	ok     bool
}

// raGlibcRuntime is privateGlibcPaths, looked at once per raGlibcRuntimeTTL.
func raGlibcRuntime() (loader, libDir string, ok bool) {
	m := &raGlibcRuntimeMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	if since := time.Since(m.at); m.at.IsZero() || since < 0 || since >= raGlibcRuntimeTTL {
		m.loader, m.libDir, m.ok = privateGlibcPaths()
		m.at = time.Now()
	}
	return m.loader, m.libDir, m.ok
}

// raGlibcRuntimeForget drops that look: an install just finished.
func raGlibcRuntimeForget() {
	raGlibcRuntimeMemo.mu.Lock()
	raGlibcRuntimeMemo.at = time.Time{}
	raGlibcRuntimeMemo.mu.Unlock()
}

// ── Which RetroArch ──────────────────────────────────────────────────────

var raGlibcInterpMemo struct {
	mu sync.Mutex
	m  map[string][2]string // path → {stamp, interpreter}
}

// raGlibcInterp is the ELF interpreter of a RetroArch the runtime can start (a
// dynamically linked 64-bit ARM program), "" for anything else: a 32-bit
// ra32.trimui, a static build, a wrapper script, a symbolic link.
func raGlibcInterp(ra string) string {
	if strings.TrimSpace(ra) == "" {
		return ""
	}
	st, err := os.Lstat(ra)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	stamp := coreFixFileStamp(st)
	raGlibcInterpMemo.mu.Lock()
	hit, ok := raGlibcInterpMemo.m[ra]
	raGlibcInterpMemo.mu.Unlock()
	if ok && hit[0] == stamp {
		return hit[1]
	}
	interp := ""
	if f, err := elf.Open(ra); err == nil {
		if f.Class == elf.ELFCLASS64 && f.Machine == elf.EM_AARCH64 {
			for _, p := range f.Progs {
				if p.Type != elf.PT_INTERP || p.Filesz == 0 || p.Filesz > 4096 {
					continue
				}
				buf := make([]byte, p.Filesz)
				if _, err := p.ReadAt(buf, 0); err == nil {
					interp = strings.TrimRight(string(buf), "\x00")
				}
				break
			}
		}
		_ = f.Close()
	}
	if !filepath.IsAbs(interp) || !regularFile(interp) {
		interp = ""
	}
	raGlibcInterpMemo.mu.Lock()
	if raGlibcInterpMemo.m == nil || len(raGlibcInterpMemo.m) > 32 {
		raGlibcInterpMemo.m = map[string][2]string{}
	}
	raGlibcInterpMemo.m[ra] = [2]string{stamp, interp}
	raGlibcInterpMemo.mu.Unlock()
	return interp
}

// raGlibcResolve is a RetroArch binary's real path (a symbolic link is
// followed: a bind mount over a link would land on its target anyway), "" when
// it is not one the runtime can start.
func raGlibcResolve(ra string) string {
	if strings.TrimSpace(ra) == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(ra)
	if err != nil {
		return ""
	}
	real = filepath.Clean(real)
	if raGlibcInterp(real) == "" {
		return ""
	}
	return real
}

// raGlibcRetroArchs are the RetroArch binaries a launch's namespace wraps:
// preferred first, then the owner of each cores folder — every one a launch
// script might call — leaving out those a probe refused under libDir.
func raGlibcRetroArchs(preferred, libDir string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ra string) {
		ra = raGlibcResolve(ra)
		if ra == "" || seen[ra] || len(out) >= raGlibcMaxRetroArchs {
			return
		}
		seen[ra] = true
		if st, _ := raGlibcProbeState(ra, libDir); st == raProbeFailed {
			return
		}
		out = append(out, ra)
	}
	add(preferred)
	for _, dir := range retroArchCoreDirs() {
		add(coreRetroArchBinary(dir))
	}
	return out
}

// ── Probe answers ────────────────────────────────────────────────────────

const (
	raProbeUnknown = iota
	raProbeOK
	raProbeFailed
)

// raGlibcProbeRec is one RetroArch's answer: Key names the RetroArch build,
// the runtime build and the library path it was asked under. Launch marks a
// refusal a real game gave (it stopped in the loader, or its namespace could
// not be set up), which a probe could not take back.
type raGlibcProbeRec struct {
	Key    string `json:"key"`
	OK     bool   `json:"ok"`
	Launch bool   `json:"launch,omitempty"`
	Why    string `json:"why,omitempty"`
	At     string `json:"at,omitempty"`
}

// refused reports a refusal that still holds: a probe's is asked again after
// raGlibcRefusalTTL (by a clock that has moved on since: TrimUI starts before
// NTP); a game's holds until the RetroArch, the runtime or the library path
// changes.
func (rec raGlibcProbeRec) refused() bool {
	if rec.OK {
		return false
	}
	if !rec.Launch {
		if at, err := time.Parse(time.RFC3339, rec.At); err == nil && time.Since(at) > raGlibcRefusalTTL {
			return false
		}
	}
	return true
}

var raGlibcProbes struct {
	mu   sync.Mutex
	path string
	m    map[string]raGlibcProbeRec // by RetroArch path
	// run lets one probe run at a time.
	run sync.Mutex
}

// raGlibcDir holds this file's scripts, mount points and probe answers.
func raGlibcDir() (string, error) {
	base, err := privateGlibcBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ra"), nil
}

var raGlibcFirmwareLDMemo struct {
	mu   sync.Mutex
	done bool
	ld   string
}

// raGlibcFirmwareLD is the LD_LIBRARY_PATH a game gets from the firmware's
// environment (childenv.go), worked out once.
func raGlibcFirmwareLD() string {
	m := &raGlibcFirmwareLDMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.done {
		m.ld, _ = childEnvLookup(screenChildEnv(os.Environ()), "LD_LIBRARY_PATH")
		m.done = true
	}
	return m.ld
}

// raGlibcProbeKey names what an answer for ra holds for, "" when ra cannot be
// read.
func raGlibcProbeKey(ra, libDir string) string {
	st, err := os.Stat(ra)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("v%d|%s|%s|%08x", raGlibcProbeVersion, coreFixFileStamp(st), filepath.Base(libDir),
		crc32.ChecksumIEEE([]byte(raGlibcFirmwareLD())))
}

// raGlibcProbesLoadLocked reads probe.json the first time (and again for
// another data folder).
func raGlibcProbesLoadLocked() {
	dir, err := raGlibcDir()
	if err != nil {
		dir = ""
	}
	file := ""
	if dir != "" {
		file = filepath.Join(dir, "probe.json")
	}
	if raGlibcProbes.m != nil && raGlibcProbes.path == file {
		return
	}
	raGlibcProbes.path = file
	raGlibcProbes.m = map[string]raGlibcProbeRec{}
	if file == "" {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil || len(data) > 256<<10 {
		return
	}
	loaded := map[string]raGlibcProbeRec{}
	if json.Unmarshal(data, &loaded) == nil {
		for ra, rec := range loaded {
			if ra != "" && rec.Key != "" {
				raGlibcProbes.m[ra] = rec
			}
		}
	}
}

func raGlibcProbesSaveLocked() {
	if raGlibcProbes.path == "" {
		return
	}
	data, err := json.MarshalIndent(raGlibcProbes.m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(raGlibcProbes.path), 0o755); err != nil {
		return
	}
	if err := linuxPkgWriteFile(raGlibcProbes.path, append(data, '\n')); err != nil {
		logWarn("emulators", "could not save RetroArch's C library probe: %v", err)
	}
}

// raGlibcRefusalTTL is how long a refusal holds before the probe asks again (a
// refusal from a moment of load or a bad card should not last for ever).
const raGlibcRefusalTTL = 7 * 24 * time.Hour

// raGlibcProbeState is ra's answer under the runtime at libDir, and why a
// refusal was given.
func raGlibcProbeState(ra, libDir string) (int, string) {
	key := raGlibcProbeKey(ra, libDir)
	if key == "" {
		return raProbeFailed, "RetroArch cannot be read"
	}
	raGlibcProbes.mu.Lock()
	defer raGlibcProbes.mu.Unlock()
	raGlibcProbesLoadLocked()
	rec, ok := raGlibcProbes.m[ra]
	switch {
	case !ok || rec.Key != key:
		return raProbeUnknown, ""
	case rec.OK:
		return raProbeOK, ""
	case !rec.refused():
		return raProbeUnknown, ""
	}
	return raProbeFailed, rec.Why
}

// raGlibcProbeRecord keeps an answer; launch marks a refusal a real game gave.
func raGlibcProbeRecord(ra, key string, ok, launch bool, why string) {
	if key == "" {
		return
	}
	raGlibcProbes.mu.Lock()
	defer raGlibcProbes.mu.Unlock()
	raGlibcProbesLoadLocked()
	raGlibcProbes.m[ra] = raGlibcProbeRec{Key: key, OK: ok, Launch: launch && !ok, Why: why, At: time.Now().UTC().Format(time.RFC3339)}
	raGlibcProbesSaveLocked()
}

// raGlibcRefusedUnder lists the RetroArchs refused under the runtime at
// libDir (refusals that still hold), sorted.
func raGlibcRefusedUnder(libDir string) []string {
	tag := "|" + filepath.Base(libDir) + "|"
	raGlibcProbes.mu.Lock()
	defer raGlibcProbes.mu.Unlock()
	raGlibcProbesLoadLocked()
	var out []string
	for ra, rec := range raGlibcProbes.m {
		if rec.refused() && strings.Contains(rec.Key, tag) {
			out = append(out, ra)
		}
	}
	sort.Strings(out)
	return out
}

var raGlibcNamespaceMemo struct {
	mu   sync.Mutex
	done bool
	ok   bool
}

// raGlibcNamespaceOK reports, once a session, that a child can get a mount
// namespace of its own here (root, a kernel that allows it): without one a
// launch on the runtime would not start at all, so the runtime is not used.
func raGlibcNamespaceOK() bool {
	m := &raGlibcNamespaceMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return m.ok
	}
	m.done = true
	sh := raGlibcTool("sh")
	if sh == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-c", ":")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if !raGlibcNamespace(cmd) {
		return false
	}
	if err := cmd.Run(); err != nil {
		logWarn("emulators", "RetroArch stays on the firmware's C library: no mount namespace for a game here (%v)", err)
		return false
	}
	m.ok = true
	return true
}

// raGlibcUsableFor reports whether ra may start on the runtime: wanted,
// installed, a RetroArch the runtime can start, a namespace possible, and not
// refused. A RetroArch not probed yet counts here (the load check); a launch
// probes first.
func raGlibcUsableFor(ra string) (libDir string, ok bool) {
	if !raGlibcWanted() {
		return "", false
	}
	if ra = raGlibcResolve(ra); ra == "" {
		return "", false
	}
	_, libDir, ready := raGlibcRuntime()
	if !ready || !raGlibcNamespaceOK() {
		return "", false
	}
	if st, _ := raGlibcProbeState(ra, libDir); st == raProbeFailed {
		return "", false
	}
	return libDir, true
}

// raGlibcLoadDir is the runtime folder the load check of a core that becomes
// target reads before the firmware's folders (compat.go), "" when the
// RetroArch that owns target's folder does not start on the runtime.
func raGlibcLoadDir(target string) string {
	if strings.TrimSpace(target) == "" || !raGlibcWanted() {
		return ""
	}
	dir, ok := raGlibcUsableFor(coreRetroArchBinary(filepath.Dir(target)))
	if !ok {
		return ""
	}
	return dir
}

// raGlibcStamp names what the load check's use of the runtime rests on, for
// corefix.go's remembered answers: the mode, the runtime build and the
// RetroArchs a probe refused under it.
func raGlibcStamp() string {
	if !raGlibcWanted() {
		return "off"
	}
	_, libDir, ready := raGlibcRuntime()
	if !ready {
		return raGlibcMode() + ":none"
	}
	stamp := raGlibcMode() + ":" + filepath.Base(libDir) + ":" + strings.Join(raGlibcRefusedUnder(libDir), ",")
	// The device libraries beside it count too (coredevlibs.go).
	if dev := coreDevLibsStamp(); dev != "" {
		stamp += ":dev=" + dev
	}
	return stamp
}

// ── Which load problems the runtime closes ───────────────────────────────

// raGlibcGapRe matches elfVersionGap's sentence for a library the runtime
// brings: "needs GLIBC_2.35; this firmware's libm.so.6 has up to GLIBC_2.32".
var raGlibcGapRe = regexp.MustCompile(`needs ((?:GLIBC|GLIBCXX|CXXABI|GCC)_[0-9.]+)(?:; this firmware's|, which this firmware's) (?:libc\.so\.6|libm\.so\.6|libpthread\.so\.0|libdl\.so\.2|librt\.so\.1|libresolv\.so\.2|libstdc\+\+\.so\.6|libgcc_s\.so\.1) `)

// raGlibcCeilings are the newest versions of each family Ubuntu 24.04's
// runtime defines (glibc 2.39, libstdc++ and libgcc_s of GCC 14): a gap above
// them is one the runtime cannot close either.
var raGlibcCeilings = map[string]string{
	"GLIBC":   "GLIBC_" + privateGlibcVersion,
	"GLIBCXX": "GLIBCXX_3.4.33",
	"CXXABI":  "CXXABI_1.3.15",
	"GCC":     "GCC_14.0.0",
}

// raGlibcCouldCover reports a load problem made only of symbol-version gaps in
// libraries the runtime replaces, none above what it defines: worth installing
// it and asking again (the second answer decides).
func raGlibcCouldCover(problem string) bool {
	gaps := raGlibcGapRe.FindAllStringSubmatch(problem, -1)
	if len(gaps) == 0 {
		return false
	}
	for _, other := range []string{"it is a", "does not have", "OpenGL"} {
		if strings.Contains(problem, other) {
			return false
		}
	}
	for _, g := range gaps {
		family, _ := elfVersionSplit(g[1])
		ceiling, known := raGlibcCeilings[family]
		if !known || elfVersionLess(ceiling, g[1]) {
			return false
		}
	}
	return true
}

// raGlibcCoreNeeds reports a core that cannot load on the firmware's glibc and
// can on the runtime; missing, one that may once the runtime is installed.
func raGlibcCoreNeeds(core string) (needs, missing bool) {
	firmware := coreLoadProblemFileWith(core, core, false)
	if firmware == "" {
		return false, false
	}
	if raGlibcLoadDir(core) != "" {
		return coreLoadProblemFileWith(core, core, true) == "", false
	}
	if _, _, ready := raGlibcRuntime(); ready {
		// Installed, but this core's RetroArch does not start on it.
		return false, false
	}
	return false, raGlibcCouldCover(firmware) && raGlibcResolve(coreRetroArchBinary(filepath.Dir(core))) != ""
}

// raGlibcForgetGapRefusals drops the updater's refusals of builds that only
// lacked what the runtime brings (core-refused.json), so the next pass fetches
// them again now that they can load. Refusals of any other kind stay.
func raGlibcForgetGapRefusals() {
	coreRefusals.mu.Lock()
	defer coreRefusals.mu.Unlock()
	coreRefusalsLoadLocked()
	changed := false
	for core, r := range coreRefusals.m {
		if strings.HasPrefix(r.Reason, coreCannotLoadReason) && raGlibcCouldCover(strings.TrimPrefix(r.Reason, coreCannotLoadReason)) {
			delete(coreRefusals.m, core)
			changed = true
		}
	}
	if changed {
		coreRefusalsSaveLocked()
		logInfo("emulators", "core updates: builds refused only for a newer glibc will be fetched again (RetroArch runs on JukaHub's)")
	}
}

// ── Scripts ──────────────────────────────────────────────────────────────

// raGlibcTool is a firmware program by its usual folders, "" when none.
func raGlibcTool(name string) string {
	for _, dir := range []string{"/bin", "/usr/bin", "/sbin", "/usr/sbin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// raGlibcLibTail is the end of the library path the private loader searches:
// the folders the firmware's loader reads from its cache (/etc/ld.so.conf),
// then its system folders. The private loader is told not to read the
// firmware's cache, and its own default folders are Ubuntu's, not TrimUI's.
func raGlibcLibTail() string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if !filepath.IsAbs(dir) || seen[dir] || strings.ContainsAny(dir, ":\n\r'\"$`\\") {
			return
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	var readConf func(file string, depth int)
	readConf = func(file string, depth int) {
		data, err := os.ReadFile(file)
		if err != nil || len(data) > 64<<10 || depth > 2 {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if rest, ok := strings.CutPrefix(line, "include "); ok {
				matches, _ := filepath.Glob(strings.TrimSpace(rest))
				for _, m := range matches {
					readConf(m, depth+1)
				}
				continue
			}
			add(line)
		}
	}
	readConf("/etc/ld.so.conf", 0)
	for _, dir := range elfSystemLibDirs {
		add(dir)
	}
	return strings.Join(dirs, ":")
}

// raGlibcWrapperText is the program a game's namespace puts over ra: the
// private loader, reached as view (a file named like ra in a bind of ra's
// folder), running the real binary reached as real. LD_LIBRARY_PATH is read
// when RetroArch starts, after the launch script set its own.
func raGlibcWrapperText(ra, view, real, libDir, tail string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Written by JukaHub (ra_glibc.go): " + ra + " on JukaHub's private C library\n")
	b.WriteString("# (glibc " + privateGlibcVersion + "). Only one game's own mount namespace shows this file at\n")
	b.WriteString("# that path; the card's RetroArch is not changed.\n")
	if st, err := os.Stat(filepath.Join(libDir, "gconv")); err == nil && st.IsDir() {
		b.WriteString("export GCONV_PATH=" + shellQuote(filepath.Join(libDir, "gconv")) + "\n")
	}
	libs := shellQuote(libDir) + `"${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"`
	if tail != "" {
		libs += shellQuote(":" + tail)
	}
	// JukaHub's probe (raGlibcProbeRun) runs this very file and has the loader
	// only list and bind RetroArch's libraries, as `ldd -r` does.
	b.WriteString("if [ \"$" + raGlibcProbeEnv + "\" = 1 ]; then\n")
	b.WriteString("\texec env LD_TRACE_LOADED_OBJECTS=1 LD_WARN=yes LD_BIND_NOW=yes " + shellQuote(view) +
		" --inhibit-cache --library-path " + libs + " " + shellQuote(real) + "\n")
	b.WriteString("fi\n")
	b.WriteString("exec " + shellQuote(view) + " --inhibit-cache --library-path " + libs +
		` --argv0 "$0" ` + shellQuote(real) + ` "$@"` + "\n")
	return b.String()
}

// raGlibcProbeEnv makes the wrapper list RetroArch's libraries instead of
// starting it (the probe only).
const raGlibcProbeEnv = "JUKAHUB_RA_GLIBC_PROBE"

// raGlibcSetupText is the first program of a game launched on the runtime. Its
// arguments are JukaHub's own mount namespace and the game's command. binds is
// one rag_bind line per RetroArch. Whatever fails, the game's own command runs.
func raGlibcSetupText(mountBin, readlinkBin, binds string) string {
	m := shellQuote(mountBin)
	return "#!/bin/sh\n" +
		"# Written by JukaHub (ra_glibc.go). The first program of a game JukaHub starts\n" +
		"# on its private C library (glibc " + privateGlibcVersion + "), in the game's own mount namespace:\n" +
		"# each RetroArch below gets a wrapper over it, then the game's own command\n" +
		"# runs. Nothing outside the game changes, and its mounts end with the game.\n" +
		"host=\"$1\"\n" +
		"shift\n" +
		"ns=$(" + shellQuote(readlinkBin) + " /proc/self/ns/mnt 2>/dev/null)\n" +
		"if [ -z \"$ns\" ] || [ \"$ns\" = \"$host\" ]; then\n" +
		"\techo \"jukahub: RetroArch stays on the firmware's C library: the game has no mount namespace of its own\" >&2\n" +
		"\texec \"$@\"\n" +
		"fi\n" +
		"rag_bind() {\n" +
		"\t" + m + " -n -o bind \"$1\" \"$2\" &&\n" +
		"\t\t" + m + " -n -o bind \"$1\" \"$3\" &&\n" +
		"\t\t" + m + " -n -o bind \"$4\" \"$3/$6\" &&\n" +
		"\t\t" + m + " -n -o bind \"$5\" \"$1/$6\" &&\n" +
		"\t\techo \"jukahub: $1/$6 runs on JukaHub's C library (glibc " + privateGlibcVersion + ")\" >&2 ||\n" +
		"\t\techo \"jukahub: $1/$6 stays on the firmware's C library: a bind mount failed\" >&2\n" +
		"}\n" +
		binds +
		"exec \"$@\"\n"
}

// raGlibcWriteOnce writes text to <dir>/<prefix><hash>.sh unless that file
// already holds it. The name follows the content, so a script a running game's
// shell is reading is never rewritten.
func raGlibcWriteOnce(dir, prefix, text string) (string, error) {
	sum := sha256.Sum256([]byte(text))
	file := filepath.Join(dir, prefix+hex.EncodeToString(sum[:])[:12]+".sh")
	if old, err := os.ReadFile(file); err == nil && string(old) == text {
		return file, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_ = os.Chmod(file, 0o755)
	return file, nil
}

// raGlibcSweepScripts removes the scripts of earlier RetroArch or runtime
// builds a day after they were written. keep holds the ones in use now. It
// stands aside while a probe runs: the probe's own setup script is not in
// keep, and is about to be started.
func raGlibcSweepScripts(dir string, keep map[string]bool) {
	if !raGlibcProbes.run.TryLock() {
		return
	}
	defer raGlibcProbes.run.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if keep[name] || e.IsDir() || !strings.HasSuffix(name, ".sh") ||
			(!strings.HasPrefix(name, "setup-") && !strings.HasPrefix(name, "wrap-")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if age := time.Since(info.ModTime()); age > 24*time.Hour {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// raGlibcFiles is what one namespace needs, written to the card.
type raGlibcFiles struct {
	setup string
	// view and real are, per RetroArch, the loader's and the real binary's
	// paths inside the namespace.
	view []string
	real []string
}

// raGlibcWriteFiles writes the setup script and the wrappers for ras and makes
// their mount points (empty folders, outside any game's namespace). extra
// folders go last on the library path: JukaHub's device libraries for a core
// that links them (coredevlibs.go), nil for every other launch and the probe.
func raGlibcWriteFiles(ras []string, loader, libDir string, extra []string) (raGlibcFiles, error) {
	var files raGlibcFiles
	dir, err := raGlibcDir()
	if err != nil {
		return files, err
	}
	mountBin, readlinkBin := raGlibcTool("mount"), raGlibcTool("readlink")
	if mountBin == "" || readlinkBin == "" {
		return files, errors.New("the firmware has no mount or readlink program")
	}
	tail := raGlibcLibTail()
	for _, d := range extra {
		d = filepath.Clean(strings.TrimSpace(d))
		if !filepath.IsAbs(d) || strings.ContainsAny(d, ":\n\r'\"$`\\") {
			continue
		}
		if tail != "" {
			tail += ":"
		}
		tail += d
	}
	nsDir := filepath.Join(dir, "ns")
	keep := map[string]bool{}
	var binds strings.Builder
	for i, ra := range ras {
		folder, name := filepath.Dir(ra), filepath.Base(ra)
		real := filepath.Join(nsDir, "r"+strconv.Itoa(i))
		view := filepath.Join(nsDir, "v"+strconv.Itoa(i))
		for _, p := range []string{real, view} {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return files, err
			}
		}
		wrap, err := raGlibcWriteOnce(dir, "wrap-", raGlibcWrapperText(ra, filepath.Join(view, name), filepath.Join(real, name), libDir, tail))
		if err != nil {
			return files, err
		}
		keep[filepath.Base(wrap)] = true
		fmt.Fprintf(&binds, "rag_bind %s %s %s %s %s %s\n", shellQuote(folder), shellQuote(real), shellQuote(view),
			shellQuote(loader), shellQuote(wrap), shellQuote(name))
		files.view = append(files.view, filepath.Join(view, name))
		files.real = append(files.real, filepath.Join(real, name))
	}
	setup, err := raGlibcWriteOnce(dir, "setup-", raGlibcSetupText(mountBin, readlinkBin, binds.String()))
	if err != nil {
		return files, err
	}
	keep[filepath.Base(setup)] = true
	raGlibcSweepScripts(dir, keep)
	files.setup = setup
	return files, nil
}

// ── The probe ────────────────────────────────────────────────────────────

// raGlibcCapped keeps the first raGlibcOutputMax bytes written to it.
type raGlibcCapped struct{ buf bytes.Buffer }

func (w *raGlibcCapped) Write(p []byte) (int, error) {
	if room := raGlibcOutputMax - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// raGlibcRunProbe runs one half of a probe and returns what it printed.
func raGlibcRunProbe(name string, args, env []string, dir string, namespace bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), raGlibcProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	if namespace && !raGlibcNamespace(cmd) {
		return "", errors.New("no mount namespaces on this system")
	}
	var out raGlibcCapped
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.buf.String(), err
}

var (
	raGlibcAddrRe = regexp.MustCompile(`\s*\(0x[0-9a-fA-F]+\)`)
	raGlibcPathRe = regexp.MustCompile("/[^\\s:'\"()`]+")
)

// raGlibcTraceProblems reads a loader's listing (LD_TRACE_LOADED_OBJECTS with
// LD_WARN and LD_BIND_NOW, as `ldd -r` asks): the lines that report something
// missing, with addresses dropped and paths cut to their names so the two
// runs compare, and how many libraries were listed.
func raGlibcTraceProblems(out string) (problems []string, listed int) {
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "jukahub:") && !strings.Contains(line, "stays on") {
			continue // the setup script's own "runs on" line
		}
		low := strings.ToLower(line)
		bad := strings.HasPrefix(line, "jukahub")
		for _, word := range []string{"not found", "undefined symbol", "error while loading", "symbol lookup error",
			"relocation error", "cannot open shared object", "no such file", "permission denied", "not executable"} {
			bad = bad || strings.Contains(low, word)
		}
		if !bad {
			if strings.Contains(line, "=>") || raGlibcAddrRe.MatchString(line) {
				listed++
			}
			continue
		}
		key := raGlibcAddrRe.ReplaceAllString(line, "")
		key = raGlibcPathRe.ReplaceAllStringFunc(key, path.Base)
		if !seen[key] {
			seen[key] = true
			problems = append(problems, key)
		}
	}
	return problems, listed
}

// raGlibcProbeInner runs inside the probe's namespace, after the setup script:
// the wrapper must be over $1 (never RetroArch itself, which would start a
// game), and is then run the way a launch script runs RetroArch; with
// raGlibcProbeEnv set it only lists.
const raGlibcProbeInner = `IFS= read -r first < "$1" 2>/dev/null
case "$first" in
'#!/bin/sh'*) ;;
*) echo "jukahub-probe: the wrapper is not in place over $1" >&2; exit 3 ;;
esac
exec "$1"`

// raGlibcExitCode is a finished command's status; clean is false for one that
// did not start, was killed or ran out of time.
func raGlibcExitCode(err error) (code int, clean bool) {
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), true
	}
	return -1, false
}

// raGlibcProbeRun asks whether ra starts on the runtime: its libraries are
// listed and bound by the firmware's loader, then — through the namespace,
// the wrapper (its exec bit and shebang on the card included) and the private
// loader a launch uses — by the runtime. A problem only the second run
// reports, or a second run that failed where the first did not, refuses.
func raGlibcProbeRun(ra, loader, libDir string) (bool, string) {
	interp := raGlibcInterp(ra)
	if interp == "" {
		return false, "it is not a dynamically linked 64-bit ARM program"
	}
	host, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil || host == "" {
		return false, "this system does not show mount namespaces"
	}
	sh := raGlibcTool("sh")
	if sh == "" {
		return false, "the firmware has no /bin/sh"
	}
	files, err := raGlibcWriteFiles([]string{ra}, loader, libDir, nil)
	if err != nil {
		return false, err.Error()
	}
	base := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LD_LIBRARY_PATH=" + raGlibcFirmwareLD()}
	trace := append([]string{"LD_TRACE_LOADED_OBJECTS=1", "LD_WARN=yes", "LD_BIND_NOW=yes"}, base...)
	firmwareOut, firmwareErr := raGlibcRunProbe(interp, []string{ra}, trace, filepath.Dir(ra), false)
	args := []string{files.setup, host, sh, "-c", raGlibcProbeInner, "jukahub-probe", ra}
	runtimeOut, runErr := raGlibcRunProbe(sh, args, append(base, raGlibcProbeEnv+"=1"), filepath.Dir(ra), true)
	firmwareProblems, _ := raGlibcTraceProblems(firmwareOut)
	runtimeProblems, listed := raGlibcTraceProblems(runtimeOut)
	firmwareCode, _ := raGlibcExitCode(firmwareErr)
	runtimeCode, runtimeClean := raGlibcExitCode(runErr)
	known := map[string]bool{}
	for _, p := range firmwareProblems {
		known[p] = true
	}
	var fresh []string
	for _, p := range runtimeProblems {
		if !known[p] {
			fresh = append(fresh, p)
		}
	}
	switch {
	case len(fresh) > 0:
		why := fresh[0]
		if len(fresh) > 1 {
			why += fmt.Sprintf(" (+%d more)", len(fresh)-1)
		}
		return false, why
	case !runtimeClean:
		return false, "the private loader did not finish: " + firstLine(fmt.Sprint(runErr))
	case runtimeCode != 0 && firmwareCode == 0:
		return false, fmt.Sprintf("the private loader ended with status %d: %s", runtimeCode, firstLine(strings.TrimSpace(runtimeOut)))
	case listed == 0:
		return false, "its libraries were not listed: " + firstLine(strings.TrimSpace(runtimeOut))
	}
	return true, ""
}

// raGlibcProbe asks for ra under the current runtime, records the answer and
// returns it. One probe runs at a time; a caller that waited gets the answer
// the other one recorded.
func raGlibcProbe(ra string) int {
	raGlibcProbes.run.Lock()
	defer raGlibcProbes.run.Unlock()
	loader, libDir, ready := raGlibcRuntime()
	if !ready {
		return raProbeUnknown
	}
	if st, _ := raGlibcProbeState(ra, libDir); st != raProbeUnknown {
		return st
	}
	key := raGlibcProbeKey(ra, libDir)
	ok, why := raGlibcProbeRun(ra, loader, libDir)
	raGlibcProbeRecord(ra, key, ok, false, why)
	if !ok {
		logWarn("emulators", "%s stays on the firmware's C library: it did not start on JukaHub's glibc %s (%s): %s",
			ra, privateGlibcVersion, filepath.Base(libDir), why)
		return raProbeFailed
	}
	logInfo("emulators", "%s starts on JukaHub's glibc %s (%s)", ra, privateGlibcVersion, filepath.Base(libDir))
	raGlibcForgetGapRefusals()
	return raProbeOK
}

// raGlibcProbeAll probes every RetroArch not asked under the current runtime
// (a background goroutine: corefix's look, a setting change).
func raGlibcProbeAll() { raGlibcProbePending("") }

// ── Launches ─────────────────────────────────────────────────────────────

// raGlibcPlan is one launch's decision: the RetroArchs its namespace wraps,
// none when it starts as before. extra are folders last on the library path
// (coreDevLibsForCore: JukaHub's device libraries for a core that links them).
type raGlibcPlan struct {
	ras    []string
	loader string
	libDir string
	why    string
	extra  []string
}

func (p raGlibcPlan) active() bool { return len(p.ras) > 0 }

// raGlibcPlanFor decides a launch. core is the core file it loads ("" when
// unknown), preferred the RetroArch it starts ("" when unknown), menu
// RetroArch's own menu, which may load any core. missing reports a launch that
// would use the runtime once it is installed; unprobed, the RetroArchs it
// would wrap that were never probed under this runtime (probing takes a second
// or two, so it is never done here, on the main thread).
func raGlibcPlanFor(core, preferred string, menu bool) (plan raGlibcPlan, missing bool, unprobed []string) {
	if !raGlibcWanted() {
		return plan, false, nil
	}
	mode := raGlibcMode()
	if preferred == "" && core != "" {
		preferred = coreRetroArchBinary(filepath.Dir(core))
	}
	why := ""
	switch {
	case mode == raGlibcAlways:
		why = "RetroArch's C library is set to JukaHub's"
	case menu:
		why = "RetroArch's own menu may load any core"
	case core != "":
		needs, miss := raGlibcCoreNeeds(core)
		if miss {
			return plan, true, nil
		}
		if !needs {
			return plan, false, nil
		}
		why = filepath.Base(core) + " needs a newer glibc than the firmware's"
	default:
		return plan, false, nil
	}
	loader, libDir, ready := raGlibcRuntime()
	if !ready {
		return plan, mode == raGlibcAlways && !menu, nil
	}
	if !raGlibcNamespaceOK() {
		return plan, false, nil
	}
	var ras []string
	for _, ra := range raGlibcRetroArchs(preferred, libDir) {
		switch st, _ := raGlibcProbeState(ra, libDir); st {
		case raProbeOK:
			ras = append(ras, ra)
		case raProbeUnknown:
			unprobed = append(unprobed, ra)
		}
	}
	if len(ras) == 0 {
		if len(unprobed) == 0 {
			logInfo("emulators", "RetroArch stays on the firmware's C library (%s): no RetroArch here starts on JukaHub's", why)
		}
		return plan, false, unprobed
	}
	plan = raGlibcPlan{ras: ras, loader: loader, libDir: libDir, why: why}
	if core != "" {
		// The device libraries a desktop build links, from JukaHub's copies
		// (coredevlibs.go): only for a core that links them.
		if plan.extra = coreDevLibsForCore(core); len(plan.extra) > 0 && mode != raGlibcAlways {
			plan.why = filepath.Base(core) + " needs a newer glibc or device libraries the firmware does not have"
		}
	}
	return plan, false, unprobed
}

// raGlibcProbePending probes, off the main thread, every RetroArch a launch of
// preferred would wrap that was never probed under the current runtime.
func raGlibcProbePending(preferred string) {
	if !raGlibcWanted() {
		return
	}
	_, libDir, ready := raGlibcRuntime()
	if !ready {
		return
	}
	for _, ra := range raGlibcRetroArchs(preferred, libDir) {
		if st, _ := raGlibcProbeState(ra, libDir); st == raProbeUnknown {
			raGlibcProbe(ra)
		}
	}
}

// raGlibcLast is the last RetroArch launch, for the game diagnosis
// (raGlibcLauncherIssue): the RetroArchs it wrapped, none when it ran on the
// firmware's glibc.
var raGlibcLast struct {
	mu     sync.Mutex
	ras    []string
	libDir string
	at     time.Time
}

func raGlibcLastSet(plan raGlibcPlan) {
	raGlibcLast.mu.Lock()
	raGlibcLast.ras, raGlibcLast.libDir, raGlibcLast.at = plan.ras, plan.libDir, time.Now()
	raGlibcLast.mu.Unlock()
}

// raGlibcApply makes cmd start in a mount namespace of its own through the
// setup script, which wraps plan's RetroArchs and then runs cmd's own command.
// It runs last, just before the start; false leaves cmd as it was.
func raGlibcApply(cmd *exec.Cmd, plan raGlibcPlan) bool {
	raGlibcLastSet(raGlibcPlan{})
	if cmd == nil || !plan.active() || cmd.Err != nil || cmd.Process != nil || cmd.Path == "" || len(cmd.Args) == 0 {
		return false
	}
	host, err := os.Readlink("/proc/self/ns/mnt")
	sh := raGlibcTool("sh")
	if err != nil || host == "" || sh == "" || !raGlibcNamespaceOK() {
		logWarn("emulators", "RetroArch stays on the firmware's C library: no mount namespaces or no /bin/sh here")
		return false
	}
	files, err := raGlibcWriteFiles(plan.ras, plan.loader, plan.libDir, plan.extra)
	if err != nil {
		logWarn("emulators", "RetroArch stays on the firmware's C library: %v", err)
		return false
	}
	if !raGlibcNamespace(cmd) {
		return false
	}
	cmd.Args = append([]string{"sh", files.setup, host, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = sh
	raGlibcLastSet(plan)
	extra := ""
	if len(plan.extra) > 0 {
		extra = "; device libraries from " + strings.Join(plan.extra, ", ")
	}
	logInfo("emulators", "RetroArch starts on JukaHub's glibc %s (%s): %s; wrapped %s%s",
		privateGlibcVersion, filepath.Base(plan.libDir), plan.why, strings.Join(plan.ras, ", "), extra)
	return true
}

// raGlibcInstall is the launch-time download of the runtime.
var raGlibcInstall struct {
	// resume is the game whose launch waited (for the download or a probe):
	// its next launch never waits again. failedAt holds off another download.
	// Main thread only.
	resume   string
	failedAt time.Time
}

func raGlibcInstallHeldOff() bool {
	f := raGlibcInstall.failedAt
	return !f.IsZero() && time.Since(f) >= 0 && time.Since(f) < raGlibcInstallRetry
}

// raGlibcLaunchCore is the core file a library launch loads and the RetroArch
// that starts it ("" when unknown).
func raGlibcLaunchCore(launcher, system string) (core, retroArch string) {
	if emuIsCoreLaunch(launcher) {
		return launcher, coreRetroArchBinary(filepath.Dir(launcher))
	}
	name := gameCoreFromLauncher(launcher, system)
	if name == "" {
		return "", ""
	}
	return coreActiveBinary(name, retroArchCoreDirs()), ""
}

// raGlibcRelaunch runs a library launch that waited again (main thread).
func raGlibcRelaunch(game libraryGame, launcher string) {
	if gameScreenSuspended() {
		return // something else took the screen meanwhile
	}
	raGlibcInstall.resume = game.Path
	if err := launchLibraryGame(game, launcher); err != nil {
		ShowToast("Cannot launch "+game.Title+": "+err.Error(), ToastKindError)
	}
	raGlibcInstall.resume = ""
}

// raGlibcPrepareLibraryLaunch is launchLibraryGame's hook (main thread): the
// plan for this launch, or deferred while the runtime is fetched or a
// RetroArch probed first — the launch then runs again from the main loop.
// Offline, after a download failed, or on the launch that already waited, the
// game starts as the plan allows (on the firmware's glibc when there is none:
// a core that cannot load there gets the diagnosis card and corefix.go's kept
// build).
func raGlibcPrepareLibraryLaunch(game libraryGame, launcher string) (plan raGlibcPlan, deferred bool) {
	raGlibcSyncMode(appConfig)
	raGlibcLastSet(raGlibcPlan{})
	resumed := raGlibcInstall.resume == game.Path
	raGlibcInstall.resume = ""
	if !raGlibcWanted() || !retroArchLauncher(launcher) {
		return plan, false
	}
	core, retroArch := raGlibcLaunchCore(launcher, game.System)
	preferred := retroArch
	if preferred == "" && core != "" {
		preferred = coreRetroArchBinary(filepath.Dir(core))
	}
	plan, missing, unprobed := raGlibcPlanFor(core, retroArch, false)
	if len(unprobed) > 0 {
		if plan.active() || resumed {
			go raGlibcProbePending(preferred) // for the next launch
		} else {
			ShowToast("Checking RetroArch with JukaHub's newer C library…", ToastKindInfo)
			go func() {
				raGlibcProbePending(preferred)
				tbxPost(func() { raGlibcRelaunch(game, launcher) })
			}()
			return raGlibcPlan{}, true
		}
	}
	if !missing {
		return plan, false
	}
	if resumed || !IsOnline() || raGlibcInstallHeldOff() {
		logInfo("emulators", "%s starts on the firmware's C library: JukaHub's glibc %s is not installed", game.Label(), privateGlibcVersion)
		return raGlibcPlan{}, false
	}
	what := "RetroArch"
	if core != "" {
		if name, _ := parseCoreFile(filepath.Base(core)); name != "" {
			what = "the " + coreLabelFor(normalizeCoreName(name))
		}
	}
	ShowToast("Getting JukaHub's newer C library for "+what+" (once, a few MB)…", ToastKindInfo)
	privateGlibcEnsureAsync(func(err error) {
		raGlibcRuntimeForget()
		if err != nil {
			raGlibcInstall.failedAt = time.Now()
			logWarn("emulators", "JukaHub's glibc %s for %s: %v", privateGlibcVersion, what, err)
			ShowToast("JukaHub's newer C library could not be installed — starting on the firmware's", ToastKindWarning)
			raGlibcRelaunch(game, launcher)
			return
		}
		go func() {
			raGlibcProbePending(preferred)
			tbxPost(func() { raGlibcRelaunch(game, launcher) })
		}()
	})
	return raGlibcPlan{}, true
}

// raGlibcPrepareDirect is the hook of a launch that starts RetroArch with a
// core itself (netplay, main thread). It never waits: a download or a probe
// starts in the background and the player is told to try again.
func raGlibcPrepareDirect(cmd *exec.Cmd, core, retroArch string) error {
	raGlibcSyncMode(appConfig)
	raGlibcLastSet(raGlibcPlan{})
	plan, missing, unprobed := raGlibcPlanFor(core, retroArch, false)
	why := filepath.Base(core) + " needs JukaHub's newer C library"
	if raGlibcMode() == raGlibcAlways {
		why = "RetroArch is set to start on JukaHub's newer C library"
	}
	if len(unprobed) > 0 {
		go raGlibcProbePending(retroArch)
		if !plan.active() {
			return errors.New(why + ", and JukaHub is checking it with this RetroArch now — try again in a moment")
		}
	}
	if missing && IsOnline() && !raGlibcInstallHeldOff() {
		privateGlibcEnsureAsync(func(err error) {
			raGlibcRuntimeForget()
			if err != nil {
				raGlibcInstall.failedAt = time.Now()
				ShowToast("JukaHub's newer C library could not be installed: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			go raGlibcProbePending(retroArch)
			ShowToast("JukaHub's newer C library is installed — start the game again", ToastKindSuccess)
		})
		return errors.New(why + ", which is downloading now — try again in a minute")
	}
	raGlibcApply(cmd, plan)
	return nil
}

// raGlibcPrepareMenu is RetroArch's own menu (the Emulators tile, main
// thread): on the runtime once it is installed and this RetroArch passed the
// probe (a first probe runs in the background for the next time).
func raGlibcPrepareMenu(cmd *exec.Cmd, retroArch string) {
	raGlibcSyncMode(appConfig)
	plan, _, unprobed := raGlibcPlanFor("", retroArch, true)
	if len(unprobed) > 0 {
		go raGlibcProbePending(retroArch)
	}
	raGlibcApply(cmd, plan)
}

// raGlibcLoaderLine reports a loader error about one of the wrapped
// RetroArchs. The loader names the program first ("./ra64.trimui: symbol
// lookup error: …"), so a firmware helper's own error in the launch script is
// not taken for RetroArch's.
func raGlibcLoaderLine(line string, names map[string]bool) bool {
	prog, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok || !names[path.Base(strings.TrimSpace(prog))] {
		return false
	}
	low := strings.ToLower(rest)
	for _, word := range []string{"symbol lookup error", "relocation error", "error while loading shared libraries", "unexpected reloc type"} {
		if strings.Contains(low, word) {
			return true
		}
	}
	return strings.Contains(rest, "GLIBC_PRIVATE")
}

// raGlibcLauncherIssue reads the output of a game that quit at once
// (gamelaunch_firmware.go). When that launch wrapped RetroArch and its setup
// could not ("stays on the firmware's C library"), or the loader stopped
// RetroArch, those RetroArchs are refused for good under this runtime (until
// RetroArch, the runtime or the library path changes), corefix.go gives the
// cores that needed the runtime builds that run here, and the card says so
// first.
func raGlibcLauncherIssue(lines []string) (launchIssue, bool) {
	raGlibcLast.mu.Lock()
	ras, libDir, at := raGlibcLast.ras, raGlibcLast.libDir, raGlibcLast.at
	raGlibcLast.ras = nil
	raGlibcLast.mu.Unlock()
	if len(ras) == 0 || time.Since(at) > 10*time.Minute {
		return launchIssue{}, false
	}
	names := map[string]bool{}
	for _, ra := range ras {
		names[filepath.Base(ra)] = true
	}
	hit := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if (strings.HasPrefix(trimmed, "jukahub:") && strings.Contains(trimmed, "stays on the firmware's C library")) ||
			raGlibcLoaderLine(trimmed, names) {
			hit = trimmed
			break
		}
	}
	if hit == "" {
		return launchIssue{}, false
	}
	for _, ra := range ras {
		raGlibcProbeRecord(ra, raGlibcProbeKey(ra, libDir), false, true, "a game could not start on it: "+hit)
	}
	logWarn("emulators", "RetroArch did not start on JukaHub's glibc %s (%s): %s; back on the firmware's", privateGlibcVersion, filepath.Base(libDir), hit)
	if coreFixSupported() {
		go coreFixScan()
	}
	return launchIssue{
		Level: int(gameDiagSure),
		Title: "RetroArch could not start on JukaHub's newer C library",
		Detail: "It said: " + hit + ". JukaHub starts RetroArch on the firmware's C library from now on, and a core that " +
			"needs the newer one gets a build that runs here. Settings → Games & emulators → RetroArch's C library turns it off for good.",
	}, true
}

// ── Updates and the start's look ─────────────────────────────────────────

// raGlibcEnsureForCore is the updater's (fetchCoreUpdate, a background
// goroutine): a downloaded build that cannot load on the firmware's glibc only
// for gaps the runtime closes gets the runtime installed and its RetroArch
// probed first. true when the load check should be asked again.
func raGlibcEnsureForCore(ctx context.Context, problem, target string) bool {
	if !raGlibcWanted() || !raGlibcCouldCover(problem) || strings.TrimSpace(target) == "" {
		return false
	}
	return raGlibcEnsureRuntimeFor(ctx, target)
}

// raGlibcEnsureRuntimeFor installs the runtime when it is not, and probes the
// RetroArch that owns target's folder when that was never done under it (a
// background goroutine). true when that RetroArch starts on the runtime.
func raGlibcEnsureRuntimeFor(ctx context.Context, target string) bool {
	if !raGlibcWanted() || strings.TrimSpace(target) == "" {
		return false
	}
	ra := raGlibcResolve(coreRetroArchBinary(filepath.Dir(target)))
	if ra == "" {
		return false
	}
	if _, _, ready := raGlibcRuntime(); ready {
		privateGlibcRefreshAsync() // the weekly look for a newer Ubuntu build
	} else {
		if err := privateGlibcInstall(ctx); err != nil {
			logWarn("emulators", "JukaHub's glibc %s for the newest cores: %v", privateGlibcVersion, err)
			return false
		}
		raGlibcRuntimeForget()
	}
	_, libDir, ready := raGlibcRuntime()
	if !ready {
		return false
	}
	if st, _ := raGlibcProbeState(ra, libDir); st == raProbeUnknown {
		raGlibcProbe(ra)
	}
	return raGlibcLoadDir(target) != ""
}

// raGlibcPending reports a load problem that only the runtime would close for
// a core that becomes target, while the runtime is not installed (its
// download failed or was stopped): the updater does not remember such a build
// as refused, since the next try may install the runtime and take it.
func raGlibcPending(problem, target string) bool {
	if !raGlibcWanted() || strings.TrimSpace(target) == "" || !raGlibcCouldCover(problem) {
		return false
	}
	if _, _, ready := raGlibcRuntime(); ready {
		return false
	}
	return raGlibcResolve(coreRetroArchBinary(filepath.Dir(target))) != ""
}

// raGlibcRefusalMayPass reports a remembered refusal (core-refused.json's
// reason) that a new try could pass: one made only of glibc gaps the runtime
// closes, or only of device libraries JukaHub brings beside it
// (coreDevLibsCouldCover), while RetroArch may start on it and — once it is
// installed — the RetroArch that owns target's folder does ("" target: any).
// A refusal of any other kind is final for that build.
func raGlibcRefusalMayPass(reason, target string) bool {
	problem, ok := strings.CutPrefix(reason, coreCannotLoadReason)
	if !ok || !raGlibcWanted() {
		return false
	}
	if coreDevLibsCouldCover(problem, target) {
		return true
	}
	if !raGlibcCouldCover(problem) {
		return false
	}
	if _, _, ready := raGlibcRuntime(); !ready || strings.TrimSpace(target) == "" {
		return true
	}
	return raGlibcLoadDir(target) != ""
}

// raGlibcRescue is corefix's look meeting active cores that cannot load: when
// the runtime would close their gaps, it is installed (download: only when the
// look may download) and probed. true when the look should be asked again.
func raGlibcRescue(broken []coreFixBroken, download bool) bool {
	if !raGlibcWanted() {
		return false
	}
	need := false
	for _, b := range broken {
		if raGlibcCouldCover(b.problem) && raGlibcResolve(coreRetroArchBinary(filepath.Dir(b.path))) != "" {
			need = true
			break
		}
	}
	if !need {
		return false
	}
	if _, _, ready := raGlibcRuntime(); !ready {
		if !download {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), coreFixTimeout)
		err := privateGlibcInstall(ctx)
		cancel()
		raGlibcRuntimeForget()
		if err != nil {
			logWarn("emulators", "core fix: JukaHub's glibc %s: %v", privateGlibcVersion, err)
			return false
		}
	}
	raGlibcProbeAll()
	for _, b := range broken {
		if raGlibcLoadDir(b.path) != "" {
			return true
		}
	}
	return false
}

// ── The Settings row ─────────────────────────────────────────────────────

func raGlibcSettingsRow() setRow {
	return setRow{
		ID: "retroArchGlibc", Label: "RetroArch's C library", Kind: setKindChoice,
		Visible: func(*Config) bool { return raGlibcSupported() },
		Detail:  raGlibcSettingDetail,
		Options: func(*Config) []setOption {
			notes := map[string]string{
				raGlibcAuto:   "Cores that need a newer glibc than the firmware's start on JukaHub's " + privateGlibcVersion + ", so the newest builds install and run",
				raGlibcAlways: "Every RetroArch game JukaHub starts runs on JukaHub's glibc " + privateGlibcVersion,
				raGlibcOff:    "Only builds the firmware's glibc opens; newer ones are refused, or put back",
			}
			out := make([]setOption, 0, len(raGlibcModes))
			for _, m := range raGlibcModes {
				out = append(out, setOption{Value: m, Label: raGlibcModeLabel(m), Note: notes[m]})
			}
			return out
		},
		Current: func(c *Config) int { return slices.Index(raGlibcModes, raGlibcModeFrom(c)) },
		Choose:  func(c *Config, value string) { setToast(raGlibcChoose(c, value)) },
	}
}

func raGlibcSettingDetail(c *Config) string {
	if raGlibcModeFrom(c) == raGlibcOff {
		return "Cores run on the firmware's glibc only"
	}
	_, libDir, ready := raGlibcRuntime()
	switch {
	case !ready:
		return "The newest cores run on JukaHub's glibc " + privateGlibcVersion + " · fetched when a core needs it"
	case len(raGlibcRefusedUnder(libDir)) > 0:
		return "RetroArch here did not start on JukaHub's glibc " + privateGlibcVersion + " · cores stay on the firmware's"
	}
	return "The newest cores run on JukaHub's glibc " + privateGlibcVersion + " · installed"
}

// raGlibcChoose applies a choice (main thread) and returns its toast. Off puts
// builds the firmware opens back (corefix's look, at once); leaving Off lets the
// updater fetch the builds it refused for a newer glibc; JukaHub's (always) is
// fetched now when online.
func raGlibcChoose(c *Config, value string) string {
	prev := raGlibcMode()
	setStoreString(c, raGlibcSetting, value)
	raGlibcSyncMode(c)
	now := raGlibcMode()
	if prev != now && raGlibcSupported() {
		switch {
		case now == raGlibcOff:
			if coreFixSupported() {
				go coreFixScan()
			}
		case now == raGlibcAlways && !privateGlibcReady() && IsOnline():
			privateGlibcEnsureAsync(func(err error) {
				raGlibcRuntimeForget()
				if err != nil {
					ShowToast("JukaHub's newer C library could not be installed: "+firstLine(err.Error()), ToastKindWarning)
					return
				}
				go raGlibcProbeAll()
			})
		default:
			go func() {
				raGlibcProbeAll()
				raGlibcForgetGapRefusals()
			}()
		}
	}
	return "RetroArch's C library: " + raGlibcModeLabel(now)
}
