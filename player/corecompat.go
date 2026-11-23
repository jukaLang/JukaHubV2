package main

// corecompat.go — which buildbot cores load on this TrimUI, known before one
// is downloaded, and JukaHub's glibc ready for them ahead of time.
//
// Juka, 2026-11-23: "Make sure all emulators that get updated work on glibc…
// we want to show the emulators that Trimui devices can run… many of emulator
// cores are asking for glibc 4.29… but even our private glibc is at 2.39". What
// the cores ask for is GLIBCXX_3.4.29 and 3.4.30: versions of the C++ library
// (libstdc++ of GCC 11 and GCC 12), not of glibc. The libretro buildbot builds
// its linux/aarch64 nightlies with a newer toolchain than the TrimUI firmware's
// (November 2026: glibc up to 2.35, GLIBCXX up to 3.4.30; the stock Smart Pro
// has glibc 2.33 and GCC 7's libstdc++, GLIBCXX_3.4.24). JukaHub's private
// runtime is Ubuntu 24.04's: glibc 2.39 and GCC 14's libstdc++, which defines
// every GLIBCXX version up to 3.4.33 (raGlibcCeilings). It covers those cores —
// once the complete runtime is installed (the pinned fallback is glibc alone,
// glibc_runtime.go) and RetroArch starts on it (ra_glibc.go). Two things were
// left to chance, and this file takes them:
//
//   - The runtime was fetched the first time a core needed it, in the middle
//     of an install or a game's start. startCoreRuntimeEnsure installs the
//     complete runtime a few minutes after JukaHub starts on a handheld whose
//     RetroArch can use it, and probes RetroArch under it, so the load check
//     of every install, update and the start-up core look reads it from the
//     first.
//   - The Emulators page could tell that a buildbot build does not load here
//     only by downloading it. JukaHub's cores workflow now surveys every
//     linux/aarch64 build the buildbot publishes (player/native/survey-cores.py,
//     weekly, .github/workflows/retroarch-cores.yml) — the libraries each links
//     and the symbol versions it needs of each, the two tables the load check
//     reads — and publishes buildbot-needs.json beside JukaHub's own TrimUI
//     builds (the cores-aarch64 release). The handheld reads it with the
//     buildbot's index (emuRunUpdateCheck) and judges each core the way the
//     loader would (coreSurveyJudge): every library it links in the firmware's
//     folders or RetroArch's, among JukaHub's stand-ins (coredevlibs.go), or
//     one JukaHub's glibc brings; every version defined by the firmware's copy
//     or the runtime's. A core that loads neither way is not offered
//     (coreTrimuiMisfit) unless JukaHub's own build of it for TrimUI exists, in
//     which case an install takes that build straight away; and the buildbot
//     build the survey read is refused before a download, by a press or by the
//     daily update (fetchCoreUpdate), so no update ever puts a build that
//     cannot load in front of a game.
//
// The survey is a hint, never the last word: a core it does not list, a
// survey older than coreSurveyMaxAge, or a build other than the one it read
// (another CRC32) changes nothing — the load check after the download decides,
// as before. Only what no runtime or setting here changes — a library nowhere
// on this handheld, a version newer than JukaHub's runtime defines, another
// architecture — refuses a download; a core that only needs RetroArch on
// JukaHub's glibc where RetroArch cannot use it (the setting, no mount
// namespace, a refused probe) is hidden from the list but still downloaded on
// a press, where the refusal says why the runtime could not be used.

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// coreSurveyName is the survey's asset in the cores-aarch64 release.
	coreSurveyName = "buildbot-needs.json"
	// coreSurveyTTL is how long one fetch is used before the update check asks
	// again (the workflow writes it weekly).
	coreSurveyTTL = 12 * time.Hour
	// coreSurveyMaxAge: an older survey is not used at all.
	coreSurveyMaxAge = 30 * 24 * time.Hour
	// coreSurveyMaxBytes bounds the download; a few hundred cores are a few
	// hundred kilobytes.
	coreSurveyMaxBytes = 8 << 20
	// coreSurveyEnvTTL is how long one look at this handheld's state (its
	// runtime, setting, RetroArch) serves the answers.
	coreSurveyEnvTTL = 5 * time.Second
	// coreRuntimeStartDelay keeps the runtime's download behind the release
	// check and the start-up core look; coreRuntimeGiveUp bounds how long the
	// start-up waits for a RetroArch, a connection or a quiet moment.
	coreRuntimeStartDelay = 150 * time.Second
	coreRuntimeGiveUp     = 3 * time.Hour
	coreRuntimeTimeout    = 10 * time.Minute
	coreRuntimeTries      = 3
)

// coreSurveyURL is where the survey comes from (a variable for the tests).
var coreSurveyURL = coreFixReleaseBase + coreSurveyName

// coreSurveyDoc is buildbot-needs.json.
type coreSurveyDoc struct {
	Format    int                        `json:"format"`
	Generated string                     `json:"generated"`
	Index     string                     `json:"index"`
	Cores     map[string]coreSurveyEntry `json:"cores"`
	at        time.Time
}

// coreSurveyEntry is one build the survey read: its index date and CRC32, the
// architecture, DT_NEEDED in order, and per library the symbol versions it
// needs of it.
type coreSurveyEntry struct {
	Date     string              `json:"date"`
	CRC32    string              `json:"crc32"`
	File     string              `json:"file"`
	Arch     string              `json:"arch"`
	Needed   []string            `json:"needed"`
	Versions map[string][]string `json:"versions"`
}

// coreSurveyParse reads and bounds a survey.
func coreSurveyParse(data []byte) (*coreSurveyDoc, error) {
	var doc coreSurveyDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unreadable survey: %w", err)
	}
	if doc.Format != 1 {
		return nil, fmt.Errorf("survey format %d, not 1", doc.Format)
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(doc.Generated))
	if err != nil {
		return nil, errors.New("the survey names no time it was made")
	}
	if len(doc.Cores) == 0 || len(doc.Cores) > 5000 {
		return nil, fmt.Errorf("the survey lists %d cores", len(doc.Cores))
	}
	doc.at = at
	cores := make(map[string]coreSurveyEntry, len(doc.Cores))
	for name, e := range doc.Cores {
		key := normalizeCoreName(name)
		if key == "" || len(e.Needed) > 256 || len(e.Versions) > 256 {
			continue
		}
		e.CRC32 = strings.ToLower(strings.TrimSpace(e.CRC32))
		cores[key] = e
	}
	doc.Cores = cores
	return &doc, nil
}

// coreSurveyState is the survey in use and the answers made from it.
var coreSurveyState struct {
	mu        sync.Mutex
	doc       *coreSurveyDoc
	fetchedAt time.Time
	loaded    bool // the copy on the card was read
	// fits are the answers for one survey and one state of this handheld
	// (stamp): the runtime, the setting, RetroArch. envAt is when that state
	// was last looked at.
	fits  map[string]coreSurveyFit
	stamp string
	env   *coreSurveyEnv
	envAt time.Time
}

// coreSurveySupported reports a handheld the survey is for.
func coreSurveySupported() bool { return coreFixSupported() }

// coreSurveyCachePath is the copy on the card, so a start offline still knows.
func coreSurveyCachePath() string { return filepath.Join(compatDir(), coreSurveyName) }

// coreSurveyDocLocked is the survey in use — read from the card the first
// time — or nil when there is none young enough.
func coreSurveyDocLocked() *coreSurveyDoc {
	s := &coreSurveyState
	if s.doc == nil && !s.loaded {
		s.loaded = true
		if data, err := os.ReadFile(coreSurveyCachePath()); err == nil && len(data) <= coreSurveyMaxBytes {
			if doc, err := coreSurveyParse(data); err == nil {
				s.doc = doc
			}
		}
	}
	if s.doc == nil || time.Since(s.doc.at) > coreSurveyMaxAge {
		return nil
	}
	return s.doc
}

// coreSurveyRefresh fetches the survey when the one in use is older than
// coreSurveyTTL (the background update check, emuupdatecheck.go). A missing
// survey (the workflow has not run yet) is not an error.
func coreSurveyRefresh(ctx context.Context, client *http.Client) {
	if !coreSurveySupported() {
		return
	}
	coreSurveyState.mu.Lock()
	recent := !coreSurveyState.fetchedAt.IsZero() && time.Since(coreSurveyState.fetchedAt) < coreSurveyTTL
	coreSurveyState.mu.Unlock()
	if recent {
		return
	}
	data, err := fetchUpdateBytes(ctx, client, coreSurveyURL, coreSurveyMaxBytes)
	if err != nil {
		if ctx.Err() == nil {
			coreSurveyState.mu.Lock()
			coreSurveyState.fetchedAt = time.Now() // not again until the next TTL
			coreSurveyState.mu.Unlock()
		}
		logDebug("emulators", "core survey: %v", err)
		return
	}
	doc, err := coreSurveyParse(data)
	coreSurveyState.mu.Lock()
	coreSurveyState.fetchedAt = time.Now()
	if err == nil {
		coreSurveyState.doc, coreSurveyState.loaded = doc, true
		coreSurveyState.fits, coreSurveyState.env = nil, nil
	}
	coreSurveyState.mu.Unlock()
	if err != nil {
		logWarn("emulators", "core survey: %v", err)
		return
	}
	if werr := linuxPkgWriteFile(coreSurveyCachePath(), data); werr != nil {
		logDebug("emulators", "core survey: could not keep a copy: %v", werr)
	}
	logInfo("emulators", "core survey: what %d buildbot cores need, read %s", len(doc.Cores), doc.Generated)
	invalidateEmulatorCache()
}

// coreSurveyForget drops the answers made so far: JukaHub's glibc, the
// setting or RetroArch changed.
func coreSurveyForget() {
	coreSurveyState.mu.Lock()
	coreSurveyState.fits, coreSurveyState.env = nil, nil
	coreSurveyState.mu.Unlock()
}

// ── Judging one build ────────────────────────────────────────────────────────

// coreSurveyLib is what one place has of a library: whether it is there, and
// the versions it defines (nil: none read, which is not held against a build).
type coreSurveyLib struct {
	found bool
	defs  map[string]bool
}

// coreSurveyEnv is what a build is judged against.
type coreSurveyEnv struct {
	// firmware looks a library up where the firmware's loader and RetroArch's
	// launches look: the game's LD_LIBRARY_PATH, /etc/ld.so.conf, the system
	// folders, RetroArch's own folders and the card's System/lib.
	firmware func(soname string) coreSurveyLib
	// runtime is JukaHub's glibc's copy of a library it brings (found false for
	// any other); found with defs nil while it is not installed, when the
	// ceilings decide.
	runtime func(soname string) coreSurveyLib
	// raHas: the RetroArch here links the library itself, so it is loaded.
	raHas func(soname string) bool
	// standIn: JukaHub writes a stand-in for it beside its glibc.
	standIn func(soname string) bool
	// runtimeOpen says RetroArch may start on JukaHub's glibc here;
	// runtimeWhy, why not.
	runtimeOpen bool
	runtimeWhy  string
	ceilings    map[string]string
}

// coreSurveyFit is the answer for one build.
type coreSurveyFit struct {
	known    bool
	firmware bool   // it loads on the firmware's libraries
	runtime  bool   // it loads with JukaHub's glibc, which RetroArch may use here
	problem  string // why it loads neither way ("" when it loads)
	// final: problem is one no runtime and no setting here changes — a library
	// nowhere on this handheld, a version newer than JukaHub's glibc has, another
	// architecture.
	final bool
	crc   string // the build the survey read
}

// coreSurveyRuntimeSonames are the libraries JukaHub's glibc runtime brings:
// glibc's own, libstdc++, libgcc_s and libtinfo (glibc_runtime.go).
var coreSurveyRuntimeSonames = map[string]bool{
	"ld-linux-aarch64.so.1": true, "libc.so.6": true, "libm.so.6": true, "libmvec.so.1": true,
	"libpthread.so.0": true, "libdl.so.2": true, "librt.so.1": true, "libresolv.so.2": true,
	"libutil.so.1": true, "libanl.so.1": true, "libBrokenLocale.so.1": true, "libthread_db.so.1": true,
	"libc_malloc_debug.so.0": true, "libnss_files.so.2": true, "libnss_dns.so.2": true,
	"libstdc++.so.6": true, "libgcc_s.so.1": true, "libtinfo.so.6": true,
}

// coreSurveyAbove is the newest of need above its family's ceiling ("" when
// every one is within them; families without a ceiling are not compared).
func coreSurveyAbove(need []string, ceilings map[string]string) string {
	worst := ""
	for _, v := range need {
		family, nums := elfVersionSplit(v)
		ceiling, ok := ceilings[family]
		if !ok || len(nums) == 0 || !elfVersionLess(ceiling, v) {
			continue
		}
		if worst == "" || elfVersionLess(worst, v) {
			worst = v
		}
	}
	return worst
}

// coreSurveyJudge says whether the build e loads under env, as the loader
// would: every library it links found, every version it needs defined.
func coreSurveyJudge(e coreSurveyEntry, env coreSurveyEnv) coreSurveyFit {
	fit := coreSurveyFit{known: true, crc: e.CRC32}
	if arch := strings.TrimSpace(e.Arch); arch != "" && arch != "aarch64" {
		fit.problem, fit.final = "it is built for "+arch+", not this handheld's 64-bit ARM", true
		return fit
	}
	// DT_NEEDED in order, then any library only the version table names.
	libs := append([]string(nil), e.Needed...)
	listed := make(map[string]bool, len(libs))
	for _, lib := range libs {
		listed[lib] = true
	}
	extra := make([]string, 0, len(e.Versions))
	for lib := range e.Versions {
		if !listed[lib] {
			extra = append(extra, lib)
		}
	}
	sort.Strings(extra)
	libs = append(libs, extra...)
	var fwProblems, rtProblems, missing []string
	beyond := false
	for _, lib := range libs {
		lib = filepath.Base(lib)
		need := e.Versions[lib]
		fw := env.firmware(lib)
		switch {
		case fw.found:
			if gap := elfVersionGap(lib, need, fw.defs); gap != "" {
				fwProblems = append(fwProblems, gap)
			}
		case env.raHas(lib):
		default:
			fwProblems = append(fwProblems, "needs "+lib+", which this firmware does not have")
		}
		rt := env.runtime(lib)
		switch {
		case rt.found && rt.defs != nil:
			if gap := elfVersionGap(lib, need, rt.defs); gap != "" {
				rtProblems = append(rtProblems, strings.Replace(gap, "this firmware's", "JukaHub's glibc "+privateGlibcVersion+" runtime's", 1))
				beyond = true
			}
		case rt.found:
			if v := coreSurveyAbove(need, env.ceilings); v != "" {
				rtProblems = append(rtProblems, "needs "+v+", newer than JukaHub's glibc "+privateGlibcVersion+" runtime has")
				beyond = true
			}
		case fw.found:
			if gap := elfVersionGap(lib, need, fw.defs); gap != "" {
				rtProblems = append(rtProblems, gap)
			}
		case env.raHas(lib), env.standIn(lib):
		default:
			missing = append(missing, lib)
			rtProblems = append(rtProblems, "needs "+lib+", which this firmware does not have")
		}
	}
	fit.firmware = len(fwProblems) == 0
	fit.runtime = len(rtProblems) == 0 && env.runtimeOpen
	switch {
	case fit.firmware || fit.runtime:
	case len(missing) > 0:
		fit.final = true
		fit.problem = coreMissingLibsProblem(missing)
		for _, lib := range missing {
			if coreDesktopGLLib(lib) {
				fit.problem = "it is built for desktop OpenGL (needs " + lib + "), and this device's RetroArch draws with OpenGL ES only"
				break
			}
		}
	case beyond:
		fit.final = true
		fit.problem = elfJoinProblems(rtProblems)
	case len(rtProblems) > 0:
		fit.problem = elfJoinProblems(rtProblems)
	default:
		// JukaHub's glibc would load it; RetroArch cannot use it here.
		fit.problem = elfJoinProblems(fwProblems) + "; JukaHub's glibc " + privateGlibcVersion + " would load it, but " + env.runtimeWhy
	}
	return fit
}

// ── This handheld ────────────────────────────────────────────────────────────

// coreRuntimeOpen reports whether a RetroArch here may start on JukaHub's
// glibc — installed or not yet — and why not: the setting allows it, there is
// a 64-bit RetroArch, a game may have a mount namespace (when that was looked
// at; ra_glibc.go asks once a session), and no probe refused it.
func coreRuntimeOpen(ra string) (bool, string) {
	if !raGlibcWanted() {
		return false, "Settings → Games & emulators → RetroArch's C library is set to Firmware's only"
	}
	if raGlibcResolve(ra) == "" {
		return false, "there is no 64-bit RetroArch here to start on it"
	}
	raGlibcNamespaceMemo.mu.Lock()
	looked, ok := raGlibcNamespaceMemo.done, raGlibcNamespaceMemo.ok
	raGlibcNamespaceMemo.mu.Unlock()
	if looked && !ok {
		return false, "this firmware does not let a game have a mount namespace of its own"
	}
	if _, libDir, ready := raGlibcRuntime(); ready {
		if st, why := raGlibcProbeState(raGlibcResolve(ra), libDir); st == raProbeFailed {
			if why == "" {
				why = "no reason was recorded"
			}
			return false, "RetroArch did not start on it (" + why + ")"
		}
	}
	return true, ""
}

// coreSurveyStampNow names the state answers are made for: the setting,
// JukaHub's glibc and whether it has its C++ library, the namespace look, the
// RetroArch and its probe's answer.
func coreSurveyStampNow(ra string) string {
	_, libDir, ready := raGlibcRuntime()
	raGlibcNamespaceMemo.mu.Lock()
	ns := fmt.Sprint(raGlibcNamespaceMemo.done, raGlibcNamespaceMemo.ok)
	raGlibcNamespaceMemo.mu.Unlock()
	probe := 0
	if real := raGlibcResolve(ra); ready && real != "" {
		probe, _ = raGlibcProbeState(real, libDir)
	}
	return strings.Join([]string{raGlibcMode(), fmt.Sprint(ready, ready && privateRuntimeHasCxx(libDir)), filepath.Base(libDir),
		ns, ra, fmt.Sprint(probe)}, "|")
}

// coreSurveyEnvFor is this handheld, for the RetroArch at ra ("" when none).
func coreSurveyEnvFor(ra string) *coreSurveyEnv {
	dirs := append(raGetFirmwareDirs(), coreGLExtraDirs(ra)...)
	fwCache := map[string]coreSurveyLib{}
	_, libDir, installed := raGlibcRuntime()
	full := installed && privateRuntimeHasCxx(libDir)
	rtCache := map[string]coreSurveyLib{}
	raNeeds := coreRetroArchNeeds(ra)
	open, why := coreRuntimeOpen(ra)
	env := &coreSurveyEnv{runtimeOpen: open, runtimeWhy: why, ceilings: raGlibcCeilings}
	env.firmware = func(soname string) coreSurveyLib {
		if hit, ok := fwCache[soname]; ok {
			return hit
		}
		info, state := elfFindLibrary(soname, elf.ELFCLASS64, elf.EM_AARCH64, dirs)
		out := coreSurveyLib{found: state != elfLibMissing, defs: info.defs}
		fwCache[soname] = out
		return out
	}
	env.runtime = func(soname string) coreSurveyLib {
		if !coreSurveyRuntimeSonames[soname] {
			return coreSurveyLib{}
		}
		if hit, ok := rtCache[soname]; ok {
			return hit
		}
		out := coreSurveyLib{found: true}
		// The installed runtime's own files when it has this one (the pinned
		// fallback has no C++ library: the complete runtime, which replaces it
		// when a core needs that, is judged by the ceilings).
		if installed && (full || !strings.HasPrefix(soname, "libstdc++") && !strings.HasPrefix(soname, "libgcc_s")) {
			if info, state := elfFindLibrary(soname, elf.ELFCLASS64, elf.EM_AARCH64, []string{libDir}); state == elfLibFound {
				out.defs = info.defs
			}
		}
		rtCache[soname] = out
		return out
	}
	env.raHas = func(soname string) bool { return raNeeds[soname] }
	env.standIn = func(soname string) bool {
		_, ok := devStubLibs[soname]
		return ok && open
	}
	return env
}

// coreSurveyFitFor is the survey's answer for core here (known false when the
// survey has none to give).
func coreSurveyFitFor(core string) coreSurveyFit {
	core = normalizeCoreName(core)
	if core == "" || !coreSurveySupported() {
		return coreSurveyFit{}
	}
	s := &coreSurveyState
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := coreSurveyDocLocked()
	if doc == nil {
		return coreSurveyFit{}
	}
	e, ok := doc.Cores[core]
	if !ok {
		return coreSurveyFit{}
	}
	// The handheld's state is looked at once per coreSurveyEnvTTL, not per
	// core: a catalog build asks about hundreds at once.
	if s.env == nil || time.Since(s.envAt) >= coreSurveyEnvTTL {
		ra := coreRetroArchBinary(deviceRetroArchCoresDir())
		if stamp := coreSurveyStampNow(ra); stamp != s.stamp || s.env == nil {
			s.stamp, s.env, s.fits = stamp, coreSurveyEnvFor(ra), map[string]coreSurveyFit{}
		}
		s.envAt = time.Now()
	}
	if fit, hit := s.fits[core]; hit {
		return fit
	}
	fit := coreSurveyJudge(e, *s.env)
	s.fits[core] = fit
	return fit
}

// coreOwnBuildListed reports a core JukaHub's own TrimUI builds (the
// cores-aarch64 release) carry, from the index fetched last (no network).
func coreOwnBuildListed(core string) bool {
	core = normalizeCoreName(core)
	coreFixState.mu.Lock()
	defer coreFixState.mu.Unlock()
	for _, e := range coreFixState.index {
		if e.Core == core {
			return true
		}
	}
	return false
}

// coreSurveyMisfit is coreTrimuiMisfit's question to the survey: why the
// buildbot's build of core loads on this handheld neither on the firmware's
// libraries nor on JukaHub's glibc — "" when it loads, when the survey cannot
// say, and when JukaHub's own build for TrimUI stands in (an install takes
// that one).
func coreSurveyMisfit(core string) string {
	fit := coreSurveyFitFor(core)
	if !fit.known || fit.problem == "" || coreOwnBuildListed(core) {
		return ""
	}
	return "the buildbot's build " + fit.problem
}

// coreSurveyNote is what a published core's tile adds from the survey: where
// it loads, when not on the firmware's own libraries.
func coreSurveyNote(core string) string {
	fit := coreSurveyFitFor(core)
	switch {
	case !fit.known || fit.firmware:
		return ""
	case fit.runtime:
		return "runs on JukaHub's glibc " + privateGlibcVersion
	case coreOwnBuildListed(core):
		return "JukaHub's build for TrimUI"
	}
	return ""
}

// coreSurveyRefusal is the reason to refuse upd before downloading it: the
// buildbot build the survey read (the same CRC32) needs what no runtime or
// setting here changes. "" for any other build, source or answer.
func coreSurveyRefusal(upd coreUpdate) string {
	if !strings.HasPrefix(upd.URL, retroArchCoreBaseURL) {
		return ""
	}
	fit := coreSurveyFitFor(upd.Core)
	if !fit.known || !fit.final || fit.problem == "" || fit.crc == "" || !strings.EqualFold(fit.crc, upd.CRC32) {
		return ""
	}
	return fit.problem
}

// ── GLIBCXX in plain words ───────────────────────────────────────────────────

// coreGLIBCXXRe finds a GLIBCXX version in a load problem.
var coreGLIBCXXRe = regexp.MustCompile(`GLIBCXX_(3\.4\.\d+)`)

// coreGLIBCXXGCC is the GCC release whose libstdc++ first defines a GLIBCXX
// version.
var coreGLIBCXXGCC = map[string]string{
	"3.4.29": "11", "3.4.30": "12", "3.4.31": "13", "3.4.32": "13", "3.4.33": "14", "3.4.34": "15",
}

// coreGapPlain is a short note for a load problem that names a GLIBCXX
// version, which reads like a glibc one ("glibc 4.29"): it is the C++
// library's, and JukaHub's runtime has every one up to GLIBCXX_3.4.33. ""
// for any other problem.
func coreGapPlain(problem string) string {
	m := coreGLIBCXXRe.FindStringSubmatch(problem)
	if m == nil {
		return ""
	}
	v := "GLIBCXX_" + m[1]
	note := " — " + v + " is a version of the C++ library"
	if gcc := coreGLIBCXXGCC[m[1]]; gcc != "" {
		note += " (GCC " + gcc + "'s)"
	}
	note += ", not of glibc"
	if !elfVersionLess(raGlibcCeilings["GLIBCXX"], v) {
		note += "; JukaHub's glibc " + privateGlibcVersion + " runtime carries GCC 14's, which has it"
	}
	return note
}

// ── JukaHub's glibc, ready ahead ─────────────────────────────────────────────

// startCoreRuntimeEnsure is the start-up's install of JukaHub's complete glibc
// runtime (main thread, from startCoreAutoUpdate): a few minutes after start,
// on a handheld whose RetroArch may use it, online and between games, unless
// Emulator updates is Off. Then RetroArch is probed under it, so the first
// game does not wait for that either.
func startCoreRuntimeEnsure() {
	if !raGlibcSupported() {
		return
	}
	go func() {
		time.Sleep(coreRuntimeStartDelay)
		tries := 0
		for waited := time.Duration(0); waited < coreRuntimeGiveUp; {
			wait, done := coreRuntimeEnsureTick(&tries)
			if done {
				return
			}
			time.Sleep(wait)
			waited += wait
		}
	}()
}

// coreRuntimeEnsureTick is one look of the start-up's install: done when
// nothing is left to do this session, else how long to wait.
func coreRuntimeEnsureTick(tries *int) (time.Duration, bool) {
	if !raGlibcWanted() || emuCoreUpdateModeNow() == coreUpdateOff {
		return 0, true
	}
	ra := raGlibcResolve(coreRetroArchBinary(deviceRetroArchCoresDir()))
	if ra == "" {
		// No 64-bit RetroArch yet: raget_ui.go may be installing one.
		return 5 * time.Minute, false
	}
	if privateGlibcFullReady() {
		raGlibcProbePending(ra)
		return 0, true
	}
	if !raGlibcNamespaceOK() {
		logInfo("emulators", "JukaHub's glibc %s is not fetched ahead: this firmware gives a game no mount namespace, so RetroArch could not start on it", privateGlibcVersion)
		return 0, true
	}
	if !IsOnline() || gameScreenSuspended() {
		return time.Minute, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), coreRuntimeTimeout)
	err := privateGlibcInstallFor(ctx, true)
	cancel()
	raGlibcRuntimeForget()
	if err != nil {
		*tries++
		logWarn("emulators", "JukaHub's glibc %s with its C++ library, ahead of the cores (try %d): %v", privateGlibcVersion, *tries, err)
		if *tries >= coreRuntimeTries {
			return 0, true
		}
		return 15 * time.Minute, false
	}
	logInfo("emulators", "JukaHub's glibc %s with GCC 14's C++ library (GLIBCXX up to 3.4.33) is installed ahead of the cores that need it", privateGlibcVersion)
	raGlibcForgetGapRefusals()
	raGlibcProbePending(ra)
	coreSurveyForget()
	invalidateEmulatorCache()
	return 0, true
}
