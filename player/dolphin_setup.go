package main

// dolphin_setup.go — what a GameCube or Wii game needs before it starts in the
// Dolphin core (dolphin.go): the core, a build new enough, and Dolphin's Sys
// folder.
//
//   - The core comes from the first of two sources that has a build this
//     handheld can load (dolphinSources). First JukaHub's own build for
//     TrimUI: the rolling release "cores-aarch64", compiled from
//     libretro/dolphin by .github/workflows/retroarch-cores.yml with GCC 13
//     and libstdc++ linked in, in the Ubuntu 20.04 (glibc 2.31) root, so it
//     loads on the firmware's own C library and starts from the stock menu
//     too. Then the libretro buildbot's linux/aarch64 nightly, which on
//     2026-11-20 needed GLIBCXX_3.4.32 (GCC 13's libstdc++, newer than the
//     firmware's): it installs when RetroArch can start on JukaHub's private
//     glibc 2.39 (ra_glibc.go), fetched and probed first. As a desktop build
//     it also links libusb and libudev, which no TrimUI firmware has (a Brick
//     Pro refused it for libusb on 2026-11-22): JukaHub writes stand-ins for
//     them beside that glibc, at the install and before every start
//     (coredevlibs.go, coredevstub.go). When neither source gives a build,
//     the error says why for each, JukaHub's own first. Both go through the
//     door every core install uses (fetchCoreUpdate: allowlisted hosts, the
//     index's CRC32, the load check against the file the build becomes;
//     activateCoreBuild). On 2026-11-20 a Brick Pro's press on the Emulators
//     page's Dolphin tile was refused all the same: the load check of a core
//     the card did not have yet asked about no file, so it never counted the
//     runtime (fetchCoreUpdate now asks about the file activation writes).
//   - The buildbot's daily core updates leave the core to this setup on the
//     handheld (dolphinManagesCore): a nightly must not replace JukaHub's
//     build with one that needs the runtime, and Dolphin is among the largest
//     downloads the buildbot has, with a new build nearly every day. A press
//     on the Emulators page's Dolphin tile looks for the newest build that
//     loads here (dolphinGetNewest); JukaHub's own build is only ever
//     replaced by a newer one of its own.
//   - A core built before dolphinRaceFixed freezes: libretro/dolphin#462 —
//     JITARM64 with the dual-core option (dolphin_main_cpu_thread, on by
//     default) stopped an aarch64 RetroArch about ten seconds into a game, its
//     main thread waiting in the GPU loop for a stop the CPU thread had sent
//     too early. PR #523 (merged 2026-10-23) keeps that stop. The file holds no
//     build date, so a core counts as older when it was last written before
//     that day by a clock already past it, and it is not the file this setup
//     last installed or checked (dolphin.json: path, size and time). A TrimUI
//     whose clock is not set yet never condemns a core.
//   - Dolphin reads system/dolphin-emu/Sys (GameSettings with the per-game
//     fixes, the GameCube's fonts, codehandler.bin for cheats) from
//     RetroArch's system folder. RetroArch's own Core System Files Downloader
//     fetches it as Dolphin.zip from the buildbot's assets; JukaHub fetches
//     the same zip and adds what is missing, never replacing a file that is
//     there (a player's IPL.bin in Sys/GC/<region> stays).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// dolphinSysURL is Dolphin's Sys folder as RetroArch's Core System Files
	// Downloader fetches it.
	dolphinSysURL = "https://buildbot.libretro.com/assets/system/Dolphin.zip"
	// dolphinSysMaxBytes bounds that download (it is tens of MB).
	dolphinSysMaxBytes = int64(512 << 20)
	// dolphinSysNeedBytes is the free space the card keeps while the zip is
	// there and unpacked beside it.
	dolphinSysNeedBytes = int64(256 << 20)
	// dolphinSetupTimeout bounds one setup: a core and the zip on handheld
	// Wi-Fi.
	dolphinSetupTimeout = 40 * time.Minute
	// dolphinStateName is the record in JukaHub's data folder.
	dolphinStateName = "dolphin.json"
	// dolphinWorkName is the setup's scratch folder inside RetroArch's system
	// folder (the same card, so the unpacked files move with a rename).
	dolphinWorkName = ".jukahub-dolphin"
	// dolphinSysUnfinished is the marker in Sys while JukaHub merges into it.
	dolphinSysUnfinished = ".jukahub-unfinished"
)

// dolphinRaceFixed is when libretro/dolphin merged the fix for the dual-core
// freeze on aarch64 (PR #523, issue #462).
var dolphinRaceFixed = time.Date(2026, time.October, 23, 0, 0, 0, 0, time.UTC)

// dolphinRecord is dolphin.json.
type dolphinRecord struct {
	// The core file this setup last installed or found current, as it was.
	CorePath string `json:"corePath,omitempty"`
	CoreSize int64  `json:"coreSize,omitempty"`
	CoreTime int64  `json:"coreTime,omitempty"` // modification time, Unix seconds
	CoreDate string `json:"coreDate,omitempty"` // its source index's date for it
	// CoreFrom names that source (dolphinSource.name, "" when unknown), and
	// CoreGlibc reports a build that starts on JukaHub's glibc only.
	CoreFrom  string `json:"coreFrom,omitempty"`
	CoreGlibc bool   `json:"coreGlibc,omitempty"`
	// Video is the driver JukaHub wrote into Dolphin's RetroArch override at
	// VideoPath ("" when it wrote none); VideoFailed lists the drivers a start
	// could not draw with in the core file VideoCore names (path, size and
	// time: a new build, from any updater, starts the list again;
	// dolphin_video.go).
	Video       string   `json:"video,omitempty"`
	VideoPath   string   `json:"videoPath,omitempty"`
	VideoFailed []string `json:"videoFailed,omitempty"`
	VideoCore   string   `json:"videoCore,omitempty"`
	// Folders lists the consoles ("GameCube", "Wii") whose Roms folder JukaHub
	// found on the card or made there (dolphin_folders.go): one the player
	// deleted since is made again only when its tile is pressed or GameCube &
	// Wii is turned on.
	Folders []string `json:"folders,omitempty"`
}

var dolphinState struct {
	mu     sync.Mutex
	loaded bool
	rec    dolphinRecord
}

// dolphinStatePath is dolphin.json's path, "" without a data folder.
func dolphinStatePath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, dolphinStateName)
}

// dolphinLoadLocked reads the record once. Under dolphinState.mu.
func dolphinLoadLocked() {
	if dolphinState.loaded {
		return
	}
	dolphinState.loaded = true
	path := dolphinStatePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rec dolphinRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		logWarn("library", "GameCube: ignoring %s: %v", path, err)
		return
	}
	dolphinState.rec = rec
}

// dolphinRecordGet is a copy of the record.
func dolphinRecordGet() dolphinRecord {
	dolphinState.mu.Lock()
	defer dolphinState.mu.Unlock()
	dolphinLoadLocked()
	rec := dolphinState.rec
	rec.VideoFailed = slices.Clone(rec.VideoFailed)
	rec.Folders = slices.Clone(rec.Folders)
	return rec
}

// dolphinRecordUpdate changes the record and saves it.
func dolphinRecordUpdate(change func(*dolphinRecord)) {
	dolphinState.mu.Lock()
	defer dolphinState.mu.Unlock()
	dolphinLoadLocked()
	change(&dolphinState.rec)
	path := dolphinStatePath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(dolphinState.rec, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		logWarn("library", "GameCube: could not save %s: %v", path, err)
	}
}

// dolphinNoteCore records path as the core this setup installed or checked
// (date: its source index's date for it, from: that source's name; "" when
// unknown), and whether it loads only on JukaHub's glibc. Off the main
// thread: the last part reads the core's ELF header.
func dolphinNoteCore(path, date, from string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	path = filepath.Clean(path)
	needsGlibc, _ := raGlibcCoreNeeds(path)
	dolphinRecordUpdate(func(r *dolphinRecord) {
		r.CorePath = path
		r.CoreSize = info.Size()
		r.CoreTime = info.ModTime().Unix()
		r.CoreDate = date
		r.CoreFrom = from
		r.CoreGlibc = needsGlibc
	})
}

// dolphinCoreIdentity names a core file as it is now: path, size and time; ""
// when it cannot be read.
func dolphinCoreIdentity(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", filepath.Clean(path), info.Size(), info.ModTime().Unix())
}

// dolphinFindCore is the Dolphin core a game starts in: the active build with
// a RetroArch beside it, or "".
func dolphinFindCore() string {
	core := coreActiveBinary(dolphinCoreName, retroArchCoreDirs())
	if core == "" || coreRetroArchBinary(filepath.Dir(core)) == "" {
		return ""
	}
	return core
}

// dolphinCoreStale reports whether core is (as far as the card can tell) a
// build from before the dual-core freeze fix.
func dolphinCoreStale(core string) bool {
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	rec := dolphinRecordGet()
	if rec.CorePath == filepath.Clean(core) && rec.CoreSize == info.Size() && rec.CoreTime == info.ModTime().Unix() {
		return false
	}
	if !info.ModTime().Before(dolphinRaceFixed) {
		return false
	}
	// A clock that is not past the fix yet (a TrimUI before NTP) proves
	// nothing about a file it wrote.
	return time.Now().After(dolphinRaceFixed.Add(24 * time.Hour))
}

// dolphinNeeds is what a start in Dolphin lacks (dolphinInspect).
type dolphinNeeds struct {
	Core        string // the core file, "" when there is none
	CoreMissing bool
	CoreStale   bool
	SysDir      string // RetroArch's system folder
	SysMissing  bool
	Why         string // what stops the start that no download fixes
}

// dolphinNeedLabel names what a setup fetches, for a toast.
func dolphinNeedLabel(n dolphinNeeds) string {
	core := n.CoreMissing || n.CoreStale
	switch {
	case core && n.SysMissing:
		return "a newer core and its system files"
	case core:
		return "a newer core"
	}
	return "its system files"
}

// dolphinInspect looks at the core launcher starts (the active Dolphin core
// for a script) and RetroArch's system folder for a game at romPath: a few
// stats and RetroArch's config, cheap enough for the main thread.
func dolphinInspect(launcher, romPath string) dolphinNeeds {
	var n dolphinNeeds
	core := launcher
	if !dolphinIsCore(core) {
		core = dolphinFindCore()
	}
	n.Core = core
	script := strings.TrimSpace(launcher) != "" && !dolphinIsCore(launcher)
	switch {
	case script:
		// A RetroArch script brings its own core: JukaHub neither installs
		// nor replaces one for it.
	case core == "" || !regularFile(core):
		n.CoreMissing = true
	default:
		n.CoreStale = dolphinCoreStale(core)
	}
	from := core
	if script || from == "" {
		from = launcher
	}
	n.SysDir, _, _ = gameDiagSystemDir(from, romPath)
	if dolphinSysPresent(n.SysDir) {
		return n
	}
	// Missing: only a folder of its own can take it.
	if err := dolphinCheckSysDir(n.SysDir, romPath); err != nil {
		n.Why = err.Error()
		return n
	}
	n.SysMissing = true
	return n
}

// dolphinCheckSysDir says why sysDir cannot take Dolphin's Sys folder, or nil.
// RetroArch set to "default" reads system files from each game's own folder,
// where two thousand files would sit among the games.
func dolphinCheckSysDir(sysDir, romPath string) error {
	if strings.TrimSpace(sysDir) == "" {
		return errors.New("RetroArch's system folder could not be found, so Dolphin's system files have nowhere to go")
	}
	if strings.TrimSpace(romPath) != "" && filepath.Clean(sysDir) == filepath.Clean(filepath.Dir(romPath)) {
		return errors.New("RetroArch reads system files from each game's own folder: set its System/BIOS folder (RetroArch → Settings → Directory), then start the game again")
	}
	return nil
}

// dolphinSysPresent reports whether Dolphin's Sys folder is in sysDir: its
// GameSettings folder, which every Sys carries, and no unfinished merge of
// JukaHub's (dolphinSysUnfinished, left by a setup that was stopped or lost
// power halfway).
func dolphinSysPresent(sysDir string) bool {
	if strings.TrimSpace(sysDir) == "" {
		return false
	}
	sys := filepath.Join(sysDir, "dolphin-emu", "Sys")
	info, err := os.Stat(filepath.Join(sys, "GameSettings"))
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(sys, dolphinSysUnfinished))
	return err != nil
}

// ── The setup ────────────────────────────────────────────────────────────

// dolphinCoreWant says what a setup does about a Dolphin core that is there.
type dolphinCoreWant int

const (
	// dolphinKeepCore installs a core only when there is none.
	dolphinKeepCore dolphinCoreWant = iota
	// dolphinFixStale also replaces a build from before the freeze fix.
	dolphinFixStale
	// dolphinGetNewest also brings any other build to the newest one this
	// device takes (a press on the Emulators page's Dolphin tile).
	dolphinGetNewest
)

// dolphinSetup installs what a game at romPath needs to start in Dolphin with
// launcher ("" for the core JukaHub sets up) and returns the core file: the
// core when there is none, a newer one over the build there when want says to
// (an update that fails keeps the old one: kept is why), and the Sys folder.
// A RetroArch script that names the core brings its own: only the Sys folder
// of the RetroArch it starts is fetched for it. romPath is "" for a press with
// no game: the Sys folder is then fetched only into a system folder of
// RetroArch's own (one set to "default" reads it from each game's folder,
// which a game's start sets up). Off the main thread; the caller holds the
// patch slot. phase names each step (with a detail line); progress gets
// download bytes.
func dolphinSetup(ctx context.Context, launcher, romPath string, want dolphinCoreWant, phase func(phase, detail string), progress func(done, total int64)) (core string, kept, err error) {
	client := newEmulatorUpdateClient()
	script := strings.TrimSpace(launcher) != "" && !dolphinIsCore(launcher)
	core = launcher
	if !dolphinIsCore(core) {
		core = dolphinFindCore()
	}
	if !script && (core == "" || want == dolphinGetNewest || (want == dolphinFixStale && dolphinCoreStale(core))) {
		// A system folder that cannot take the Sys folder stops the setup
		// before a core is downloaded for nothing.
		if pre, _, _ := gameDiagSystemDir(core, romPath); romPath != "" && !dolphinSysPresent(pre) {
			if err := dolphinCheckSysDir(pre, romPath); err != nil {
				return "", nil, err
			}
		}
		installed, err := dolphinInstallCore(ctx, client, core, phase)
		switch {
		case err == nil:
			core = installed
		case core != "" && ctx.Err() == nil:
			// Only an update: the old build still plays (the caller says so).
			logWarn("library", "GameCube: the Dolphin core could not be updated, keeping %s: %v", core, err)
			kept = err
		default:
			return "", nil, err
		}
	}
	from := core
	if script || from == "" {
		from = launcher
	}
	sysDir, _, _ := gameDiagSystemDir(from, romPath)
	if !dolphinSysPresent(sysDir) {
		if romPath == "" && !filepath.IsAbs(sysDir) {
			// No game, and RetroArch reads system files from each game's
			// folder: the first game's start fetches them (and says how to
			// give RetroArch a folder of its own).
			logInfo("library", "GameCube: Dolphin's system files wait for a game: RetroArch's system folder is %q", sysDir)
			return core, kept, nil
		}
		if err := dolphinCheckSysDir(sysDir, romPath); err != nil {
			return "", nil, err
		}
		if err := dolphinInstallSys(ctx, client, sysDir, phase, progress); err != nil {
			return "", nil, err
		}
	}
	return core, kept, nil
}

// dolphinSource is one place the Dolphin core comes from.
type dolphinSource struct {
	name string // for the card, the log and dolphin.json
	// index reads the source's list, in the buildbot's index format.
	index func(ctx context.Context, client *http.Client) ([]coreIndexEntry, error)
	base  string // the folder its zips are served from
	own   bool   // JukaHub's own build for TrimUI
}

// dolphinSourceOwn and dolphinSourceBuildbot name the two sources.
const (
	dolphinSourceOwn      = "JukaHub's build for TrimUI"
	dolphinSourceBuildbot = "the libretro buildbot"
)

// dolphinSources lists the sources in the order a setup tries them (see the
// file comment): JukaHub's own build first where JukaHub publishes one for
// this device (coreFixSupported: the 64-bit TrimUI handhelds), then the
// buildbot's nightly.
func dolphinSources() []dolphinSource {
	buildbot := dolphinSource{name: dolphinSourceBuildbot, index: fetchCoreIndex, base: emuCoreBaseURL()}
	if !coreFixSupported() {
		return []dolphinSource{buildbot}
	}
	own := dolphinSource{name: dolphinSourceOwn, index: coreFixIndex, base: coreFixBaseURL, own: true}
	return []dolphinSource{own, buildbot}
}

// dolphinActiveIsOwn reports whether core is the build this setup last
// installed or found current from JukaHub's own source (dolphin.json: the
// same path, size and time).
func dolphinActiveIsOwn(core string) bool {
	if strings.TrimSpace(core) == "" {
		return false
	}
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	rec := dolphinRecordGet()
	return rec.CoreFrom == dolphinSourceOwn && rec.CorePath == filepath.Clean(core) &&
		rec.CoreSize == info.Size() && rec.CoreTime == info.ModTime().Unix()
}

// dolphinManagesCore reports a core the buildbot's core updates leave alone
// (planCoreUpdates, emuCompareCores) and Get more cores does not fetch: on the
// handheld this setup picks the Dolphin build, from the source whose build
// loads here (see the file comment).
func dolphinManagesCore(core string) bool {
	return normalizeCoreName(core) == dolphinCoreName && dolphinSoC() != "" && coreFixSupported()
}

// dolphinInstallCore installs the newest Dolphin core this device can load
// over current ("" for none) and returns the active file: the first source,
// in dolphinSources order, that lists a build the card takes. The active core
// being that source's build already ends the look with nothing downloaded. A
// build the load check refused here before is skipped, unless JukaHub's glibc
// would close what it lacked and RetroArch may start on it (a refusal from
// before a first install counted the runtime; raGlibcRefusalMayPass). The
// firmware's own core, on its read-only partition, stays.
func dolphinInstallCore(ctx context.Context, client *http.Client, current string, phase func(phase, detail string)) (string, error) {
	// The file a build becomes: the core RetroArch loads now, replaced where
	// it is, or a new one in the first cores folder JukaHub may write.
	target, err := coreActivationTarget(coreBuild{Core: dolphinCoreName, File: dolphinCoreName + "_libretro.so"})
	if err != nil {
		if current != "" && coreDirIsSystem(filepath.Dir(current)) {
			logWarn("library", "GameCube: keeping %s: %v", current, err)
			dolphinNoteCore(current, "", "")
			return current, nil
		}
		return "", err
	}
	pool := compatCorePoolDir()
	var lastErr error
	// Why each source gave no build, in the order tried: with two sources the
	// error names both (a tester's Brick Pro, 2026-11-22, was told only the
	// buildbot's refusal, which said nothing about JukaHub's own build).
	var notes []string
	note := func(src dolphinSource, why string) {
		notes = append(notes, src.name+": "+why)
	}
	for _, src := range dolphinSources() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !src.own && dolphinActiveIsOwn(dolphinFindCore()) {
			// JukaHub's build loads on the firmware's own glibc: only a newer
			// one of it replaces it, never a nightly that needs the runtime
			// because GitHub could not be reached just now.
			logInfo("library", "GameCube: keeping JukaHub's Dolphin build: %s is not asked", src.name)
			break
		}
		phase(installPhaseResolving, "Dolphin core: "+src.name)
		index, err := src.index(ctx, client)
		if errors.Is(err, errCoreFixNoBuild) {
			note(src, "no Dolphin build published yet")
			logInfo("library", "GameCube: %s publishes no cores for this device yet", src.name)
			continue
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src.name, err)
			note(src, "its list could not be read ("+firstLine(err.Error())+")")
			logInfo("library", "GameCube: %s cannot be read: %v", src.name, err)
			continue
		}
		if !src.own {
			// The catalog learns what the buildbot publishes (emumorecores.go).
			emuNoteCoreCheck(index, nil, nil, nil)
		}
		entry, ok := coreFixEntry(index, dolphinCoreName)
		if !ok && src.own {
			// Published by the cores workflow after a JukaHub release may be
			// read before: a press asks GitHub again rather than an hour-old
			// copy of the list (coreFixIndexTTL).
			coreFixIndexForget()
			if fresh, err := src.index(ctx, client); err == nil {
				entry, ok = coreFixEntry(fresh, dolphinCoreName)
			}
		}
		if !ok {
			note(src, "no Dolphin build published yet")
			logInfo("library", "GameCube: %s has no Dolphin core", src.name)
			continue
		}
		if active := dolphinFindCore(); active != "" {
			if sum, err := emuCachedCRC32(active); err == nil && sum == entry.CRC32 {
				// This source's newest build already: an old file time said
				// otherwise only because the clock was off when it was written.
				dolphinNoteCore(active, entry.Date, src.name)
				return active, nil
			}
		}
		if reason, refused := coreBuildRefused(dolphinCoreName, entry.CRC32); refused && !raGlibcRefusalMayPass(reason, target) {
			// Not fetched again; the source's next build gets its own check.
			logInfo("library", "GameCube: %s's %s Dolphin build was refused here before: %s", src.name, entry.Date, reason)
			lastErr = &coreRefusedError{Core: dolphinCoreName, Build: entry.Date, Reason: "was refused here before: " + reason}
			note(src, "the "+entry.Date+" build "+reason)
			continue
		}
		phase(installPhaseDownload, "Dolphin core, "+entry.Date+" build · "+src.name)
		build, err := fetchCoreUpdate(ctx, client, coreUpdate{
			Core:   dolphinCoreName,
			Date:   entry.Date,
			CRC32:  entry.CRC32,
			URL:    src.base + entry.File,
			Active: target,
		}, pool)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			logInfo("library", "GameCube: %s's %s Dolphin build: %v", src.name, entry.Date, err)
			lastErr = err
			note(src, firstLine(err.Error()))
			continue
		}
		phase(installPhaseExtract, "Dolphin core, "+entry.Date+" build")
		active, _, err := activateCoreBuild(build)
		emuCRCForget(active)
		if err != nil || !coreKeepActivatedInPool() {
			// Active now (on the handheld a pool copy would be a second copy
			// of a large core), or refused: the pool copy goes either way.
			if dropErr := coreDropPoolBuild(pool, build); dropErr != nil {
				logDebug("library", "GameCube: kept the pool copy of the Dolphin core: %v", dropErr)
			}
		}
		if err != nil {
			logInfo("library", "GameCube: %s's %s Dolphin build not installed: %v", src.name, entry.Date, err)
			lastErr = err
			note(src, firstLine(err.Error()))
			continue
		}
		// The catalog (and with it the games' choices), the compatibility
		// panel and the Core builds page all read the cores folder.
		invalidateEmulatorCache()
		invalidateCompatCache()
		coreBuildsRequestReload()
		if err := emuConfirmInstalledCore(dolphinCoreName, build); err != nil {
			return "", err
		}
		if active = dolphinFindCore(); active == "" {
			return "", errors.New("the Dolphin core is in, but there is no RetroArch beside it to start it")
		}
		dolphinNoteCore(active, entry.Date, src.name)
		logInfo("library", "GameCube: Dolphin core %s from %s installed at %s", entry.Date, src.name, active)
		return active, nil
	}
	if len(notes) > 1 {
		// Every source said why: the card and the toast name them all, JukaHub's
		// own build first.
		logWarn("library", "GameCube: no Dolphin core could be installed · %s", strings.Join(notes, " · "))
		return "", errors.New(strings.Join(notes, "; "))
	}
	if lastErr == nil {
		lastErr = errors.New("no Dolphin core is published for this device right now — try again later")
	}
	return "", lastErr
}

// dolphinInstallSys fetches Dolphin.zip and adds its Sys folder to
// sysDir/dolphin-emu/Sys.
func dolphinInstallSys(ctx context.Context, client *http.Client, sysDir string, phase func(phase, detail string), progress func(done, total int64)) error {
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		return err
	}
	if free, ok := emuMoreCoresLeastFree([]string{sysDir}); ok && free < dolphinSysNeedBytes {
		return fmt.Errorf("only %s free on the card: Dolphin's system files need about %s while they unpack", bootFormatBytes(free), bootFormatBytes(dolphinSysNeedBytes))
	}
	work := filepath.Join(sysDir, dolphinWorkName)
	// What a setup that died midway left.
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archive := filepath.Join(work, "Dolphin.zip")
	phase(installPhaseDownload, "Dolphin's system files")
	if err := dolphinDownload(ctx, client, dolphinSysURL, archive, dolphinSysMaxBytes, progress); err != nil {
		return fmt.Errorf("Dolphin's system files: %w", err)
	}
	phase(installPhaseExtract, "Dolphin's system files")
	stage := filepath.Join(work, "unpacked")
	if err := emuExtractZip(ctx, archive, stage, nil); err != nil {
		return fmt.Errorf("Dolphin's system files: %w", err)
	}
	_ = os.Remove(archive)
	src := dolphinFindSys(stage)
	if src == "" {
		return errors.New("the buildbot's Dolphin.zip has no Sys folder")
	}
	dest := filepath.Join(sysDir, "dolphin-emu", "Sys")
	// The marker says "not finished" until the last file is in: a merge that
	// stops halfway is merged again (it never replaces a file, so that is
	// safe), instead of a half Sys passing for a whole one.
	marker := filepath.Join(dest, dolphinSysUnfinished)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte("JukaHub is adding Dolphin's system files here\n"), 0o644); err != nil {
		return err
	}
	added, kept, err := dolphinMergeTree(ctx, src, dest)
	if err != nil {
		return fmt.Errorf("Dolphin's system files into %s: %w", dest, err)
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	phase(installPhaseVerify, "")
	if !dolphinSysPresent(sysDir) {
		return errors.New("Dolphin's system files did not land in " + dest)
	}
	logInfo("library", "GameCube: Dolphin's Sys folder is in %s (%d files added, %d already there)", dest, added, kept)
	return nil
}

// dolphinFindSys is the Sys folder in an unpacked Dolphin.zip: under
// dolphin-emu/ as RetroArch's downloader lays it out, at the top, or one
// folder down. "" when none has GameSettings.
func dolphinFindSys(stage string) string {
	candidates := []string{
		filepath.Join(stage, "dolphin-emu", "Sys"),
		filepath.Join(stage, "Sys"),
	}
	if entries, err := os.ReadDir(stage); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidates = append(candidates,
					filepath.Join(stage, e.Name(), "Sys"),
					filepath.Join(stage, e.Name(), "dolphin-emu", "Sys"))
			}
		}
	}
	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(c, "GameSettings")); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}

// dolphinMergeTree moves every file of src that dest lacks into dest (the
// same card: a rename) and leaves the ones dest has. It reports the files
// added and kept.
func dolphinMergeTree(ctx context.Context, src, dest string) (added, kept int, err error) {
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if _, statErr := os.Lstat(target); statErr == nil {
			kept++
			return nil
		}
		if renameErr := os.Rename(path, target); renameErr != nil {
			return renameErr
		}
		added++
		return nil
	})
	return added, kept, err
}

// dolphinDownload fetches raw to dest (through dest.part), at most limit
// bytes.
func dolphinDownload(ctx context.Context, client *http.Client, raw, dest string, limit int64, progress func(done, total int64)) error {
	if err := emulatorUpdateValidateURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	if resp.ContentLength > limit {
		return fmt.Errorf("%s is larger than expected", filepath.Base(raw))
	}
	part := dest + ".part"
	defer os.Remove(part)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var body io.Reader = io.LimitReader(resp.Body, limit+1)
	if progress != nil {
		body = &progressReader{r: body, total: resp.ContentLength, fn: progress}
	}
	n, copyErr := io.Copy(out, body)
	syncErr := out.Sync()
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	case n > limit:
		return fmt.Errorf("%s is larger than expected", filepath.Base(raw))
	case resp.ContentLength > 0 && n != resp.ContentLength:
		return fmt.Errorf("%s ended after %d of %d bytes — try again", filepath.Base(raw), n, resp.ContentLength)
	}
	return os.Rename(part, dest)
}
