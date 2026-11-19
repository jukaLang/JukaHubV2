package main

// ledindicators.go — the stick lights as indicators (NextUI's LED indicators),
// and the layer that puts them, and the Screen colours mode, over the user's
// lights.
//
// Three indicators, each with its own switch in Settings → Stick lights and
// one shared Indicator brightness (the lights' own Brightness does not dim
// them: a warning dimmed to Off would be no warning):
//
//   - Low battery (on by default): at 10% or less and not charging, a slow red
//     pulse (a breath that never goes near dark), whatever the lights were
//     showing, until the charge is back at 13%.
//   - Charging: the Charging mode's slow green breath (steady green once
//     full) while plugged in, whatever mode is picked.
//   - Standby: a dim, slow breath in the theme's accent while the screen is
//     off — the screen-off sleep, the dock's dark screen, the idle low-power
//     state, and the moment before a kernel suspend (whatever the rings show
//     then is what they keep while the device sleeps).
//
// The order is that one too: low battery wins over charging, charging over
// standby. A change of indicator (or back to none) waits until it has been due
// for ledIndSettle, and the charger reading for ledChargeSettle, so a reading
// that wobbles never switches the rings back and forth; standby, the screen's
// own state, comes and goes at once. Juka's layer
// (ledjuka.go) still wins over all of them while the orb is up, because the
// user is looking at it.
//
// They are layers of the LED loop, not a second LED stack: ledLayersNow
// decides once a frame what is due, ledsync.go's loop asks it whether to keep
// the rings over a game (the loop otherwise hands them back to the firmware
// then), and ledLayersFrame paints the frame. An indicator only shows while
// JukaHub's loop runs, i.e. with Stick lights on; with them off the rings are
// the firmware's, which has its own charging light.
//
// The loop never reads the config (see ledLoopSettings in ledsync.go): the
// settings here are published from the main thread by syncLEDLoopSettings,
// like the rest.

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// The custom keys behind the Stick lights rows.
	ledKeyIndLowBattery = "led_ind_low_battery"
	ledKeyIndCharging   = "led_ind_charging"
	ledKeyIndStandby    = "led_ind_standby"
	ledKeyIndBrightness = "led_ind_brightness"

	// ledIndBrightnessDefault is the indicators' brightness until one is set.
	ledIndBrightnessDefault = 60
	// ledIndLowBatteryPct is the charge at and below which the warning shows.
	ledIndLowBatteryPct = 10
	// ledIndLowPeriodMs is one slow pulse of the low-battery warning.
	ledIndLowPeriodMs = 3000
	// ledIndStandbyPeriodMs is one breath of the standby glow.
	ledIndStandbyPeriodMs = 8000
	// ledIndStandbyLevel is the standby glow's peak, before the indicator
	// brightness: it is meant to be seen in a dark room, not across one.
	ledIndStandbyLevel = 0.35
	// ledIndLowClearPct is the charge at which a low-battery warning that
	// is showing goes away again.
	ledIndLowClearPct = 13
	// ledIndSettle is how long a new indicator (or none) must be due before
	// the rings change to it, and ledChargeSettle how long the charger must
	// read differently before the lights believe it. A charger that tops off
	// at full flips "charging" every few seconds; each flip switched the
	// rings between green and the user's colour (dark, in the Charging mode).
	ledIndSettle    = 2 * time.Second
	ledChargeSettle = 5 * time.Second
)

// ledSettled is a value that only changes once a new one has held for a
// while. Locked: a loop started right after a stop may overlap the old one.
type ledSettled[T comparable] struct {
	mu    sync.Mutex
	shown T
	set   bool
	cand  T
	since time.Time
}

// settleFor returns what to show for want at now: want once it has been
// wanted for hold. The first value shows at once.
func (s *ledSettled[T]) settleFor(want T, now time.Time, hold time.Duration) T {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set || want == s.shown {
		s.shown, s.set = want, true
		s.cand = want
		return s.shown
	}
	if want != s.cand || now.Before(s.since) {
		s.cand, s.since = want, now
	}
	if now.Sub(s.since) >= hold {
		s.shown = want
	}
	return s.shown
}

// current is what shows now.
func (s *ledSettled[T]) current() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shown
}

var (
	// ledIndShown is the indicator the rings show. LED loop.
	ledIndShown ledSettled[ledIndicator]
	// ledChargeShown is the settled charger reading. LED loop.
	ledChargeShown ledSettled[bool]
)

// ledChargingSteady is getBatteryCharging once it has held ledChargeSettle.
// LED loop.
func ledChargingSteady(now time.Time) bool {
	return ledChargeShown.settleFor(getBatteryCharging(), now, ledChargeSettle)
}

// ledIndicator is what the indicator layer shows.
type ledIndicator int

const (
	ledIndNone ledIndicator = iota
	ledIndLowBattery
	ledIndCharging
	ledIndStandby
)

// ledExtrasSettings is the part of the config the layers need, published from
// the main thread (ledExtrasPublish) like ledLoopSettings.
type ledExtrasSettings struct {
	lowBattery    bool
	charging      bool
	standby       bool
	indBrightness int
	// screenGames and screenOff: Screen colours over games, and the consoles
	// that keep the firmware's lights (lower case) (ledscreen.go).
	screenGames bool
	screenOff   []string
	// accent is the theme's accent: the standby glow's colour and the
	// Screen colours stand-in where no picture can be read.
	accent [3]uint8
}

var ledExtrasState struct {
	sync.Mutex
	s   ledExtrasSettings
	set bool
}

// ledExtrasFrom reads the layers' settings out of config. Main thread.
func ledExtrasFrom(config *Config) ledExtrasSettings {
	x := ledExtrasSettings{
		lowBattery:    true,
		indBrightness: ledIndBrightnessDefault,
		screenGames:   true,
		accent:        [3]uint8{accentColor.R, accentColor.G, accentColor.B},
	}
	if config == nil {
		return x
	}
	m := config.Variables.Custom
	x.lowBattery = customBool(m, ledKeyIndLowBattery, true)
	x.charging = customBool(m, ledKeyIndCharging, false)
	x.standby = customBool(m, ledKeyIndStandby, false)
	x.indBrightness = ledIndBrightnessFrom(config)
	x.screenGames = customBool(m, ledKeyScreenGames, true)
	x.screenOff = ledScreenOffSystems(config)
	return x
}

// ledExtrasPublish hands the layers' settings to the loop. Main thread; called
// by syncLEDLoopSettings.
func ledExtrasPublish(config *Config) {
	x := ledExtrasFrom(config)
	ledExtrasState.Lock()
	ledExtrasState.s, ledExtrasState.set = x, true
	ledExtrasState.Unlock()
}

// currentLEDExtras is the loop's read of the published copy; before the first
// publish it is the defaults.
func currentLEDExtras() ledExtrasSettings {
	ledExtrasState.Lock()
	defer ledExtrasState.Unlock()
	if !ledExtrasState.set {
		return ledExtrasSettings{lowBattery: true, indBrightness: ledIndBrightnessDefault, screenGames: true}
	}
	return ledExtrasState.s
}

// ledIndBrightnessFrom is the stored indicator brightness, 10..100.
func ledIndBrightnessFrom(c *Config) int {
	if c == nil {
		return ledIndBrightnessDefault
	}
	raw, ok := customString(c.Variables.Custom, ledKeyIndBrightness)
	if !ok {
		return ledIndBrightnessDefault
	}
	pct, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return ledIndBrightnessDefault
	}
	if pct < ledBrightnessMin {
		pct = ledBrightnessMin
	}
	if pct > ledBrightnessMax {
		pct = ledBrightnessMax
	}
	return pct
}

// ledStandbyNow reports whether the screen is off while JukaHub runs: the
// screen-off sleep, a sleep about to suspend the kernel, the dock's dark
// screen, or the idle low-power state. Any goroutine.
func ledStandbyNow() bool {
	return powerSleepActive() || powerSleep.screenOff.Load() || saverLowPowerEngaged() || ambientIsScreenOff()
}

// ledIndicatorFor picks the indicator due under x for a battery reading. low
// is whether the low-battery warning shows now: once on it stays until the
// charge is ledIndLowClearPct, so a reading that wobbles between 10 and 11%
// does not switch the rings between red and the user's colour. Pure.
func ledIndicatorFor(x ledExtrasSettings, pct int, charging, standby, low bool) ledIndicator {
	lowLimit := ledIndLowBatteryPct
	if low {
		lowLimit = ledIndLowClearPct - 1
	}
	switch {
	case x.lowBattery && !charging && pct >= 0 && pct <= lowLimit:
		return ledIndLowBattery
	case x.charging && charging:
		return ledIndCharging
	case x.standby && standby:
		return ledIndStandby
	}
	return ledIndNone
}

// ledLayers is what the layers want for one frame.
type ledLayers struct {
	x   ledExtrasSettings
	ind ledIndicator
	// screen: the Screen colours mode draws over the game on screen.
	screen bool
}

// holds reports whether the layers keep the rings while a game owns the
// screen, instead of handing them back to the firmware.
func (l ledLayers) holds() bool { return l.ind != ledIndNone || l.screen }

// ledLayersNow decides the layers for one frame. With the loop running for
// Juka alone (the user's lights are off) there are none. LED loop.
func ledLayersNow(s ledLoopSettings, now time.Time, overGame bool) ledLayers {
	if s.policy != ledPolicyScreen || s.brightness <= 0 {
		// Another mode, or Off: the framebuffer is not read, so not held open
		// either.
		ledScreenRelease()
	}
	if ledJukaOnly.Load() {
		return ledLayers{}
	}
	x := currentLEDExtras()
	l := ledLayers{x: x}
	standby := ledStandbyNow()
	want := ledIndNone
	if x.lowBattery || x.charging || x.standby {
		// Both readings are cached for a second (extras.go, widgets.go); the
		// charger's is settled on top (ledChargingSteady).
		want = ledIndicatorFor(x, getBatteryPercent(), ledChargingSteady(now), standby, ledIndShown.current() == ledIndLowBattery)
	}
	hold := ledIndSettle
	if want == ledIndStandby || ledIndShown.current() == ledIndStandby {
		// The screen going off or on is the device's own state, not a
		// reading that wobbles: the glow follows it at once, so it is up
		// before a kernel suspend freezes the rings, and the idle low-power
		// state (a background pause) does not hand them to the firmware for
		// the settle's two seconds first.
		hold = 0
	}
	l.ind = ledIndShown.settleFor(want, now, hold)
	if overGame && s.policy == ledPolicyScreen && s.brightness > 0 && !standby {
		l.screen = ledScreenOverGame(x, now)
	}
	return l
}

// ledLayersFrame is one frame of the lights: the user's mode with Juka's layer
// over it (ledJukaFrame), lit over a game when Screen colours draws there, and
// an indicator over both unless Juka is showing. LED loop.
func ledLayersFrame(s ledLoopSettings, l ledLayers, now time.Time, overGame bool) (uint8, uint8, uint8) {
	r, g, b := ledJukaFrame(s, now, overGame && !l.screen)
	if l.ind == ledIndNone || ledJukaHolds(now) {
		return r, g, b
	}
	return ledIndicatorColor(l.ind, l.x, now)
}

// ledIndBreath is a raised cosine over periodMs, eased in perceived brightness
// from floor to 1 (like ledSmooth). Returns the LED level. Pure.
func ledIndBreath(now time.Time, periodMs, floor float64) float64 {
	phase := math.Mod(float64(now.UnixMilli()), periodMs) / periodMs
	if phase < 0 {
		phase++
	}
	ease := (1 - math.Cos(2*math.Pi*phase)) / 2
	return math.Pow(floor+(1-floor)*ease, ledSmoothGamma)
}

// ledIndicatorColor is an indicator's colour at the indicator brightness. Only
// the level moves within an indicator, never the hue, so the TrimUI rings do
// not re-arm (blink) while one shows. LED loop.
func ledIndicatorColor(ind ledIndicator, x ledExtrasSettings, now time.Time) (uint8, uint8, uint8) {
	var r, g, b uint8
	switch ind {
	case ledIndLowBattery:
		level := ledIndBreath(now, ledIndLowPeriodMs, 0.45)
		r = uint8(math.Round(255 * level))
	case ledIndCharging:
		if getBatteryPercent() >= 100 {
			r, g, b = hsvToRGB(0.33, 1, 1)
		} else {
			r, g, b = hsvToRGB(0.33, 1, ledSmooth(now))
		}
	case ledIndStandby:
		level := ledIndStandbyLevel * ledIndBreath(now, ledIndStandbyPeriodMs, 0.6)
		a := x.accent
		if a[0] == 0 && a[1] == 0 && a[2] == 0 {
			a = [3]uint8{0, 128, 255}
		}
		hue, _ := ledAnimSplit(a[0], a[1], a[2])
		r = uint8(math.Round(float64(hue[0]) * level))
		g = uint8(math.Round(float64(hue[1]) * level))
		b = uint8(math.Round(float64(hue[2]) * level))
	default:
		return 0, 0, 0
	}
	r, g, b = ledApplyBrightness(r, g, b, x.indBrightness)
	// Keep a dim indicator's hue: under a peak of about 8 the rounding changes
	// it, and the rings would re-arm (ledJukaMinPeak in ledjuka.go).
	return ledJukaDarkFloor(r, g, b)
}

// ── Settings → Stick lights ────────────────────────────────────────────────

// ledIndToggleRow is one indicator's switch. Shown while the lights are on: the
// indicators are a layer of JukaHub's lights.
func ledIndToggleRow(id, label, detail, key string, def bool) setRow {
	return setRow{
		ID: id, Label: label, Kind: setKindToggle,
		Detail:  setStatic(detail),
		Visible: ledSettingsOn,
		Get: func(c *Config) bool {
			return c != nil && customBool(c.Variables.Custom, key, def)
		},
		Set: func(c *Config, on bool) {
			if c == nil {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[key] = boolString(on)
			syncLEDLoopSettings(c)
			ledPanelPersist(c)
		},
	}
}

func ledIndLowBatteryRow() setRow {
	return ledIndToggleRow("ledIndLowBattery", "Low battery warning",
		"A slow red pulse at 10% or less, whatever the lights show", ledKeyIndLowBattery, true)
}

func ledIndChargingRow() setRow {
	return ledIndToggleRow("ledIndCharging", "Charging light",
		"Breathe green while plugged in, whatever the lights show", ledKeyIndCharging, false)
}

func ledIndStandbyRow() setRow {
	return ledIndToggleRow("ledIndStandby", "Standby glow",
		"A dim, slow glow while the screen is off or asleep", ledKeyIndStandby, false)
}

func ledIndBrightnessOptions(*Config) []setOption { return setLevelOptions(ledBrightnessMin) }

// ledIndBrightnessRow is the indicators' own brightness.
func ledIndBrightnessRow() setRow {
	return setRow{
		ID: "ledIndBrightness", Label: "Indicator brightness", Kind: setKindChoice, Clamp: true,
		Detail: setStatic("For the warnings above; the lights' Brightness does not dim them"),
		Visible: func(c *Config) bool {
			if !ledSettingsOn(c) {
				return false
			}
			m := c.Variables.Custom
			return customBool(m, ledKeyIndLowBattery, true) || customBool(m, ledKeyIndCharging, false) ||
				customBool(m, ledKeyIndStandby, false)
		},
		Options: ledIndBrightnessOptions,
		Current: func(c *Config) int {
			return setNearestStop(ledIndBrightnessOptions(c), ledIndBrightnessFrom(c))
		},
		Choose: func(c *Config, value string) {
			pct, err := strconv.Atoi(value)
			if c == nil || err != nil {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[ledKeyIndBrightness] = pct
			syncLEDLoopSettings(c)
			ledPanelPersist(c)
		},
	}
}
