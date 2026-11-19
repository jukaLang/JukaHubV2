package main

// ledscreen.go — the stick lights' "Screen colours" mode (NextUI's ambient
// LEDs): the rings glow with the colours at the edges of the picture, like an
// Ambilight TV, in games too.
//
// The picture comes from the framebuffer. A game draws straight to /dev/fb0 on
// the TrimUI (castcap_fb.go: RetroArch's GLES output goes through the mali
// fbdev path, which is why Screen Share and the time-lapse can read it), and
// JukaHub's own frames land there too, so one reader serves both. It reads
// little: nine short rows of the outer 15% on each side (ledscreen_linux.go),
// with pread into buffers allocated once, about twelve times a second — some
// 15 KB a tick, no full-frame copy and no allocation per tick. Off the
// handheld there is no readable picture, and the theme's accent stands in.
//
// The TrimUI rings re-arm their effect on a hue change, which shows as a blink
// (ledanim.go), while a level change is free. So the two are handled apart: the
// level follows the picture's brightness within a fraction of a second, and the
// hue only moves to a new colour once that colour has held for half a second,
// and never more than about once a second. A game's flashes and scrolling then
// read as a glow that breathes with the scene, not a strobe.
//
// The LED loop normally hands the rings back to the firmware while a game owns
// the screen (ledsync.go). With this mode it keeps drawing them over a game —
// unless "In games" is off, the game's console is in led_screen_off_systems
// (the "This console" row edits it for the last game played), the lights'
// brightness is Off, or the device is in standby (ledindicators.go): a dark or
// sleeping panel is never sampled.
//
// The rings take one colour for both sticks (effect_rgb_hex_lr), so the two
// sides are averaged; the sampler keeps them apart in case a per-side writer
// comes later.

import (
	"math"
	"strings"
	"sync"
	"time"
)

const (
	// ledPolicyScreen is the mode's key in led_policy (ledsync.go's order).
	ledPolicyScreen = "screen"

	// ledKeyScreenGames switches the mode on over games (default on).
	ledKeyScreenGames = "led_screen_games"
	// ledKeyScreenOffSystems lists the consoles (library system names, comma
	// separated, any case) whose games keep the firmware's lights.
	ledKeyScreenOffSystems = "led_screen_off_systems"

	// ledScreenSampleEvery is the sampling period: about twelve reads a
	// second, every other frame of the 25 fps loop.
	ledScreenSampleEvery = 80 * time.Millisecond
	// ledScreenRiseSec / ledScreenFallSec ease the level: a flash brightens
	// quickly, a cut to dark fades a little slower.
	ledScreenRiseSec = 0.12
	ledScreenFallSec = 0.35
	// ledScreenHueHold is how long a new colour must hold before the rings
	// take it, and ledScreenHueGap the least time between two hue changes
	// (ledScreenHueGapCostly where every change is a dip, ledanim.go).
	ledScreenHueHold      = 500 * time.Millisecond
	ledScreenHueGap       = 1200 * time.Millisecond
	ledScreenHueGapCostly = 4 * time.Second
	// ledScreenHueTolerance is how far (per channel, at full value) the
	// picture's colour may wander before it counts as a new one: wider than
	// the rings' own re-arm tolerance (ledAnimHueTolerance), so a colour that
	// is "the same" for the eye never costs a blink.
	ledScreenHueTolerance = 64
	// ledScreenDarkPeak is the sample peak (0..255) under which a scene is
	// dark and the rings go out.
	ledScreenDarkPeak = 14
	// ledScreenSystemRefresh is how often the running game's console is
	// looked up again while a game owns the screen.
	ledScreenSystemRefresh = 2 * time.Second
)

// ledScreen is the mode's state. The LED loop is its only user, but a loop
// started right after a stop may overlap the old one's last frame, so it is
// locked.
var ledScreen struct {
	mu        sync.Mutex
	sampledAt time.Time
	at        time.Time
	target    float64 // the picture's level, 0..1
	level     float64 // the eased level
	hue       [3]uint8
	hueSet    bool
	hueAt     time.Time
	cand      [3]uint8
	candSince time.Time
	candSet   bool
}

// ledScreenColor is the mode's colour for one LED-loop frame. LED loop.
func ledScreenColor(now time.Time) (uint8, uint8, uint8) {
	if ledStandbyNow() {
		return 0, 0, 0
	}
	st := &ledScreen
	st.mu.Lock()
	defer st.mu.Unlock()
	dt := now.Sub(st.at).Seconds()
	st.at = now
	if dt < 0 || dt > 1 {
		// The first frame, or the first after a pause: start on the picture,
		// not on the hue an earlier session (or game) left behind.
		dt, st.level, st.sampledAt = 0, 0, time.Time{}
		st.hueSet, st.candSet = false, false
	}
	if st.sampledAt.IsZero() || now.Sub(st.sampledAt) >= ledScreenSampleEvery || now.Before(st.sampledAt) {
		st.sampledAt = now
		var left, right [3]float64
		if ledScreenSampleFB(now, &left, &right) {
			ledScreenTakeSample([3]float64{
				(left[0] + right[0]) / 2, (left[1] + right[1]) / 2, (left[2] + right[2]) / 2,
			}, now)
		} else {
			// No readable picture (a desktop, a framebuffer that would not
			// open): the theme's accent, steady.
			a := currentLEDExtras().accent
			ledScreenTakeSample([3]float64{float64(a[0]), float64(a[1]), float64(a[2])}, now)
		}
	}
	if dt == 0 {
		st.level = st.target
	} else {
		tau := ledScreenFallSec
		if st.target > st.level {
			tau = ledScreenRiseSec
		}
		st.level += (st.target - st.level) * (1 - math.Exp(-dt/tau))
	}
	if !st.hueSet || st.level < 0.02 {
		return 0, 0, 0
	}
	level := st.level
	// Below a peak of about 8 the rounded channels lose the hue, and the rings
	// would re-arm a wrong one (ledJukaMinPeak in ledjuka.go).
	if floor := float64(ledJukaMinPeak+1) / 255; level < floor {
		level = floor
	}
	return uint8(math.Round(float64(st.hue[0]) * level)),
		uint8(math.Round(float64(st.hue[1]) * level)),
		uint8(math.Round(float64(st.hue[2]) * level))
}

// ledScreenRelease closes the framebuffer reader while the mode is not in use
// (another mode picked, the loop stopping). LED loop.
func ledScreenRelease() {
	ledScreen.mu.Lock()
	ledScreenFBRelease()
	ledScreen.mu.Unlock()
}

// ledScreenTakeSample turns one averaged sample (0..255 a channel) into the
// target level and, when it has held long enough, a new hue. Under ledScreen.mu.
func ledScreenTakeSample(avg [3]float64, now time.Time) {
	st := &ledScreen
	hue, level, ok := ledScreenHueOf(avg)
	if !ok {
		// A dark scene: the rings go out, the hue is kept for when it lights.
		st.target = 0
		return
	}
	st.target = level
	if !st.hueSet {
		st.hue, st.hueSet, st.hueAt, st.candSet = hue, true, now, false
		return
	}
	if !ledScreenHueFar(hue, st.hue) {
		st.candSet = false
		return
	}
	if !st.candSet || ledScreenHueFar(hue, st.cand) {
		st.cand, st.candSince, st.candSet = hue, now, true
		return
	}
	gap := ledScreenHueGap
	if ledAnimHueCostly.Load() {
		// Each change is a dip on rings that can only re-arm.
		gap = ledScreenHueGapCostly
	}
	if now.Sub(st.candSince) >= ledScreenHueHold && now.Sub(st.hueAt) >= gap {
		st.hue, st.hueAt, st.candSet = hue, now, false
	}
}

// ledScreenHueOf is a sample's colour at full value, with its saturation
// lifted (a picture's average is greyer than any of its colours, and a grey
// ring reads as white), and the level to show it at. ok is false for a dark
// scene. Pure.
func ledScreenHueOf(avg [3]float64) (hue [3]uint8, level float64, ok bool) {
	peak := math.Max(avg[0], math.Max(avg[1], avg[2]))
	low := math.Min(avg[0], math.Min(avg[1], avg[2]))
	if peak < ledScreenDarkPeak {
		return [3]uint8{}, 0, false
	}
	sat := (peak - low) / peak
	h := ledScreenHue(avg[0], avg[1], avg[2], peak, low)
	sat = math.Min(1, sat*1.6+0.05)
	if sat < 0.2 {
		// A grey scene stays white: boosting it would invent a colour.
		sat = 0
	}
	r, g, b := hsvToRGB(h, sat, 1)
	// The level follows the picture's brightness, with a floor so a dim scene
	// still glows.
	return [3]uint8{r, g, b}, 0.2 + 0.8*peak/255, true
}

// ledScreenHue is the hue (0..1) of a colour whose largest and smallest
// channels are peak and low. Pure.
func ledScreenHue(r, g, b, peak, low float64) float64 {
	delta := peak - low
	if delta <= 0 {
		return 0
	}
	var h float64
	switch peak {
	case r:
		h = math.Mod((g-b)/delta, 6)
	case g:
		h = (b-r)/delta + 2
	default:
		h = (r-g)/delta + 4
	}
	h /= 6
	if h < 0 {
		h++
	}
	return h
}

// ledScreenHueFar reports whether two full-value colours differ by more than
// ledScreenHueTolerance in any channel. Pure.
func ledScreenHueFar(a, b [3]uint8) bool {
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < -ledScreenHueTolerance || d > ledScreenHueTolerance {
			return true
		}
	}
	return false
}

// ledScreenGame caches the console of the game on screen: the newest entry of
// the recents, which every library launch writes as it starts
// (recordGameLaunch).
var ledScreenGame struct {
	mu     sync.Mutex
	system string
	at     time.Time
}

// ledScreenCurrentSystem is the running game's console, looked up at most every
// ledScreenSystemRefresh. LED loop.
func ledScreenCurrentSystem(now time.Time) string {
	g := &ledScreenGame
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.at.IsZero() || now.Sub(g.at) >= ledScreenSystemRefresh || now.Before(g.at) {
		g.at = now
		g.system = ""
		if recent := recentGames(); len(recent) > 0 {
			g.system = strings.ToLower(strings.TrimSpace(recent[0].System))
		}
	}
	return g.system
}

// ledScreenOverGame reports whether the mode draws over the game on screen
// now: In games is on and the game's console is not switched off. LED loop.
func ledScreenOverGame(x ledExtrasSettings, now time.Time) bool {
	if !x.screenGames {
		return false
	}
	if len(x.screenOff) == 0 {
		return true
	}
	sys := ledScreenCurrentSystem(now)
	for _, off := range x.screenOff {
		if off == sys {
			return false
		}
	}
	return true
}

// ledScreenParseSystems reads led_screen_off_systems: comma separated, any
// case, blanks dropped. Pure.
func ledScreenParseSystems(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if s := strings.ToLower(strings.TrimSpace(part)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ledScreenLastSystem is the console of the last game played, as the recents
// spell it ("" before the first game). Main thread.
func ledScreenLastSystem() string {
	if recent := recentGames(); len(recent) > 0 {
		return strings.TrimSpace(recent[0].System)
	}
	return ""
}

func ledScreenOffSystems(c *Config) []string {
	if c == nil {
		return nil
	}
	raw, _ := customString(c.Variables.Custom, ledKeyScreenOffSystems)
	return ledScreenParseSystems(raw)
}

// ledScreenSystemOn reports whether sys's games get Screen colours. Main
// thread.
func ledScreenSystemOn(c *Config, sys string) bool {
	want := strings.ToLower(strings.TrimSpace(sys))
	for _, off := range ledScreenOffSystems(c) {
		if off == want {
			return false
		}
	}
	return true
}

// ledScreenSetSystem adds sys to the off list or takes it out. Main thread.
func ledScreenSetSystem(c *Config, sys string, on bool) {
	want := strings.ToLower(strings.TrimSpace(sys))
	if c == nil || want == "" {
		return
	}
	keep := make([]string, 0, 4)
	for _, off := range ledScreenOffSystems(c) {
		if off != want {
			keep = append(keep, off)
		}
	}
	if !on {
		keep = append(keep, want)
	}
	ledPanelEnsureKeys(c)
	if len(keep) == 0 {
		delete(c.Variables.Custom, ledKeyScreenOffSystems)
	} else {
		c.Variables.Custom[ledKeyScreenOffSystems] = strings.Join(keep, ",")
	}
}

// ledScreenGamesRow is Settings → Stick lights → In games, shown in the Screen
// colours mode.
func ledScreenGamesRow() setRow {
	return setRow{
		ID: "ledScreenGames", Label: "Screen colours in games", Kind: setKindToggle,
		Detail:  setStatic("Off: games get the firmware's own stick lights"),
		Visible: ledSettingsInMode(ledPolicyScreen),
		Get: func(c *Config) bool {
			return c != nil && customBool(c.Variables.Custom, ledKeyScreenGames, true)
		},
		Set: func(c *Config, on bool) {
			if c == nil {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[ledKeyScreenGames] = boolString(on)
			syncLEDLoopSettings(c)
			ledPanelPersist(c)
		},
	}
}

// ledScreenSystemRow switches Screen colours for the console of the last game
// played (NextUI's per-emulator choice, without a list of every console):
// play a game, come back, and this row names its console.
func ledScreenSystemRow() setRow {
	return setRow{
		ID: "ledScreenSystem", Label: "For this console", Kind: setKindToggle,
		Detail: func(c *Config) string {
			return "Screen colours while playing " + ledScreenLastSystem() + " games"
		},
		Visible: func(c *Config) bool {
			return ledSettingsInMode(ledPolicyScreen)(c) && c != nil &&
				customBool(c.Variables.Custom, ledKeyScreenGames, true) && ledScreenLastSystem() != ""
		},
		Get: func(c *Config) bool { return ledScreenSystemOn(c, ledScreenLastSystem()) },
		Set: func(c *Config, on bool) {
			ledScreenSetSystem(c, ledScreenLastSystem(), on)
			syncLEDLoopSettings(c)
			ledPanelPersist(c)
		},
	}
}
