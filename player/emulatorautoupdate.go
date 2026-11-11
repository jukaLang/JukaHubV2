package main

// emulatorautoupdate.go — keeping the emulator cores current without a press.
//
// The Emulators page can bring every installed core forward in one action, but
// it needs the user to open that page and press it. On the handheld the card is
// the whole console: a core that is three nightlies behind is a game that runs
// worse for no reason the user can see, and the fix was a trip to a page they
// did not know about.
//
// So a daily pass runs by itself. What it does is the Emulator updates setting
// (Settings → Games & emulators, or Start on the Core builds page). It covers
// the libretro cores here; JukaHub's own PPSSPP, whose daily pass reads the
// same mode (emuCoreUpdateModeNow); and everything registered in
// emuautoupdate_registry.go — hakuX, the Windows-app runtime, AndroidRT —
// which this pass brings forward after the cores, at the same moment, with one
// install card and one toast for the lot (emuautoupdate_runtimes.go). A PC runs
// the same pass over the emulators JukaHub installed there and its RetroArch's
// cores (emuautoupdate_desktop.go):
//
//   - Install automatically (the default): the pass described below.
//   - Notify me: read the index, compare it with the installed cores, ask the
//     other updaters whether they have a newer build, and say once what does.
//     Nothing is downloaded; the Emulators and Core builds pages install on a
//     press.
//   - Off: no pass at all.
//
// The default has moved twice. It began as one switch, on unless switched off.
// LIB-16 made it three-way and turned the unasked default into Notify, on the
// reasoning that a new build can make a game's save states unloadable, so
// installing should be something the user asked for. Testers then asked for the
// opposite — "we need auto update cores … when available", and Juka's call on
// 2026-11-10 was to install automatically by default. What makes that safe
// enough to do unasked is what surrounds the install, not the asking: the build
// it replaces is kept as a .bak, rolling back is one press on the device
// (Emulators R1, or the Core builds page), a game whose save states came from
// another build says so on its page (corebuilds_history.go), and a build that
// cannot load on this firmware is refused before it replaces anything
// (elfcompat.go).
//
// coreUpdateModeFrom therefore keeps every explicit choice — an explicit off as
// Off, an explicit on as Install automatically, any mode the user picked — and
// gives everything else the new default. The one ambiguous value is a stored
// "notify" with no coreUpdateModeChosen marker beside it: LIB-16 wrote that for
// every card whose user never chose, so it reads as the default, and
// startCoreAutoUpdate rewrites it once with a toast that says what changed and
// where to change it back. The marker is written whenever the user picks a mode
// (the Settings row, the Core builds page), so a Notify chosen from now on
// stays Notify.
//
// The pass runs at most once per coreAutoUpdateInterval. A scheduler wakes
// every coreAutoUpdatePoll after the interface is up — every
// coreAutoUpdateRetry while a pass is due and only waiting for its moment — and
// runs the pass when it is due and the moment is right:
//
//   - online, and no other patch operation, install or playing video owns the
//     device;
//   - never while a game or emulator is on screen — and a game that starts
//     mid-pass stops it between cores;
//   - while charging, or after a couple of idle minutes, so a download never
//     competes with someone navigating; a pass more than three days overdue,
//     or the first one on a card that has never had one once it has waited a
//     poll's worth, runs at the next quiet moment regardless.
//
// It is the same work the page's action does — download, verify the CRC32 the
// index states, check that the build can load here, activate, keep the replaced
// build as a .bak up to maxCoreBackups — reported through the same install bar,
// and it ends with a toast ("Updated 3 emulator cores") that the notification
// centre keeps.
//
// What it will not do:
//   - touch, on a PC, anything JukaHub did not install there: an emulator
//     installed by hand, or the cores of a RetroArch someone else installed
//     (Steam's included), keeps its own way of updating;
//   - stand down for a whole interval when the check failed or was stopped.
//     Only a pass that read the index and finished is stamped, so a device that
//     boots before its Wi-Fi associates tries again at the next poll, and a pass
//     a game interrupted resumes at the next quiet moment;
//   - fetch a build it already refused (too large, or unable to load here)
//     again: only the next build the index lists is tried.
//
// The index is an unsigned nightly listing, fetched over HTTPS only
// (validatePatchURL, redirects included). The libretro buildbot publishes
// per-core archives for linux/aarch64 in its nightly tree alone — its stable
// tree has versioned bundles for other platforms, not per-core builds for this
// one — and the only checksum it states is the CRC32 of each core binary in
// .index-extended; there is no SHA-256 and no signature to check. So an update
// here is checked against that CRC32, its SHA-256 is recorded beside the build
// (source.json), the build it replaces is kept, and a nightly that turns out
// worse is one press away from being rolled back on the device.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// coreAutoUpdateInterval is how often the automatic pass may run. The
	// buildbot publishes nightly, so a daily cadence is as fresh as the channel
	// gets without re-downloading the same build on every launch.
	coreAutoUpdateInterval = 24 * time.Hour
	// coreAutoUpdateStartupDelay keeps the pass out of the boot path and behind
	// the release check's own window: both want the one patch slot, and a core
	// download can hold it for minutes.
	coreAutoUpdateStartupDelay = autoUpdateStartupDelay + 30*time.Second
	// coreAutoUpdatePoll is how often the scheduler looks for a good moment.
	coreAutoUpdatePoll = 20 * time.Minute
	// coreAutoUpdateRetry is how often it looks while a pass is due and only
	// the moment is wrong. The look is a few cheap reads (battery, input age,
	// game state), and at the poll's pace a device that is idle for five
	// minutes between games would rarely be caught idle.
	coreAutoUpdateRetry = time.Minute
	// coreAutoUpdateIdle is how long the user must have been away before a
	// pass on battery may start.
	coreAutoUpdateIdle = 2 * time.Minute
	// coreAutoUpdateOverdue lets a pass run at the next quiet moment even on
	// battery and in use, so a device that is never idle still updates.
	coreAutoUpdateOverdue = 72 * time.Hour
	// coreUpdateModeSetting is the custom variable that holds the Emulator
	// updates setting: "off", "notify" or "install". The name predates PPSSPP's
	// share of it and is kept so existing cards read their choice.
	coreUpdateModeSetting = "coreUpdateMode"
	// coreUpdateModeChosenSetting marks a mode the user picked (true), as
	// opposed to one JukaHub stored by itself. Only an unmarked "notify" is
	// migrated to the new default.
	coreUpdateModeChosenSetting = "coreUpdateModeChosen"
	// emuAutoUpdateSetting is the on/off switch older builds kept. It is read
	// once to migrate, and written beside the mode (on only for "install"), so
	// a build from before the mode still does the safe thing.
	emuAutoUpdateSetting = "emuAutoUpdate"
)

// coreUpdateMode is what the daily core pass does.
type coreUpdateMode int32

const (
	coreUpdateOff coreUpdateMode = iota
	coreUpdateNotify
	coreUpdateInstall
)

// coreUpdateDefault is the mode of a card whose user never chose one.
const coreUpdateDefault = coreUpdateInstall

// coreUpdateModes is the order the setting offers them in.
var coreUpdateModes = []coreUpdateMode{coreUpdateOff, coreUpdateNotify, coreUpdateInstall}

// key is how a mode is stored in the user config.
func (m coreUpdateMode) key() string {
	switch m {
	case coreUpdateOff:
		return "off"
	case coreUpdateInstall:
		return "install"
	}
	return "notify"
}

// label is how the setting and the Core builds page name a mode.
func (m coreUpdateMode) label() string {
	switch m {
	case coreUpdateOff:
		return "Off"
	case coreUpdateInstall:
		return "Install automatically"
	}
	return "Notify me"
}

// parseCoreUpdateMode reads a stored mode. An unknown value reads as the
// default, with known false.
func parseCoreUpdateMode(s string) (coreUpdateMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "no", "0":
		return coreUpdateOff, true
	case "notify", "ask":
		return coreUpdateNotify, true
	case "install", "auto", "automatic", "on":
		return coreUpdateInstall, true
	}
	return coreUpdateDefault, false
}

// coreUpdateModeChosen reports the marker a user's own pick writes.
func coreUpdateModeChosen(custom map[string]interface{}) bool {
	return customBool(custom, coreUpdateModeChosenSetting, false)
}

// coreUpdateModeFrom reads the mode from the user's custom variables. A stored
// mode wins — except an unmarked "notify", which is what LIB-16 stored for a
// user who never chose and so reads as the default now. Otherwise the old switch
// decides: it was written only when the user flipped it, so an explicit off
// stays Off and an explicit on becomes Install automatically — what they chose.
// Anything else is the default, Install automatically.
func coreUpdateModeFrom(custom map[string]interface{}) coreUpdateMode {
	if v, ok := customValue(custom, coreUpdateModeSetting); ok {
		if s, isString := v.(string); isString {
			if m, known := parseCoreUpdateMode(s); known {
				if m == coreUpdateNotify && !coreUpdateModeChosen(custom) {
					return coreUpdateDefault
				}
				return m
			}
		}
	}
	if v, ok := customValue(custom, emuAutoUpdateSetting); ok {
		if s, isString := v.(string); isString {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "false", "0", "off", "no":
				return coreUpdateOff
			case "true", "1", "on", "yes":
				return coreUpdateInstall
			}
			return coreUpdateDefault
		}
		if coerceBool(v) {
			return coreUpdateInstall
		}
		return coreUpdateOff
	}
	return coreUpdateDefault
}

// coreUpdateModeNeedsMigration reports the ambiguous stored value: "notify"
// with no marker that the user chose it.
func coreUpdateModeNeedsMigration(custom map[string]interface{}) bool {
	v, ok := customValue(custom, coreUpdateModeSetting)
	if !ok {
		return false
	}
	s, isString := v.(string)
	if !isString {
		return false
	}
	m, known := parseCoreUpdateMode(s)
	return known && m == coreUpdateNotify && !coreUpdateModeChosen(custom)
}

// coreUpdateModeFor is coreUpdateModeFrom for a config (main thread only).
func coreUpdateModeFor(config *Config) coreUpdateMode {
	if config == nil {
		return coreUpdateDefault
	}
	return coreUpdateModeFrom(config.Variables.Custom)
}

// coreAutoUpdateDue reports whether the background core pass may run, given the
// stamp of the last check that read the index. It follows autoUpdateCheckDue's
// rule, which matters on this hardware: the handheld has no RTC battery, so a
// missing, garbled, future or pre-2020 stamp must never suppress the update.
func coreAutoUpdateDue(last string, now time.Time) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok {
		return true
	}
	if now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return true
	}
	return now.Sub(t) >= coreAutoUpdateInterval
}

// coreAutoUpdateOverdueFor reports whether a due pass should stop waiting for
// the device to be charging or idle; waited is how long it has been waiting this
// session (the scheduler's own clock, zero when it does not keep one). With a
// trustworthy stamp overdue is three days without a pass. Without one — a card
// that has never had a pass, or a clock that cannot be trusted — the first pass
// still waits for a quiet moment, but only one poll's worth: saying no forever,
// as this once did, left a device that is always in use on battery without its
// first update until someone happened to leave it idle at the right minute.
// JukaHub's PPSSPP pass (ppsspp_managed.go) asks the same question.
func coreAutoUpdateOverdueFor(last string, now time.Time, waited time.Duration) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok || now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return waited >= coreAutoUpdatePoll
	}
	return now.Sub(t) >= coreAutoUpdateOverdue
}

// coreAutoUpdateAllowed is the policy for this device, with the opt-outs kept
// separate so an operator (or a test) can stand the pass down without touching
// the device checks.
func coreAutoUpdateAllowed(disabled, supported, handheld, online bool) bool {
	if disabled || !supported || !handheld || !online {
		return false
	}
	return true
}

// coreAutoUpdateMoment is the "is now a good time" half of the decision: never
// over a game, never over another install or a playing video, and on battery
// only once the user has stepped away — unless the pass is long overdue.
func coreAutoUpdateMoment(inGame, busy, charging bool, idle time.Duration, overdue bool) bool {
	if inGame || busy {
		return false
	}
	return charging || idle >= coreAutoUpdateIdle || overdue
}

// emuCoreUpdateMode mirrors the setting for the scheduler goroutines. The
// Custom map belongs to the main thread, so they read this instead.
var emuCoreUpdateMode atomic.Int32

func init() { emuCoreUpdateMode.Store(int32(coreUpdateDefault)) }

// emuCoreUpdateModeNow is the mode the scheduler, the Core builds page and
// JukaHub's PPSSPP pass act on.
func emuCoreUpdateModeNow() coreUpdateMode { return coreUpdateMode(emuCoreUpdateMode.Load()) }

// emuAutoUpdateEnabled reports whether the daily pass runs at all — to notify
// or to install (main thread only).
func emuAutoUpdateEnabled(config *Config) bool {
	return coreUpdateModeFor(config) != coreUpdateOff
}

// emuSetCoreUpdateMode stores a mode the user chose and saves it (main thread
// only): every control that offers the setting calls it, so the choice carries
// the marker that keeps it from being migrated. A pick made before the
// scheduler's first look also retires the migration's notice, which would
// otherwise announce a mode the user has just changed.
func emuSetCoreUpdateMode(config *Config, m coreUpdateMode) {
	coreUpdateMigrationPending.Store(false)
	emuStoreCoreUpdateMode(config, m, true)
}

// emuStoreCoreUpdateMode stores a mode and saves it (main thread only). chosen
// writes the marker; JukaHub's own writes (the start-up store, the migration)
// leave it as it was. The old switch is written beside the mode, on only for
// "install", so a build from before the mode never installs where this one
// would not.
func emuStoreCoreUpdateMode(config *Config, m coreUpdateMode, chosen bool) {
	emuCoreUpdateMode.Store(int32(m))
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[coreUpdateModeSetting] = m.key()
	config.Variables.Custom[emuAutoUpdateSetting] = m == coreUpdateInstall
	if chosen {
		config.Variables.Custom[coreUpdateModeChosenSetting] = true
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// coreUpdateModeVisible, coreUpdateModeDetail, coreUpdateModeOptions,
// coreUpdateModeIndex and coreUpdateModeChoose are the Settings row
// (setmodel.go). The row shows wherever the setting looks after something —
// the TrimUI handhelds and a PC — and says what that is.
func coreUpdateModeVisible(*Config) bool { return emuAutoUpdateCovers() != "" }

func coreUpdateModeDetail(*Config) string {
	covers := emuAutoUpdateCovers()
	if covers == "" {
		return "What the daily check does; the old build is always kept"
	}
	return "Covers " + covers + " · the old build is always kept"
}

func coreUpdateModeOptions(*Config) []setOption {
	when := "while charging or idle"
	if !IsTrimuiDevice() {
		when = "while JukaHub sits idle"
	}
	notes := map[coreUpdateMode]string{
		coreUpdateOff:     "No daily check; update from the Emulators page",
		coreUpdateNotify:  "A daily check says what has a new build; nothing is installed",
		coreUpdateInstall: "Installs new builds " + when + ", keeping the old one",
	}
	out := make([]setOption, 0, len(coreUpdateModes))
	for _, m := range coreUpdateModes {
		out = append(out, setOption{Value: m.key(), Label: m.label(), Note: notes[m]})
	}
	return out
}

func coreUpdateModeIndex(c *Config) int {
	cur := coreUpdateModeFor(c)
	for i, m := range coreUpdateModes {
		if m == cur {
			return i
		}
	}
	return -1
}

// coreUpdateModeChoose applies a choice and returns the toast for it.
func coreUpdateModeChoose(c *Config, value string) string {
	m, _ := parseCoreUpdateMode(value)
	emuSetCoreUpdateMode(c, m)
	return "Emulator updates: " + m.label()
}

// coreUpdateMigratedNotice is the one toast the migration leaves, naming what
// the setting covers on this device. It is shown by the scheduler at its first
// look that is not over a game, not during boot, where the splash would hide
// it.
func coreUpdateMigratedNotice() string {
	return "Emulator updates now install automatically (" + emuAutoUpdateCovers() + "), keeping the old build for a one-press roll back · " +
		"change it in Settings → Games & emulators → Emulator updates"
}

// coreUpdateMigrationPending holds that notice until it can be seen. A PC
// uses it for its own one-time notice (emuDesktopNotice): a choice made in
// the meantime retires either.
var coreUpdateMigrationPending atomic.Bool

// coreAutoUpdateDisabled reports the environment opt-out. It mirrors
// JUKAHUB_NO_AUTOUPDATE, which stands the release check down.
func coreAutoUpdateDisabled() bool {
	return os.Getenv("JUKAHUB_NO_CORE_AUTOUPDATE") != ""
}

// coreAutoUpdateLastCheck is the stamp of the last pass that read the index.
func coreAutoUpdateLastCheck() string {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return patchState.CoreUpdateCheckedAt
}

// emuAutoUpdateSummary is the notification a pass that changed something
// leaves: how many cores, and which, kept short enough for a toast.
func emuAutoUpdateSummary(report emuInstallReport) string {
	n := len(report.Updated)
	if n == 0 {
		if len(report.Refused) > 0 && len(report.Errors) == 0 && report.LowSpace == "" {
			return coreRefusedSummary(report)
		}
		return report.summary()
	}
	noun := "emulator cores"
	if n == 1 {
		noun = "emulator core"
	}
	names := report.Updated
	more := ""
	if len(names) > 3 {
		more = fmt.Sprintf(" +%d more", len(names)-3)
		names = names[:3]
	}
	msg := fmt.Sprintf("Updated %d %s (%s%s)", n, noun, strings.Join(names, ", "), more)
	if kept := len(report.Refused); kept > 0 {
		msg += fmt.Sprintf(" · %d kept (the new build cannot be used here)", kept)
	}
	if len(report.Errors) > 0 {
		msg += fmt.Sprintf(" · %d failed", len(report.Errors))
	}
	if report.LowSpace != "" {
		msg += " · " + coreLowSpaceNote(report.LowSpace)
	}
	return msg
}

// coreRefusedSummary is the toast of a pass whose only news is builds this
// device refused. It is said once per build: a refused build is not fetched
// again, so tomorrow's pass is quiet about it.
func coreRefusedSummary(report emuInstallReport) string {
	if len(report.Refused) == 1 && report.RefusedNote != "" {
		return report.Refused[0] + ": " + report.RefusedNote + " · the installed build is kept"
	}
	names := report.Refused
	more := ""
	if len(names) > 3 {
		more = fmt.Sprintf(" +%d more", len(names)-3)
		names = names[:3]
	}
	return "New builds of " + strings.Join(names, ", ") + more + " cannot be used on this device · the installed builds are kept"
}

// coreAutoUpdateSession is the scheduler's memory of this run: when a due
// pass started waiting for its moment, and when the last pass that read the
// index finished, both by the monotonic clock. The second is a guard the stamp
// cannot be: on a handheld whose clock reads 1970 until it reaches the network,
// every stamp is untrustworthy and so always due, and without it the daily pass
// would run at every poll until the clock was set.
var coreAutoUpdateSession struct {
	mu           sync.Mutex
	waitingSince time.Time
	lastPass     time.Time
}

// coreAutoUpdateWaited starts the waiting clock if it is not running and
// reports how long the pass has been waiting.
func coreAutoUpdateWaited() time.Duration {
	coreAutoUpdateSession.mu.Lock()
	defer coreAutoUpdateSession.mu.Unlock()
	if coreAutoUpdateSession.waitingSince.IsZero() {
		coreAutoUpdateSession.waitingSince = time.Now()
		return 0
	}
	return time.Since(coreAutoUpdateSession.waitingSince)
}

// coreAutoUpdateStopWaiting resets the waiting clock: the pass ran, or is not
// due.
func coreAutoUpdateStopWaiting() {
	coreAutoUpdateSession.mu.Lock()
	coreAutoUpdateSession.waitingSince = time.Time{}
	coreAutoUpdateSession.mu.Unlock()
}

// coreAutoUpdateRanRecently reports a pass inside the last interval of this
// session, whatever the wall clock says.
func coreAutoUpdateRanRecently() bool {
	coreAutoUpdateSession.mu.Lock()
	defer coreAutoUpdateSession.mu.Unlock()
	last := coreAutoUpdateSession.lastPass
	return !last.IsZero() && time.Since(last) < coreAutoUpdateInterval
}

// startCoreAutoUpdate starts the scheduler. It is best-effort: every reason it
// cannot run is a log line, never a startup error, and a device with nothing to
// update sees only the install bar settling. It runs from the boot queue, on
// the main thread, which is where the setting may be read and written. A
// device that is not a TrimUI handheld — a PC — gets the desktop scheduler
// (emuautoupdate_desktop.go) instead, under the same setting.
func startCoreAutoUpdate(config *Config) {
	if config != nil && IsTrimuiDevice() && coreUpdateModeNeedsMigration(config.Variables.Custom) {
		// LIB-16 stored "notify" for every card whose user never chose. It is
		// rewritten once as the new default, and the scheduler says so.
		logInfo("emulators", "core updates: the unchosen %q becomes %q, the new default", coreUpdateNotify.key(), coreUpdateDefault.key())
		emuStoreCoreUpdateMode(config, coreUpdateDefault, false)
		coreUpdateMigrationPending.Store(true)
	}
	mode := coreUpdateModeFor(config)
	emuCoreUpdateMode.Store(int32(mode))
	chosen := false
	// The resolved mode is written once, beside the old switch, so it stops
	// depending on how the old switch is read, and a build from before the
	// mode reads a switch that matches (on only for Install automatically).
	if config != nil {
		chosen = coreUpdateModeChosen(config.Variables.Custom)
		if _, stored := customValue(config.Variables.Custom, coreUpdateModeSetting); !stored {
			logInfo("emulators", "core updates: storing the setting as %q", mode.key())
			emuStoreCoreUpdateMode(config, mode, false)
		}
	}
	if !IsTrimuiDevice() {
		// Nothing on a PC updated by itself before; the default does now, so a
		// PC whose user never chose is told once (emuDesktopNotice).
		startDesktopEmuAutoUpdate(IsWindows() && mode == coreUpdateInstall && !chosen)
		return
	}
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), true) {
		return
	}
	go func() {
		time.Sleep(coreAutoUpdateStartupDelay)
		for {
			_, waiting := coreAutoUpdateTick(time.Now())
			if waiting {
				time.Sleep(coreAutoUpdateRetry)
			} else {
				time.Sleep(coreAutoUpdatePoll)
			}
		}
	}()
}

// coreAutoUpdateTick runs the pass once if it is allowed, due and the moment is
// right. ran reports that a pass ran; waiting that one is due and the scheduler
// should look again soon (the moment was wrong, the network or the patch slot
// was not there, or a game stopped the pass part-way). The pass is due when the
// cores are, or when one of the other updaters is (emuRuntimesForPass, which
// lets the nearly due ones ride along with a pass that runs anyway).
func coreAutoUpdateTick(now time.Time) (ran, waiting bool) {
	if !gameScreenSuspended() && coreUpdateMigrationPending.Swap(false) {
		ShowToast(coreUpdateMigratedNotice(), ToastKindInfo)
	}
	mode := emuCoreUpdateModeNow()
	if mode == coreUpdateOff {
		coreAutoUpdateStopWaiting()
		return false, false
	}
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), true) {
		return false, false
	}
	if gameScreenSuspended() {
		// Nothing runs over a game, and nothing is looked at during one (the
		// updaters' checks of what is installed read the card): the first
		// look after it decides, a minute at most after the game ends.
		return false, true
	}
	last := coreAutoUpdateLastCheck()
	coresDue := coreAutoUpdateDue(last, now) && !coreAutoUpdateRanRecently()
	extras := emuRuntimesForPass(now, coresDue)
	if !coresDue && len(extras) == 0 {
		coreAutoUpdateStopWaiting()
		return false, false
	}
	waited := coreAutoUpdateWaited()
	if !IsOnline() {
		return false, true
	}
	// Both modes wait for the same quiet moment: even a check reads every
	// installed core once to checksum it, which is card work a game, a video
	// or a scrolling list should not compete with.
	busy := installBarActive() || videoHoldsScreenAwake()
	overdue := (coresDue && coreAutoUpdateOverdueFor(last, now, waited)) || emuRuntimesOverdue(extras, now, waited)
	if !coreAutoUpdateMoment(gameScreenSuspended(), busy, getBatteryCharging(), userInputAge(now), overdue) {
		return false, true
	}
	coreAutoUpdateStopWaiting()
	if coresDue && len(emuInstalledCoreSet()) == 0 {
		// No cores to update: they are looked at again tomorrow rather than
		// at every poll.
		coreAutoUpdateStamp(now)
		coresDue = false
	}
	if mode == coreUpdateNotify {
		var active func() map[string]coreBuild
		if coresDue {
			active = func() map[string]coreBuild { return scanCoreInventory().Active }
		}
		return emuAutoNotifyPass(now, active, extras), false
	}
	var cores emuAutoCoreRunner
	if coresDue {
		cores = runInstalledCoreUpdates
	}
	return emuAutoInstallPass(now, cores, extras)
}

// emuAutoCoreRunner brings cores forward for a pass: runInstalledCoreUpdates
// on the handheld, the managed RetroArch's cores on a PC
// (emuDesktopCoreRunner). progress drives the install card.
type emuAutoCoreRunner func(ctx context.Context, client *http.Client, progress func(phase, detail string)) emuInstallReport

// emuAutoInstallPass is the Install automatically pass, on the handheld and on
// a PC alike: the cores when cores is not nil, then every updater in extras,
// under one install card and one toast. ran reports that a pass ran; waiting
// that the scheduler should look again soon (the patch slot was taken, or a
// game stopped the pass part-way).
//
// It is the same work the pages' actions do — for the cores: download, verify
// the CRC32 the index states, check that the build can load here, activate,
// keep the replaced build as a .bak up to maxCoreBackups — reported through the
// same install bar, and it ends with a toast ("Updated 3 emulator cores (…) ·
// updated hakuX v0.1.5") that the notification centre keeps.
func emuAutoInstallPass(now time.Time, cores emuAutoCoreRunner, extras []emuRuntimeUpdater) (ran, waiting bool) {
	if cores == nil && len(extras) == 0 {
		return false, false
	}
	// Looked at every minute while due, so these two say so in the debug log
	// only.
	if !tryBeginPatchOp() {
		logDebug("emulators", "auto-update: waiting, another Patch operation is running")
		return false, true
	}
	defer endPatchOp()

	// A helper-tool download runs outside the Patch lock and may own the card
	// right now — on a first boot ffmpeg is often still arriving — and a begin
	// would retire its token and stop it. The pass waits; nothing is stamped,
	// so everything is still due at the next look.
	if installBarActive() {
		logDebug("emulators", "auto-update: waiting, an install is running")
		return false, true
	}
	bar := &emuAutoBar{label: "Updating emulator cores"}
	if len(extras) > 0 {
		bar.label = "Updating emulators"
	}
	// A game that starts while the pass runs gets the device back: the
	// watcher stops the pass, which ends between cores and between updaters.
	pass, stopPass := context.WithCancel(context.Background())
	defer stopPass()
	var stoppedForGame atomic.Bool
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pass.Done():
				return
			case <-ticker.C:
				if gameScreenSuspended() {
					logPatch("emulator auto-update: a game started, stopping")
					stoppedForGame.Store(true)
					stopPass()
					return
				}
			}
		}
	}()

	var coreReport *emuInstallReport
	coreResume, cardCancelled := false, false
	if cores != nil {
		logPatch("core auto-update: checking installed cores")
		bar.begin()
		report, timedOut, cancelled := emuAutoRunCores(pass, cores)
		// A run another install took over (its begin retires this run's
		// token) is not the user's "not now": what it did not reach follows
		// at the next quiet moment, as after a game.
		_, coresTaken := bar.lost()
		cardCancelled = cancelled && !coresTaken
		coreReport, coreResume = coreAutoUpdateSettle(now, report, timedOut, stoppedForGame.Load() || coresTaken)
	}
	var extrasReport emuRuntimeReport
	_, taken := bar.lost()
	switch {
	case len(extras) == 0:
	case taken:
		// Another run took the card while the cores ran (a helper-tool
		// download starts outside the Patch lock): the updaters wait for the
		// next quiet moment rather than run behind its card.
		extrasReport.Resume = true
	case cardCancelled:
		// Cancelled from the card while the cores ran: a "not now" for the
		// whole pass, not only for its first half.
		for _, u := range extras {
			emuRuntimeDefer(u.Key, coreAutoUpdateInterval)
		}
	case pass.Err() == nil:
		extrasReport = emuRunRuntimeUpdaters(pass, coreUpdateInstall, extras, bar, now)
	}
	bar.finish()
	emuAutoPassToast(coreReport, extrasReport)
	// Whatever a game stopped follows at the next quiet moment: it was not
	// stamped.
	return true, coreResume || stoppedForGame.Load() || extrasReport.Resume
}

// emuAutoRunCores runs the core half of a pass under the card the pass has
// begun. The deadline derives from the card's run token, so a cancel from the
// card or the activity strip stops the transfers, and the pass's own stop (a
// game starting) reaches it too. cardCancelled reports the first.
func emuAutoRunCores(pass context.Context, cores emuAutoCoreRunner) (report emuInstallReport, timedOut, cardCancelled bool) {
	ctx, cancel := context.WithTimeout(installBarRunContext(), 20*time.Minute)
	defer cancel()
	stop := context.AfterFunc(pass, cancel)
	defer stop()
	report = cores(ctx, newEmulatorUpdateClient(), installBarProgressHook())
	timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	cardCancelled = errors.Is(ctx.Err(), context.Canceled) && pass.Err() == nil
	return report, timedOut, cardCancelled
}

// coreAutoUpdateSettle records what the core half of a pass did: the stamp,
// the log lines and the caches its updates outdate. stopped reports a run
// stopped by something other than the user — a game starting, or another
// install taking the card. It returns the report when it has news for the
// toast (nil otherwise), and whether the rest of the cores should follow at the
// next quiet moment.
func coreAutoUpdateSettle(now time.Time, report emuInstallReport, timedOut, stopped bool) (*emuInstallReport, bool) {
	if !report.Checked {
		// The index never arrived (offline mid-poll, a redirect refused).
		// Leaving the stamp alone means the next poll tries again.
		for _, e := range report.Errors {
			logPatch("core auto-update: %s", e)
		}
		return nil, false
	}
	// A pass a game stopped, or one that ran out of time on a card with a
	// hundred cores to bring forward, is not stamped: what it updated stays
	// updated and the rest follows at the next quiet moment. A pass that ran
	// out of time having settled no core is stamped like a finished one: on a
	// link too slow to bring one build in before the deadline, resuming would
	// start the same downloads again a minute later, all day. One the user
	// cancelled from the card is stamped — that was a "not now".
	settled := len(report.Updated)+len(report.Refused) > 0
	resume := report.Interrupted && (stopped || (timedOut && settled))
	if resume {
		logPatch("core auto-update: stopped after %d update(s); the rest at the next quiet moment", len(report.Updated))
	} else {
		coreAutoUpdateStamp(now)
	}
	if report.KnownRefused > 0 {
		logPatch("core auto-update: %d core(s) kept, their new build was refused before", report.KnownRefused)
	}
	if len(report.Updated) > 0 {
		// The tile list, the compatibility panel and the footer's rollback
		// cache all read the cores folder.
		invalidateEmulatorCache()
		invalidateCompatCache()
		emuForgetAllUpdates()
		coreBuildsRequestReload()
	}
	if len(report.Updated) == 0 && len(report.Errors) == 0 && len(report.Refused) == 0 && report.LowSpace == "" {
		switch {
		case !report.Interrupted:
			logPatch("core auto-update: %d core(s) checked, all current", report.Current)
		case !resume:
			logPatch("core auto-update: stopped before any core was updated; the next pass is tomorrow's")
		}
		return nil, resume
	}
	return &report, resume
}

// coreAutoUpdateStamp records a pass that read the index (or found nothing to
// update), so the next one waits a day.
func coreAutoUpdateStamp(now time.Time) {
	patchStateMu.Lock()
	patchState.CoreUpdateCheckedAt = now.UTC().Format(time.RFC3339)
	patchStateMu.Unlock()
	_ = savePatchState()
	coreAutoUpdateSession.mu.Lock()
	coreAutoUpdateSession.lastPass = time.Now()
	coreAutoUpdateSession.mu.Unlock()
}

// emuAutoNotifyPass is the Notify mode's daily pass: the cores active returns
// (nil when they are not due) compared with the build index, and every updater
// in extras asked whether it has a newer build. What is new is said once, in
// one toast; nothing is downloaded or replaced. It reports whether a pass ran.
func emuAutoNotifyPass(now time.Time, active func() map[string]coreBuild, extras []emuRuntimeUpdater) bool {
	ran := false
	coreMsg := ""
	if active != nil {
		coreMsg, ran = coreAutoUpdateNotifyCheck(now, active())
	}
	var report emuRuntimeReport
	if len(extras) > 0 {
		report = emuRunRuntimeUpdaters(context.Background(), coreUpdateNotify, extras, nil, now)
		ran = true
	}
	emuAutoNotifyToast(coreMsg, report.Available)
	return ran
}

// coreAutoUpdateNotifyCheck is the cores' share of the Notify pass: it reads
// the build index and compares it with active. It returns the sentence to
// announce — "" when nothing differs, or when the same builds were announced
// before — and whether a check ran.
func coreAutoUpdateNotifyCheck(now time.Time, active map[string]coreBuild) (string, bool) {
	if len(active) == 0 {
		coreAutoUpdateStamp(now)
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	index, err := fetchCoreIndex(ctx, newEmulatorUpdateClient())
	if err != nil {
		// Not stamped: a device that boots before its Wi-Fi associates asks
		// again at the next poll.
		logPatch("core update check: %v", err)
		return "", true
	}
	coreAutoUpdateStamp(now)
	stale, _ := emuCompareCores(index, active)
	if len(stale) == 0 {
		logPatch("core update check: the installed cores match the index")
		return "", true
	}
	if !coreUpdateAnnounce(coreUpdateSignature(stale)) {
		logPatch("core update check: %d core update(s), already announced", len(stale))
		return "", true
	}
	msg := coreUpdateNotifyMessage(stale)
	logPatch("core update check: %s", msg)
	// The Emulators and Core builds pages badge the same cores from their own
	// check; starting it now means they already know when opened.
	emuStartUpdateCheck(0)
	return msg, true
}

// coreUpdateSignature names a set of available builds ("mgba@1a2b3c4d,…"), so
// the same updates are announced once rather than every day.
func coreUpdateSignature(stale map[string]coreIndexEntry) string {
	parts := make([]string, 0, len(stale))
	for core, entry := range stale {
		parts = append(parts, core+"@"+entry.CRC32)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// coreUpdateAnnounce records sig as announced and reports whether it is new.
func coreUpdateAnnounce(sig string) bool {
	patchStateMu.Lock()
	if patchState.CoreUpdateNotified == sig {
		patchStateMu.Unlock()
		return false
	}
	patchState.CoreUpdateNotified = sig
	patchStateMu.Unlock()
	_ = savePatchState()
	return true
}

// coreUpdateNotifyMessage is the Notify pass's toast: which cores, briefly,
// and where to install them.
func coreUpdateNotifyMessage(stale map[string]coreIndexEntry) string {
	cores := make([]string, 0, len(stale))
	for core := range stale {
		cores = append(cores, core)
	}
	sort.Strings(cores)
	names := make([]string, 0, 3)
	for i, core := range cores {
		if i == 3 {
			break
		}
		names = append(names, coreConfigDisplayName(core))
	}
	list := strings.Join(names, ", ")
	if len(cores) > 3 {
		list += fmt.Sprintf(" +%d more", len(cores)-3)
	}
	// "Different", not "newer": the check compares checksums, and a build the
	// user rolled back to differs from the index as much as an old one does.
	if len(cores) == 1 {
		return "A different build is available for " + list + " · install it on the Emulators page (the current build is kept)"
	}
	return "Different builds are available for " + list + " · install them on the Emulators page (the current builds are kept)"
}
