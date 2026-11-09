package main

// bootguard.go — crash-loop protection that counts boots instead of reading the
// clock.
//
// The recovery chord (safeboot.go) needs a person who knows about it. When a
// build fails on its first frames, nobody is pressing L+R+Start: the handheld
// boots into it, dies, is restarted, forever. The boot guard is the automatic
// half: bootguard.json in the Patch state folder records every boot, and the
// next boot reads how the previous one ended.
//
// What counts is decided inside the process, never from wall-clock times saved
// across a power cycle: the handheld may have no RTC, so after a cold boot its
// clock can sit at a factory epoch (before the update, before the last boot) or
// jump years ahead once NTP answers. So:
//
//   - a boot is HEALTHY once it drew its first frame and then kept running for
//     bootGuardHealthyAfter (a time.AfterFunc: the monotonic clock of this
//     process), or when its main loop ended on a quit (clearBootGuard);
//   - any other end — a crash, a kill, a hang, a power cut in the first minute
//     and a half — leaves the record "running and not healthy": a FAILED boot;
//   - a boot asked to stop before its loop ended (a stop signal, a quit:
//     requestAppExit records it) is STOPPED, not failed, however long the stop
//     took — the exit watchdog forcing a stop during a slow splash included —
//     unless it crashed on the way out. It ends a streak, but proves nothing:
//     it keeps the rollback count;
//   - a crash after a long session is a healthy boot that died, and does not
//     count towards a loop.
//
// Two decisions come out of the count:
//
//   - bootGuardThreshold failed boots in a row, whatever build and whatever the
//     update history, start safe mode for this launch and say why (REL-2);
//   - when those failures are the running build's own, began within its first
//     bootGuardRecentBoots boots — "recent" is a boot counter since the build
//     was installed, not a date — and at least one of them was a crash, the
//     previous version is put back (FIX-2). A crash is a crash report filed
//     for it, or the exit status launch.sh saw (.last-exit, read at the next
//     start): 1 (a fatal error), 2 (a Go crash), or a signal — 134 an abort,
//     137 a SIGKILL (the out-of-memory killer), 139 a segfault — but the stop
//     signals, and a SIGKILL that followed a stop request (a stop escalated).
//     Without that status (Windows, a desktop, an older launch.sh), dying
//     before the first frame is the evidence. A power cut, or a quit that was
//     forced or never recorded, in a session that was working leaves none: it
//     can start safe mode, never a rollback.
//
// A loop gets one automatic rollback. Until a start outside safe mode proves
// itself (bootGuardMarkHealthy, or a clean exit), Reverts keeps the next one
// away, so failures that are not the build's end in safe mode instead of
// swapping two builds for ever. Holding the recovery chord keeps it away too:
// the person holding it chooses, and the recovery card offers the previous
// version first.
//
// A healthy start outside safe mode also keeps the settings files as it read
// them at boot (lastgood/ beside the record): the recovery card offers them
// first as the last good settings, because the .bak every save rewrites holds
// the settings from just before the last change, good or bad.
//
// The rollback runs after the Patch module loaded its state and Version history
// (bootGuardStartRollbackIfDue), through the same journaled swap an update uses.
// JUKAHUB_NO_BOOTGUARD=1 disables all of it for debugging.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// bootGuardFile lives in the patch state directory.
	bootGuardFile = "bootguard.json"
	// bootGuardThreshold failed boots in a row start safe mode, and the same
	// number of failed boots of one new build roll it back.
	bootGuardThreshold = 3
	// bootGuardHealthyAfter is how long a boot must keep running after its
	// first frame to count as healthy. Measured in this process (monotonic),
	// so a clock set during the session cannot shorten or stretch it.
	bootGuardHealthyAfter = 90 * time.Second
	// bootGuardRecentBoots is how many boots a build counts as newly installed:
	// a crash loop that starts later is rarely the update's fault, and gets
	// safe mode instead of a rollback.
	bootGuardRecentBoots = 10
	// bootGuardStreakCap keeps a wedged device from growing the stored count
	// without bound; anything at or above the threshold behaves the same.
	bootGuardStreakCap = 99
	// bootGuardLastGoodDir (beside the record) holds the settings files the
	// last healthy start read at boot; bootGuardLastGoodMax bounds each one.
	bootGuardLastGoodDir = "lastgood"
	bootGuardLastGoodMax = 4 << 20
	// bootGuardLastExitFile is the line launch.sh writes beside the executable
	// after every run, "<exit status> <pid>"; the next start reads and removes
	// it. bootGuardLastExitMax bounds that read.
	bootGuardLastExitFile = ".last-exit"
	bootGuardLastExitMax  = 64
	// bootGuardSignalBase plus a signal's number is the status a shell reports
	// for a process that signal ended (Linux numbers: only launch.sh writes it).
	bootGuardSignalBase = 128
)

// bootGuardLastGoodFiles are the settings files a healthy start keeps, in the
// folder JukaHub runs in.
var bootGuardLastGoodFiles = [...]string{"jukauser.json", "jukaconfig.json"}

// bootGuardFileState is bootguard.json: what the last boot was and how far it
// got. The wall-clock fields are information for a person reading the file;
// no decision reads them.
type bootGuardFileState struct {
	Version  string    `json:"version"`             // stamp of the build that booted last
	Unclean  int       `json:"unclean"`             // failed boots in a row before that boot, any build
	Running  bool      `json:"running"`             // that boot's main loop has not ended on a quit
	Healthy  bool      `json:"healthy"`             // it drew its first frame and ran bootGuardHealthyAfter
	Boots    int       `json:"boots"`               // boots of Version so far, that one included
	LastBoot time.Time `json:"last_boot"`           // wall clock when it started (UTC), unreliable without an RTC
	Since    time.Time `json:"since"`               // wall clock of Version's first boot (UTC), same caveat
	SafeNext string    `json:"safe_next,omitempty"` // one-shot: start the next boot in safe mode, with this reason
	Recovery bool      `json:"recovery,omitempty"`  // that boot was started with the recovery chord held

	// What an automatic rollback needs besides the count.
	Framed     bool `json:"framed,omitempty"`      // that boot drew its first frame
	Stopping   bool `json:"stopping,omitempty"`    // that boot was asked to stop (a stop signal, a quit) while it was running
	CrashFails int  `json:"crash_fails,omitempty"` // Version's failed boots before that one that crashed (bootGuardDecide)
	Reverts    int  `json:"reverts,omitempty"`     // automatic rollbacks since a start outside safe mode proved itself
	PID        int  `json:"pid,omitempty"`         // that boot's process: launch.sh's .last-exit line names it
}

// bootGuardLastExit is how a start's process ended, as launch.sh saw it: the
// .last-exit line. PID 0 is no line.
type bootGuardLastExit struct {
	Status int // the exit status, or bootGuardSignalBase + the signal that ended it
	PID    int // the process it is about
}

// String is the status for the log: "unknown" without a line.
func (e bootGuardLastExit) String() string {
	switch {
	case e.PID <= 0:
		return "unknown"
	case e.Status > bootGuardSignalBase:
		return fmt.Sprintf("%d (signal %d)", e.Status, e.Status-bootGuardSignalBase)
	}
	return strconv.Itoa(e.Status)
}

// bootGuardStopSignalStatus reports whether status is a death by one of the
// stop signals JukaHub turns into a quit (installExitSignalHandler): SIGHUP,
// SIGINT, SIGQUIT, SIGTERM. Their default action ends a process that has no
// handler yet, so a stop that came early still reads as a stop.
func bootGuardStopSignalStatus(status int) bool {
	switch status - bootGuardSignalBase {
	case 1, 2, 3, 15:
		return true
	}
	return false
}

// bootGuardExitCrashed reports whether an exit status is crash evidence: any
// failure but a deliberate stop. 1 is a fatal error JukaHub reported
// (logFatal), 2 a Go panic or fatal runtime error; above 128 a signal ended
// the process — 134 an abort, 137 a SIGKILL (the out-of-memory killer), 139 a
// segfault — except the stop signals, and a SIGKILL after a stop request
// (stopping), which is how a stop that takes too long is escalated. 0 is a
// quit, or the exit watchdog's forced one.
func bootGuardExitCrashed(status int, stopping bool) bool {
	switch {
	case status == 0, bootGuardStopSignalStatus(status):
		return false
	case status == bootGuardSignalBase+9: // SIGKILL
		return !stopping
	}
	return true
}

// bootGuardParseLastExit reads a .last-exit line. Anything but "<status> <pid>"
// with a status of 0-255 and a positive pid is no line.
func bootGuardParseLastExit(data []byte) bootGuardLastExit {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return bootGuardLastExit{}
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil || status < 0 || status > 255 {
		return bootGuardLastExit{}
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil || pid <= 0 {
		return bootGuardLastExit{}
	}
	return bootGuardLastExit{Status: status, PID: pid}
}

// bootGuardReadLastExit reads and removes the .last-exit line in dir. Best
// effort and bounded: a missing, damaged or oversized file is no line, and
// what was read never speaks for a later start.
func bootGuardReadLastExit(dir string) bootGuardLastExit {
	path := filepath.Join(dir, bootGuardLastExitFile)
	data, err := crashReadHead(path, bootGuardLastExitMax)
	if err != nil {
		return bootGuardLastExit{}
	}
	if err := os.Remove(path); err != nil {
		logWarn("bootguard", "the last exit status could not be removed: %v", err)
	}
	return bootGuardParseLastExit(data)
}

// bootGuardVerdict is what one boot decided from the record the previous boot
// left.
type bootGuardVerdict struct {
	Next        bootGuardFileState // the record this boot writes
	PrevFailed  bool               // the boot before this one failed
	PrevStopped bool               // it was asked to stop before it finished, and did not crash: not counted
	Exit        bootGuardLastExit  // launch.sh's line about it, when the line names its process
	Streak      int                // failed boots in a row before this one, any build
	BuildFails  int                // how many of those were this build
	CrashFails  int                // how many of BuildFails crashed (bootGuardDecide)
	Reverts     int                // automatic rollbacks since a start outside safe mode proved itself
	SafeMode    bool               // REL-2: Streak reached the threshold
	Rollback    bool               // FIX-2: this new build failed the threshold, crashing, and none was undone yet
	SafeNext    string             // a one-shot safe-mode request the previous boot left
}

// bootGuardDecide is the decision core. It is pure: now is only copied into
// the record's information fields, so a clock reset, a clock jump and a normal
// clock all give the same verdict. crashed says whether this start filed a
// crash report for the previous session (initCrashReports runs first); exit is
// the .last-exit line launch.sh left, PID 0 when there was none.
func bootGuardDecide(prev bootGuardFileState, had bool, version string, now time.Time, crashed bool, exit bootGuardLastExit) bootGuardVerdict {
	var v bootGuardVerdict
	// A record without a boot count was written by a build from before the
	// count existed. That build deleted the file on every clean exit, so the
	// file being there says the boot ended uncleanly but not whether it ever
	// worked: nothing is counted from it.
	legacy := had && prev.Boots <= 0
	known := had && !legacy
	// The line speaks for the previous boot only when it names that boot's
	// process: a start that died before it recorded itself (in SDL init) left
	// one the record knows nothing about.
	if known && exit.PID > 0 && exit.PID == prev.PID {
		v.Exit = exit
	}
	// How the previous boot ended, beyond the record: a crash report, or an exit
	// status that is a failure. A boot that was asked to stop and did not crash
	// was stopped on purpose, however long the stop took: the exit watchdog
	// forcing it during a slow splash is not a failed start. So is one a stop
	// signal ended before its handler was up.
	crash := crashed || (v.Exit.PID > 0 && bootGuardExitCrashed(v.Exit.Status, prev.Stopping))
	stopped := prev.Stopping || (v.Exit.PID > 0 && bootGuardStopSignalStatus(v.Exit.Status))
	if known && prev.Running && !prev.Healthy {
		if stopped && !crash {
			// Not a failed start, and no proof either: the streak ends (Unclean
			// is 0 below) and Reverts stays.
			v.PrevStopped = true
		} else {
			v.PrevFailed = true
			v.Streak = prev.Unclean + 1
			if v.Streak > bootGuardStreakCap {
				v.Streak = bootGuardStreakCap
			}
		}
	}
	boots := 1
	since := now.UTC()
	sameBuild := known && prev.Version == version
	if sameBuild {
		boots = prev.Boots + 1
		if !prev.Since.IsZero() {
			since = prev.Since
		}
	}
	// Every boot since the version last changed was this build, so the streak's
	// most recent boots-1 entries are its own.
	v.BuildFails = v.Streak
	if v.BuildFails > boots-1 {
		v.BuildFails = boots - 1
	}
	// Crash evidence: the failed boot left a crash report or a failure status
	// (bootGuardExitCrashed). Without launch.sh's line, dying before the first
	// frame is the evidence (a startup failure files no report); with it, the
	// status says how it ended — 0 is a quit or a forced one, framed or not. A
	// power cut, or a quit that was forced or never recorded, in a session that
	// was working leaves none.
	if v.PrevFailed && sameBuild {
		v.CrashFails = prev.CrashFails
		if crash || (v.Exit.PID == 0 && !prev.Framed) {
			v.CrashFails++
		}
		if v.CrashFails > v.BuildFails {
			v.CrashFails = v.BuildFails
		}
	}
	// Cleared by a start outside safe mode that proved itself (it rewrites the
	// record); a failed start leaves it as it was.
	if known {
		v.Reverts = prev.Reverts
	}
	v.SafeMode = v.Streak >= bootGuardThreshold
	// The loop has to have begun while the build was new: the first failed boot
	// of the streak was within its first bootGuardRecentBoots boots. At least
	// one failure must be a crash, and an automatic rollback that did not end
	// the failures is not followed by another: they are probably not the
	// build's, and the next one would go back to the build just left.
	v.Rollback = v.BuildFails >= bootGuardThreshold && boots-v.BuildFails <= bootGuardRecentBoots &&
		v.CrashFails > 0 && v.Reverts == 0
	if known {
		v.SafeNext = strings.TrimSpace(prev.SafeNext)
	}
	v.Next = bootGuardFileState{
		Version:    version,
		Unclean:    v.Streak,
		Running:    true,
		Boots:      boots,
		LastBoot:   now.UTC(),
		Since:      since,
		CrashFails: v.CrashFails,
		Reverts:    v.Reverts,
	}
	return v
}

// bootGuardStampFrom builds the stamp that identifies one build: the release
// tag, the compile time and the executable's size. The tag alone is not
// enough: a rollback puts an older binary back under the same
// release-info.json, and a binary copied over by hand keeps the old stamp file.
func bootGuardStampFrom(tag, built string, size int64) string {
	parts := make([]string, 0, 3)
	if tag = strings.TrimSpace(tag); tag != "" {
		parts = append(parts, tag)
	}
	if built = strings.TrimSpace(built); built != "" {
		parts = append(parts, built)
	}
	if size > 0 {
		parts = append(parts, strconv.FormatInt(size, 10))
	}
	if len(parts) == 0 {
		return "build"
	}
	return strings.Join(parts, "@")
}

// bootGuardVersionStamp is bootGuardStampFrom for the running build.
func bootGuardVersionStamp() string {
	built := "build:" + buildDate
	if t, ok := parseReleaseTimestamp(buildTime); ok {
		built = t.Format(time.RFC3339)
	} else if strings.TrimSpace(buildDate) == "" {
		built = ""
	}
	var size int64
	if self, err := os.Executable(); err == nil {
		if info, err := os.Stat(self); err == nil {
			size = info.Size()
		}
	}
	return bootGuardStampFrom(readInstalledReleaseInfo().Tag, built, size)
}

// readBootGuardFile loads the record, reporting whether one existed.
func readBootGuardFile(dir string) (bootGuardFileState, bool) {
	data, err := os.ReadFile(filepath.Join(dir, bootGuardFile))
	if err != nil {
		return bootGuardFileState{}, false
	}
	var s bootGuardFileState
	if json.Unmarshal(data, &s) != nil {
		// A garbled record must never suppress the guard: treat as none.
		return bootGuardFileState{}, false
	}
	return s, true
}

// writeBootGuardFile durably records s for the next boot.
func writeBootGuardFile(dir string, s bootGuardFileState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	if err := bootGuardWriteDurable(filepath.Join(dir, bootGuardFile), append(data, '\n')); err != nil {
		logWarn("bootguard", "the boot record could not be written: %v", err)
	}
}

// bootGuardWriteDurable replaces path with data: a temp file that is synced,
// then renamed over it, then the folder synced. The record is written on every
// boot, so it skips AtomicWrite's .bak copy (a read, a write and a sync more
// on a slow card): a torn write leaves the old record, which only means one
// boot counted the way the one before it ended.
func bootGuardWriteDurable(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

// bootGuardLive is this boot's record and where it lives. The health timer,
// the teardown and the recovery card update it from different goroutines.
// keepReverts is set when this start rolled back or runs in safe mode: its end
// proves nothing about the build, so it does not clear Reverts. lastGood holds
// the settings files as this start read them, until the health mark.
var bootGuardLive struct {
	mu          sync.Mutex
	dir         string
	state       bootGuardFileState
	ok          bool
	health      *time.Timer
	keepReverts bool
	lastGood    [len(bootGuardLastGoodFiles)][]byte
}

// bootGuardOutcomeState is what this boot decided, for the safe-mode card and
// Diagnostics.
type bootGuardOutcomeState struct {
	Streak          int  // failed boots in a row before this one
	SafeMode        bool // this boot started safe mode because of them
	RollbackDue     bool // a rollback was decided; bootGuardStartRollbackIfDue runs it
	RollbackOffered bool // a rollback was due, but the chord was held: the recovery card offers it
	Reverts         int  // automatic rollbacks since a start outside safe mode proved itself
}

// bootGuardOutcome is written once at boot, before the loop starts.
var bootGuardOutcome bootGuardOutcomeState

// bootGuardRollback puts the previous version back. A variable so tests can
// watch it; rollbackToPreviousVersion is in safeboot.go.
var bootGuardRollback = rollbackToPreviousVersion

// bootGuardPrevCrashed reports whether this start filed a crash report for
// the session before it (initCrashReports runs before initBootGuard). A
// variable so tests can say how each simulated start ended.
var bootGuardPrevCrashed = func() bool {
	report, _ := crashLastReport()
	return report != ""
}

// bootGuardPrevExit reads and removes launch.sh's .last-exit line, beside the
// executable as the script's other markers are. Nothing writes one on Windows
// or a desktop. A variable so tests can say how each simulated process ended.
var bootGuardPrevExit = func() bootGuardLastExit {
	dir, err := P().ExecutableDir()
	if err != nil {
		return bootGuardLastExit{}
	}
	return bootGuardReadLastExit(dir)
}

// initBootGuard reads how the previous boot ended, decides, and records this
// boot. main.go runs it right after SDL is up, before the config is loaded,
// so a start that dies loading it is counted too; it needs only the Patch
// state folder (DetectDevice and the executable's folder, never the config).
func initBootGuard() {
	if strings.TrimSpace(os.Getenv("JUKAHUB_NO_BOOTGUARD")) != "" {
		return
	}
	dir, err := PatchStateDir()
	if err != nil {
		Log().Warn("boot guard: no patch state directory", "err", err)
		return
	}
	applyBootGuard(dir, bootGuardVersionStamp(), time.Now())
	// The settings this start reads, kept until the health mark proves them
	// (bootGuardSaveLastGood). Not in safe mode: a start that skips plugins and
	// network proves nothing about the settings a normal start chokes on. Read
	// beside the boot, not in front of it: nothing writes them before the user
	// changes something, and a save is debounced by seconds.
	if !safeMode.Active {
		go func() {
			files := bootGuardCaptureSettings("")
			bootGuardLive.mu.Lock()
			bootGuardLive.lastGood = files
			bootGuardLive.mu.Unlock()
		}()
	}
}

// applyBootGuard is initBootGuard with the folder, the stamp and the clock
// given, so every rule is testable with a temp dir.
func applyBootGuard(dir, version string, now time.Time) bootGuardVerdict {
	prev, had := readBootGuardFile(dir)
	v := bootGuardDecide(prev, had, version, now, bootGuardPrevCrashed(), bootGuardPrevExit())
	v.Next.Recovery = safeboot.Triggered
	v.Next.PID = os.Getpid()
	// Safe mode asked for by a flag, the environment or the command line is a
	// developer's or a support session's choice: it keeps the automatic
	// rollback out of the way, as it always did. So does the recovery chord:
	// the person holding it chooses, and the recovery card offers the previous
	// version first (recoveryOpenMenu).
	explicit := safeMode.Active
	if v.SafeNext != "" && !safeMode.Active {
		safeMode = safeModeState{Active: true, Reason: v.SafeNext}
		Log().Warn("safe mode enabled: requested by the previous session", "reason", v.SafeNext)
	}
	bootGuardOutcome.Streak = v.Streak
	bootGuardOutcome.Reverts = v.Reverts
	if v.SafeMode && !safeMode.Active {
		safeMode = safeModeState{Active: true, Reason: fmt.Sprintf("%d failed starts in a row", v.Streak)}
		bootGuardOutcome.SafeMode = true
		Log().Warn("safe mode enabled: the last starts did not finish", "failedInARow", v.Streak)
	}
	switch {
	case v.Rollback && safeboot.Triggered:
		bootGuardOutcome.RollbackOffered = true
		Log().Warn("boot guard: this build failed its first starts; the recovery chord is held, so the card offers the previous version",
			"failedStarts", v.BuildFails, "bootsOfThisBuild", v.Next.Boots)
	case v.Rollback && !explicit:
		bootGuardOutcome.RollbackDue = true
		Log().Warn("boot guard: this build failed its first starts; the previous version will be restored",
			"failedStarts", v.BuildFails, "crashed", v.CrashFails, "bootsOfThisBuild", v.Next.Boots,
			// Information only: the clock may be a factory epoch after a cold boot.
			"wallClockSinceInstall", now.UTC().Sub(v.Next.Since).Round(time.Second).String())
	case v.Rollback:
		Log().Warn("boot guard: this build failed its first starts; safe mode was asked for, so it is not rolled back",
			"reason", safeMode.Reason)
	case v.PrevFailed:
		Log().Info("boot guard: the previous start did not finish", "failedInARow", v.Streak,
			"bootsOfThisBuild", v.Next.Boots, "crashed", v.CrashFails, "rollbacksSinceHealthy", v.Reverts,
			"exitStatus", v.Exit.String())
	case v.PrevStopped:
		Log().Info("boot guard: the previous start was stopped before it finished; not counted as a failed start",
			"exitStatus", v.Exit.String())
	}
	bootGuardLive.mu.Lock()
	// A stop asked for before this record existed found nothing to mark
	// (bootGuardNoteStop), so it is written with it. Read under the lock: a
	// later request finds the record and marks it itself.
	v.Next.Stopping = exitRequested.Load()
	bootGuardLive.dir, bootGuardLive.state, bootGuardLive.ok = dir, v.Next, true
	// The end of a safe-mode start proves nothing about the build.
	bootGuardLive.keepReverts = safeMode.Active
	writeBootGuardFile(dir, v.Next)
	bootGuardLive.mu.Unlock()
	return v
}

// bootGuardStartRollbackIfDue runs the rollback initBootGuard decided on. It is
// called right after InitPatchModule (main.go), once Version history is loaded
// and an interrupted swap has been dealt with, and before the window opens. A
// Version-history rollback restarts JukaHub through the swap helper; this
// launch continues in safe mode until then.
func bootGuardStartRollbackIfDue(config *Config) {
	if !bootGuardOutcome.RollbackDue {
		return
	}
	bootGuardOutcome.RollbackDue = false
	name, version, err := bootGuardRollback(config)
	safeboot.AutoRevert = true
	if err != nil {
		safeboot.Err = err
		Log().Warn("boot guard: the previous version could not be restored", "err", err)
		return
	}
	safeboot.RolledBack = name
	safeboot.Version = version
	// A Version-history rollback hands over to the swap helper, which starts
	// the restored build; a backup-set one only replaced the file, and the
	// restored build takes over at the next start (the safe-mode card says so).
	safeboot.Restarts = appRestartRequested.Load()
	bootGuardNoteRollback()
	Log().Warn("boot guard: restoring the previous JukaHub", "set", name, "version", version, "restarts", safeboot.Restarts)
}

// bootGuardNoteRollback records that this start put the previous version back.
// The count stays until a start outside safe mode proves itself, so a loop
// that is not the build's fault gets safe mode next instead of a second
// rollback, back to the build just left.
func bootGuardNoteRollback() {
	bootGuardLive.mu.Lock()
	bootGuardLive.keepReverts = true
	bootGuardLive.mu.Unlock()
	bootGuardUpdate(func(s *bootGuardFileState) {
		if s.Reverts < bootGuardStreakCap {
			s.Reverts++
		}
	})
}

// bootGuardUpdate changes this boot's record and writes it. No-op when the
// guard did not run this boot (disabled, or no state folder).
func bootGuardUpdate(change func(s *bootGuardFileState)) {
	bootGuardLive.mu.Lock()
	defer bootGuardLive.mu.Unlock()
	if !bootGuardLive.ok {
		return
	}
	before := bootGuardLive.state
	change(&bootGuardLive.state)
	if bootGuardLive.state == before {
		return
	}
	writeBootGuardFile(bootGuardLive.dir, bootGuardLive.state)
}

// bootGuardFirstFrame starts the health clock and records that this start drew
// a frame (a start that dies before one is a crash for the rollback rule). The
// main loop calls it once, when the first frame has been presented; the writes
// happen on other goroutines, never on the render thread.
func bootGuardFirstFrame() {
	bootGuardLive.mu.Lock()
	defer bootGuardLive.mu.Unlock()
	if !bootGuardLive.ok || bootGuardLive.health != nil {
		return
	}
	bootGuardLive.health = time.AfterFunc(bootGuardHealthyAfter, bootGuardMarkHealthy)
	go bootGuardMarkFramed()
}

// bootGuardMarkFramed records the first frame.
func bootGuardMarkFramed() {
	bootGuardUpdate(func(s *bootGuardFileState) {
		if s.Running {
			s.Framed = true
		}
	})
}

// bootGuardMarkHealthy records that this boot worked: whatever ends it now, the
// next boot starts counting from zero. Outside safe mode it also keeps the
// settings this start read as the last good ones. It runs on the timer's
// goroutine.
func bootGuardMarkHealthy() {
	bootGuardUpdate(func(s *bootGuardFileState) {
		if s.Running {
			s.Healthy = true
			if !bootGuardLive.keepReverts { // bootGuardUpdate holds the lock
				s.Reverts = 0
			}
		}
	})
	bootGuardSaveLastGood()
}

// bootGuardCaptureSettings reads the settings files in appDir ("" is the
// folder JukaHub runs in) as this start found them. A file that is missing,
// empty or over bootGuardLastGoodMax is left out, and so is a design file a
// newer jukaconfig.xml export overrides (ResolveConfigPath): the start does
// not run with it.
func bootGuardCaptureSettings(appDir string) [len(bootGuardLastGoodFiles)][]byte {
	var out [len(bootGuardLastGoodFiles)][]byte
	for i, name := range bootGuardLastGoodFiles {
		path := filepath.Join(appDir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > bootGuardLastGoodMax {
			continue
		}
		if ResolveConfigPath(path) != path {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			out[i] = data
		}
	}
	return out
}

// bootGuardLastGoodPath is the folder the last good settings are kept in.
func bootGuardLastGoodPath() (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, bootGuardLastGoodDir), nil
}

// bootGuardSaveLastGood writes the settings this start read at boot as the
// last good ones, once, after the health mark. Files that do not parse are
// left out (a damaged file the loader replaced from its .bak is not good);
// unchanged ones are not rewritten.
func bootGuardSaveLastGood() {
	bootGuardLive.mu.Lock()
	files, dir := bootGuardLive.lastGood, bootGuardLive.dir
	bootGuardLive.lastGood = [len(bootGuardLastGoodFiles)][]byte{}
	bootGuardLive.mu.Unlock()
	if dir == "" {
		return
	}
	out := filepath.Join(dir, bootGuardLastGoodDir)
	for i, name := range bootGuardLastGoodFiles {
		data := files[i]
		if len(data) == 0 || !json.Valid(data) {
			continue
		}
		path := filepath.Join(out, name)
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := os.MkdirAll(out, 0o700); err != nil {
			logWarn("bootguard", "the last good settings could not be kept: %v", err)
			return
		}
		if err := bootGuardWriteDurable(path, data); err != nil {
			logWarn("bootguard", "the last good %s could not be kept: %v", name, err)
		}
	}
}

// bootGuardRequestSafeNext asks the next boot to start in safe mode, once.
func bootGuardRequestSafeNext(reason string) bool {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "requested from the recovery card"
	}
	done := false
	bootGuardUpdate(func(s *bootGuardFileState) {
		s.SafeNext = reason
		done = true
	})
	return done
}

// bootGuardNoteStop records that this start was asked to stop (requestAppExit:
// a stop signal, the Exit button, any quit). A stop the exit watchdog then has
// to force — a power press during a slow splash — is read at the next start as
// a stop, not a failed start, unless the process crashed on the way out. It
// writes to the card: callers run it off the render thread and the signal
// handler. A start whose loop already ended is not marked again.
func bootGuardNoteStop() {
	bootGuardUpdate(func(s *bootGuardFileState) {
		if s.Running {
			s.Stopping = true
		}
	})
}

// bootGuardNoteStopAsync runs bootGuardNoteStop on its own goroutine when a
// stop was asked for, and returns a channel closed once it is done (at once
// without a stop request). The exit watchdog waits on it, bounded, before it
// ends the process: the note the request started may still be on its way to
// the card.
func bootGuardNoteStopAsync() <-chan struct{} {
	done := make(chan struct{})
	if !exitRequested.Load() {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		bootGuardNoteStop()
	}()
	return done
}

// clearBootGuard marks a clean exit. main.go calls it (on a goroutine) as soon
// as the main loop has ended on a quit, before the teardown, so a second stop
// signal, the exit watchdog or a power cut during the teardown cannot turn a
// normal session into a failed start; the teardown calls it again, which
// writes nothing. A boot that got here was healthy whatever its length: the
// next boot starts counting from zero. The record itself stays — its boot
// count is how a new build is told from one that has run for weeks.
func clearBootGuard() {
	bootGuardLive.mu.Lock()
	if bootGuardLive.health != nil {
		bootGuardLive.health.Stop()
	}
	bootGuardLive.mu.Unlock()
	bootGuardUpdate(func(s *bootGuardFileState) {
		s.Running = false
		if !bootGuardLive.keepReverts { // bootGuardUpdate holds the lock
			s.Reverts = 0
		}
	})
}

// bootGuardDiagnostic is the Diagnostics line about the last starts, empty when
// nothing failed.
func bootGuardDiagnostic() string {
	if bootGuardOutcome.Streak == 0 {
		return ""
	}
	line := fmt.Sprintf("%-13s: %d in a row before this start", "Failed starts", bootGuardOutcome.Streak)
	if bootGuardOutcome.SafeMode {
		line += " (safe mode)"
	}
	return line
}
