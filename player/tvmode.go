package main

// tvmode.go — TV mode: the handheld on a television.
//
// The owner's list (2026-11-19) asked for a Brick Pro TV mode: use its USB-C
// video output with a larger interface and set up an outside controller on
// its own. The Brick Pro's firmware drives the port (DisplayPort alt mode);
// JukaHub does not, and nobody has told us yet which file flips when a cable
// goes in, whether the firmware mirrors the panel or makes the TV the only
// screen, or what size the picture becomes. So this first version watches
// and adapts, and says plainly what it saw.
//
// Watching: on a model whose hardware row has a video out (the Brick Pro), or
// anywhere the player sets TV mode to On, a goroutine reads the kernel's
// video-out status files every two seconds (tvmode_sysfs.go). Two readings in
// a row must agree before "a TV is connected" changes, so a cable wiggle does
// not flap the interface, and every change of the reading is logged once —
// the first Brick Pro owner's log names the real path. Off stops the watch.
//
// The setting, Settings → Display → TV mode, is Auto (on while a display is
// connected), On (the TV interface on any device, TV or not: it is how anyone
// can try it) or Off. While TV mode is on:
//
//   - Text is larger by the TV text size (130% unless changed), through a
//     run-time multiplier in a11y.go: the player's own Text size is never
//     overwritten, and the product stays within Accessibility's largest size.
//   - A USB or Bluetooth controller is player 1 in RetroArch games started
//     from JukaHub, the handheld's own pad ("TRIMUI Player1") the player after
//     the outside ones: a launch hook (gamelaunch_hooks.go) sets
//     input_playerN_joypad_index for that one launch (racfgscope.go undoes it
//     after). The numbers are the pads' places in SDL's device list, the same
//     estimate Local play uses (hotseat.go); a Local play session of two or
//     more players decides the order itself instead. JukaHub's own menus
//     already take every pad's buttons (gamepad_hotplug.go opens them).
//   - A TV that arrives with no outside controller gets a toast pointing at
//     Pair a controller, which opens the Bluetooth page.
//   - "Built-in screen off on the TV" (off unless chosen, and only on a
//     model with a video out and a TV actually seen) switches the panel's
//     backlight off while the TV is connected; the backlight guard
//     (devpower.go) leaves it dark only while TV mode holds it.
//
// The canvas: a 4:3 model draws a 960x720 canvas (uiprofile.go), and the
// scene geometry is squeezed into it once, at start, for the window SDL made.
// That cannot change under a running window, so when the display is 16:9 at
// start (the firmware made the TV the screen) the wide canvas is chosen then,
// and when the screen's shape changes later the TV mode rows offer a restart.

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// The settings, in the config's Custom map.
	tvModeKey      = "tvMode"         // "auto" (default), "on", "off"
	tvModeTextKey  = "tvModeText"     // the TV text factor: "1.15", "1.3", "1.45"
	tvModePadsKey  = "tvModePads"     // an outside controller is player 1 (default on)
	tvModePanelKey = "tvModePanelOff" // the panel's light off on the TV (default off)

	tvModeAuto = "auto"
	tvModeOn   = "on"
	tvModeOff  = "off"

	// tvModeTextDefault is the TV text size when none is chosen.
	tvModeTextDefault = 1.3
	// tvPollEvery is the watch's pace; tvDebounceReads readings in a row
	// must agree before the answer changes.
	tvPollEvery     = 2 * time.Second
	tvDebounceReads = 2
	// tvMaxPlayers is the seats the player order fills, the handheld's own
	// pad included.
	tvMaxPlayers = 4
)

// The setting as the watch goroutine reads it.
const (
	tvSetAuto int32 = iota
	tvSetOn
	tvSetOff
)

var (
	// tvSettingNow mirrors the setting for the watch goroutine.
	tvSettingNow atomic.Int32
	// tvModeActive mirrors tvMode.active for the watch goroutine.
	tvModeActive atomic.Bool
	// tvPanelDark is set while TV mode holds the panel's backlight off.
	tvPanelDark atomic.Bool
	// tvRefreshQueued is set while a tvModeRefresh waits in tbxPost's queue.
	// The queue is not drained while a game owns the screen, so without it
	// a long game piled up one refresh every two seconds, all run in one
	// frame when the game ended.
	tvRefreshQueued atomic.Bool
)

// tvProbe is the watch: its goroutine and its readings. Guarded by mu.
var tvProbe struct {
	mu      sync.Mutex
	running bool
	ever    bool       // a reading was taken since the watch started
	last    tvEvidence // the newest reading
	raw     bool       // the newest reading's answer
	streak  int        // readings in a row with that answer
	stable  bool       // the debounced answer: a display is connected
	logged  string     // the last reading logged
	classes bool       // /sys/class was listed in the log
	// fbPanel says fb0 had the panel's size at the first reading, so a later
	// size of its own counts as a display (tvProbeSystem's fbCheck).
	fbPanel bool
}

// tvPad is one controller in SDL's device list.
type tvPad struct {
	index   int
	id      sdl.JoystickID
	name    string
	builtIn bool // the handheld's own pad
	ignored bool // a device of JukaHub's own, not a player's
}

// tvMode is TV mode's state on the main thread.
var tvMode struct {
	active    bool
	detected  bool
	announced map[sdl.JoystickID]bool // outside pads already named in a toast
	refit     string                  // the screen's size when the canvas no longer fits it
	refitSeen string                  // the size the restart toast was shown for
	panelLog  bool
	panelFail bool
	cleanup   bool
	note      string // what the last launch did with the controllers
}

// tvBoot is the start-time canvas decision (tvModeStartProfile).
var tvBoot struct {
	decided bool
	wide    bool
	w, h    int32
}

func init() {
	gameLaunchRegisterPrepare(tvModePrepare)
	// After the first frame, on the main thread.
	deferStartupTask("tv-mode", func() { tvModeApplyMain(appConfig) })
}

// ── Settings ─────────────────────────────────────────────────────────────

// tvModeSetting is the stored choice; anything else is Auto.
func tvModeSetting(c *Config) string {
	if c == nil {
		return tvModeAuto
	}
	switch strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, tvModeKey))) {
	case tvModeOn:
		return tvModeOn
	case tvModeOff:
		return tvModeOff
	}
	return tvModeAuto
}

// tvModeLabel is a setting value's word.
func tvModeLabel(mode string) string {
	switch mode {
	case tvModeOn:
		return "On"
	case tvModeOff:
		return "Off"
	}
	return "Auto"
}

// tvSettingCode is the setting for tvSettingNow.
func tvSettingCode(mode string) int32 {
	switch mode {
	case tvModeOn:
		return tvSetOn
	case tvModeOff:
		return tvSetOff
	}
	return tvSetAuto
}

// tvModeTextFactor is the TV text size.
func tvModeTextFactor(c *Config) float64 {
	if c == nil {
		return tvModeTextDefault
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(customStringValue(c.Variables.Custom, tvModeTextKey)), 64)
	if err != nil || f < 1 || f > 1.5 {
		return tvModeTextDefault
	}
	return f
}

// tvModePadsFirst reports whether an outside controller is made player 1.
func tvModePadsFirst(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, tvModePadsKey, true)
}

// tvModePanelOff reports whether the panel goes dark while a TV shows.
func tvModePanelOff(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, tvModePanelKey, false)
}

// tvModeHasVideoOut reports whether this model's hardware row lists a video
// out (devicedrivers.go).
func tvModeHasVideoOut() bool { return deviceHardwareCurrent().VideoOut != "" }

// tvModeHoldsPanel reports whether TV mode keeps the panel's backlight off;
// the backlight guard (devpower.go) asks it. Any goroutine.
func tvModeHoldsPanel() bool { return tvPanelDark.Load() }

// ── The watch ────────────────────────────────────────────────────────────

// tvLoopWanted reports whether the watch should run: On anywhere, Auto on a
// model with a video out, never Off.
func tvLoopWanted() bool {
	switch tvSettingNow.Load() {
	case tvSetOff:
		return false
	case tvSetOn:
		return true
	}
	return tvModeHasVideoOut()
}

// tvLoopEnsure starts the watch when it is wanted and not running. Only a
// TrimUI handheld is watched: a PC's displays are its desktop's business.
func tvLoopEnsure() {
	if !tvProbeSupported || !IsTrimuiDevice() {
		return
	}
	tvProbe.mu.Lock()
	defer tvProbe.mu.Unlock()
	if tvProbe.running || !tvLoopWanted() {
		return
	}
	tvProbe.running = true
	go tvLoopRun()
}

// tvLoopRun is the watch: a reading every tvPollEvery until it is no longer
// wanted, when it forgets its readings and lets the main thread know.
func tvLoopRun() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("tvmode", "video-out watch stopped: %v", r)
			tvProbe.mu.Lock()
			tvProbe.running = false
			tvProbe.mu.Unlock()
		}
	}()
	for {
		tvProbe.mu.Lock()
		if !tvLoopWanted() {
			tvProbe.running = false
			tvProbe.ever, tvProbe.raw, tvProbe.streak, tvProbe.stable = false, false, 0, false
			tvProbe.last, tvProbe.fbPanel = tvEvidence{}, false
			tvProbe.mu.Unlock()
			tvModePostRefresh()
			return
		}
		tvProbe.mu.Unlock()
		tvProbeTick()
		time.Sleep(tvPollEvery)
	}
}

// tvProbeTick takes one reading: debounces it, logs a change, and asks the
// main thread to act when something changed or TV mode is on (its controller
// and backlight checks ride on the same pace).
func tvProbeTick() {
	tvProbe.mu.Lock()
	fbCheck := tvProbe.ever && tvProbe.fbPanel
	tvProbe.mu.Unlock()
	ev := tvProbeSystem(fbCheck)
	sum := ev.summary()
	raw := ev.connected()
	tvProbe.mu.Lock()
	first := !tvProbe.ever
	if first {
		hw := deviceHardwareCurrent()
		tvProbe.fbPanel = hw.PanelW > 0 && ev.FBW == int(hw.PanelW) && ev.FBH == int(hw.PanelH)
	}
	prevW, prevH := tvProbe.last.FBW, tvProbe.last.FBH
	tvProbe.ever = true
	tvProbe.last = ev
	if raw == tvProbe.raw && !first {
		tvProbe.streak++
	} else {
		tvProbe.raw, tvProbe.streak = raw, 1
	}
	changed := false
	if tvProbe.streak >= tvDebounceReads && tvProbe.stable != raw {
		tvProbe.stable = raw
		changed = true
	}
	logIt := sum != tvProbe.logged
	if logIt {
		tvProbe.logged = sum
	}
	listClasses := !tvProbe.classes
	tvProbe.classes = true
	tvProbe.mu.Unlock()

	if logIt {
		logInfo("tvmode", "video out: %s", sum)
	}
	if listClasses {
		if names := tvSysfsClassNames(); names != "" {
			logInfo("tvmode", "sysfs classes (where a TV cable may show up): %s", names)
		}
	}
	// A TV that left while a game owns the screen: the main loop is parked
	// until the game ends (no tbxPost drain, no backlight guard), so the
	// panel's light comes back from here, unless a sleep or the dark dock
	// wants it off. backlightOn is safe from any goroutine.
	if changed && !raw && tvPanelDark.Swap(false) && !powerSleepActive() && !ambientIsScreenOff() {
		backlightOn()
	}
	fbMoved := !first && (prevW != ev.FBW || prevH != ev.FBH)
	if changed || fbMoved || first || tvModeActive.Load() {
		tvModePostRefresh()
	}
}

// tvModePostRefresh asks the main thread for one tvModeRefresh, unless one is
// already waiting. Any goroutine.
func tvModePostRefresh() {
	if tvRefreshQueued.CompareAndSwap(false, true) {
		tbxPost(tvModeRefresh)
	}
}

// tvProbeSnapshot is the watch's newest reading, its debounced answer and
// whether it has read anything yet.
func tvProbeSnapshot() (ev tvEvidence, stable, ever bool) {
	tvProbe.mu.Lock()
	defer tvProbe.mu.Unlock()
	return tvProbe.last, tvProbe.stable, tvProbe.ever
}

// tvModeRefresh is tvModeApplyMain with the live config, for tbxPost. The
// flag drops first, so a reading taken while this runs posts again.
func tvModeRefresh() {
	tvRefreshQueued.Store(false)
	tvModeApplyMain(appConfig)
}

// ── Applying it (main thread) ────────────────────────────────────────────

// tvModeApplyMain brings TV mode in line with the setting and the watch's
// answer: the text size, the toasts, the controllers, the panel's light and
// the restart offer. Main thread: the startup task, the settings rows and the
// watch (through tbxPost) call it.
func tvModeApplyMain(c *Config) {
	if c == nil {
		c = appConfig
	}
	mode := tvModeSetting(c)
	tvSettingNow.Store(tvSettingCode(mode))
	tvLoopEnsure()
	ev, stable, ever := tvProbeSnapshot()
	detected := stable && mode != tvModeOff
	active := mode == tvModeOn || (mode == tvModeAuto && detected)

	var outside []tvPad
	if active && IsTrimuiDevice() {
		outside = tvOutsidePads(tvModePadsNow())
	}
	arrived := detected && !tvMode.detected
	left := !detected && tvMode.detected
	tvMode.detected = detected
	if active != tvMode.active {
		tvMode.active = active
		tvModeActive.Store(active)
		logInfo("tvmode", "TV mode %s (setting %s, display connected: %t)", strings.ToLower(setOnOff(active)), mode, detected)
	}
	tvModeSetBoost(c, active)

	toasted := false
	switch {
	case arrived:
		msg := "TV connected"
		if mode == tvModeAuto {
			msg += " — TV mode on"
		}
		if IsTrimuiDevice() {
			switch {
			case len(outside) == 0:
				msg += " · pair a controller: Settings → Display → TV mode"
			case tvModePadsFirst(c):
				msg += " · " + outside[0].name + " is player 1"
			}
		}
		ShowToast(msg, ToastKindInfo)
		toasted = true
		// The pads named (or not needed) now are not announced again.
		tvMode.announced = make(map[sdl.JoystickID]bool, len(outside))
		for _, p := range outside {
			tvMode.announced[p.id] = true
		}
	case left && mode != tvModeOff:
		msg := "TV disconnected"
		if mode == tvModeAuto {
			msg += " — TV mode off"
		}
		ShowToast(msg, ToastKindInfo)
		toasted = true
	}
	if tvModeAnnouncePads(c, active, outside, toasted) {
		toasted = true
	}
	tvModePanel(active && detected && tvModeHasVideoOut() && tvModePanelOff(c))
	tvModeRefit(ev, ever && mode != tvModeOff, toasted)
}

// tvModeSetBoost sets the run-time text multiplier (a11y.go) and redraws the
// text when it changed.
func tvModeSetBoost(c *Config, active bool) {
	want := 1.0
	if active {
		want = tvModeTextFactor(c)
	}
	if math.Abs(a11yTextBoost-want) < 0.001 {
		return
	}
	a11yTextBoost = want
	a11yRefreshFonts()
	// The on-screen keyboard's, its settings' and the clipboard's memos are
	// keyed on the player's Text size (a11y.fontScale), which did not change:
	// they would keep drawing the old faces and hint plans.
	vkFontMemo.valid = false
	vkSetFonts.title = nil
	vkChordPlan.ok = false
	vkSet.hintPlanOK = false
	vkClipView.planOK = false
}

// tvModeAnnouncePads names an outside controller that arrived while TV mode
// is on, once, unless another toast of this round said enough. It reports
// whether it showed one.
func tvModeAnnouncePads(c *Config, active bool, outside []tvPad, quiet bool) bool {
	if !active {
		tvMode.announced = nil
		return false
	}
	if tvMode.announced == nil {
		tvMode.announced = make(map[sdl.JoystickID]bool, len(outside))
	}
	present := make(map[sdl.JoystickID]bool, len(outside))
	seat, name := 0, ""
	for i, p := range outside {
		present[p.id] = true
		if !tvMode.announced[p.id] {
			tvMode.announced[p.id] = true
			if name == "" {
				seat, name = i+1, p.name
			}
		}
	}
	for id := range tvMode.announced {
		if !present[id] {
			delete(tvMode.announced, id)
		}
	}
	if name == "" || quiet || !tvModePadsFirst(c) || seat > tvMaxPlayers-1 {
		return false
	}
	ShowToast(name+" is player "+strconv.Itoa(seat)+" in games started here", ToastKindInfo)
	return true
}

// tvModePanel holds the panel's backlight off while want is true. Letting go
// only clears the hold: the backlight guard brings the light back on the next
// frame unless a sleep or the dark dock wants it off. A switch that fails is
// not tried again every two seconds (backlightOff logs each failure), only at
// the next connection, and does not hold the guard meanwhile.
func tvModePanel(want bool) {
	if !want {
		tvPanelDark.Store(false)
		tvMode.panelFail = false
		return
	}
	if tvMode.panelFail {
		return
	}
	tvPanelDark.Store(true)
	if !tvMode.cleanup {
		tvMode.cleanup = true
		Cleanup = append(Cleanup, tvModeShutdown)
	}
	if backlightIsOff.Load() {
		return
	}
	backlightOff(tvModeHoldsPanel)
	switch {
	case !backlightIsOff.Load():
		tvMode.panelFail = true
		tvPanelDark.Store(false)
	case !tvMode.panelLog:
		tvMode.panelLog = true
		logInfo("tvmode", "built-in screen's backlight off while the TV is connected")
	}
}

// tvModeShutdown gives the panel its light back at exit.
func tvModeShutdown() {
	if tvPanelDark.Swap(false) {
		backlightOn()
	}
}

// tvModeRefit notices a screen whose shape no longer matches the canvas
// JukaHub started with (a 16:9 TV under the 4:3 canvas, or the panel back
// under the wide one) and offers the restart that fixes it, with a toast once
// per size unless another toast of this round is up. A portrait framebuffer
// is a panel the display engine rotates, not a TV, and is left alone.
func tvModeRefit(ev tvEvidence, watching, quiet bool) {
	if !watching || ev.FBW <= 0 || ev.FBH <= 0 || ev.FBW < ev.FBH || screenWidth <= 0 || screenHeight <= 0 ||
		tvSizeWide(ev.FBW, ev.FBH) == tvSizeWide(int(screenWidth), int(screenHeight)) {
		tvMode.refit = ""
		return
	}
	size := tvSizeLabel(ev.FBW, ev.FBH)
	tvMode.refit = size
	if tvMode.refitSeen == size {
		return
	}
	tvMode.refitSeen = size
	logInfo("tvmode", "screen is now %s; the %dx%d canvas was made for another shape", size, screenWidth, screenHeight)
	if !quiet {
		ShowToast("The screen is now "+size+" — restart JukaHub to fit it (Settings → Display → TV mode)", ToastKindInfo)
	}
}

// ── Controllers ──────────────────────────────────────────────────────────

// tvModePadsNow lists SDL's joysticks in device order. Main thread.
func tvModePadsNow() []tvPad {
	n := sdl.NumJoysticks()
	if n <= 0 {
		return nil
	}
	out := make([]tvPad, 0, n)
	for i := 0; i < n; i++ {
		name := strings.TrimSpace(sdl.JoystickNameForIndex(i))
		out = append(out, tvPad{
			index:   i,
			id:      sdl.JoystickGetDeviceInstanceID(i),
			name:    setOr(name, "Controller"),
			builtIn: tvPadBuiltIn(name),
			ignored: tvPadIgnored(name),
		})
	}
	return out
}

// tvPadBuiltIn reports whether a joystick is the handheld's own pad: the
// firmware's uinput "TRIMUI Player1" (an Xbox 360 id, so its name tells it
// apart, not its ids), or "retrogame_joypad" on images that use that driver.
func tvPadBuiltIn(name string) bool {
	u := strings.ToUpper(name)
	return strings.Contains(u, "TRIMUI") || strings.Contains(u, "RETROGAME_JOYPAD")
}

// tvPadIgnored reports whether a joystick is a device of JukaHub's own (a
// virtual pad a feature made), which is nobody's controller.
func tvPadIgnored(name string) bool {
	return strings.Contains(strings.ToUpper(name), "JUKAHUB")
}

// tvOutsidePads is the controllers that are neither the handheld's own nor
// JukaHub's, in device order.
func tvOutsidePads(pads []tvPad) []tvPad {
	var out []tvPad
	for _, p := range pads {
		if !p.builtIn && !p.ignored {
			out = append(out, p)
		}
	}
	return out
}

// tvModePadValues is the player order for RetroArch: the outside controllers
// first, in device order, then the handheld's own pad, as
// input_playerN_joypad_index values; words says it for the log and the
// settings row. Nil when there is no outside controller.
func tvModePadValues(pads []tvPad) (values []raScopeValue, words string) {
	order := tvOutsidePads(pads)
	if len(order) == 0 {
		return nil, ""
	}
	if len(order) > tvMaxPlayers-1 {
		order = order[:tvMaxPlayers-1]
	}
	for _, p := range pads {
		if p.builtIn {
			order = append(order, p)
			break
		}
	}
	parts := make([]string, 0, len(order))
	for n, p := range order {
		key := "input_player" + strconv.Itoa(n+1) + "_joypad_index"
		values = append(values, raScopeValue{Key: key, Value: strconv.Itoa(p.index)})
		parts = append(parts, "player "+strconv.Itoa(n+1)+" "+p.name)
	}
	return values, strings.Join(parts, ", ")
}

// tvModePrepare is the launch hook: in TV mode, a RetroArch game started
// from JukaHub gets the player order for this one launch. Main thread.
func tvModePrepare(launcher string, game libraryGame) {
	if !tvMode.active || !IsTrimuiDevice() || strings.TrimSpace(game.Path) == "" {
		return
	}
	// A NextUI pak's launch.sh names a libretro core, so retroArchLauncher
	// says yes, but its minarch never reads retroarch.cfg.
	if !tvModePadsFirst(appConfig) || nextuiIsPakLauncher(launcher) || !retroArchLauncher(launcher) {
		return
	}
	if len(hotseat.players) >= 2 {
		tvMode.note = "Local play decides the player order while it has two or more players"
		return
	}
	values, words := tvModePadValues(tvModePadsNow())
	if len(values) == 0 {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates()))
	applied := 0
	for _, path := range targets {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("tvmode", "cannot set the player order in %s: %v", path, err)
			continue
		}
		applied++
	}
	if applied == 0 {
		if len(targets) == 0 {
			logInfo("tvmode", "no retroarch.cfg found for %s; RetroArch's own player order is used", filepath.Base(launcher))
		}
		tvMode.note = "Not applied last time: JukaHub could not write RetroArch's settings file"
		return
	}
	logInfo("tvmode", "%s: RetroArch player order %s (SDL device order)", filepath.Base(game.Path), words)
	tvMode.note = "Last game: " + words
}

// ── Start-time canvas (uiprofile.go) ─────────────────────────────────────

// tvModeStartProfile is the canvas TV mode wants at start: the wide one when
// a 4:3 model's display is 16:9 then (the firmware made a TV the screen).
// Decided once: a hot reload (hotreload.go) keeps the canvas the window was
// made for. Main thread, with SDL's video up.
func tvModeStartProfile(c *Config, device DeviceModel) (deviceScreenProfile, string, bool) {
	if !tvBoot.decided {
		tvBoot.decided = true
		tvBoot.wide, tvBoot.w, tvBoot.h = tvModeStartWide(c, device)
	}
	if !tvBoot.wide {
		return deviceScreenProfile{}, "", false
	}
	profile := deviceScreenProfile{
		NativeW: tvBoot.w, NativeH: tvBoot.h,
		CanvasW: uiCanvasWideWidth, CanvasH: uiCanvasWideHeight,
	}
	label := deviceDisplayName(device) + " on a " + tvSizeLabel(int(tvBoot.w), int(tvBoot.h)) + " screen (TV mode)"
	return profile, label, true
}

// tvModeStartWide reports whether the display is 16:9 at start on a model
// that otherwise gets the 4:3 canvas, with its size. Like the watch, Auto
// only acts on a model with a video out; Off never does.
func tvModeStartWide(c *Config, device DeviceModel) (bool, int32, int32) {
	profile, ok := deviceScreenProfileFor(device)
	if !ok || !profile.Compact {
		return false, 0, 0
	}
	switch tvModeSetting(c) {
	case tvModeOff:
		return false, 0, 0
	case tvModeAuto:
		if deviceHardware(device).VideoOut == "" {
			return false, 0, 0
		}
	}
	mode, err := sdl.GetCurrentDisplayMode(0)
	if err != nil || !tvSizeWide(int(mode.W), int(mode.H)) {
		return false, 0, 0
	}
	logInfo("tvmode", "screen is %dx%d at start: the wide canvas instead of the 4:3 one", mode.W, mode.H)
	return true, mode.W, mode.H
}

// ── Drivers page (devicedrivers.go) ──────────────────────────────────────

// tvModeDriverStatus is the Video out row: a fresh reading of the status
// files. Off the render thread, under the page's deadline.
func tvModeDriverStatus(videoOut string) driverStatus {
	st := driverStatus{State: driverUnknown, Driver: videoOut, Detail: "the firmware drives the port"}
	if !tvProbeSupported {
		return st
	}
	ev := tvProbeSystem(false)
	switch {
	case ev.connected():
		st.State = driverOK
		st.Detail = "display connected: " + strings.Join(ev.Sources, ", ")
	case len(ev.Paths) > 0:
		st.Detail = "nothing connected on " + strings.Join(ev.Paths, ", ") + " (not verified with a TV yet)"
	default:
		st.Detail = "no video-out status file found; the firmware drives the port"
	}
	if ev.FBW > 0 && ev.FBH > 0 {
		st.Node = "fb0 " + tvSizeLabel(ev.FBW, ev.FBH)
	}
	return st
}

// ── Settings → Display rows ──────────────────────────────────────────────

// tvModeSettingsRows are TV mode's rows on the Display page.
func tvModeSettingsRows() []setRow {
	modes := []setOption{
		{Value: tvModeAuto, Label: "Auto"},
		{Value: tvModeOn, Label: "On"},
		{Value: tvModeOff, Label: "Off"},
	}
	sizes := []setOption{
		{Value: "1.15", Label: "115%"},
		{Value: "1.3", Label: "130%"},
		{Value: "1.45", Label: "145%"},
	}
	// on: TV mode can turn on here — On anywhere, Auto only on a model with a
	// video out (on a Smart Pro, Auto never watches, so its rows mean nothing).
	on := func(c *Config) bool {
		switch tvModeSetting(c) {
		case tvModeOn:
			return true
		case tvModeAuto:
			return tvModeHasVideoOut()
		}
		return false
	}
	onHandheld := func(c *Config) bool { return on(c) && IsTrimuiDevice() }
	return []setRow{
		{
			ID: tvModeKey, Label: "TV mode", Kind: setKindChoice,
			Detail:  tvModeDetail,
			Options: func(*Config) []setOption { return modes },
			Current: func(c *Config) int {
				mode := tvModeSetting(c)
				for i, o := range modes {
					if o.Value == mode {
						return i
					}
				}
				return -1
			},
			Value: func(c *Config) string { return tvModeLabel(tvModeSetting(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, tvModeKey, value)
				tvModeApplyMain(c)
				setToast("TV mode: " + tvModeLabel(value))
			},
		},
		{
			ID: "tvModeConnection", Label: "TV connection", Kind: setKindInfo,
			Detail:  tvModeConnectionDetail,
			Visible: onHandheld,
			Value:   tvModeConnectionLabel,
		},
		{
			ID: tvModeTextKey, Label: "TV text size", Kind: setKindChoice, Clamp: true,
			Detail:  setStatic("How much larger text is in TV mode; your own Text size stays as it is"),
			Visible: on,
			Options: func(*Config) []setOption { return sizes },
			Current: func(c *Config) int {
				f := tvModeTextFactor(c)
				for i, o := range sizes {
					if v, err := strconv.ParseFloat(o.Value, 64); err == nil && math.Abs(v-f) < 0.001 {
						return i
					}
				}
				return -1
			},
			Value: func(c *Config) string { return strconv.Itoa(int(math.Round(tvModeTextFactor(c)*100))) + "%" },
			Choose: func(c *Config, value string) {
				setStoreString(c, tvModeTextKey, value)
				tvModeApplyMain(c)
				setToast("TV text size: " + strconv.Itoa(int(math.Round(tvModeTextFactor(c)*100))) + "%")
			},
		},
		{
			ID: tvModePadsKey, Label: "Controller is player 1", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if tvMode.note != "" && tvModePadsFirst(c) {
					return tvMode.note
				}
				return "In TV mode a USB or Bluetooth controller is player 1 in RetroArch games, the handheld's own buttons the next player"
			},
			Visible: onHandheld,
			Get:     tvModePadsFirst,
			Set: func(c *Config, v bool) {
				setStoreBool(c, tvModePadsKey, v)
				setToast("Controller is player 1: " + setOnOff(v))
			},
		},
		{
			ID: "tvModePair", Label: "Pair a controller", Kind: setKindAction,
			Detail:  tvModePairDetail,
			Visible: onHandheld,
			Open:    btScreenOpen,
		},
		{
			ID: tvModePanelKey, Label: "Built-in screen off on the TV", Kind: setKindToggle,
			Detail:  setStatic("Switches the handheld's backlight off while a TV is connected. Not tried on a Brick Pro yet: if the TV goes dark too, turn this off"),
			Visible: func(c *Config) bool { return on(c) && tvModeHasVideoOut() },
			Get:     tvModePanelOff,
			Set: func(c *Config, v bool) {
				setStoreBool(c, tvModePanelKey, v)
				tvModeApplyMain(c)
				setToast("Built-in screen off on the TV: " + setOnOff(v))
			},
		},
		{
			ID: "tvModeRestart", Label: "Restart to fit the screen", Kind: setKindAction,
			Detail: func(*Config) string {
				if recoveryRestartKind() == "" {
					return "JukaHub closes; start it again to draw for the " + tvMode.refit + " screen"
				}
				return "JukaHub starts again drawn for the " + tvMode.refit + " screen"
			},
			Visible: func(c *Config) bool { return on(c) && tvMode.refit != "" },
			Open:    func(*Config) { recoveryRestartApp() },
		},
	}
}

// tvModeDetail is the TV mode row's line: what the setting does here, now.
func tvModeDetail(c *Config) string {
	mode := tvModeSetting(c)
	switch {
	case mode == tvModeOff:
		return "Off: a TV shows the handheld's own interface"
	case tvMode.active && tvMode.detected:
		return "On: a TV is connected"
	case mode == tvModeOn:
		return "On: larger text, and a connected controller is player 1"
	case tvModeHasVideoOut():
		return "Turns on when a TV is connected to the USB-C port (not verified on a Brick Pro yet)"
	}
	return "This model has no video out; On shows the TV interface here"
}

// tvModeConnectionLabel is the TV connection row's read-out.
func tvModeConnectionLabel(*Config) string {
	ev, stable, ever := tvProbeSnapshot()
	switch {
	case !ever:
		return "Not checked yet"
	case stable:
		return "Connected"
	case ev.connected():
		return "Checking…"
	case len(ev.Paths) == 0:
		return "Unknown"
	}
	return "Not connected"
}

// tvModeConnectionDetail is the TV connection row's line: what said so.
func tvModeConnectionDetail(*Config) string {
	ev, _, ever := tvProbeSnapshot()
	switch {
	case !ever:
		return "Read every 2 s from the kernel's video-out status files"
	case ev.connected():
		return "Seen on " + strings.Join(ev.Sources, ", ")
	case len(ev.Paths) > 0:
		return "Watching " + strings.Join(ev.Paths, ", ")
	}
	return "This firmware has no video-out status file JukaHub knows; the log lists what it has"
}

// tvModePairDetail is the Pair a controller row's line: the controllers
// connected now.
func tvModePairDetail(*Config) string {
	var names []string
	for _, p := range tvOutsidePads(tvModePadsNow()) {
		names = append(names, p.name)
	}
	if len(names) == 0 {
		return "No controller connected yet: opens Bluetooth to pair one (a USB pad just plugs in)"
	}
	return "Connected: " + strings.Join(names, ", ")
}
