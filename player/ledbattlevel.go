package main

// ledbattlevel.go — the battery level on the stick lights.
//
// L2 + R2 pressed together on Home (never in a game) flashes the charge on
// the lights for ledBattFlashLen: as much of the lights lit as the charge
// fills, green from half up, amber below that and red at the lights'
// low-battery level (Settings → Power → Low battery warning at, batterywarn.go).
// The same flash is the lights' half of an in-game battery warning
// (batteryWarnTick). And with Settings → Stick lights → "Charging shows a
// filling bar", the Charging light shows the charge as a green bar by
// quarters, breathing while it charges and steady once full, instead of the
// breath alone.
//
// How much of the lights can be lit apart is the hardware's. The LED stack
// paints one colour on every light except the TrimUI driver's (ledanim.go,
// ledAnimFill): LED by LED when the rings are driven through frame_hex, else
// zone by zone in the driver's order — the Brick's four (shoulders, FN keys,
// top bar), the Smart Pro's two (stick rings, logo). With one zone (or the
// kernel's LED class) the colour alone tells the band.
//
// The chord: on Home each trigger has a job of its own (L2 Edit Home, R2 the
// liquid background, shoulders.go), so while the chord is possible a trigger
// press on Home waits battChordWindow for the other one (battChordTrigger,
// controller.go). A lone press then goes its usual way (battChordTick); two
// make the chord. Only Home is delayed, only on a device with lights and only
// with the switch on.
//
// The layer is one of ledindicators.go's (ledLayers.level): it wins over the
// indicators, Juka's layer wins over it. With the lights off, a flash starts
// JukaHub's LED loop for itself like Juka's layer does (ledJukaOnly: the
// user's mode stays dark) and stops it again once the flash is over
// (ledBattLevelTick). The loop never reads the config: the flash is an atomic
// deadline on the monotonic clock, the rest rides on ledExtrasSettings.

import (
	"sync/atomic"
	"time"
)

const (
	// ledKeyBattChord is the switch for the L2 + R2 flash (on by default);
	// ledKeyIndChargeBar the Charging light's bar (off: the breath).
	ledKeyBattChord    = "led_battery_chord"
	ledKeyIndChargeBar = "led_ind_charging_bar"

	// ledBattFlashLen is how long a flash shows.
	ledBattFlashLen = 2 * time.Second
	// ledBattReleaseAfter is how long a loop started for a flash keeps running
	// after it: the rings' dip back has to finish first.
	ledBattReleaseAfter = 700 * time.Millisecond
	// battChordWindow is how long a trigger press on Home waits for the other
	// trigger. Two thumbs pressing "together" land well inside it.
	battChordWindow = 180 * time.Millisecond
)

// ledBattFlashUntil is when the flash ends, on powerMonoNow's clock; 0 none.
// Any goroutine.
var ledBattFlashUntil atomic.Int64

// ledBattForced: a flash started the LED loop (the lights were off), and
// ledBattLevelTick stops it. Main thread.
var ledBattForced bool

// ledBattShow is what the level layer shows for one frame.
type ledBattShow struct {
	on  bool
	pct int
}

// ledBattLevelNow is the flash, when one is up. LED loop.
func ledBattLevelNow() ledBattShow {
	if ledBattFlashUntil.Load() <= int64(powerMonoNow()) {
		return ledBattShow{}
	}
	pct := getBatteryPercent()
	if pct < 0 {
		return ledBattShow{}
	}
	return ledBattShow{on: true, pct: pct}
}

// ledBattFillMilli is the share of the lights a charge lights, in
// thousandths, at least one light's worth. Pure.
func ledBattFillMilli(pct int) int32 {
	pct = min(max(pct, 1), 100)
	return int32(pct * 10)
}

// ledBattBarMilli is the charging bar's share: whole quarters, rounded up so
// some light always shows, all four once full. Pure.
func ledBattBarMilli(pct int) int32 {
	q := (min(max(pct, 1), 100) + 24) / 25
	return int32(q * 250)
}

// ledBattHue is the flash's colour for a charge: red at or below low, amber
// under half, green from half up. Pure.
func ledBattHue(pct, low int) [3]uint8 {
	if low <= 0 {
		low = ledIndLowBatteryPct
	}
	switch {
	case pct <= low:
		return [3]uint8{255, 0, 0}
	case pct < 50:
		return [3]uint8{255, 140, 0}
	}
	return [3]uint8{0, 255, 0}
}

// ledBattLevelColor is the flash's colour at the indicator brightness, and the
// share of the lights it lights. LED loop.
func ledBattLevelColor(show ledBattShow, x ledExtrasSettings) (uint8, uint8, uint8, int32) {
	hue := ledBattHue(show.pct, x.lowPct)
	r, g, b := ledApplyBrightness(hue[0], hue[1], hue[2], x.indBrightness)
	r, g, b = ledJukaDarkFloor(r, g, b)
	return r, g, b, ledBattFillMilli(show.pct)
}

// ledBattBarColor is the charging bar: green, breathing gently (only the level
// moves, so the rings never re-arm) until full, then steady. LED loop.
func ledBattBarColor(x ledExtrasSettings, now time.Time) (uint8, uint8, uint8, int32) {
	pct := getBatteryPercent()
	level := 1.0
	if pct < 100 {
		level = ledSmooth(now)
	}
	r, g, b := hsvToRGB(0.33, 1, level)
	r, g, b = ledApplyBrightness(r, g, b, x.indBrightness)
	r, g, b = ledJukaDarkFloor(r, g, b)
	return r, g, b, ledBattBarMilli(pct)
}

// ledBattLevelFlash shows the charge on the lights for d, starting the LED
// loop for it when the lights are off. Main thread.
func ledBattLevelFlash(config *Config, d time.Duration) {
	if getBatteryPercent() < 0 || !ledSettingsAvailable(config) {
		return
	}
	ledBattFlashUntil.Store(int64(powerMonoNow() + d))
	if LEDSyncRunning() {
		return
	}
	userOn := config != nil && config.Variables.LEDSync
	ledJukaOnly.Store(!userOn)
	if err := SetLEDSync(true); err != nil {
		ledJukaOnly.Store(false)
		logDebug("led", "battery level: the lights would not start: %v", err)
		return
	}
	ledBattForced = !userOn
}

// ledBattLevelTick stops a loop a flash started, once the flash is over and
// the rings have dipped back. Main thread, every frame (the parked game loop
// too, hwKeysTick).
func ledBattLevelTick(config *Config) {
	if !ledBattForced {
		return
	}
	if time.Duration(ledBattFlashUntil.Load())+ledBattReleaseAfter > powerMonoNow() {
		return
	}
	ledBattForced = false
	userOn := config != nil && config.Variables.LEDSync
	switch {
	case userOn || !LEDSyncRunning():
		// The lights are the user's now, or something else stopped them.
		if !ledJukaState.forced {
			ledJukaOnly.Store(false)
		}
	case ledJukaState.phase != ledJukaIdle:
		// Juka is on the lights: its tick stops the loop when it is done.
		ledJukaState.forced = true
	case ledPanel.open || ledPanel.forcedSync:
		// The style panel opened during the flash and previews on this loop
		// (it found it running, so it would never stop it): the loop is the
		// panel's now, and closing the panel stops it (closeLEDPanel).
		ledPanel.forcedSync = true
		ledJukaOnly.Store(false)
	default:
		_ = SetLEDSync(false)
		ledJukaOnly.Store(false)
	}
}

// ── The chord ──────────────────────────────────────────────────────────────

// battChord is a trigger press on Home waiting for the other one. Main thread.
var battChord struct {
	pending bool
	side    padShoulder
	at      time.Duration // powerMonoNow
}

// ledBattChordOn reads the switch. Main thread.
func ledBattChordOn(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, ledKeyBattChord, true)
}

// battChordArmed reports whether a trigger press here may be the chord's
// first half: Home itself has the triggers, the device has lights and the
// switch is on.
func battChordArmed(config *Config) bool {
	return battChordFree() && ledBattChordOn(config) &&
		ledSettingsAvailable(config) && getBatteryPercent() >= 0
}

// battChordFree reports whether Home is what the triggers reach now: nothing
// above it that would take them for itself (handleShoulderPress's list in
// shoulders.go, and a playing video's skip), so a trigger anywhere else is
// neither delayed nor taken.
func battChordFree() bool {
	return homeLayoutActive && !homeEditActive() && !homeTabsEdit.open && !modalPanelBlocksShoulders() &&
		!tb.startOpen && !nc.open && !qs.open && !clipSearchOpen && !globalSearchOpen && !wpOpen &&
		!shortcutsOpen && !fileEditor.open && !virtualKeyboardActive && !triggerOwnedByOverlay() &&
		snapshotVideoState().phase == "idle"
}

// battChordTrigger takes a trigger press (controller.go) when it may be half
// of L2 + R2 on Home. It reports whether the press was taken; a press it
// returns goes to handleShoulderPress as before. Main thread.
func battChordTrigger(s padShoulder, config *Config) bool {
	if s != padL2 && s != padR2 {
		return false
	}
	now := powerMonoNow()
	if battChord.pending {
		first, at := battChord.side, battChord.at
		battChord.pending = false
		if first != s && now-at <= battChordWindow {
			if battChordFree() {
				ledBattLevelFlash(config, ledBattFlashLen)
				PlayNavSound()
				logInfo("led", "battery level on the lights (L2 + R2, %d%%)", getBatteryPercent())
				return true
			}
			// Not Home's to take: both presses go their usual way, in order.
			handleShoulderPress(first, config)
			return false
		}
		// The same trigger twice, or too late: the first was a lone press.
		handleShoulderPress(first, config)
	}
	if !battChordArmed(config) {
		return false
	}
	battChord.pending, battChord.side, battChord.at = true, s, now
	return true
}

// battChordTick lets a lone trigger press go its usual way once the window
// has passed. overGame drops it instead: a game started meanwhile. Main
// thread, every frame.
func battChordTick(config *Config, overGame bool) {
	if !battChord.pending {
		return
	}
	if overGame {
		battChord.pending = false
		return
	}
	if powerMonoNow()-battChord.at <= battChordWindow {
		return
	}
	battChord.pending = false
	handleShoulderPress(battChord.side, config)
}

// ── Settings → Stick lights ────────────────────────────────────────────────

// ledBattChordRow is the flash's switch. Shown wherever there are lights: the
// flash starts them itself when they are off.
func ledBattChordRow() setRow {
	return setRow{
		ID: "ledBattChord", Label: "L2 + R2 shows the battery", Kind: setKindToggle,
		Detail: setStatic("On Home, press both triggers: the lights show the charge for 2 seconds"),
		Visible: func(c *Config) bool {
			return ledSettingsAvailable(c) && getBatteryPercent() >= 0
		},
		Get: ledBattChordOn,
		Set: func(c *Config, on bool) {
			if c == nil {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[ledKeyBattChord] = boolString(on)
			ledPanelPersist(c)
			if on {
				// Show what it looks like.
				ledBattLevelFlash(c, ledBattFlashLen)
			}
		},
	}
}

// ledIndChargeBarRow is the Charging light's style, under its switch.
func ledIndChargeBarRow() setRow {
	r := ledIndToggleRow("ledIndChargeBar", "Charging shows a filling bar",
		"The charge in green quarters instead of the breath; all lit when full", ledKeyIndChargeBar, false)
	r.Visible = func(c *Config) bool {
		return ledSettingsOn(c) && customBool(c.Variables.Custom, ledKeyIndCharging, false)
	}
	return r
}
