package main

// stillframes.go — still frames: an idle Home is drawn only when something
// on it changes.
//
// Home left alone used to be redrawn 15 to 24 times a second (adaptive.go's
// idle cadences) although nothing on it moved, and on a handheld every one of
// those frames is battery. Once Home has been left alone for
// stillAfterInputMS and nothing on it has changed for stillLingerMS, the main
// loop stops drawing: the gate below (called just before the loop reads its
// events) sleeps in short steps instead, and the next frame is drawn the
// moment
//
//   - a press, a key, a click, a pad plugged in, a deliberate stick push or a
//     window event is queued (stick drift is not a press);
//   - the phone companion, a tool screen's goroutine (tbxPost) or a toast has
//     something to show, the power key asks for its card, or the app is asked
//     to quit;
//   - the clock reaches the next minute, or anything calls stillWake;
//   - and at the latest one second after the last frame, on the second, so a
//     clock with seconds still ticks and nothing on Home is ever more than a
//     second old (the safety redraw).
//
// It stays out of the way of everything that moves by itself: a transition,
// an overlay sliding in, the focus ring gliding, a toast, a video, a moving
// backdrop or animated wallpaper (they ask for frames, adaptive.go), the
// screensaver and the dock (saver_power.go has their own low-power state),
// music playing, a screen shared over VNC or the companion, an open panel,
// sheet or keyboard, the first-run tour, and the start-up's own work.
//
// Settings → Power & battery → "Still frames" (stillFramesKey, on by default)
// and JUKAHUB_STILL_FRAMES=0 turn it off. powercheck.go measures what it saves.
//
// Nothing here was run on a device from this desk.

import (
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	stillFramesKey = "stillFrames"
	// stillAfterInputMS: Home is left alone this long before frames stop.
	stillAfterInputMS = 2000
	// stillLingerMS: a change on screen keeps frames coming this long (its
	// fade or slide finishes, a cover that loaded is shown).
	stillLingerMS = 1200
	// stillSafety is the longest Home goes without a frame.
	stillSafety = time.Second
	// stillPollMS is how often the quiet sleep looks for a press.
	stillPollMS = uint32(12)
	// stillStickPush is how far a stick must be pushed to count as a press.
	stillStickPush = 16000
)

// still is the gate's state. Main thread, except the atomics.
var still struct {
	// drawNext: the last gate call slept and woke; this iteration draws.
	drawNext bool
	// lastDrawn is when the last frame was presented; changedAt when the
	// screen's signature last changed; sig is that signature.
	lastDrawn time.Time
	changedAt time.Time
	sig       stillSignature
	// lastToast is the newest toast's start when the last frame was drawn.
	lastToast uint64
	animating bool
	// quiet counts the quiet sleeps of this run, for the log line.
	quiet int
	// published is when the setting was last copied for stillFramesEnabled.
	published time.Time
}

var (
	// stillDirty is set by stillWake: something off the main thread changed
	// what Home shows.
	stillDirty atomic.Bool
	// stillPresented counts the frames presented (powercheck.go reads it).
	stillPresented atomic.Uint64
	// stillForce is the power check's override: 1 forces still frames on, -1
	// off, 0 follows the setting.
	stillForce atomic.Int32
	// stillOnSetting mirrors the setting for any goroutine.
	stillOnSetting atomic.Bool
	// stillEnvOff is JUKAHUB_STILL_FRAMES=0, read once.
	stillEnvOff = func() bool {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_STILL_FRAMES")))
		return v == "0" || v == "false" || v == "off" || v == "no"
	}()
)

func init() { stillOnSetting.Store(true) }

// stillSignature is what Home shows that can change without a press.
type stillSignature struct {
	scene  int
	tab    string
	minute int
	phase  string
}

// stillWake asks for a frame now. Any goroutine.
func stillWake() { stillDirty.Store(true) }

// stillNotePresented is the main loop's word, right after Present.
func stillNotePresented() {
	still.lastDrawn = time.Now()
	stillPresented.Add(1)
	still.lastToast = stillToastStart()
}

// stillNoteAnimating is the main loop's word on whether the frame just drawn
// was animating (the same answer the adaptive frame rate gets).
func stillNoteAnimating(animating bool) { still.animating = animating }

// stillFramesEnabled is the setting (and the environment's veto, and the
// power check's override).
func stillFramesEnabled() bool {
	switch stillForce.Load() {
	case 1:
		return true
	case -1:
		return false
	}
	return !stillEnvOff && stillOnSetting.Load()
}

// stillFramesSetting reads the setting from the config. Main thread.
func stillFramesSetting(c *Config) bool {
	if c == nil {
		return true
	}
	v, ok := customValue(c.Variables.Custom, stillFramesKey)
	if !ok {
		return true
	}
	on, ok := v.(bool)
	if ok {
		return on
	}
	s, _ := v.(string)
	return !strings.EqualFold(strings.TrimSpace(s), "false")
}

// stillFrameGate is the main loop's call before it reads its events. While
// Home is quiet it sleeps until something asks for a frame (at most until
// the safety redraw) and returns true: the loop starts over, so the frame
// that follows is timed from its own start, and draws. ready is false until
// the start-up's own work is done. Main thread.
func stillFrameGate(config *Config, ready bool) bool {
	now := time.Now()
	if now.Sub(still.published) >= time.Second {
		still.published = now
		stillFramesPublish(config)
	}
	powerCheckTick(config, now)
	if still.drawNext {
		still.drawNext = false
		return false
	}
	if !ready || !stillFramesEnabled() || !stillQuiet(config, now) {
		return false
	}
	deadline := still.lastDrawn.Add(stillSafety)
	// On the second, so a clock with seconds ticks on time.
	if next := now.Truncate(time.Second).Add(time.Second); next.Before(deadline) {
		deadline = next
	}
	if !now.Before(deadline) {
		return false
	}
	still.quiet++
	if still.quiet == 1 {
		logInfo("power", "still frames: Home is quiet, drawn when it changes (at least once a second)")
	}
	minute := now.Minute()
	for {
		stallWatchBeat()
		sdl.Delay(stillPollMS)
		sdl.PumpEvents()
		t := time.Now()
		if !t.Before(deadline) || t.Minute() != minute || stillWakeNow() {
			still.drawNext = true
			return true
		}
	}
}

// stillQuiet reports Home left alone with nothing on it moving. Main thread.
func stillQuiet(config *Config, now time.Time) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex != homeSceneIndex(config) {
		return false
	}
	sig := stillSignature{scene: currentSceneIndex, tab: homeTabs.cur, minute: now.Minute(), phase: transitionPhase}
	if sig != still.sig {
		still.sig, still.changedAt = sig, now
		return false
	}
	switch {
	case now.Sub(still.changedAt) < stillLingerMS*time.Millisecond,
		still.lastDrawn.IsZero(),
		sdl.GetTicks64()-frameRateCtrl.LastInput() < stillAfterInputMS:
		return false
	case still.animating, uiAnimating(), transitionPhase != "none", html5GameActive():
		return false
	case ambientIsActive(), ss.active, voiceOrbActive(), saverToastShowing():
		return false
	case onboarding.active, tbxSheet.open, virtualKeyboardActive, recoveryCard.open:
		return false
	case quickMusicPlaying(), castIsEnabled(), vncSrv.Running():
		return false
	}
	if video, moving := frameRateCtrl.stillBlockers(); video || moving {
		return false
	}
	return modalPanelBlockReason() == ""
}

// stillWakeNow reports something that wants a frame now. Main thread; the
// caller has pumped SDL's queue.
func stillWakeNow() bool {
	if stillDirty.Swap(false) || exitRequested.Load() || powerKeyCardRequested.Load() {
		return true
	}
	if saverPressQueued() || sdl.HasEvent(sdl.TEXTINPUT) || sdl.HasEvent(sdl.WINDOWEVENT) || sdl.HasEvent(sdl.DROPFILE) {
		return true
	}
	for _, t := range stillDeviceEvents {
		if sdl.HasEvent(t) {
			return true
		}
	}
	if !P().IsHandheld() && sdl.HasEvent(sdl.MOUSEMOTION) {
		return true // a pointer over a desktop window: hover states
	}
	if stillStickPushed() {
		return true
	}
	if stillToastStart() != still.lastToast {
		return true
	}
	companionQueue.mu.Lock()
	phone := len(companionQueue.items) > 0
	companionQueue.mu.Unlock()
	if phone {
		return true
	}
	tbxUIQueue.mu.Lock()
	posted := len(tbxUIQueue.fns) > 0
	tbxUIQueue.mu.Unlock()
	return posted
}

// stillDeviceEvents are a pad or keyboard plugged in or out: the hints
// change at once.
var stillDeviceEvents = []uint32{
	uint32(sdl.JOYDEVICEADDED), uint32(sdl.JOYDEVICEREMOVED),
	uint32(sdl.CONTROLLERDEVICEADDED), uint32(sdl.CONTROLLERDEVICEREMOVED),
}

// stillStickPeek holds the queued stick motions stillStickPushed looks at.
var stillStickPeek [16]sdl.Event

// stillStickPushed reports a stick pushed well past drift in the queue.
func stillStickPushed() bool {
	n, err := sdl.PeepEvents(stillStickPeek[:], sdl.PEEKEVENT, sdl.CONTROLLERAXISMOTION, sdl.CONTROLLERAXISMOTION)
	if err != nil {
		return false
	}
	for i := 0; i < n; i++ {
		if e, ok := stillStickPeek[i].(*sdl.ControllerAxisEvent); ok && (e.Value > stillStickPush || e.Value < -stillStickPush) {
			return true
		}
	}
	return false
}

// stillToastStart is the newest toast's start tick (0 without one).
func stillToastStart() uint64 {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	return currentToast.StartedAt
}

// stillBlockers reports a playing video and a moving backdrop in the last
// frame (they ask for frames of their own).
func (fc *FrameRateController) stillBlockers() (video, moving bool) {
	if fc == nil {
		return false, false
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.videoActive, fc.backdropMS > 0
}

// stillFramesSettingsRow is Settings → Power & battery → Still frames.
func stillFramesSettingsRow() setRow {
	return setRow{
		ID: stillFramesKey, Label: "Still frames", Kind: setKindToggle,
		Detail: func(c *Config) string {
			switch {
			case stillEnvOff:
				return "Off for this start (JUKAHUB_STILL_FRAMES=0)"
			case !stillFramesSetting(c):
				return "Off: Home is redrawn many times a second even when nothing on it changes"
			}
			return "Home left alone is drawn only when something on it changes, at least once a second"
		},
		Get: stillFramesSetting,
		Set: func(c *Config, on bool) {
			setStoreBool(c, stillFramesKey, on)
			stillOnSetting.Store(on)
			setToast("Still frames: " + setOnOff(on))
		},
	}
}

// stillFramesPublish copies the setting for the gate (startup, hot reload).
func stillFramesPublish(c *Config) { stillOnSetting.Store(stillFramesSetting(c)) }
