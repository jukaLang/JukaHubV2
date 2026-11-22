package main

// corefix.go — a core RetroArch cannot open on this firmware gets a build it
// can open.
//
// The libretro buildbot builds its linux/aarch64 nightlies on a newer Ubuntu
// than the TrimUI firmware runs: in November 2026 its mgba_libretro.so needed
// GLIBC_2.35 from libm.so.6, and the stock Smart Pro (glibc 2.33, libm up to
// GLIBC_2.32) cannot open it. JukaHub's own updates never install such a build
// (elfcompat.go refuses it before anything is replaced), but RetroArch's own
// Online Updater ("Update Installed Cores", "Core Downloader") fetches the same
// nightlies with no check at all, and so does a core copied over by hand. On
// stock firmware Game Boy, Game Boy Color and Game Boy Advance all run on mGBA,
// so one bad file stops three consoles: every game quits a second after A, and
// the diagnosis card could only say "replace it with a build for this device"
// with nowhere to get one.
//
// So a core that cannot load is replaced, from the first of:
//
//  1. a build of it the card kept (the .jukahub-<time>.bak beside it) that
//     loads here — the build the card had before JukaHub's first update
//     (the firmware's, CrossMix's) first, else the newest;
//  2. JukaHub's own build for this device: the rolling release "cores-aarch64"
//     of JukaHub's repository, built from upstream libretro sources in an Ubuntu
//     20.04 (glibc 2.31) root by .github/workflows/retroarch-cores.yml, and
//     indexed in the buildbot's own format, so fetchCoreUpdate downloads it
//     with the usual checks (allowlisted hosts, the index's CRC32, the load
//     check against this firmware's libraries).
//
// The broken build is kept as a .bak like any replaced build (installCoreFile);
// no install path takes it back, since every one runs the load check. Two
// moments start a fix: a game whose diagnosis says the core cannot
// load (the reaper, gamediag.go — the card says a fix is under way, a toast
// says when it is done), and a look over every active core a minute after
// JukaHub starts, which catches what RetroArch's updater broke while JukaHub
// was closed. That look remembers the cores that loaded, by size and time, so a
// start reads only the files that changed.
//
// Since 2026-11-19 RetroArch may start on JukaHub's private glibc 2.39
// (ra_glibc.go, setting "RetroArch's C library", Automatic by default): the
// load check then reads that runtime's libraries first, so a nightly that only
// needs a newer glibc loads and is not "broken" here. The look installs the
// runtime for such cores when it may download, and fixes them as above only
// when RetroArch cannot start on it (the setting is off, a probe refused it).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// coreFixReleaseBase is the release the index and the builds come from.
	coreFixReleaseBase = "https://github.com/jukaLang/JukaHubV2/releases/download/cores-aarch64/"
	// coreFixScanDelay keeps the look over the cores out of the boot path, and
	// after the one-time GPU repair (coreGPURepairDelay).
	coreFixScanDelay = 60 * time.Second
	// coreFixTimeout bounds one fix or one start's look, downloads included.
	coreFixTimeout = 10 * time.Minute
	// coreFixIndexTTL is how long a fetched index is used before it is fetched
	// again: one start's look and the games after it share one fetch.
	coreFixIndexTTL = time.Hour
)

// The URLs are variables so tests can point the fix at a local server.
var (
	coreFixIndexURL = coreFixReleaseBase + "index-extended.txt"
	coreFixBaseURL  = coreFixReleaseBase
)

// coreFixSupported reports a device whose cores the fix may replace: the
// 64-bit handheld, the platform the release's builds are for.
func coreFixSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && IsTrimuiDevice() && emuCoreInstallSupported()
}

var coreFixState struct {
	mu sync.Mutex
	// running holds the cores a fix is working on now.
	running map[string]bool
	// index is the release's index as last fetched, at indexAt.
	index   []coreIndexEntry
	indexAt time.Time
}

// coreFixBegin claims core for one fix; false when one is already running.
func coreFixBegin(core string) bool {
	coreFixState.mu.Lock()
	defer coreFixState.mu.Unlock()
	if coreFixState.running == nil {
		coreFixState.running = map[string]bool{}
	}
	if coreFixState.running[core] {
		return false
	}
	coreFixState.running[core] = true
	return true
}

func coreFixEnd(core string) {
	coreFixState.mu.Lock()
	delete(coreFixState.running, core)
	coreFixState.mu.Unlock()
}

// coreFixIndexForget drops the index fetched last, so the next look asks
// GitHub again: a press looking for a core the cores workflow may have
// published since (dolphin_setup.go).
func coreFixIndexForget() {
	coreFixState.mu.Lock()
	coreFixState.index, coreFixState.indexAt = nil, time.Time{}
	coreFixState.mu.Unlock()
}

// coreFixIndex is the release's index, fetched at most once an hour.
func coreFixIndex(ctx context.Context, client *http.Client) ([]coreIndexEntry, error) {
	coreFixState.mu.Lock()
	if coreFixState.index != nil && time.Since(coreFixState.indexAt) < coreFixIndexTTL {
		index := coreFixState.index
		coreFixState.mu.Unlock()
		return index, nil
	}
	coreFixState.mu.Unlock()
	data, err := fetchUpdateBytes(ctx, client, coreFixIndexURL, patchMaxMetadata)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			// The release is not published (yet): nothing to fetch, which no
			// retry changes until it is.
			return nil, errCoreFixNoBuild
		}
		return nil, err
	}
	index := parseCoreIndex(string(data))
	if len(index) == 0 {
		return nil, errors.New("JukaHub's core index is empty")
	}
	coreFixState.mu.Lock()
	coreFixState.index, coreFixState.indexAt = index, time.Now()
	coreFixState.mu.Unlock()
	return index, nil
}

// coreFixEntry is core's line in the index.
func coreFixEntry(index []coreIndexEntry, core string) (coreIndexEntry, bool) {
	for _, e := range index {
		if e.Core == core {
			return e, true
		}
	}
	return coreIndexEntry{}, false
}

// coreFixResult is what one fix did.
type coreFixResult struct {
	Core string
	// Fixed: a build that loads is in place now. Source says which.
	Fixed  bool
	Source string
	// Problem is why the build that was there could not load.
	Problem string
	Err     error
}

// errCoreFixNoBuild is a core with no kept build that loads and no build in
// JukaHub's release.
var errCoreFixNoBuild = errors.New("no build of it that runs here is kept on the card or published for this device")

// errCoreFixNoKept is a core with no kept build that loads, when the fix may
// not download one (offline, or a start's look while Emulator updates is not
// Install automatically).
var errCoreFixNoKept = errors.New("no build of it that runs here is kept on the card, and none was downloaded (offline, or Emulator updates is not Install automatically); a game of it that fails fetches one")

// coreFixCore gives core, whose build at path RetroArch cannot open on this
// firmware, a build it can open: a kept build first, then the release's. The
// caller holds the patch slot. A build that loads (one fixed meanwhile) is
// left alone with Fixed false and no error.
func coreFixCore(ctx context.Context, client *http.Client, core, path string) coreFixResult {
	core = normalizeCoreName(core)
	res := coreFixResult{Core: core}
	dir := filepath.Dir(path)
	fileCore, _ := parseCoreFile(filepath.Base(path))
	switch {
	case core == "" || strings.TrimSpace(path) == "" || !regularFile(path):
		res.Err = errors.New("the core file is not there")
		return res
	case normalizeCoreName(fileCore) != core:
		// Another core's build must never be written under this file's name.
		res.Err = fmt.Errorf("%s is not a build of the %s core", filepath.Base(path), core)
		return res
	case coreDirIsSystem(dir):
		res.Err = fmt.Errorf("it is part of the firmware (%s), which JukaHub does not change", dir)
		return res
	case !isRetroArchCoreDir(dir):
		res.Err = fmt.Errorf("%s is not one of RetroArch's cores folders", dir)
		return res
	}
	res.Problem = coreLoadProblemFile(path, path)
	if res.Problem == "" {
		return res
	}
	cur, err := coreCRC32File(path)
	if err != nil {
		res.Err = err
		return res
	}
	// 1. A build the card kept.
	if bak, ok := coreFixKeptBuild(core, path, cur); ok {
		bak.Target = path
		// Asked before the restore: rollbackCoreBuild removes the .bak.
		original := coreBackupIsOriginalCRC(core, bak)
		_, err := rollbackCoreBuild(bak)
		if err == nil {
			res.Fixed = true
			res.Source = "the build kept from " + bak.Stamp.Format("2006-01-02")
			if original {
				res.Source = "the build the card came with"
			}
			coreFixDone(core, path, cur, res.Problem)
			return res
		}
		logWarn("emulators", "core fix %s: restoring %s failed: %v", core, filepath.Base(bak.Path), err)
	}
	// 2. JukaHub's build for this device. A nil client is a caller that may
	// not download (the start's look with core updates Off).
	if client == nil {
		res.Err = errCoreFixNoKept
		return res
	}
	if err := ctx.Err(); err != nil {
		res.Err = err
		return res
	}
	index, err := coreFixIndex(ctx, client)
	if err != nil {
		res.Err = fmt.Errorf("JukaHub's builds for this device: %w", err)
		return res
	}
	entry, ok := coreFixEntry(index, core)
	if !ok {
		res.Err = errCoreFixNoBuild
		return res
	}
	if reason, refused := coreBuildRefused(core, entry.CRC32); refused {
		res.Err = &coreRefusedError{Core: core, Build: entry.Date + " (JukaHub's)", Reason: "was refused here before: " + reason}
		return res
	}
	pool := compatCorePoolDir()
	// fetchCoreUpdate runs the load check against path before the build
	// reaches the pool, and records a refusal itself.
	build, err := fetchCoreUpdate(ctx, client, coreUpdate{
		Core:   core,
		Date:   entry.Date,
		CRC32:  entry.CRC32,
		URL:    coreFixBaseURL + entry.File,
		Active: path,
	}, pool)
	if err != nil {
		if strings.Contains(err.Error(), "checksum mismatch") {
			// The release was republished since the index was fetched.
			coreFixState.mu.Lock()
			coreFixState.index = nil
			coreFixState.mu.Unlock()
		}
		res.Err = err
		return res
	}
	// Straight over the file that failed (not activateCoreBuild's choice of
	// the active copy, which may be another folder's), and without the
	// probation: its rollback would bring back a build that cannot load.
	backup, err := installCoreFile(build.Path, path)
	if err != nil {
		res.Err = err
		return res
	}
	if !coreKeepActivatedInPool() {
		if dropErr := coreDropPoolBuild(pool, build); dropErr != nil {
			logDebug("emulators", "core fix %s: kept the pool copy: %v", core, dropErr)
		}
	}
	logInfo("emulators", "core fix %s: installed JukaHub's %s build (crc %s) over %s; the build that could not load is %s", core, entry.Date, entry.CRC32, path, filepath.Base(backup))
	res.Fixed = true
	res.Source = "JukaHub's " + entry.Date + " build for this device"
	coreFixDone(core, path, cur, res.Problem)
	return res
}

// coreBackupIsOriginalCRC reports whether a kept build is the one the card had
// before JukaHub's first update.
func coreBackupIsOriginalCRC(core string, b coreBackup) bool {
	orig := coreOriginalCRC(core)
	if orig == "" {
		return false
	}
	sum, err := coreCRC32File(b.Path)
	return err == nil && strings.EqualFold(sum, orig)
}

// coreFixKeptBuild picks the kept build of core beside path that loads here:
// the card's original when it is kept and loads, else the newest one that
// loads. A copy of the broken build (same checksum) is never picked.
func coreFixKeptBuild(core, path, cur string) (coreBackup, bool) {
	orig := coreOriginalCRC(core)
	var newest coreBackup
	found := false
	for _, b := range coreBackupsIn(filepath.Dir(path), core) {
		sum, err := coreCRC32File(b.Path)
		if err != nil || strings.EqualFold(sum, cur) {
			continue
		}
		// A build a probation rolled back loads, but could not start a game.
		if _, refused := coreBuildRefused(core, sum); refused {
			continue
		}
		if coreLoadProblemFile(b.Path, path) != "" {
			continue
		}
		if orig != "" && strings.EqualFold(sum, orig) {
			return b, true
		}
		if !found {
			newest, found = b, true
		}
	}
	return newest, found
}

// coreFixDone tells every cache that read the old file. The broken build is not
// recorded as refused here: no install path takes a build that fails the load
// check, the daily pass records the refusal itself the one time it meets that
// build, and core-refused.json keeps one build per core — this record would
// push out a probation's refusal of a build that loads but crashes, the only
// thing that keeps the daily pass from putting that one back.
func coreFixDone(core, path, brokenCRC, problem string) {
	emuCRCForget(path)
	invalidateEmulatorCache()
	invalidateCompatCache()
	emuForgetUpdate(core)
	coreBuildsRequestReload()
	logWarn("emulators", "core fix %s: the build at %s (crc %s) could not load here (%s); replaced", core, path, brokenCRC, problem)
}

// coreFixHasKept reports whether any build of core is kept beside path — a
// fix may then work offline.
func coreFixHasKept(core, path string) bool {
	return len(coreBackupsIn(filepath.Dir(path), normalizeCoreName(core))) > 0
}

// ── After a game ─────────────────────────────────────────────────────────

// coreFixAfterGame is the reaper's hook for a game that did not start
// (gamediag.go): when a finding says its core cannot load on this device, the
// fix starts in the background and the card gets a finding of its own (ok).
// The fix acts on the file that finding names, and takes the core's name from
// that file: RetroArch's log may name another core (a launch script that can
// run several), and one core's build must never land under another's name.
func coreFixAfterGame(issues []launchIssue) (issue launchIssue, ok bool) {
	if !coreFixSupported() {
		return launchIssue{}, false
	}
	path := ""
	for _, is := range issues {
		if is.Level < int(gameDiagMaybe) && strings.Contains(strings.ToLower(is.Title), "cannot load on this device") {
			path = is.Where
			break
		}
	}
	if path == "" || !regularFile(path) || coreDirIsSystem(filepath.Dir(path)) || !isRetroArchCoreDir(filepath.Dir(path)) {
		return launchIssue{}, false
	}
	core, _ := parseCoreFile(filepath.Base(path))
	core = normalizeCoreName(core)
	problem := coreLoadProblemFile(path, path)
	if core == "" || problem == "" {
		return launchIssue{}, false
	}
	label := coreLabelFor(core)
	// JUKAHUB_NO_CORE_AUTOUPDATE stands every core download down; a kept build
	// is still put back.
	download := IsOnline() && !coreAutoUpdateDisabled()
	if !download && !coreFixHasKept(core, path) {
		if coreAutoUpdateDisabled() {
			return launchIssue{
				Level:  int(gameDiagSure),
				Title:  "The " + label + " cannot run on this firmware",
				Detail: "RetroArch cannot open this build of the core here (" + problem + "), and JUKAHUB_NO_CORE_AUTOUPDATE turns JukaHub's core downloads off. Put a build for this device in its place by hand (docs/COMPATIBILITY.md, \"A core that is already broken\").",
			}, true
		}
		return launchIssue{
			Level:  int(gameDiagSure),
			Title:  "Connect to the internet to fix the " + label,
			Detail: "RetroArch cannot open this build of the core on this firmware (" + problem + "). Connect to Wi-Fi and start the game again: JukaHub then puts its own build for this device in its place.",
		}, true
	}
	if !coreFixBegin(core) {
		return launchIssue{
			Level:  int(gameDiagSure),
			Title:  "JukaHub is already fixing the " + label,
			Detail: "A message says when it is done; then start the game again.",
		}, true
	}
	go func() {
		defer coreFixEnd(core)
		if !coreRepairLock() {
			ShowToast("The "+label+" could not be fixed yet: an update or a game held the device. Start the game again to retry", ToastKindWarning)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), coreFixTimeout)
		var client *http.Client
		if download {
			client = newEmulatorUpdateClient()
		}
		res := coreFixCore(ctx, client, core, path)
		cancel()
		endPatchOp()
		switch {
		case res.Fixed:
			ShowToast("The "+label+" works again ("+res.Source+") — start the game again", ToastKindSuccess)
		case res.Err != nil:
			logWarn("emulators", "core fix %s after a game: %v", core, res.Err)
			ShowToast("The "+label+" still cannot run here: "+emuFriendlyError(res.Err), ToastKindWarning)
		default:
			ShowToast("The "+label+" loads here now — start the game again", ToastKindInfo)
		}
	}()
	return launchIssue{
		Level: int(gameDiagSure),
		Title: "JukaHub is fixing the " + label,
		Detail: "RetroArch cannot open this build of the core on this firmware (" + problem + "); RetroArch's own core updater installs such builds. " +
			"JukaHub is putting a build that runs here in its place — one the card kept, else JukaHub's own build for this device. A message says when it is done; then start the game again.",
	}, true
}

// ── A look over every core after start ───────────────────────────────────

// coreFixScanFile is core-loadcheck.json: the core files that loaded, by path,
// with the size and time they had then, and the firmware libraries those
// answers relied on. A changed library (a firmware update) drops every answer.
// Check is the version of the load check the answers came from
// (coreFixCheckVersion): a JukaHub whose check reads more drops them. Told
// holds, per core, the checksum of the broken build whose failed fix a start's
// toast already named, so the next starts only log it.
type coreFixScanFile struct {
	Check int               `json:"check"`
	Libs  string            `json:"libs"`
	OK    map[string]string `json:"ok"`
	Told  map[string]string `json:"told,omitempty"`
}

// coreFixCheckVersion is raised whenever coreLoadProblemFile learns to find a
// problem it did not before, so the remembered "it loads" answers are read
// again. 2 (2026-11-20): device-access libraries the firmware lacks
// (coreDeviceLibLoadProblem, coretrimui.go).
const coreFixCheckVersion = 2

// coreFixScanMu serialises core-loadcheck.json's read-modify-write.
var coreFixScanMu sync.Mutex

func coreFixScanPath() string { return filepath.Join(compatDir(), "core-loadcheck.json") }

// coreFixScanLoadLocked reads the file ("" state when it is missing or bad).
func coreFixScanLoadLocked() coreFixScanFile {
	var state coreFixScanFile
	if data, err := os.ReadFile(coreFixScanPath()); err == nil && len(data) <= 1<<20 {
		_ = json.Unmarshal(data, &state)
	}
	return state
}

func coreFixScanSaveLocked(state coreFixScanFile) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	if err := writeCompatAtomic(coreFixScanPath(), append(data, '\n'), 0o644); err != nil {
		logDebug("emulators", "could not save the core load check: %v", err)
	}
}

// coreFixFileStamp is a file's size and time, the key a remembered answer is
// valid for.
func coreFixFileStamp(info os.FileInfo) string {
	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

// coreFixLibStamp names the firmware libraries a core's load check reads, as
// they are now.
func coreFixLibStamp() string {
	env := elfHostEnv(nil)
	parts := make([]string, 0, 5)
	for _, lib := range []string{"libc.so.6", "libm.so.6", "libstdc++.so.6", "libgcc_s.so.1", "libz.so.1"} {
		info, state := elfFindLibrary(lib, env.class, env.machine, env.libDirs)
		if state == elfLibMissing {
			parts = append(parts, lib+"=")
			continue
		}
		parts = append(parts, lib+"="+info.path+":"+strconv.FormatInt(info.size, 10)+":"+strconv.FormatInt(info.mod.UnixNano(), 10))
	}
	// The RetroArch that opens the cores decides their architecture
	// (coreLoadEnv): a swapped one reads every core again.
	for _, dir := range retroArchCoreDirs() {
		if ra := coreRetroArchBinary(dir); ra != "" {
			if st, err := os.Stat(ra); err == nil {
				parts = append(parts, ra+"="+coreFixFileStamp(st))
			}
		}
	}
	// And whether RetroArch starts on JukaHub's private C library, which the
	// load check then reads first (ra_glibc.go).
	parts = append(parts, "glibc="+raGlibcStamp())
	return strings.Join(parts, ";")
}

// coreFixBroken is one active core that cannot load here.
type coreFixBroken struct {
	core, path, problem string
}

// coreFixScanSure reports a load problem the start's look may act on without a
// failed game behind it: the wrong architecture, or a C library symbol version
// the firmware's glibc does not define (libc, libm, libpthread, libdl, librt).
// Nothing a launch script could supply through its own library path decides
// those. Anything else — a GLIBCXX gap, desktop OpenGL — waits for a game that
// fails (coreFixAfterGame), which proves it.
func coreFixScanSure(problem string) bool {
	return strings.HasPrefix(problem, "it is a") || coreFixGlibcGapRe.MatchString(problem)
}

// coreFixGlibcGapRe matches elfVersionGap's sentence for a glibc library:
// "needs GLIBC_2.35; this firmware's libm.so.6 has up to GLIBC_2.32".
var coreFixGlibcGapRe = regexp.MustCompile(`needs GLIBC_[0-9A-Z_.]+(?:; this firmware's|, which this firmware's) (?:libc\.so\.6|libm\.so\.6|libpthread\.so\.0|libdl\.so\.2|librt\.so\.1) `)

// coreFixFindBroken checks every active core outside the firmware's folders,
// skipping the files that loaded last time and have not changed since. stop
// ends the walk early (a game started); the answers so far are kept.
func coreFixFindBroken(stop func() bool) []coreFixBroken {
	coreFixScanMu.Lock()
	defer coreFixScanMu.Unlock()
	state := coreFixScanLoadLocked()
	before, _ := json.Marshal(state)
	libs := coreFixLibStamp()
	if state.Check != coreFixCheckVersion || state.Libs != libs || state.OK == nil {
		// The libraries (a firmware update) or the check changed: every
		// answer is read again. What a toast already said stays said.
		state = coreFixScanFile{Check: coreFixCheckVersion, Libs: libs, OK: map[string]string{}, Told: state.Told}
	}
	next := map[string]string{}
	var broken []coreFixBroken
	inv := scanCoreInventory()
	cores := make([]string, 0, len(inv.Active))
	for core := range inv.Active {
		cores = append(cores, core)
	}
	sort.Strings(cores)
	for _, core := range cores {
		b := inv.Active[core]
		// Only libretro cores: a helper library beside them parses as a core
		// name too, and no build of it is published.
		if b.Path == "" || coreDirIsSystem(filepath.Dir(b.Path)) || !strings.Contains(strings.ToLower(filepath.Base(b.Path)), "_libretro.") {
			continue
		}
		info, err := os.Stat(b.Path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		stamp := coreFixFileStamp(info)
		if state.OK[b.Path] == stamp {
			next[b.Path] = stamp
			continue
		}
		if stop != nil && stop() {
			// Keep what is known of the rest for the next start.
			for p, s := range state.OK {
				if _, seen := next[p]; !seen {
					next[p] = s
				}
			}
			break
		}
		if problem := coreLoadProblemFile(b.Path, b.Path); problem != "" {
			if coreFixScanSure(problem) {
				broken = append(broken, coreFixBroken{core: core, path: b.Path, problem: problem})
			} else {
				logInfo("emulators", "core fix: %s may not load here (%s); left for a game to prove", core, problem)
			}
			continue
		}
		next[b.Path] = stamp
	}
	state.OK = next
	if after, err := json.Marshal(state); err != nil || string(after) != string(before) {
		coreFixScanSaveLocked(state)
	}
	return broken
}

// startCoreFixScan runs the look a minute after start (startCoreAutoUpdate,
// handheld only), off the boot path.
func startCoreFixScan() {
	if !coreFixSupported() {
		return
	}
	go func() {
		time.Sleep(coreFixScanDelay)
		coreFixScan()
	}()
}

// coreFixScan finds the active cores that cannot load and fixes each, then says
// in one toast what it did. A core with nothing to put in its place is logged,
// and said once per build of it (coreFixToldOnce).
func coreFixScan() {
	for gameScreenSuspended() {
		time.Sleep(10 * time.Second)
	}
	// A RetroArch never asked under the current private C library is probed
	// first, so the answers below rest on a probe (ra_glibc.go).
	raGlibcProbeAll()
	broken := coreFixFindBroken(gameScreenSuspended)
	// A core that only needs a newer glibc than the firmware's keeps its build
	// when RetroArch can start on JukaHub's: that runtime is installed (only
	// when this look may download, as below) and the cores are asked again.
	if len(broken) > 0 && raGlibcRescue(broken, emuCoreUpdateModeNow() == coreUpdateInstall && !coreAutoUpdateDisabled() && IsOnline()) {
		broken = coreFixFindBroken(gameScreenSuspended)
	}
	if len(broken) == 0 {
		return
	}
	names := make([]string, 0, len(broken))
	for _, b := range broken {
		names = append(names, b.core+" ("+b.problem+")")
	}
	logWarn("emulators", "core fix: %d core(s) cannot load on this firmware: %s", len(broken), strings.Join(names, "; "))
	if !coreRepairLock() {
		logWarn("emulators", "core fix: the device stayed busy; the cores are looked at again at the next start")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), coreFixTimeout)
	// Nothing is downloaded unasked unless Emulator updates is Install
	// automatically (Notify me promises that nothing is installed): otherwise
	// only kept builds are put back. A game that fails still fetches one
	// (coreFixAfterGame).
	var client *http.Client
	if emuCoreUpdateModeNow() == coreUpdateInstall && !coreAutoUpdateDisabled() && IsOnline() {
		client = newEmulatorUpdateClient()
	}
	var fixed, failed []string
	for _, b := range broken {
		if ctx.Err() != nil || gameScreenSuspended() {
			break
		}
		if !coreFixBegin(b.core) {
			continue
		}
		res := coreFixCore(ctx, client, b.core, b.path)
		coreFixEnd(b.core)
		switch {
		case res.Fixed:
			fixed = append(fixed, coreConfigDisplayName(b.core))
		case res.Err != nil:
			logWarn("emulators", "core fix %s: %v", b.core, res.Err)
			// Said once per broken build, and only when nothing will change by
			// itself: no build exists for it, or the one there is was refused.
			// A download that failed or a handheld offline is tried again at
			// the next start, quietly.
			var refused *coreRefusedError
			definite := errors.Is(res.Err, errCoreFixNoBuild) || errors.As(res.Err, &refused)
			if definite {
				if sum, err := emuCachedCRC32(b.path); err == nil && coreFixToldOnce(b.core, sum) {
					failed = append(failed, coreConfigDisplayName(b.core))
				}
			}
		}
	}
	cancel()
	endPatchOp()
	switch {
	case len(fixed) > 0 && len(failed) > 0:
		ShowToast("Fixed cores that could not run here: "+strings.Join(fixed, ", ")+" · still broken: "+strings.Join(failed, ", "), ToastKindWarning)
	case len(fixed) > 0:
		ShowToast("Fixed cores that could not run on this firmware: "+strings.Join(fixed, ", "), ToastKindSuccess)
	case len(failed) > 0:
		ShowToast("These cores cannot run on this firmware and JukaHub has no build to replace them: "+strings.Join(failed, ", "), ToastKindWarning)
	}
}

// coreFixToldOnce reports whether the failed fix of this build of core is news:
// true the first time, after which core-loadcheck.json remembers it and later
// starts only log it. A game of that core still gets the card and its own try.
func coreFixToldOnce(core, crc string) bool {
	coreFixScanMu.Lock()
	defer coreFixScanMu.Unlock()
	state := coreFixScanLoadLocked()
	if state.Told[core] == crc {
		return false
	}
	if state.Told == nil {
		state.Told = map[string]string{}
	}
	state.Told[core] = crc
	coreFixScanSaveLocked(state)
	return true
}
