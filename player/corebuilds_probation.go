package main

// corebuilds_probation.go — a new core build is on probation until it has run
// a game.
//
// Every core build JukaHub activates — the daily pass, the Emulators page's
// Update, the compatibility panel — keeps the build it replaced as a .bak, and
// the user can roll back in one press. That is not much help on a handheld
// whose N64 games quit to the menu a second after A, with no word of why: the
// player does not know that a core changed overnight, let alone which one. And
// the case is real. The buildbot's generic linux/aarch64 builds are made for
// desktop Linux; a GPU-rendered core (corerisk.go) built for desktop OpenGL
// cannot draw under the firmware's OpenGL ES RetroArch, while the firmware's
// own build of it could.
//
// So the build an activation puts in place is watched (coreProbationStart, in
// activateCoreBuild) until the first game that runs it ends (the game's reaper,
// gamediag.go, calls coreProbationJudge):
//
//   - the game ran past the quick-exit window: the build is proven and the
//     watch ends;
//   - it did not start — RetroArch's log says the core could not be opened, the
//     content failed to load or the GPU context was refused, the process
//     crashed, or it exited with an error within seconds, and nothing else (a
//     missing game file, a missing BIOS) explains it: the build it replaced is
//     put back (rollbackCoreBuild, which keeps the failed one as a .bak), its
//     checksum is remembered as refused (coreRefusalRecord) so no automatic
//     pass fetches it again, and one toast says what happened;
//   - anything in between (a quick, clean exit with a quiet log: a player who
//     changed their mind) says nothing, and the next game decides.
//
// The watch is kept in data/performance/core-probation.json, so a reboot
// between the update and the first game changes nothing.
//
// The same file remembers the one-time repair (coreGPURepairOnce): builds of
// 2026-11-10 to 11-18 replaced GPU cores automatically before any of this
// existed, so on the first start of this build a GPU core that JukaHub's own
// update replaced, and that never proved itself here, gets the firmware's
// build back from its .bak — once per core, with a toast, and undone with one
// press (the build it replaces is kept as a .bak like any other). That is the
// safest answer for a player whose N64 is broken now: the firmware's build is
// the one known to draw on this GPU, the generic build is not, and nothing is
// lost either way.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// coreRunOutcome is what the reaper learned about a game's run of a core.
type coreRunOutcome int

const (
	// coreRunUnclear: nothing either way (a quick, clean exit).
	coreRunUnclear coreRunOutcome = iota
	// coreRunProven: the game ran past the quick-exit window.
	coreRunProven
	// coreRunFailed: the core build could not start the game.
	coreRunFailed
)

const (
	// coreGPURepairDelay keeps the one-time repair out of the boot path and
	// ahead of the daily pass (coreAutoUpdateStartupDelay).
	coreGPURepairDelay = 20 * time.Second
	// coreRepairClockWait is how long the repair waits for the clock to be
	// set (coreRepairLock) before it goes ahead without.
	coreRepairClockWait = 3 * time.Minute
	// coreProbationLockWait bounds how long a rollback waits for the patch
	// slot (another install or the daily pass holding it). A first pass over
	// a CrossMix card can run for many minutes, and the card has promised the
	// build back once it ends.
	coreProbationLockWait = 30 * time.Minute
)

// coreProbationWatch is one build being watched.
type coreProbationWatch struct {
	// Target is the active binary the build was written to; Backup is the
	// .bak of the build it replaced.
	Target string `json:"target"`
	Backup string `json:"backup"`
	CRC32  string `json:"crc32"`
	Build  string `json:"build,omitempty"`
	At     string `json:"at,omitempty"`
}

// coreProbationFile is core-probation.json: the builds being watched, the
// last build of each core that proved itself here, and the builds the
// one-time repair already acted on (core → the CRC32 it found).
type coreProbationFile struct {
	Watch    map[string]coreProbationWatch `json:"watch,omitempty"`
	Proven   map[string]string             `json:"proven,omitempty"`
	Repaired map[string]string             `json:"repaired,omitempty"`
	// RepairRan is set once the one-time repair has been through every core:
	// it is the first start of this build's, not every start's.
	RepairRan bool `json:"repairRan,omitempty"`
}

var coreProbations struct {
	mu   sync.Mutex
	path string
	f    *coreProbationFile
}

func coreProbationPath() string { return filepath.Join(compatDir(), "core-probation.json") }

// coreProbationLoadLocked reads the file the first time, and again whenever
// the data folder is another one (a test pointing compatDir elsewhere).
func coreProbationLoadLocked() *coreProbationFile {
	path := coreProbationPath()
	if coreProbations.f != nil && coreProbations.path == path {
		return coreProbations.f
	}
	f := &coreProbationFile{}
	if data, err := os.ReadFile(path); err == nil && len(data) <= 1<<20 {
		_ = json.Unmarshal(data, f)
	}
	if f.Watch == nil {
		f.Watch = map[string]coreProbationWatch{}
	}
	if f.Proven == nil {
		f.Proven = map[string]string{}
	}
	if f.Repaired == nil {
		f.Repaired = map[string]string{}
	}
	coreProbations.path, coreProbations.f = path, f
	return f
}

func coreProbationSaveLocked() {
	data, err := json.MarshalIndent(coreProbations.f, "", "  ")
	if err != nil {
		return
	}
	if err := writeCompatAtomic(coreProbations.path, append(data, '\n'), 0o644); err != nil {
		logWarn("emulators", "could not save the core probation: %v", err)
	}
}

// coreProbationStart watches the build just written to target (activateCoreBuild).
// backup is the .bak of the build it replaced; build its label (the buildbot
// date). A newer activation of the same core replaces the watch.
func coreProbationStart(core, target, backup, build string) {
	core = normalizeCoreName(core)
	if core == "" || target == "" || backup == "" {
		return
	}
	sum, err := coreCRC32File(target)
	if err != nil {
		return
	}
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	f := coreProbationLoadLocked()
	f.Watch[core] = coreProbationWatch{Target: target, Backup: backup, CRC32: sum, Build: build, At: time.Now().UTC().Format(time.RFC3339)}
	coreProbationSaveLocked()
	logInfo("emulators", "core %s %s (crc %s) is on probation until a game has run with it; the build before it is %s", core, build, sum, filepath.Base(backup))
}

// coreProbationFor is the watch on core, if any.
func coreProbationFor(core string) (coreProbationWatch, bool) {
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	w, ok := coreProbationLoadLocked().Watch[normalizeCoreName(core)]
	return w, ok
}

// coreProbationEnd stops watching core; proven, when not "", is the build's
// CRC32, remembered as one that ran a game here.
func coreProbationEnd(core, proven string) {
	core = normalizeCoreName(core)
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	f := coreProbationLoadLocked()
	_, watched := f.Watch[core]
	delete(f.Watch, core)
	if proven != "" {
		f.Proven[core] = strings.ToLower(proven)
	}
	if watched || proven != "" {
		coreProbationSaveLocked()
	}
}

// coreBuildProven reports whether crc is the build of core that last ran a
// game here.
func coreBuildProven(core, crc string) bool {
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	got := coreProbationLoadLocked().Proven[normalizeCoreName(core)]
	return got != "" && strings.EqualFold(got, strings.TrimSpace(crc))
}

// coreProbationJudge is the reaper's report of a game that ran core (gamediag.go);
// path is the file RetroArch's log says it loaded ("" when unknown). It
// returns a sentence for the diagnosis card when the build was (or is about to
// be) put back, "" otherwise. It reads the card: never call it on the render
// thread.
func coreProbationJudge(core, path string, outcome coreRunOutcome, why string) string {
	core = normalizeCoreName(core)
	if core == "" || outcome == coreRunUnclear {
		return ""
	}
	w, ok := coreProbationFor(core)
	if !ok {
		if outcome == coreRunProven && coreNeedsGPU(core) {
			// A GPU core that ran a game is remembered as one that draws
			// here, so the one-time repair never takes a build that works —
			// the file RetroArch's log names when it names one: another
			// cores folder's copy proves nothing about the active one.
			ran := path
			if ran == "" {
				ran = coreActivePath(core)
			}
			if ran != "" {
				if sum, err := emuCachedCRC32(ran); err == nil && !coreBuildProven(core, sum) {
					coreProbationEnd(core, sum)
				}
			}
		}
		return ""
	}
	if path != "" && !coreSameFile(path, w.Target) {
		// RetroArch ran another copy of the core (another cores folder): this
		// game says nothing about the build on probation.
		logInfo("emulators", "core %s: the game ran %s, not the build on probation (%s)", core, path, w.Target)
		return ""
	}
	sum, err := coreCRC32File(w.Target)
	if err != nil || !strings.EqualFold(sum, w.CRC32) {
		// Replaced since (a rollback, another update, a copy by hand):
		// nothing is left to judge.
		coreProbationEnd(core, "")
		return ""
	}
	if outcome == coreRunProven {
		coreProbationEnd(core, w.CRC32)
		logInfo("emulators", "core %s %s ran its first game here; its probation is over", core, w.Build)
		return ""
	}
	logWarn("emulators", "core %s %s (crc %s) could not start its first game: %s", core, w.Build, w.CRC32, why)
	if tryBeginPatchOp() {
		err := coreProbationRollback(core, w)
		endPatchOp()
		if err != nil {
			return coreProbationFailedNote(core, err)
		}
		return "The new " + coreLabelFor(core) + " build could not start this game, so the build before it is back. Start the game again."
	}
	// Another install holds the card: the rollback follows when it is done.
	go func() {
		deadline := time.Now().Add(coreProbationLockWait)
		for !tryBeginPatchOp() {
			if time.Now().After(deadline) {
				logWarn("emulators", "core %s: the rollback waited too long for the running update; it is tried after the next failed game", core)
				return
			}
			time.Sleep(2 * time.Second)
		}
		err := coreProbationRollback(core, w)
		endPatchOp()
		if err != nil {
			ShowToast(coreProbationFailedNote(core, err), ToastKindWarning)
		}
	}()
	return "The new " + coreLabelFor(core) + " build could not start this game; the build before it comes back as soon as the running update finishes."
}

// coreSameFile reports whether two paths name one file: the same path, or —
// a log that spells the card's mount another way — the same file on disk.
func coreSameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

// coreProbationFailedNote is what the card says when the rollback itself failed.
func coreProbationFailedNote(core string, err error) string {
	return "The new " + coreLabelFor(core) + " build could not start this game, and putting the build before it back failed (" +
		err.Error() + "). Roll it back on the Emulators page (R1)."
}

// coreProbationRollback puts the build before w back (the caller holds the
// patch slot), refuses w's build, and says so in one toast.
func coreProbationRollback(core string, w coreProbationWatch) error {
	_, _, stamp, ok := parseCoreBackupName(filepath.Base(w.Backup))
	if !ok || !regularFile(w.Backup) {
		coreProbationEnd(core, "")
		return errors.New("the build before it is no longer kept")
	}
	// Checked again under the slot: the file may have changed while waiting.
	if sum, err := coreCRC32File(w.Target); err != nil || !strings.EqualFold(sum, w.CRC32) {
		coreProbationEnd(core, "")
		return nil
	}
	if _, err := rollbackCoreBuild(coreBackup{Core: core, Path: w.Backup, Target: w.Target, Stamp: stamp}); err != nil {
		return err
	}
	coreRefusalRecord(core, w.CRC32, w.Build, "could not start a game on this device and was rolled back")
	coreProbationEnd(core, "")
	invalidateEmulatorCache()
	invalidateCompatCache()
	emuForgetUpdate(core)
	coreBuildsRequestReload()
	logWarn("emulators", "core %s: rolled back from %s (crc %s) to %s", core, w.Build, w.CRC32, filepath.Base(w.Backup))
	ShowToast("The new "+coreLabelFor(core)+" didn't start here — went back to the one that worked", ToastKindWarning)
	return nil
}

// ── The one-time repair ──────────────────────────────────────────────────

// coreOriginalCRC is the CRC32 of the build a core had before JukaHub first
// replaced it (the history's entry without a start time), "" when unknown.
func coreOriginalCRC(core string) string {
	// coreHistoryOriginal survives the history list's trim (12 entries).
	return coreHistoryOriginal(normalizeCoreName(core))
}

// coreBackupIsOriginal reports whether a kept build of core is the one the
// card had before JukaHub first replaced it — the firmware's or CrossMix's.
// pruneCoreBackups never removes it: it is the build known to work here.
// The checksum comes from the cache (by size and time): a prune follows R1 on
// the event thread, and the kept original is past the newest five every time.
func coreBackupIsOriginal(core, path string) bool {
	orig := coreOriginalCRC(core)
	if orig == "" {
		return false
	}
	sum, err := emuCachedCRC32(path)
	return err == nil && strings.EqualFold(sum, orig)
}

// startCoreGPURepair runs the one-time repair a little after start, off the
// boot path (startCoreAutoUpdate, handheld only).
func startCoreGPURepair() {
	go func() {
		time.Sleep(coreGPURepairDelay)
		coreGPURepairOnce()
	}()
}

// coreGPURepairOnce gives every GPU core that JukaHub's own update replaced
// the firmware's build back, once per core and only on the first start that
// gets through every core (see the file comment and coreRepairDone): after
// that, a buildbot build on a GPU core is one a press put there, and its own
// probation decides.
func coreGPURepairOnce() {
	if !IsTrimuiDevice() || coreRepairRan() {
		return
	}
	defer coreRepairSetRan()
	for core, b := range scanCoreInventory().Active {
		if !coreNeedsGPU(core) || b.Path == "" || coreDirIsSystem(filepath.Dir(b.Path)) {
			continue
		}
		cur, err := emuCachedCRC32(b.Path)
		if err != nil || coreBuildProven(core, cur) || coreRepairDone(core) {
			continue
		}
		if _, watched := coreProbationFor(core); watched {
			// Activated by this build: its own first game decides.
			continue
		}
		// Two cases: the history says JukaHub's update replaced the
		// firmware's build, or — with no history to go on (a first update
		// made while the clock read 1970 is not recorded) — the active file
		// itself cannot be opened here (a desktop OpenGL build).
		orig := coreOriginalCRC(core)
		replaced := orig != "" && !strings.EqualFold(cur, orig) && coreHistoryHasBuildbot(core, cur)
		broken := strings.Contains(coreLoadProblemFile(b.Path, b.Path), "desktop OpenGL")
		if !replaced && !broken {
			continue
		}
		coreRepairMark(core, cur)
		var found coreBackup
		backups := coreBackupsIn(filepath.Dir(b.Path), core)
		if orig != "" {
			for _, bak := range backups {
				if sum, err := coreCRC32File(bak.Path); err == nil && strings.EqualFold(sum, orig) {
					found = bak
					break
				}
			}
		}
		if found.Path == "" && broken {
			// Newest first: the most recent kept build this RetroArch can open.
			for _, bak := range backups {
				if sum, err := coreCRC32File(bak.Path); err == nil && !strings.EqualFold(sum, cur) && coreLoadProblemFile(bak.Path, b.Path) == "" {
					found = bak
					break
				}
			}
		}
		if found.Path == "" {
			logInfo("emulators", "core %s (crc %s): JukaHub's update replaced the build that worked here, and no kept build can take its place; nothing to restore", core, cur)
			continue
		}
		// The kept build goes over the file RetroArch loads (and the one
		// checked again below), whatever name the .bak was taken under.
		found.Target = b.Path
		if !coreRepairLock() {
			logWarn("emulators", "core %s: the firmware's build was not restored (a game or an install held the device); it is not tried again", core)
			continue
		}
		// Checked again under the slot: while the repair waited, a press or
		// the daily pass may have put another build in place, and that one is
		// not the repair's to undo.
		if sum, sumErr := coreCRC32File(b.Path); sumErr != nil || !strings.EqualFold(sum, cur) {
			endPatchOp()
			logInfo("emulators", "core %s changed while the repair waited; left as it is", core)
			continue
		}
		_, err = rollbackCoreBuild(found)
		endPatchOp()
		if err != nil {
			logWarn("emulators", "core %s: restoring the firmware's build failed: %v", core, err)
			continue
		}
		invalidateEmulatorCache()
		invalidateCompatCache()
		emuForgetUpdate(core)
		coreBuildsRequestReload()
		logWarn("emulators", "core %s: restored the kept build %s over JukaHub's generic update (crc %s, cannot open here: %v); the update is kept as a .bak", core, filepath.Base(found.Path), cur, broken)
		why := "may not draw on this GPU"
		if broken {
			why = "cannot run on this GPU"
		}
		// "A JukaHub update": the history does not say whether the daily
		// pass or a press put the generic build there.
		ShowToast("The "+coreLabelFor(core)+" is back to the build that worked here: a JukaHub update had put in a generic build that "+why+" · Emulators → R1 undoes this", ToastKindInfo)
	}
}

// coreRepairLock takes the patch slot for the repair, waiting while a game is
// on screen or another install runs — and, for a few minutes, for the clock:
// the build the repair replaces is kept under the time it was replaced, R1
// brings back the newest one, and a handheld that boots before its clock is
// set would name it 1970, the oldest of all. (The deadlines run on the
// monotonic clock, so the clock being set does not end them.)
func coreRepairLock() bool {
	start := time.Now()
	deadline := start.Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		clockSet := !time.Now().Before(minSaneClock) || time.Since(start) > coreRepairClockWait
		if clockSet && !gameScreenSuspended() && tryBeginPatchOp() {
			return true
		}
		time.Sleep(10 * time.Second)
	}
	return false
}

// coreRepairDone reports whether the repair already acted on core, whatever
// build it found. Once is per core, not per build: after it, the automatic
// pass holds the core (coreGPUAutoHold), so every later buildbot build on it
// is one the user chose — R1, a pick in Core builds' kept list, an Update —
// and the repair putting the firmware's build back over that would fight the
// user. Such a build has the probation instead.
func coreRepairDone(core string) bool {
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	return coreProbationLoadLocked().Repaired[normalizeCoreName(core)] != ""
}

// coreRepairRan reports whether the one-time repair has run to its end.
func coreRepairRan() bool {
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	return coreProbationLoadLocked().RepairRan
}

// coreRepairSetRan records that it has: a start cut short before the end
// (power off while it waited for a game to end) runs it again, and the cores
// it already acted on are skipped then (coreRepairDone).
func coreRepairSetRan() {
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	coreProbationLoadLocked().RepairRan = true
	coreProbationSaveLocked()
}

// coreRepairMark records that the repair acted on core (crc is the build it
// found, for the log file): whatever happens next, it is not looked at again.
func coreRepairMark(core, crc string) {
	coreProbations.mu.Lock()
	defer coreProbations.mu.Unlock()
	coreProbationLoadLocked().Repaired[normalizeCoreName(core)] = strings.ToLower(crc)
	coreProbationSaveLocked()
}

// ── Diagnostics ──────────────────────────────────────────────────────────

// coreLaunchNote logs, for a RetroArch launch, which core build the game is
// about to run: its file, checksum, the GPU library it links, and where it
// came from (the firmware's, a JukaHub update, on probation). The device log
// is how a report says which build a game that would not start ran. It reads
// the card: call it on a goroutine.
func coreLaunchNote(launcher, system string) {
	core := gameCoreFromLauncher(launcher, system)
	if core == "" {
		return
	}
	path := ""
	if ext := strings.ToLower(filepath.Ext(launcher)); ext == ".so" || ext == ".dll" {
		path = launcher
	} else if dirs := retroArchCoreDirs(); len(dirs) > 0 {
		path = coreActiveBinary(core, dirs)
	}
	if path == "" {
		logInfo("library", "core for this launch: %s (not in RetroArch's cores folders)", core)
		return
	}
	sum, _ := emuCachedCRC32(path)
	origin := coreOriginOf(core, sum)
	gpu := ""
	if api := coreGPUAPIOf(path); api != "" && (coreNeedsGPU(core) || strings.HasPrefix(api, "desktop")) {
		gpu = ", GPU " + api
	}
	if w, ok := coreProbationFor(core); ok && strings.EqualFold(w.CRC32, sum) {
		origin += ", on probation"
	}
	logInfo("library", "core for this launch: %s at %s (crc %s%s, %s)", core, path, sum, gpu, origin)
}

// coreOriginOf says where the active build of core with checksum crc came
// from, by the history (corebuilds_history.go).
func coreOriginOf(core, crc string) string {
	if crc == "" {
		return "checksum unknown"
	}
	if orig := coreOriginalCRC(core); orig != "" && strings.EqualFold(orig, crc) {
		return "the build the card had before JukaHub's first update"
	}
	for _, e := range coreHistoryFor(normalizeCoreName(core)) {
		if !strings.EqualFold(e.CRC32, crc) {
			continue
		}
		switch {
		case e.From == "":
			return "the build the card had before JukaHub's first update"
		case e.Build != "":
			return "JukaHub's update to the " + e.Build + " buildbot build"
		default:
			return "a kept build JukaHub restored"
		}
	}
	if len(coreHistoryFor(normalizeCoreName(core))) == 0 {
		return "never replaced by JukaHub"
	}
	return "not a build JukaHub installed"
}

// coreReplacedIssue is the diagnosis card's note for a GPU core whose active
// build is a JukaHub update rather than the firmware's — a "maybe", never a
// reason for the card on its own.
func coreReplacedIssue(core, path string) (launchIssue, bool) {
	if !coreNeedsGPU(core) || path == "" {
		return launchIssue{}, false
	}
	sum, err := emuCachedCRC32(path)
	if err != nil || !coreHistoryHasBuildbot(core, sum) || coreBuildProven(core, sum) {
		return launchIssue{}, false
	}
	detail := "This core draws with the GPU, and the buildbot's generic build may not run under this device's RetroArch. " +
		"Roll the core back to the build before it on the Emulators page (R1) or in Core builds."
	if api := coreGPUAPIOf(path); api != "" {
		detail = "It links " + api + ". " + detail
	}
	return launchIssue{
		Level:  int(gameDiagMaybe),
		Title:  "The " + coreConfigDisplayName(core) + " core is a JukaHub update, not the firmware's build",
		Where:  path,
		Detail: detail,
	}, true
}
