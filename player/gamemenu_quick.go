package main

// gamemenu_quick.go — Quick settings in the in-game menu: the switches a player reaches for mid-game, without quitting it.
//
// The menu's "Quick settings" row (gamemenu.go) opens a card drawn the way the
// menu is drawn — the same overlay, the game paused and dimmed under it — so it
// works over RetroArch, PPSSPP and ports alike. Its rows:
//
//   - Brightness and Volume: Left/Right step them at once (the header cards'
//     own paths, brightnessStepBy and volumeStepBy, which also move Quick
//     Settings' sliders and save the level); A dims or mutes, as on the cards.
//   - Mute: the speaker to 0 and back to the last audible level
//     (volumeToggleMute, the FN switch's and the volume card's mute).
//   - Music in game: only while JukaHub's music plays under the game (the
//     shared mix, musicgame.go). It is "Music volume in games"; the change is
//     heard at once: the running mix's gain is rescaled here, and the setting
//     is saved for the next game.
//   - Wi-Fi and Bluetooth: Quick Settings' own switches (qsToggleRow; for
//     Bluetooth the same bring-up and power-off workers its switch uses, so
//     this card can tell when they are done). Both run off the render thread:
//     the row says "Switching…" meanwhile and the menu keeps drawing.
//   - Lights: "Lights in games" (power_options.go) — dark in games but for the
//     low-battery and charging indicators — where JukaHub drives the stick
//     lights; it applies at once, the game is still the one on screen.
//   - Rumble: RetroArch's rumble strength (ratuning.go, input_rumble_gain).
//     RetroArch has no command to change it while it runs, so it applies
//     from the next game started — the row says so. Vibration is JukaHub's own
//     buzz (haptics.go), Off / Soft / Normal / Strong, felt at once. Both only
//     where the handheld has a motor.
//   - Power off: A, then A again (unless "Ask before power off" is off): the
//     game is given back and the power card's Power off runs
//     (devPowerRequest), which saves a RetroArch game and closes a standalone
//     one cleanly first — the low-battery guard's own save-and-quit
//     (retroArchSaveAndQuit, sessionsafe.go).
//
// A line at the top reads the system once a second on a goroutine (cheap
// reads: /proc/stat, /proc/meminfo, the thermal zone, the clock, the battery's
// cached level and battery_eta's estimate). The game is stopped while the card
// is up, so the CPU figure is the handheld at rest, not the game's load.
//
// B goes back to the menu (the game when the card was opened by itself,
// gameQuickOpen); Select + L2 goes straight back to the game.

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// gmQuick is the in-game menu's Quick settings row, numbered past
// gamemenu.go's own rows like the Stopwatch row (stopwatch.go).
const gmQuick gmItem = 130

const (
	// gmqSampleEvery paces the system line's readings.
	gmqSampleEvery = time.Second
	// gmqDirectFresh is how long a gameQuickOpen request waits for the
	// overlay to come up over the game.
	gmqDirectFresh = 3 * time.Second
	// gmqPowerArm is how long the first A of Power off waits for the second.
	gmqPowerArm = 4 * time.Second
	// gmqRadioBusyMax stops a Bluetooth switch whose answer never came from
	// saying "Switching…" for ever.
	gmqRadioBusyMax = 20 * time.Second
	// gmqMusicStep is one step of Music in game, the Music settings' own.
	gmqMusicStep = 10
)

// gmqRow is one row of the card.
type gmqRow int

const (
	gmqBrightness gmqRow = iota
	gmqVolume
	gmqMute
	gmqMusic
	gmqWifi
	gmqBluetooth
	gmqLights
	gmqRumble
	gmqVibration
	gmqPower
)

// gmq is the card. Main thread only.
var gmq struct {
	open bool
	// at is the overlay opening the card belongs to: a new opening of the
	// in-game menu starts on the menu.
	at time.Time
	// direct: opened by gameQuickOpen, so B goes back to the game.
	direct bool
	sel    int
	first  int // the first row shown when they do not all fit
	// pendingAt is when gameQuickOpen asked the pad watcher for the menu.
	pendingAt time.Time
	// powerArmedAt is the first A of Power off.
	powerArmedAt time.Time
	// btBusy is a Bluetooth switch still running; btSeq its press.
	btBusy   bool
	btBusyAt time.Time
	btSeq    int
}

// gmqSample is one reading of the system line.
type gmqSample struct {
	cpuPct            float64
	cpuOK             bool
	freqMHz           int
	tempC             float64
	tempOK            bool
	memUsed, memTotal uint64
	memOK             bool
	battery           int
	charging          bool
	eta               string
}

// gmqMon is the system line's readings, written by the sampling goroutine.
var gmqMon struct {
	mu       sync.Mutex
	busy     bool
	at       time.Time
	gen      int
	prev     sysToolCPUTimes
	havePrev bool
	have     bool
	s        gmqSample
}

// ── Opening and closing ──────────────────────────────────────────────────

// gmQuickActive reports whether the card is up over this opening of the menu.
// It also opens it on an overlay that came up for gameQuickOpen.
func gmQuickActive() bool {
	if !gaOverlay.active || gaOverlay.mode != gaModeMenu {
		return false
	}
	if at := gmq.pendingAt; !at.IsZero() {
		gmq.pendingAt = time.Time{}
		if !gaOverlay.openedAt.Before(at) && gaOverlay.openedAt.Sub(at) < gmqDirectFresh {
			gmqOpen(true)
		}
	}
	if gmq.open && !gmq.at.Equal(gaOverlay.openedAt) {
		gmq.open = false
	}
	return gmq.open
}

// gmQuickOpenRow is the menu row's A.
func gmQuickOpenRow() { gmqOpen(false) }

// gmqOpen shows the card: the radios read again (Quick Settings' own probe)
// and the system line started afresh.
func gmqOpen(direct bool) {
	gmq.open, gmq.at, gmq.direct = true, gaOverlay.openedAt, direct
	gmq.sel, gmq.first, gmq.powerArmedAt = 0, 0, time.Time{}
	if len(qs.toggles) >= 3 && len(qs.sliders) >= 1 {
		qsStartProbe()
	}
	gmqMon.mu.Lock()
	gmqMon.gen++
	gmqMon.at, gmqMon.havePrev, gmqMon.have = time.Time{}, false, false
	gmqMon.mu.Unlock()
	PlayActivateSound()
}

// gameQuickOpen opens the card from anywhere, for a button chooser (Hold
// Menu, a front key) or a control profile: over the in-game menu at once;
// over a running game by asking the pad watcher for the in-game menu, as the
// Smart Pro S Home key does (frontHomeAskGameMenu), and opening the card on
// it; and on JukaHub's own screens as Quick Settings. It reports whether
// something opens. Main thread.
func gameQuickOpen() bool {
	config := appConfig
	if gaOverlay.active {
		if !gameMenuEnabled(config) {
			return false
		}
		if gaOverlay.mode != gaModeMenu {
			gmShowMenu()
		}
		if !gmQuickActive() {
			gmqOpen(false)
		}
		return true
	}
	if gameScreenSuspended() && len(liveScreenChildren()) > 0 {
		if gmQuitting.Load() || powerSleepActive() || !gameAssistSupported() || !gameMenuEnabled(config) {
			logInfo("gamemenu", "quick settings over the game: the in-game menu is off or cannot open now")
			return false
		}
		gmq.pendingAt = time.Now()
		frontHomeAskGameMenu(config)
		return true
	}
	if !qs.open {
		qsToggleOpen()
	}
	return qs.open
}

// gmQuickLabel is the menu row's words: the levels it holds.
func gmQuickLabel() (string, string) {
	parts := []string{}
	if pct, ok := gmqBrightnessNow(); ok {
		parts = append(parts, fmt.Sprintf("Brightness %d%%", pct))
	}
	if volumeControlAvailable() {
		if pct := gmqVolumeNow(); pct == 0 {
			parts = append(parts, "Muted")
		} else {
			parts = append(parts, fmt.Sprintf("Volume %d%%", pct))
		}
	}
	if pct := getBatteryPercent(); pct >= 0 {
		parts = append(parts, fmt.Sprintf("Battery %d%%", pct))
	}
	if len(parts) == 0 {
		return "Quick settings", "Brightness, volume, Wi-Fi, lights, power off"
	}
	return "Quick settings", strings.Join(parts, " · ")
}

// ── Rows ─────────────────────────────────────────────────────────────────

// gmqRows is the card's rows on this handheld now.
func gmqRows(config *Config) []gmqRow {
	rows := make([]gmqRow, 0, 10)
	rows = append(rows, gmqBrightness)
	if volumeControlAvailable() {
		rows = append(rows, gmqVolume, gmqMute)
	}
	if audioRouteGameMixActive() {
		rows = append(rows, gmqMusic)
	}
	if gmqToggleIndex(qsActionWifi) >= 0 {
		rows = append(rows, gmqWifi)
	}
	if gmqToggleIndex(qsActionBluetooth) >= 0 {
		rows = append(rows, gmqBluetooth)
	}
	if ledSettingsAvailable(config) {
		rows = append(rows, gmqLights)
	}
	if hapticsAvailable() {
		if raTuneHasRetroArch() {
			rows = append(rows, gmqRumble)
		}
		rows = append(rows, gmqVibration)
	}
	if devPowerSupported() {
		rows = append(rows, gmqPower)
	}
	return rows
}

// gmqToggleIndex is the Quick Settings switch for action, -1 when this
// system has none.
func gmqToggleIndex(action qsAction) int {
	for i := range qs.toggles {
		if qs.toggles[i].Action == action {
			if !qs.toggles[i].Available {
				return -1
			}
			return i
		}
	}
	return -1
}

func gmqLabel(r gmqRow) string {
	switch r {
	case gmqBrightness:
		return "Brightness"
	case gmqVolume:
		return "Volume"
	case gmqMute:
		return "Mute"
	case gmqMusic:
		return "Music in game"
	case gmqWifi:
		return "Wi-Fi"
	case gmqBluetooth:
		return "Bluetooth"
	case gmqLights:
		return "Lights"
	case gmqRumble:
		return "Rumble"
	case gmqVibration:
		return "Vibration"
	case gmqPower:
		return "Power off"
	}
	return ""
}

// gmqHelp is the line under the rows for the highlighted one.
func gmqHelp(config *Config, r gmqRow) string {
	switch r {
	case gmqBrightness:
		return "Left/Right darker or brighter · " + orbKeyName("A") + " dims"
	case gmqVolume:
		return "Left/Right quieter or louder · " + orbKeyName("A") + " mutes"
	case gmqMute:
		return "The speaker off, then back to its last level"
	case gmqMusic:
		return "How loud JukaHub's music plays under the game"
	case gmqWifi, gmqBluetooth:
		return orbKeyName("A") + " switches it · a few seconds, the game stays paused"
	case gmqLights:
		if !ledSettingsOn(config) {
			return "The firmware lights the sticks: turn on Settings → Stick lights first"
		}
		return "Stick lights in games · low battery and charging still show"
	case gmqRumble:
		return "How hard RetroArch games shake · from the next game you start"
	case gmqVibration:
		return "JukaHub's buzz when you move and press"
	case gmqPower:
		if gmqPowerArmed() {
			return "Press " + orbKeyName("A") + " again to power off · any other button cancels"
		}
		return "Saves the game first where it can, then switches off"
	}
	return ""
}

// gmqValue is a row's read-out: its words, the bar's fill (0..100, -1 for no
// bar), whether it reads as on, and whether it cannot be used here.
func gmqValue(config *Config, r gmqRow) (text string, bar int, on bool, dim bool) {
	bar = -1
	switch r {
	case gmqBrightness:
		pct, ok := gmqBrightnessNow()
		if !ok {
			return "…", -1, false, true
		}
		return fmt.Sprintf("%d%%", pct), pct, true, false
	case gmqVolume:
		pct := gmqVolumeNow()
		if pct == 0 {
			return "Muted", 0, false, false
		}
		return fmt.Sprintf("%d%%", pct), pct, true, false
	case gmqMute:
		muted := gmqVolumeNow() == 0
		return gmqOnOff(muted), -1, muted, false
	case gmqMusic:
		pct := musicGameLevel(config)
		return fmt.Sprintf("%d%%", pct), pct, true, false
	case gmqWifi:
		i := gmqToggleIndex(qsActionWifi)
		if i < 0 {
			return "—", -1, false, true
		}
		if qsRadioBusy[qsRadioWifi] > 0 || qsRadioBusy[qsRadioAirplane] > 0 {
			return "Switching…", -1, qs.toggles[i].Value, false
		}
		return gmqOnOff(qs.toggles[i].Value), -1, qs.toggles[i].Value, false
	case gmqBluetooth:
		i := gmqToggleIndex(qsActionBluetooth)
		if i < 0 {
			return "—", -1, false, true
		}
		if gmqBTBusy() {
			return "Switching…", -1, qs.toggles[i].Value, false
		}
		return gmqOnOff(qs.toggles[i].Value), -1, qs.toggles[i].Value, false
	case gmqLights:
		if !ledSettingsOn(config) {
			return "Firmware's", -1, false, true
		}
		lit := ledGamesLit(config)
		return gmqOnOff(lit), -1, lit, false
	case gmqRumble:
		opts := raTuneRumbleOptions(config)
		i := raTuneOptionIndex(opts, raTuneRumbleSetting(config))
		if i < 0 || i >= len(opts) {
			return "RetroArch's own", -1, true, false
		}
		return opts[i].Label, -1, opts[i].Value != "0", false
	case gmqVibration:
		i := gmqVibrationIndex(config)
		return gmqVibrationLabels()[i], -1, i > 0, false
	case gmqPower:
		if gmqPowerArmed() {
			return "Press " + orbKeyName("A") + " again", -1, true, false
		}
		return "", -1, false, false
	}
	return "", bar, false, false
}

func gmqOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// gmqBrightnessNow is the level the header's sun shows, ok false before the
// brightness controller knows its backend.
func gmqBrightnessNow() (int, bool) {
	if !brightnessControlAvailable() {
		return 0, false
	}
	if pct, known := brightnessCurrent(); known {
		return pct, true
	}
	return brightnessBase(), true
}

// gmqVolumeNow is the level the header's speaker shows (or where its next
// step starts, before the first reading).
func gmqVolumeNow() int {
	if pct, known := volumeCurrent(); known {
		return pct
	}
	return volumeBase()
}

// ── Presses ──────────────────────────────────────────────────────────────

// gmQuickPress acts on one press while the card is up (gmPress).
func gmQuickPress(config *Config, b gaButton) {
	rows := gmqRows(config)
	if len(rows) == 0 {
		gmq.open = false
		return
	}
	if gmq.sel >= len(rows) {
		gmq.sel = len(rows) - 1
	}
	r := rows[gmq.sel]
	if b != gaBtnA || r != gmqPower {
		gmq.powerArmedAt = time.Time{}
	}
	switch b {
	case gaBtnUp:
		gmq.sel = (gmq.sel + len(rows) - 1) % len(rows)
		PlayNavSound()
	case gaBtnDown:
		gmq.sel = (gmq.sel + 1) % len(rows)
		PlayNavSound()
	case gaBtnLeft:
		gmqChange(config, r, -1)
	case gaBtnRight:
		gmqChange(config, r, 1)
	case gaBtnA:
		gmqActivate(config, r)
	case gaBtnB, gaBtnMenu:
		PlayBackSound()
		gmq.open = false
		if gmq.direct {
			gaClose("the player went back", true)
		}
	case gaBtnMenuChord:
		// Select + L2 again: straight back to the game.
		PlayBackSound()
		gmq.open = false
		gaClose("the player went back", true)
	}
}

// gmqChange is Left (dir -1) or Right on a row.
func gmqChange(config *Config, r gmqRow, dir int) {
	switch r {
	case gmqBrightness:
		if !brightnessControlAvailable() {
			PlayErrorSound()
			return
		}
		if before, after := brightnessStepBy(config, dir*max(1, brightnessGrid())); before == after {
			PlayErrorSound()
			return
		}
		PlayNavSound()
	case gmqVolume:
		if before, after := volumeStepBy(config, dir*volumeStep); before == after {
			PlayErrorSound()
			return
		}
		PlayNavSound()
	case gmqMute:
		gmqSetMute(config, dir > 0)
	case gmqMusic:
		gmqMusicStepBy(config, dir)
	case gmqWifi:
		gmqSetWifi(dir > 0)
	case gmqBluetooth:
		gmqSetBluetooth(config, dir > 0)
	case gmqLights:
		gmqSetLights(config, dir > 0)
	case gmqRumble:
		gmqRumbleStep(config, dir, false)
	case gmqVibration:
		gmqVibrationStep(config, dir, false)
	}
}

// gmqActivate is A on a row.
func gmqActivate(config *Config, r gmqRow) {
	switch r {
	case gmqBrightness:
		if !brightnessControlAvailable() {
			PlayErrorSound()
			return
		}
		brightnessToggleDim(config)
		PlayToggleSound()
	case gmqVolume, gmqMute:
		gmqSetMute(config, gmqVolumeNow() > 0)
	case gmqMusic:
		PlayNavSound()
	case gmqWifi:
		if i := gmqToggleIndex(qsActionWifi); i >= 0 {
			gmqSetWifi(!qs.toggles[i].Value)
		}
	case gmqBluetooth:
		if i := gmqToggleIndex(qsActionBluetooth); i >= 0 {
			gmqSetBluetooth(config, !qs.toggles[i].Value)
		}
	case gmqLights:
		gmqSetLights(config, !ledGamesLit(config))
	case gmqRumble:
		gmqRumbleStep(config, 1, true)
	case gmqVibration:
		gmqVibrationStep(config, 1, true)
	case gmqPower:
		if gmqPowerArmed() || !powerOffConfirmWanted(config) {
			gmqPowerOff()
			return
		}
		gmq.powerArmedAt = time.Now()
		PlayNavSound()
	}
}

// gmqSetMute mutes (on) or brings the last audible level back.
func gmqSetMute(config *Config, on bool) {
	if !volumeControlAvailable() {
		PlayErrorSound()
		return
	}
	if (gmqVolumeNow() == 0) == on {
		return
	}
	volumeToggleMute(config)
	PlayToggleSound()
}

// gmqMusicStepBy moves "Music volume in games" and rescales the music playing
// under this game now, so the step is heard at once. The mix's gain is the
// level the launch read times the output's own (musicGamePlanBuild), so the
// ratio of the two levels carries it over.
func gmqMusicStepBy(config *Config, dir int) {
	cur := musicGameLevel(config)
	pct := min(100, max(10, cur+dir*gmqMusicStep))
	if pct == cur {
		PlayErrorSound()
		return
	}
	old := musicGameSettingsNow().level
	musicGameSetLevel(config, pct)
	if old > 0 {
		outAudio.mu.Lock()
		if outAudio.gameMix != "" {
			outAudio.gameGain = outAudio.gameGain * float64(pct) / float64(old)
		}
		outAudio.mu.Unlock()
	}
	gmqSave(config)
	PlayNavSound()
}

// gmqSetWifi is Quick Settings' Wi-Fi switch, set rather than flipped.
func gmqSetWifi(on bool) {
	i := gmqToggleIndex(qsActionWifi)
	if i < 0 {
		PlayErrorSound()
		return
	}
	if qs.toggles[i].Value == on {
		return
	}
	// The command runs on a worker; the row says "Switching…" until it is
	// done (qsRadioBusy), and the probe it ends with shows the truth.
	qsToggleRow(i)
}

// gmqSetBluetooth is Quick Settings' Bluetooth switch, set rather than
// flipped. It uses the switch's own workers (qsApplyToggle's Bluetooth case)
// with a done of its own, so the row knows when the radio has answered.
func gmqSetBluetooth(config *Config, on bool) {
	i := gmqToggleIndex(qsActionBluetooth)
	if i < 0 {
		PlayErrorSound()
		return
	}
	if qs.toggles[i].Value == on && !gmqBTBusy() {
		return
	}
	qs.toggles[i].Value = on
	gmq.btSeq++
	seq := gmq.btSeq
	gmq.btBusy, gmq.btBusyAt = true, time.Now()
	done := func(err error) {
		if gmq.btSeq == seq {
			gmq.btBusy = false
		}
		if on && btBringUpFailed(err) {
			qsSetToggleValue(qsActionBluetooth, false)
		}
		qsNoteEdit()
		qsStartProbe()
	}
	if on {
		btBringUpAsync(done)
	} else {
		btPowerOffAsync(done)
	}
	qsNoteEdit()
	PlayToggleSound()
	qsPersist(config)
}

// gmqBTBusy reports a Bluetooth switch still running.
func gmqBTBusy() bool {
	return gmq.btBusy && time.Since(gmq.btBusyAt) < gmqRadioBusyMax
}

// gmqSetLights is "Lights in games" (power_options.go), where JukaHub drives
// the stick lights.
func gmqSetLights(config *Config, on bool) {
	if !ledSettingsOn(config) {
		PlayErrorSound()
		return
	}
	if ledGamesLit(config) == on {
		return
	}
	setStoreBool(config, ledGamesKey, on)
	powerOptLEDPublish(config)
	gmqSave(config)
	PlayToggleSound()
}

// gmqRumbleStep moves RetroArch's rumble strength one stop; wrap: A's step,
// which goes round.
func gmqRumbleStep(config *Config, dir int, wrap bool) {
	opts := raTuneRumbleOptions(config)
	if len(opts) == 0 {
		return
	}
	i := raTuneOptionIndex(opts, raTuneRumbleSetting(config))
	if i < 0 {
		i = 0
	}
	next := i + dir
	switch {
	case wrap:
		next = (next + len(opts)) % len(opts)
	case next < 0 || next >= len(opts):
		PlayErrorSound()
		return
	}
	setStoreString(config, raTuneRumbleKey, opts[next].Value)
	gmqSave(config)
	PlayNavSound()
}

// gmqVibrationLabels are the Vibration row's stops: Off, then haptics.go's
// strengths.
func gmqVibrationLabels() []string {
	out := []string{"Off"}
	for _, l := range hapticLevels {
		out = append(out, l.label)
	}
	return out
}

// gmqVibrationIndex is the stop in force: 0 off, else 1 + the strength.
func gmqVibrationIndex(config *Config) int {
	if !hapticsConfiguredOn(config) {
		return 0
	}
	return 1 + hapticsLevelIndex(hapticsLevelNow().key)
}

// gmqVibrationStep moves JukaHub's vibration one stop, the way Settings →
// Sound & haptics' two rows do (the switch, then the strength).
func gmqVibrationStep(config *Config, dir int, wrap bool) {
	n := len(hapticLevels) + 1
	next := gmqVibrationIndex(config) + dir
	switch {
	case wrap:
		next = (next + n) % n
	case next < 0 || next >= n:
		PlayErrorSound()
		return
	}
	if next == 0 {
		setStoreBool(config, hapticsVariable, false)
		hapticsSetEnabled(false)
		gmqSave(config)
		PlayNavSound()
		return
	}
	if !hapticsConfiguredOn(config) {
		setStoreBool(config, hapticsVariable, true)
		hapticsSetEnabled(true)
	}
	hapticsSetLevel(config, next-1)
	gmqSave(config)
	PlayNavSound()
	// Felt at the new strength, as Settings does.
	playHaptic(hapticActivatePulse)
}

// gmqSave writes the settings the card changed (the levels save themselves).
func gmqSave(config *Config) {
	if config == nil {
		return
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// gmqPowerArmed reports a first A of Power off still waiting for the second.
func gmqPowerArmed() bool {
	return !gmq.powerArmedAt.IsZero() && time.Since(gmq.powerArmedAt) < gmqPowerArm
}

// gmqPowerOff gives the game back — a stopped RetroArch reads no commands, and
// a stopped emulator cannot save — and runs the power card's Power off, which
// saves first (devPowerRequest → quickPowerOffSavesFirst). No overlay opens
// over the game while it saves (gmQuitting); the flag is let go after a while
// in case the power off never comes.
func gmqPowerOff() {
	logInfo("gamemenu", "quick settings: power off (the game is saved first where it can be)")
	PlayActivateSound()
	gmq.open, gmq.powerArmedAt = false, time.Time{}
	gmQuitting.Store(true)
	gaClose("the player powered off", true)
	time.AfterFunc(90*time.Second, func() { gmQuitting.Store(false) })
	devPowerRequest(devPowerPoweroff)
}

// ── The system line ──────────────────────────────────────────────────────

// gmqMonitorTick starts a reading when one is due. Main thread, every frame
// the card is drawn; the reading runs on its own goroutine.
func gmqMonitorTick(now time.Time) {
	gmqMon.mu.Lock()
	due := !gmqMon.busy && (gmqMon.at.IsZero() || now.Sub(gmqMon.at) >= gmqSampleEvery)
	if due {
		gmqMon.busy, gmqMon.at = true, now
	}
	gen, prev, havePrev := gmqMon.gen, gmqMon.prev, gmqMon.havePrev
	gmqMon.mu.Unlock()
	if due {
		go gmqMonitorRead(gen, prev, havePrev)
	}
}

// gmqMonitorRead takes one reading. Its own goroutine: the same cheap reads
// as the Hardware screen (systool_hw.go) and the battery's cached values.
func gmqMonitorRead(gen int, prev sysToolCPUTimes, havePrev bool) {
	var s gmqSample
	var cur sysToolCPUTimes
	curOK := false
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "reading the system for quick settings failed: %v", r)
		}
		gmqMon.mu.Lock()
		gmqMon.busy = false
		if gmqMon.gen == gen {
			if curOK {
				gmqMon.prev, gmqMon.havePrev = cur, true
			}
			if !s.cpuOK && gmqMon.have {
				// The first reading of a pair has no CPU figure: keep the last.
				s.cpuPct, s.cpuOK = gmqMon.s.cpuPct, gmqMon.s.cpuOK
			}
			gmqMon.s, gmqMon.have = s, true
		}
		gmqMon.mu.Unlock()
	}()
	if runtime.GOOS == "linux" {
		if c, ok := sysToolReadCPUTimes(); ok {
			cur, curOK = c, true
			if havePrev {
				s.cpuPct, s.cpuOK = sysToolCPUPercent(prev, c), true
			}
		}
	} else {
		s.cpuPct, s.cpuOK = sysToolGopsutilCPU()
	}
	s.freqMHz = sysToolCPUFreqMHz()
	s.tempC, s.tempOK = cpuTempValueC()
	s.memUsed, s.memTotal, s.memOK = sysToolMemory()
	s.battery = getBatteryPercent()
	if s.battery >= 0 {
		s.charging = getBatteryCharging()
		s.eta = batteryETAShort()
	}
}

// gmqMonitorLines is the system line in two parts: the processor and memory,
// then the battery.
func gmqMonitorLines() (string, string) {
	gmqMon.mu.Lock()
	s, have := gmqMon.s, gmqMon.have
	gmqMon.mu.Unlock()
	if !have {
		return "Reading the system…", ""
	}
	var a []string
	if s.cpuOK {
		a = append(a, fmt.Sprintf("CPU %d%%", int(s.cpuPct+0.5)))
	} else {
		a = append(a, "CPU …")
	}
	switch {
	case s.freqMHz >= 1000:
		a = append(a, fmt.Sprintf("%.1f GHz", float64(s.freqMHz)/1000))
	case s.freqMHz > 0:
		a = append(a, fmt.Sprintf("%d MHz", s.freqMHz))
	}
	if s.tempOK {
		a = append(a, fmt.Sprintf("%d °C", int(s.tempC+0.5)))
	}
	if s.memOK && s.memTotal > 0 {
		a = append(a, fmt.Sprintf("RAM %d / %d MB", s.memUsed>>20, s.memTotal>>20))
	}
	battery := ""
	if s.battery >= 0 {
		battery = fmt.Sprintf("Battery %d%%", s.battery)
		switch {
		case s.eta != "":
			battery += " · " + s.eta
		case s.charging:
			battery += " · charging"
		}
	}
	return strings.Join(a, " · "), battery
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gmQuickDraw is the card: the game, the system line, the rows with their
// values (a bar for the levels), what the highlighted row does, and the
// buttons.
func gmQuickDraw(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	// Quick Settings' radio probe lands here while the card is up (its own
	// panel drains it only while it is drawn).
	if len(qs.toggles) >= 3 && len(qs.sliders) >= 1 {
		qsDrainProbe()
	}
	gmqMonitorTick(time.Now())
	rows := gmqRows(config)
	if len(rows) == 0 {
		return
	}
	if gmq.sel >= len(rows) {
		gmq.sel = len(rows) - 1
	}
	pad := orbPad
	w := screenWidth * 50 / 100
	if minW := scaleX(480, screenWidth); w < minW {
		w = minW
	}
	if w > screenWidth-4*pad {
		w = screenWidth - 4*pad
	}
	inner := w - 2*pad
	rowH := fonts.bodyH + pad
	rowGap := int32(2)
	per := rowH + rowGap
	mon1, mon2 := gmqMonitorLines()
	title := gaOverlay.title
	if title == "" {
		title = "Your game"
	}
	help := gmqHelp(config, rows[gmq.sel])
	hint := "D-pad move · Left/Right change · " + orbKeyName("A") + " select · " + orbKeyName("B") + " back"

	head := pad + fonts.tinyH + 2 + fonts.bodyH + 2 + fonts.smallH + pad/2
	if mon2 != "" {
		head += 2 + fonts.smallH
	}
	foot := pad/2 + fonts.smallH + 4 + fonts.smallH + pad
	visible := len(rows)
	if maxH := screenHeight - 2*pad; head+foot+int32(visible)*per > maxH {
		visible = max(1, int((maxH-head-foot)/per))
	}
	if gmq.sel < gmq.first {
		gmq.first = gmq.sel
	}
	if gmq.sel >= gmq.first+visible {
		gmq.first = gmq.sel - visible + 1
	}
	gmq.first = max(0, min(gmq.first, len(rows)-visible))
	h := head + int32(visible)*per + foot

	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 240
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())

	accent := glassAccent()
	x, y := card.X+pad, card.Y+pad
	drawText(renderer, fonts.tiny, "QUICK SETTINGS", x, y, accent, textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, ellipsize(fonts.body, title, inner), x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH + 2
	drawText(renderer, fonts.small, ellipsize(fonts.small, mon1, inner), x, y, ColorTextSecondary(), textAlignLeft)
	y += fonts.smallH
	if mon2 != "" {
		drawText(renderer, fonts.small, ellipsize(fonts.small, mon2, inner), x, y+2, ColorTextSecondary(), textAlignLeft)
		y += 2 + fonts.smallH
	}
	y += pad / 2

	// The arrows are drawn, not typed: the bundled fonts may lack ▲ and ▼.
	arrowR := max32(4, fonts.smallH/4)
	arrowX := card.X + card.W - pad/4 - arrowR
	if gmq.first > 0 {
		ay := y + arrowR
		fillTriangleFilled(renderer, pt{arrowX - arrowR, ay + arrowR/2}, pt{arrowX + arrowR, ay + arrowR/2}, pt{arrowX, ay - arrowR}, ColorTextSecondary())
	}
	// The read-outs' room at the right: a level's percentage (or "Muted"),
	// and the longest words a switch or a choice shows.
	pctW, _ := measureText(fonts.body, "100%")
	if mw, _ := measureText(fonts.body, "Muted"); mw > pctW {
		pctW = mw
	}
	valueMaxW, _ := measureText(fonts.body, "RetroArch's own")
	for i := gmq.first; i < gmq.first+visible && i < len(rows); i++ {
		r := rows[i]
		row := sdl.Rect{X: card.X + pad/2, Y: y, W: card.W - pad, H: rowH}
		if i == gmq.sel {
			f := accent
			f.A = 56
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, f)
			strokeRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, 2, accent)
		}
		value, bar, on, dim := gmqValue(config, r)
		labelCol, valueCol := ColorTextPrimary(), ColorTextSecondary()
		if dim {
			labelCol = ColorTextSecondary()
		} else if on && bar < 0 {
			valueCol = accent
		}
		ty := row.Y + (rowH-fonts.bodyH)/2
		right := row.X + row.W - pad
		valueW := min(valueMaxW, (row.W-2*pad)/2)
		labelW := row.W - 2*pad - valueW - pad/2
		if bar >= 0 {
			// The level: a bar, its percentage at the right.
			barW := max32(scaleX(90, screenWidth), (row.W-2*pad)*36/100)
			barH := max32(6, fonts.smallH/3)
			bx := right - pctW - pad/2 - barW
			by := row.Y + (rowH-barH)/2
			track := ColorTextSecondary()
			track.A = 60
			fillRoundedRect(renderer, bx, by, barW, barH, barH/2, track)
			if fw := barW * int32(min(100, max(0, bar))) / 100; fw > 0 {
				fillRoundedRect(renderer, bx, by, max32(fw, barH), barH, barH/2, accent)
			}
			labelW = bx - pad/2 - (row.X + pad)
			drawText(renderer, fonts.body, value, right, ty, ColorTextPrimary(), textAlignRight)
		} else if value != "" {
			drawText(renderer, fonts.body, ellipsize(fonts.body, value, valueW), right, ty, valueCol, textAlignRight)
		}
		drawText(renderer, fonts.body, ellipsize(fonts.body, gmqLabel(r), max32(labelW, fonts.bodyH)), row.X+pad, ty, labelCol, textAlignLeft)
		y += per
	}
	if gmq.first+visible < len(rows) {
		ay := y - rowGap - arrowR
		fillTriangleFilled(renderer, pt{arrowX - arrowR, ay - arrowR/2}, pt{arrowX + arrowR, ay - arrowR/2}, pt{arrowX, ay + arrowR}, ColorTextSecondary())
	}
	y += pad / 2
	drawText(renderer, fonts.small, ellipsize(fonts.small, help, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
	y += fonts.smallH + 4
	drawText(renderer, fonts.small, ellipsize(fonts.small, hint, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
}
