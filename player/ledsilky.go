package main

// ledsilky.go — the stick lights' extras: a critical battery warning, lights that follow the
// screen's brightness, a rainbow when an achievement unlocks, and the dark the
// FN switch's "Lights off" asks for (fnswitch.go).
//
//   - Critical battery: at 5% or less and not charging, the low-battery warning
//     (ledindicators.go) turns into a faster, brighter red pulse, until the
//     charge is back at 7%. It is a tier of that warning, so it is off with it
//     and only the level moves (the TrimUI rings re-arm, a blink, on a hue
//     change, never on a level change).
//   - Follow screen brightness (led_follow_screen, off by default): the lights'
//     own Brightness is scaled by the backlight's level, so a dimmed screen in a
//     dark room does not sit between two bright rings. Never below the lowest
//     step the Brightness row offers, and Off stays Off. The indicators keep
//     their own brightness: a warning dimmed to nothing would be none.
//   - Celebrate achievements (led_celebrate, on by default): ledAchievementFlash
//     — called when RetroArch unlocks an achievement (raach_*.go) — runs a
//     three-second rainbow over the rings, over the game too, then they show
//     what they showed before. Nothing happens with the lights off, at
//     Brightness Off, or where the LEDs have no colour. Where a colour change
//     costs the rings a dip (ledAnimHueCostly) the rainbow is six pure colours,
//     each faded in and out through dark, like the Rainbow effect there.
//   - Dark: while the FN switch holds the lights off, every frame is black, over
//     games too (the loop otherwise hands the rings back to the firmware there,
//     which would light them in its own colour). With the user's lights off the
//     loop is started for the dark alone and stopped again after.
//
// All of it is a layer of the LED loop (ledLayers in ledindicators.go): the
// loop asks ledSilkyLayerNow once a frame and paints ledSilkyLayer.frame over
// everything else. The loop never reads the config: ledSilkyPublish, called by
// syncLEDLoopSettings on the main thread, hands it the switches.

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// The custom keys behind the two Stick lights rows.
	ledKeyFollowScreen = "led_follow_screen"
	ledKeyCelebrate    = "led_celebrate"

	// ledSilkyCriticalPct is the charge at and below which the low-battery
	// warning turns critical; ledSilkyCriticalClearPct is where a critical
	// warning that shows calms down again, so 5↔6% never flickers.
	ledSilkyCriticalPct      = 5
	ledSilkyCriticalClearPct = 7
	// ledSilkyCriticalPeriodMs is one pulse of the critical warning: three
	// times the low warning's pace (ledIndLowPeriodMs).
	ledSilkyCriticalPeriodMs = 1000
	// ledSilkyCriticalFloor is its dimmest point in perceived brightness: much
	// deeper than the low warning's breath, so it reads as a pulse.
	ledSilkyCriticalFloor = 0.2
	// ledSilkyCriticalLift is how many points above the indicator brightness
	// the critical pulse runs (at most 100).
	ledSilkyCriticalLift = 40

	// ledSilkyFlashFor is the achievement rainbow's length.
	ledSilkyFlashFor = 3 * time.Second
	// ledSilkyFlashTurns is how often the hue goes round in that time where
	// the rings change colour freely.
	ledSilkyFlashTurns = 2
	// ledSilkyFlashFade is that rainbow's fade at its end, so the lights do
	// not snap back to their own colour.
	ledSilkyFlashFade = 300 * time.Millisecond
	// ledSilkyFlashSteps is how many colours the rainbow shows where a colour
	// change costs the rings a dip, and ledSilkyFlashEdge the share of each
	// step spent fading in and out of dark.
	ledSilkyFlashSteps = 6
	ledSilkyFlashEdge  = 0.2

	// ledSilkyDarkRetry is how soon a loop the dark needs is started again,
	// ledSilkyDarkFailRetry after a start that failed.
	ledSilkyDarkRetry     = time.Second
	ledSilkyDarkFailRetry = 5 * time.Second
)

// ledSilkyEpoch is the zero of the flash request's clock: requests are kept as
// the monotonic time since it. Wall-clock time is never compared (the TrimUI
// boots before NTP).
var ledSilkyEpoch = time.Now()

// ledSilky is the state the LED loop and any goroutine share.
var ledSilky struct {
	// celebrate is Celebrate achievements as last published.
	celebrate atomic.Bool
	// flashAt is when the newest achievement asked for the rainbow (time since
	// ledSilkyEpoch, in nanoseconds); 0 is none.
	flashAt atomic.Int64
	// dark is set while the FN switch holds the lights off.
	dark atomic.Bool
	// critical is set while the low-battery warning shows its critical tier.
	// LED loop.
	critical atomic.Bool
	// colour is whether any light the loop drives shows a colour, looked up
	// once (colourOnce) the first time a flash is due.
	colourOnce sync.Once
	colour     bool
}

// ledSilkyMain is the dark's loop bookkeeping. Main thread.
var ledSilkyMain struct {
	// forced: ledSilkyKeepDark started the loop (the user's lights were off)
	// and the dark's end stops it.
	forced  bool
	retryAt time.Time
}

func init() { ledSilky.celebrate.Store(true) }

// ── Published settings ─────────────────────────────────────────────────────

// ledSilkyPublish hands the loop this file's switches and returns the lights'
// brightness after Follow screen brightness. syncLEDLoopSettings calls it on
// the main thread with the brightness the config asks for. The backlight is
// read from brightnessctl.go's cache, which the header's sun and every change
// keep current; no reading is started from here.
func ledSilkyPublish(config *Config, brightness int) int {
	celebrate, follow := true, false
	if config != nil {
		celebrate = customBool(config.Variables.Custom, ledKeyCelebrate, true)
		follow = customBool(config.Variables.Custom, ledKeyFollowScreen, false)
	}
	ledSilky.celebrate.Store(celebrate)
	if !follow {
		return brightness
	}
	brightnessLevel.mu.Lock()
	screen, known := brightnessLevel.pct, brightnessLevel.known
	brightnessLevel.mu.Unlock()
	if !known {
		return brightness
	}
	return ledSilkyFollow(brightness, screen)
}

// ledSilkyFollow scales the lights' brightness by the screen's, both
// percentages: never below the lowest lit step (and never above the lights'
// own), and Off stays Off. Pure.
func ledSilkyFollow(lights, screen int) int {
	if lights <= 0 {
		return 0
	}
	if screen < 0 {
		screen = 0
	}
	if screen > 100 {
		screen = 100
	}
	pct := int(math.Round(float64(lights) * float64(screen) / 100))
	if pct < ledBrightnessMin {
		pct = ledBrightnessMin
	}
	if pct > lights {
		pct = lights
	}
	return pct
}

// ── Critical battery ───────────────────────────────────────────────────────

// ledSilkyCriticalDue reports whether the critical tier shows at pct: from
// ledSilkyCriticalPct down, and once showing until ledSilkyCriticalClearPct.
// Pure.
func ledSilkyCriticalDue(pct int, showing bool) bool {
	limit := ledSilkyCriticalPct
	if showing {
		limit = ledSilkyCriticalClearPct - 1
	}
	return pct >= 0 && pct <= limit
}

// ledSilkyCriticalColor is the low-battery warning's critical tier for one
// frame, ok false above it. The warning itself only shows while not charging
// (ledIndicatorFor). LED loop.
func ledSilkyCriticalColor(x ledExtrasSettings, now time.Time) (r, g, b uint8, ok bool) {
	due := ledSilkyCriticalDue(getBatteryPercent(), ledSilky.critical.Load())
	ledSilky.critical.Store(due)
	if !due {
		return 0, 0, 0, false
	}
	level := ledIndBreath(now, ledSilkyCriticalPeriodMs, ledSilkyCriticalFloor)
	pct := x.indBrightness + ledSilkyCriticalLift
	if pct > ledBrightnessMax {
		pct = ledBrightnessMax
	}
	r, g, b = ledApplyBrightness(uint8(math.Round(255*level)), 0, 0, pct)
	r, g, b = ledJukaDarkFloor(r, g, b)
	return r, g, b, true
}

// ── Achievement rainbow ────────────────────────────────────────────────────

// ledAchievementFlash runs the achievement rainbow on the stick rings: about
// three seconds, over a running game too, then the lights show what they
// showed before. Safe from any goroutine and never blocks: it only leaves a
// request the LED loop picks up (ledSilkyLayerNow), which shows nothing with
// Celebrate achievements off, the lights off or at Brightness Off, or where the
// LEDs have no colour. A second unlock during the rainbow starts it again.
func ledAchievementFlash() {
	if !ledSilky.celebrate.Load() {
		return
	}
	at := int64(time.Since(ledSilkyEpoch))
	if at <= 0 {
		at = 1
	}
	ledSilky.flashAt.Store(at)
}

// ledSilkyColourLEDs reports whether a light the loop can drive shows a
// colour (the TrimUI rings, a multicolour or colour LED): a single-channel
// light cannot show a rainbow. Looked up once, on the loop's goroutine.
func ledSilkyColourLEDs() bool {
	ledSilky.colourOnce.Do(func() {
		for _, d := range discoverLEDs(ledRootDir) {
			if d.Mode != ledModeBrightness {
				ledSilky.colour = true
				return
			}
		}
	})
	return ledSilky.colour
}

// ledSilkyRainbow is the rainbow's colour at in (time since it began), at full
// level; black once it is over. costly: a colour change costs the rings a dip,
// so six pure colours each fade in and out through dark (whole sixths of the
// wheel, whose hue no level rounds away). Pure.
func ledSilkyRainbow(in time.Duration, costly bool) (uint8, uint8, uint8) {
	if in < 0 {
		in = 0
	}
	if in >= ledSilkyFlashFor {
		return 0, 0, 0
	}
	t := float64(in) / float64(ledSilkyFlashFor)
	if costly {
		pos := t * ledSilkyFlashSteps
		step := math.Floor(pos)
		within := pos - step
		level := 1.0
		if edge := math.Min(within, 1-within); edge < ledSilkyFlashEdge {
			e := edge / ledSilkyFlashEdge
			level = e * e * (3 - 2*e)
		}
		return hsvToRGB(step/ledSilkyFlashSteps, 1, level)
	}
	level := 1.0
	if left := ledSilkyFlashFor - in; left < ledSilkyFlashFade {
		level = float64(left) / float64(ledSilkyFlashFade)
	}
	return hsvToRGB(math.Mod(t*ledSilkyFlashTurns, 1), 1, level)
}

// ── The layer ──────────────────────────────────────────────────────────────

// ledSilkyLayer is what this file wants painted for one frame: dark, the
// rainbow (in: how far into it, at brightness), or nothing.
type ledSilkyLayer struct {
	dark       bool
	flashing   bool
	in         time.Duration
	brightness int
}

// holds reports whether the layer keeps the rings while a game owns the screen.
func (l ledSilkyLayer) holds() bool { return l.dark || l.flashing }

// ledSilkyLayerNow decides the layer for one frame. With the loop running for
// Juka alone (ledJukaOnly: the user's lights are off) there is no rainbow, but
// the dark still holds. LED loop.
func ledSilkyLayerNow(s ledLoopSettings, now time.Time) ledSilkyLayer {
	if ledSilky.dark.Load() {
		return ledSilkyLayer{dark: true}
	}
	at := ledSilky.flashAt.Load()
	if at == 0 {
		return ledSilkyLayer{}
	}
	in := now.Sub(ledSilkyEpoch) - time.Duration(at)
	if in >= ledSilkyFlashFor {
		ledSilky.flashAt.CompareAndSwap(at, 0)
		return ledSilkyLayer{}
	}
	if in < 0 {
		in = 0 // asked a moment after this frame's clock was read
	}
	if !ledSilky.celebrate.Load() || ledJukaOnly.Load() || s.brightness <= 0 || !ledSilkyColourLEDs() {
		return ledSilkyLayer{}
	}
	return ledSilkyLayer{flashing: true, in: in, brightness: s.brightness}
}

// frame is the layer's colour for one frame; ok false when it shows nothing and
// the layers under it paint. LED loop.
func (l ledSilkyLayer) frame() (r, g, b uint8, ok bool) {
	switch {
	case l.dark:
		return 0, 0, 0, true
	case l.flashing:
		r, g, b = ledSilkyRainbow(l.in, ledAnimHueCostly.Load())
		r, g, b = ledApplyBrightness(r, g, b, l.brightness)
		r, g, b = ledJukaDarkFloor(r, g, b)
		return r, g, b, true
	}
	return 0, 0, 0, false
}

// ── Dark (the FN switch's Lights off) ──────────────────────────────────────

// ledSilkySetDark turns the FN switch's dark on or off. On, the loop is made
// sure of (ledSilkyKeepDark); off stops a loop started for the dark alone —
// the rings go back to the firmware's colour, as with the user's lights off —
// and then lets the user's frames through again. Main thread.
func ledSilkySetDark(config *Config, on bool) {
	st := &ledSilkyMain
	if on {
		ledSilky.dark.Store(true)
		st.retryAt = time.Time{}
		ledSilkyKeepDark(config, time.Now())
		return
	}
	if st.forced {
		st.forced = false
		userOn := config != nil && config.Variables.LEDSync
		// Juka's layer or the LED style panel may use the loop now; they stop
		// it themselves.
		if !userOn && !ledJukaState.forced && !ledPanel.forcedSync && LEDSyncRunning() {
			_ = SetLEDSync(false)
		}
	}
	ledSilky.dark.Store(false)
}

// ledSilkyKeepDark keeps a loop running while the dark holds: the user's lights
// may be off, or switched off since (a loop that stops hands the rings to the
// firmware, which lights them). A loop the user switches on meanwhile is
// theirs. Main thread, from fnSwitchTick.
func ledSilkyKeepDark(config *Config, now time.Time) {
	st := &ledSilkyMain
	if !ledSilky.dark.Load() {
		return
	}
	userOn := config != nil && config.Variables.LEDSync
	if st.forced && userOn {
		st.forced = false
	}
	if now.Before(st.retryAt) || LEDSyncRunning() {
		return
	}
	if err := SetLEDSync(true); err != nil {
		st.retryAt = now.Add(ledSilkyDarkFailRetry)
		return
	}
	st.retryAt = now.Add(ledSilkyDarkRetry)
	st.forced = !userOn
}

// ── Settings → Stick lights ────────────────────────────────────────────────

// ledSilkyStore writes one of this file's switches and hands it to the loop.
func ledSilkyStore(c *Config, key string, on bool) {
	if c == nil {
		return
	}
	ledPanelEnsureKeys(c)
	c.Variables.Custom[key] = boolString(on)
	syncLEDLoopSettings(c)
	ledPanelPersist(c)
}

// ledSilkyFollowScreenRow is Settings → Stick lights → Follow screen
// brightness, below Brightness.
func ledSilkyFollowScreenRow() setRow {
	return setRow{
		ID: "ledFollowScreen", Label: "Follow screen brightness", Kind: setKindToggle,
		Detail:  setStatic("The lights dim and brighten with the screen; Brightness is their top"),
		Visible: ledSettingsOn,
		Get: func(c *Config) bool {
			return c != nil && customBool(c.Variables.Custom, ledKeyFollowScreen, false)
		},
		Set: func(c *Config, on bool) { ledSilkyStore(c, ledKeyFollowScreen, on) },
	}
}

// ledSilkyCelebrateRow is Settings → Stick lights → Celebrate achievements.
func ledSilkyCelebrateRow() setRow {
	return setRow{
		ID: "ledCelebrate", Label: "Celebrate achievements", Kind: setKindToggle,
		Detail:  setStatic("A rainbow on the sticks when RetroAchievements unlocks one"),
		Visible: ledSettingsOn,
		Get: func(c *Config) bool {
			return c == nil || customBool(c.Variables.Custom, ledKeyCelebrate, true)
		},
		Set: func(c *Config, on bool) { ledSilkyStore(c, ledKeyCelebrate, on) },
	}
}
