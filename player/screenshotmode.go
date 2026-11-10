package main

// screenshotmode.go — a capture session whose pictures are safe to publish.
//
// `JukaHub --screenshot-mode` (or JUKAHUB_SCREENSHOT_MODE=1) runs the app as
// usual, but draws what a README or store screenshot should show instead of
// what identifies the person taking it:
//
//   - the hints name the pad's buttons, as if a controller were connected,
//     whatever is plugged in (inputHintsTick);
//   - every clock face and the dock's date read 9:41 on Saturday 14 November,
//     the greeting follows that hour (clockformat.go, defaultGreeting), and the
//     dock's next alarm reads 10:45 (saverDrawAlarmChip);
//   - no weather is drawn: the header chip and the dock chip are gone and the
//     backdrop has no forecast to follow (weatherDayFor, ambientWeather);
//   - the LAN address the app reports reads 192.168.1.42 (localIPv4), and the
//     pairing QR encodes that address with PIN 123456 (companionURL);
//   - the Wi-Fi name on the dock and in Tools reads "HomeNet"
//     (saverWifiLabel, toolsHubWiFiSSID);
//   - the greeting carries no user name; no wallpaper, update offer, toast,
//     first-run tour, handoff card or headset name is drawn; Media leaves out
//     the recent searches; the dock shows no chat preview, photo or lock
//     picture; and the update check is off.
//
// What it does not hide: the Continue card and Media's rows show the watch
// history. Screens the tour does not open read the system directly and still
// show real values — the Wi-Fi screen and its details sheets (network names,
// addresses), the Hardware monitor and network tools (interface addresses),
// the starfield screensaver's lock picture — so do not capture those. Watch
// Party, netplay hosting, Remote Cast and the FTP/SFTP panels advertise the
// stand-in address and cannot be joined during a screenshot session.
//
// On the desktop the window is set to the canvas size, windowed, when the
// session starts, so a picture is one frame of the 1280x720 canvas. F12 and
// Start → Screenshot save the next frame as a PNG into --screenshot-dir /
// JUKAHUB_SCREENSHOT_DIR (default Pictures\JukaHub), with the success chime
// instead of a toast.
//
// `JukaHub --screenshot-tour <dir>` turns the mode on, opens the screens the
// README shows one after another, writes home.png, media.png, settings.png and
// ambient.png into <dir>, and quits.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// The stand-ins screenshot mode draws in place of the real values.
const (
	shotLANAddress = "192.168.1.42"
	shotPairPIN    = "123456"
	shotSSID       = "HomeNet"
	// shotNowSlack is how close to the real clock a time has to be to count
	// as "now": a clock face formats the frame's own time.Now().
	shotNowSlack = 5 * time.Second
)

// shotOptions is what the command line and the environment asked for.
type shotOptions struct {
	on      bool
	dir     string // where F12 captures go
	tourDir string // set when --screenshot-tour asked for the walk
}

// shotMode is this run's screenshot mode. init writes it once, before any
// goroutine exists, so readers on any goroutine need no lock.
var shotMode shotOptions

func init() {
	// A test binary never takes the mode from a developer's environment.
	if testing.Testing() {
		return
	}
	opts := shotParseOptions(os.Args[1:], os.Getenv)
	if !opts.on {
		return
	}
	if opts.tourDir != "" {
		if abs, err := filepath.Abs(opts.tourDir); err == nil {
			opts.tourDir = abs
		}
	}
	if opts.dir == "" {
		opts.dir = shotDefaultDir()
	}
	shotMode = opts
	// A release offer is a pill on Home and a prompt over it: neither belongs
	// in a screenshot, and a capture session must not install anything.
	_ = os.Setenv("JUKAHUB_NO_AUTOUPDATE", "1")
}

// shotParseOptions reads the flags and the environment. It is pure so the
// rules are testable: --screenshot-mode (or --screenshot-mode=false),
// --screenshot-tour <dir> and --screenshot-dir <dir> (each with one or two
// dashes, the value also as =<dir>), JUKAHUB_SCREENSHOT_MODE=1 and
// JUKAHUB_SCREENSHOT_DIR. A tour with no folder named writes into
// "screenshots".
func shotParseOptions(args []string, getenv func(string) string) shotOptions {
	var o shotOptions
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		switch name {
		case "screenshot-mode":
			o.on = !hasValue || shotTruthy(value)
		case "screenshot-tour", "screenshot-dir":
			if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				value = args[i+1]
				i++
			}
			value = strings.TrimSpace(value)
			if name == "screenshot-dir" {
				o.dir = value
				continue
			}
			o.on = true
			o.tourDir = value
			if o.tourDir == "" {
				o.tourDir = "screenshots"
			}
		}
	}
	if shotTruthy(getenv("JUKAHUB_SCREENSHOT_MODE")) {
		o.on = true
	}
	if o.dir == "" {
		o.dir = strings.TrimSpace(getenv("JUKAHUB_SCREENSHOT_DIR"))
	}
	return o
}

// shotTruthy reads an on/off value the way the other JUKAHUB_* switches do.
func shotTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// shotDefaultDir is where F12 captures go when no folder is named: the
// Pictures\JukaHub folder the BMP screenshots use.
func shotDefaultDir() string {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, "Pictures", "JukaHub")
}

// shotModeOn reports whether this run is a screenshot session.
func shotModeOn() bool { return shotMode.on }

// shotClock is the time a screenshot shows for t: 9:41 on Saturday
// 14 November when t is the present moment (a clock face, the dock's date
// line), t itself for any other moment, and always t outside screenshot mode.
func shotClock(t time.Time) time.Time {
	if !shotMode.on {
		return t
	}
	return shotClockFor(t, time.Now(), true)
}

// shotClockFor is shotClock with the present moment and the mode passed in,
// so the rule is testable.
func shotClockFor(t, now time.Time, on bool) time.Time {
	if !on {
		return t
	}
	if d := now.Sub(t); d > shotNowSlack || d < -shotNowSlack {
		return t
	}
	return time.Date(2026, time.November, 14, 9, 41, 0, 0, t.Location())
}

// ── The capture tick ─────────────────────────────────────────────────────

// shotState is the mode's own bookkeeping. Render thread only.
var shotState struct {
	started    bool
	lastManual time.Time
	manualN    int
}

// shotTick runs once per frame, after everything is drawn and before it is
// presented (capturePendingScreenshot): it sizes the window once and drives the
// tour.
func shotTick(renderer *sdl.Renderer) {
	if !shotMode.on || renderer == nil {
		return
	}
	now := time.Now()
	if !shotState.started {
		shotState.started = true
		shotFitWindow()
		logInfo("screenshot", "mode on: pad hints, clock 9:41, no weather, LAN %s, Wi-Fi %q; F12 saves PNGs to %s",
			shotLANAddress, shotSSID, shotMode.dir)
		if shotMode.tourDir != "" {
			shotTour.steps = shotTourSteps()
			shotTour.phase = shotPhaseBoot
			shotTour.at = now
			logInfo("screenshot", "tour: %d screens into %s", len(shotTour.steps), shotMode.tourDir)
		}
	}
	if shotMode.tourDir == "" || shotTour.phase == shotPhaseDone {
		return
	}
	c := appConfig
	if c == nil {
		return
	}
	// The dock and the screensaver wait for the user to go idle; a tour that
	// is still walking is not idle.
	noteUserInputTime(now)
	shotTourAdvance(renderer, c, now)
}

// shotFitWindow puts the desktop window at the canvas size, windowed, so a
// capture is one frame of the canvas pixel for pixel. The saved fullscreen
// choice is not touched (applyFullscreen with no config). The handheld's
// window is its panel and is left alone.
func shotFitWindow() {
	if mainWindow == nil || !P().ResizableDefault() {
		return
	}
	applyFullscreen(nil, false)
	mainWindow.SetSize(screenWidth, screenHeight)
}

// ── The tour ─────────────────────────────────────────────────────────────

// shotStep is one screen of the tour.
type shotStep struct {
	file   string               // written into the tour folder
	settle time.Duration        // how long the screen loads before the capture
	enter  func(c *Config) bool // puts the screen up; false skips a screen this config lacks
	ready  func(c *Config) bool // the screen is up and no transition is running
	arrive func(c *Config)      // runs once, when ready first holds
	leave  func(c *Config)      // runs after the capture
}

// The tour's phases.
const (
	shotPhaseBoot = iota
	shotPhaseEnter
	shotPhaseWait
	shotPhaseSettle
	shotPhaseDone
)

// shotTourBoot is how long the tour lets the first screen load (the pairing
// QR, the Continue thumbnail, fonts) before it starts.
const shotTourBoot = 6 * time.Second

// shotBusyLimit is how much longer than its settle time a screen may keep its
// loading card up before it is captured anyway.
const shotBusyLimit = 20 * time.Second

// shotTour is the walk's progress. Render thread only.
var shotTour struct {
	steps   []shotStep
	i       int
	phase   int
	at      time.Time
	retried bool
	wrote   int
}

// shotTourSteps lists the README's screens, in the order they are taken.
func shotTourSteps() []shotStep {
	return []shotStep{
		{file: "home.png", settle: 3 * time.Second, enter: shotEnterHome, ready: shotOnHome, arrive: shotFocusContinue},
		{file: "media.png", settle: 12 * time.Second, enter: shotEnterMedia, ready: shotOnMedia, arrive: shotMediaForYou},
		{file: "settings.png", settle: 3 * time.Second, enter: shotEnterSettings, ready: shotOnSettings},
		{file: "ambient.png", settle: 4 * time.Second, enter: shotEnterDock, ready: shotOnDock, leave: shotLeaveDock},
	}
}

// shotEnterHome goes back to Home.
func shotEnterHome(c *Config) bool {
	return shotGoScene(c, homeSceneIndex(c))
}

// shotOnHome reports whether Home is up.
func shotOnHome(c *Config) bool {
	return shotOnScene(c, homeSceneIndex(c))
}

// shotFocusContinue puts the focus ring on the Continue card, the one the
// Home screenshot is about.
func shotFocusContinue(*Config) {
	if homeLayout != nil && homeLayout.Focus != nil {
		homeLayout.Focus.Set(HomeFocusContinue)
	}
}

// shotEnterMedia opens Media.
func shotEnterMedia(c *Config) bool {
	return shotGoScene(c, sceneIndexExact(c, mediaHubSceneName))
}

// shotOnMedia reports whether Media is up.
func shotOnMedia(c *Config) bool {
	return shotOnScene(c, sceneIndexExact(c, mediaHubSceneName))
}

// shotMediaForYou shows Media's first tab, whatever tab was left open.
func shotMediaForYou(c *Config) {
	mediaHubSelectTab(c, mediaTabForYou, false)
}

// shotEnterSettings opens Settings on Display & theme: the hub opens on the
// section it showed last (setHubEnter), with the rail focused.
func shotEnterSettings(c *Config) bool {
	if i := setSectionIndex("display"); i >= 0 {
		setHub.lastSection = i
	}
	return shotGoScene(c, sceneIndexExact(c, setHubSceneNames[0]))
}

// shotOnSettings reports whether Settings is up.
func shotOnSettings(c *Config) bool {
	return shotOnScene(c, sceneIndexExact(c, setHubSceneNames[0]))
}

// shotEnterDock opens the dock screen over whatever scene is up.
func shotEnterDock(c *Config) bool {
	ambientEnter(c)
	return true
}

// shotOnDock reports whether the dock is up.
func shotOnDock(*Config) bool {
	return ambientIsActive() && transitionPhase == "none"
}

// shotLeaveDock closes the dock again.
func shotLeaveDock(*Config) {
	ambientLeave()
}

// shotGoScene starts the switch to scene idx; nothing to do when it is already
// up. False when the config has no such scene.
func shotGoScene(c *Config, idx int) bool {
	if c == nil || idx < 0 || idx >= len(c.Scenes) {
		return false
	}
	if currentSceneIndex != idx {
		changeSceneTo(c, idx)
	}
	return true
}

// shotOnScene reports whether scene idx is on screen with no transition
// running and nothing drawn over it.
func shotOnScene(c *Config, idx int) bool {
	return c != nil && idx >= 0 && currentSceneIndex == idx && transitionPhase == "none" && !ambientIsActive()
}

// shotTourAdvance moves the tour on by one frame.
func shotTourAdvance(renderer *sdl.Renderer, c *Config, now time.Time) {
	switch shotTour.phase {
	case shotPhaseBoot:
		if now.Sub(shotTour.at) >= shotTourBoot {
			shotTour.phase = shotPhaseEnter
		}
	case shotPhaseEnter:
		if shotTour.i >= len(shotTour.steps) {
			logInfo("screenshot", "tour: wrote %d of %d screens to %s",
				shotTour.wrote, len(shotTour.steps), shotMode.tourDir)
			shotTour.phase = shotPhaseDone
			requestAppExit()
			return
		}
		step := shotTour.steps[shotTour.i]
		if !step.enter(c) {
			logWarn("screenshot", "tour: %s skipped, this config has no such screen", step.file)
			shotTour.i++
			return
		}
		shotTour.phase, shotTour.at, shotTour.retried = shotPhaseWait, now, false
	case shotPhaseWait:
		step := shotTour.steps[shotTour.i]
		if step.ready(c) {
			if step.arrive != nil {
				step.arrive(c)
			}
			shotTour.phase, shotTour.at = shotPhaseSettle, now
			return
		}
		waited := now.Sub(shotTour.at)
		if waited >= 3*time.Second && !shotTour.retried {
			// A switch asked for while another transition ran is dropped
			// (switchScene); ask once more.
			shotTour.retried = true
			step.enter(c)
		}
		if waited >= 15*time.Second {
			logWarn("screenshot", "tour: %s never came up, skipped", step.file)
			shotTourNext(c)
		}
	case shotPhaseSettle:
		step := shotTour.steps[shotTour.i]
		settled := now.Sub(shotTour.at)
		if settled < step.settle || transitionPhase != "none" {
			return
		}
		if !step.ready(c) {
			// Something else took the screen while it loaded (an alarm, a
			// reload): open it again rather than save the wrong picture.
			shotTour.phase, shotTour.at, shotTour.retried = shotPhaseWait, now, false
			return
		}
		if shotBusy(c) && settled < step.settle+shotBusyLimit {
			return
		}
		path := filepath.Join(shotMode.tourDir, step.file)
		if err := shotWriteFrame(renderer, path); err != nil {
			logWarn("screenshot", "tour: %s: %v", step.file, err)
		} else {
			shotTour.wrote++
			logInfo("screenshot", "tour: wrote %s", path)
		}
		shotTourNext(c)
	}
}

// shotBusy reports whether the loading card is up (a feed or a video still
// loading), which a capture waits out.
func shotBusy(c *Config) bool {
	return c.Variables.LoadingSpinner || videoPrepareActive()
}

// shotTourNext closes the current screen and moves on to the next one.
func shotTourNext(c *Config) {
	if step := shotTour.steps[shotTour.i]; step.leave != nil {
		step.leave(c)
	}
	shotTour.i++
	shotTour.phase = shotPhaseEnter
}

// ── Saving a frame ───────────────────────────────────────────────────────

// shotSaveManual is F12 / Start → Screenshot in screenshot mode: the frame
// just drawn, as a PNG in the capture folder. No toast is drawn in this mode,
// so the chime and the log say how it went.
func shotSaveManual(renderer *sdl.Renderer) {
	now := time.Now()
	if now.Sub(shotState.lastManual) < time.Second {
		return
	}
	shotState.lastManual = now
	path := ""
	for tries := 0; tries < 999; tries++ {
		shotState.manualN++
		candidate := filepath.Join(shotMode.dir, fmt.Sprintf("jukahub-%02d-%s.png", shotState.manualN, shotSceneSlug()))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			path = candidate
			break
		}
	}
	if path == "" {
		PlayErrorSound()
		logWarn("screenshot", "not saved: no free file name in %s", shotMode.dir)
		return
	}
	if err := shotWriteFrame(renderer, path); err != nil {
		PlayErrorSound()
		logWarn("screenshot", "not saved: %v", err)
		return
	}
	PlaySuccessSound()
	logInfo("screenshot", "saved %s", path)
}

// shotSceneSlug names the screen on display for a capture's file name.
func shotSceneSlug() string {
	if ambientIsActive() {
		return "dock"
	}
	c := appConfig
	if c == nil || currentSceneIndex < 0 || currentSceneIndex >= len(c.Scenes) {
		return "screen"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(c.Scenes[currentSceneIndex].Name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "screen"
	}
	return b.String()
}

// shotWriteFrame saves the frame drawn this tick as a PNG at path. It reads
// the window's output, which is the canvas size once shotFitWindow has run.
func shotWriteFrame(renderer *sdl.Renderer, path string) error {
	w, h, err := renderer.GetOutputSize()
	if err != nil {
		return err
	}
	if w <= 0 || h <= 0 {
		return errors.New("the window has no size")
	}
	surface, err := sdl.CreateRGBSurfaceWithFormat(0, w, h, 32, sdl.PIXELFORMAT_ARGB8888)
	if err != nil {
		return err
	}
	defer surface.Free()
	if err := surface.Lock(); err != nil {
		return err
	}
	readErr := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&surface.Pixels()[0]), int(surface.Pitch))
	pitch := int(surface.Pitch)
	pixels := append([]byte(nil), surface.Pixels()...)
	surface.Unlock()
	if readErr != nil {
		return readErr
	}
	// The back buffer's alpha is whatever the last blend left: a picture is
	// opaque. ARGB8888 is stored B, G, R, A in memory.
	for y := 0; y < int(h); y++ {
		row := pixels[y*pitch : y*pitch+int(w)*4]
		for x := 3; x < len(row); x += 4 {
			row[x] = 0xff
		}
	}
	data, err := encodeScreenshotPNG(pixels, w, h, pitch)
	if err != nil {
		return err
	}
	if w != screenWidth || h != screenHeight {
		logInfo("screenshot", "%s is %dx%d, not the %dx%d canvas: the window is scaled",
			filepath.Base(path), w, h, screenWidth, screenHeight)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
