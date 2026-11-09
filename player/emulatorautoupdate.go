package main

// emulatorautoupdate.go — keeping the emulator cores current without a press.
//
// The Emulators page can bring every installed core forward in one action, but
// it needs the user to open that page and press it. On the handheld the card is
// the whole console: a core that is three nightlies behind is a game that runs
// worse for no reason the user can see, and the fix was a trip to a page they
// did not know about.
//
// So a daily pass runs by itself. What it does is the Core updates setting
// (Settings → Games, or Start on the Core builds page), three-way since LIB-16:
//
//   - Notify (the default): read the index, compare it with the installed
//     cores, and say once which have a different build. Nothing is
//     downloaded; the Emulators and Core builds pages install on a press.
//   - Install automatically: the pass described below.
//   - Off: no pass at all.
//
// It used to be one switch, on unless switched off. A new build can make a
// game's save states unloadable, so installing should be something the user
// asked for: coreUpdateModeFrom keeps an explicit off as Off and an explicit on
// as Install automatically, and turns the old unasked default into Notify.
//
// The pass runs at most once per coreAutoUpdateInterval. A scheduler wakes
// every coreAutoUpdatePoll after the interface is up and runs the pass when it
// is due and the moment is right:
//
//   - online, and no other patch operation or install owns the device;
//   - never while a game or emulator is on screen — and a game that starts
//     mid-pass stops it between cores;
//   - while charging, or after a couple of idle minutes, so a download never
//     competes with someone navigating; a pass more than three days overdue
//     runs at the next quiet moment regardless.
//
// It is the same work the page's action does — download, verify the CRC32 the
// index states, activate, keep the replaced build as a .bak up to
// maxCoreBackups — reported through the same install bar, and it ends with a
// toast ("Updated 3 emulator cores") that the notification centre keeps.
//
// What it will not do:
//   - run on a desktop, where the Emulators page updates on a press;
//   - stand down for a whole interval when the check failed. Only a pass that
//     actually read the index is stamped, so a device that boots before its
//     Wi-Fi associates tries again at the next poll.
//
// The index is an unsigned nightly listing, fetched over HTTPS only
// (validatePatchURL, redirects included). The libretro buildbot publishes
// per-core archives for linux/aarch64 in its nightly tree alone — its stable
// tree has versioned bundles for other platforms, not per-core builds for this
// one — and the only checksum it states is the CRC32 of each core binary in
// .index-extended; there is no SHA-256 and no signature to check. So an update
// here is checked against that CRC32, its SHA-256 is recorded beside the build
// (source.json), the build it replaces is kept, and a nightly that turns out
// worse is one press away from being rolled back on the device (Emulators R1,
// or the Core builds page). Save states made with the build that was replaced
// are flagged on the game's page (corebuilds_history.go).

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
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
	// coreAutoUpdateIdle is how long the user must have been away before a
	// pass on battery may start.
	coreAutoUpdateIdle = 2 * time.Minute
	// coreAutoUpdateOverdue lets a pass run at the next quiet moment even on
	// battery and in use, so a device that is never idle still updates.
	coreAutoUpdateOverdue = 72 * time.Hour
	// coreUpdateModeSetting is the custom variable that holds the Core updates
	// setting: "off", "notify" or "install".
	coreUpdateModeSetting = "coreUpdateMode"
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

// parseCoreUpdateMode reads a stored mode.
func parseCoreUpdateMode(s string) (coreUpdateMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "no", "0":
		return coreUpdateOff, true
	case "notify", "ask":
		return coreUpdateNotify, true
	case "install", "auto", "automatic", "on":
		return coreUpdateInstall, true
	}
	return coreUpdateNotify, false
}

// coreUpdateModeFrom reads the mode from the user's custom variables. A stored
// mode wins. Otherwise the old switch decides: it was written only when the
// user flipped it, so an explicit off stays Off and an explicit on becomes
// Install automatically — what they chose. No value at all is the old
// unasked default, and becomes Notify.
func coreUpdateModeFrom(custom map[string]interface{}) coreUpdateMode {
	if v, ok := customValue(custom, coreUpdateModeSetting); ok {
		if s, isString := v.(string); isString {
			if m, known := parseCoreUpdateMode(s); known {
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
			return coreUpdateNotify
		}
		if coerceBool(v) {
			return coreUpdateInstall
		}
		return coreUpdateOff
	}
	return coreUpdateNotify
}

// coreUpdateModeFor is coreUpdateModeFrom for a config (main thread only).
func coreUpdateModeFor(config *Config) coreUpdateMode {
	if config == nil {
		return coreUpdateNotify
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

// coreAutoUpdateOverdueFor reports whether the last successful pass is so old
// that the pass should not wait for the device to be charging or idle.
func coreAutoUpdateOverdueFor(last string, now time.Time) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok || now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		// No trustworthy stamp: the first pass on a fresh card still waits for
		// a quiet moment, but only one poll's worth.
		return false
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
// over a game, never over another install, and on battery only once the user
// has stepped away — unless the pass is long overdue.
func coreAutoUpdateMoment(inGame, busy, charging bool, idle time.Duration, overdue bool) bool {
	if inGame || busy {
		return false
	}
	return charging || idle >= coreAutoUpdateIdle || overdue
}

// emuCoreUpdateMode mirrors the setting for the scheduler goroutine. The Custom
// map belongs to the main thread, so the goroutine reads this instead.
var emuCoreUpdateMode atomic.Int32

func init() { emuCoreUpdateMode.Store(int32(coreUpdateNotify)) }

// emuCoreUpdateModeNow is the mode the scheduler and the Core builds page act
// on.
func emuCoreUpdateModeNow() coreUpdateMode { return coreUpdateMode(emuCoreUpdateMode.Load()) }

// emuAutoUpdateEnabled reports whether the daily pass runs at all — to notify
// or to install (main thread only).
func emuAutoUpdateEnabled(config *Config) bool {
	return coreUpdateModeFor(config) != coreUpdateOff
}

// emuSetCoreUpdateMode stores the Core updates setting and saves it (main
// thread only). The old switch is written beside it, on only for "install",
// so a build from before the mode never installs where this one would not.
func emuSetCoreUpdateMode(config *Config, m coreUpdateMode) {
	emuCoreUpdateMode.Store(int32(m))
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[coreUpdateModeSetting] = m.key()
	config.Variables.Custom[emuAutoUpdateSetting] = m == coreUpdateInstall
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// coreUpdateModeOptions, coreUpdateModeIndex and coreUpdateModeChoose are the
// Settings row (setmodel.go).
func coreUpdateModeOptions(*Config) []setOption {
	notes := map[coreUpdateMode]string{
		coreUpdateOff:     "No daily check; update from the Emulators page",
		coreUpdateNotify:  "A daily check says which cores have a different build",
		coreUpdateInstall: "Installs them while charging or idle, keeping the old build",
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
	return "Core updates: " + m.label()
}

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
	if len(report.Errors) > 0 {
		msg += fmt.Sprintf(" · %d failed", len(report.Errors))
	}
	return msg
}

// startCoreAutoUpdate starts the scheduler. It is best-effort: every reason it
// cannot run is a log line, never a startup error, and a device with nothing to
// update sees only the install bar settling. It runs from the deferred startup
// queue, on the main thread, which is where the setting may be read.
func startCoreAutoUpdate(config *Config) {
	mode := coreUpdateModeFor(config)
	emuCoreUpdateMode.Store(int32(mode))
	// The migrated mode is written once, beside the old switch, so it stops
	// depending on how the old switch is read, and a build from before the
	// mode reads a switch that matches (on only for Install automatically).
	if config != nil {
		if _, stored := customValue(config.Variables.Custom, coreUpdateModeSetting); !stored {
			logInfo("emulators", "core updates: storing the setting as %q", mode.key())
			emuSetCoreUpdateMode(config, mode)
		}
	}
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), true) {
		return
	}
	go func() {
		time.Sleep(coreAutoUpdateStartupDelay)
		for {
			coreAutoUpdateTick(time.Now())
			time.Sleep(coreAutoUpdatePoll)
		}
	}()
}

// coreAutoUpdateTick runs the pass once if it is allowed, due and the moment is
// right. It reports whether a pass ran.
func coreAutoUpdateTick(now time.Time) bool {
	mode := emuCoreUpdateModeNow()
	if mode == coreUpdateOff {
		return false
	}
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), IsOnline()) {
		return false
	}
	last := coreAutoUpdateLastCheck()
	if !coreAutoUpdateDue(last, now) {
		return false
	}
	// Both modes wait for the same quiet moment: even a check reads every
	// installed core once to checksum it, which is card work a game or a
	// scrolling list should not compete with.
	if !coreAutoUpdateMoment(gameScreenSuspended(), installBarActive(), getBatteryCharging(),
		userInputAge(now), coreAutoUpdateOverdueFor(last, now)) {
		return false
	}
	if mode == coreUpdateNotify {
		return coreAutoUpdateNotifyPass(now)
	}
	if len(emuInstalledCoreSet()) == 0 {
		// Nothing to update; check again tomorrow rather than every poll.
		coreAutoUpdateStamp(now)
		return false
	}
	if !tryBeginPatchOp() {
		logPatch("core auto-update: skipped, another Patch operation is running")
		return false
	}
	defer endPatchOp()

	// A helper-tool download runs outside the Patch lock and may own the card
	// right now — on a first boot ffmpeg is often still arriving — and a begin
	// would retire its token and stop it. The cores wait for the next poll;
	// the check stamp is untouched, so they are still due then.
	if installBarActive() {
		logPatch("core auto-update: skipped, an install is running")
		return false
	}
	logPatch("core auto-update: checking installed cores")
	installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	// The timeout derives from the card's run token (minted by the begin
	// above), so a cancel from the card or the strip stops the transfers.
	ctx, cancel := context.WithTimeout(installBarRunContext(), 20*time.Minute)
	defer cancel()
	// A game that starts while the pass runs gets the device back: the
	// watcher cancels the pass, which stops between cores.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if gameScreenSuspended() {
					logPatch("core auto-update: a game started, stopping")
					cancel()
					return
				}
			}
		}
	}()

	report := runInstalledCoreUpdates(ctx, newEmulatorUpdateClient(), installBarProgressHook())
	installBarPhase(installPhaseDone)
	installBarDone()

	if !report.Checked {
		// The index never arrived (offline mid-poll, a redirect refused).
		// Leaving the stamp alone means the next poll tries again.
		for _, e := range report.Errors {
			logPatch("core auto-update: %s", e)
		}
		return true
	}
	coreAutoUpdateStamp(now)

	if len(report.Updated) == 0 && len(report.Errors) == 0 {
		logPatch("core auto-update: %d core(s) checked, all current", report.Current)
		return true
	}
	if len(report.Updated) > 0 {
		// The tile list, the compatibility panel and the footer's rollback
		// cache all read the cores folder.
		invalidateEmulatorCache()
		invalidateCompatCache()
		emuForgetAllUpdates()
		coreBuildsRequestReload()
		PlaySuccessSound()
	}
	kind := ToastKindSuccess
	if len(report.Errors) > 0 {
		kind = ToastKindWarning
	}
	summary := emuAutoUpdateSummary(report)
	ShowToast(summary, kind)
	logPatch("core auto-update: %s", summary)
	return true
}

// coreAutoUpdateStamp records a pass that read the index (or found nothing to
// update), so the next one waits a day.
func coreAutoUpdateStamp(now time.Time) {
	patchStateMu.Lock()
	patchState.CoreUpdateCheckedAt = now.UTC().Format(time.RFC3339)
	patchStateMu.Unlock()
	_ = savePatchState()
}

// coreAutoUpdateNotifyPass is the Notify mode's daily pass: it reads the build
// index, compares it with the installed cores and, when some have a different
// build, says so once. Nothing is downloaded or replaced. It reports whether a
// pass ran.
func coreAutoUpdateNotifyPass(now time.Time) bool {
	if len(emuInstalledCoreSet()) == 0 {
		coreAutoUpdateStamp(now)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	index, err := fetchCoreIndex(ctx, newEmulatorUpdateClient())
	if err != nil {
		// Not stamped: a device that boots before its Wi-Fi associates asks
		// again at the next poll.
		logPatch("core update check: %v", err)
		return true
	}
	coreAutoUpdateStamp(now)
	stale, _ := emuCompareCores(index, scanCoreInventory().Active)
	if len(stale) == 0 {
		logPatch("core update check: the installed cores match the index")
		return true
	}
	if !coreUpdateAnnounce(coreUpdateSignature(stale)) {
		logPatch("core update check: %d core update(s), already announced", len(stale))
		return true
	}
	msg := coreUpdateNotifyMessage(stale)
	ShowToast(msg, ToastKindInfo)
	logPatch("core update check: %s", msg)
	// The Emulators and Core builds pages badge the same cores from their own
	// check; starting it now means they already know when opened.
	emuStartUpdateCheck(0)
	return true
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
