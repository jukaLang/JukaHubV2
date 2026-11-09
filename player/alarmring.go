package main

// alarmring.go — a firing alarm taking the whole screen.
//
// A clock that rings under another window is not an alarm, so a due alarm owns
// the screen until someone snoozes or dismisses it: a red wash that blinks once
// a second over everything — the scene, every panel, the idle screens and,
// on the handheld, a game paused under it (alarmvol.go) — the time it is now,
// the alarm's own label and time, and two big glass buttons that are the only
// things the pad, the keyboard and a finger can press while it rings.
//
// The sound is the alarm player (alarmtone.go): the alarm's ringtone looped,
// fading in from a fifth of full scale over five seconds, with the mixer raised
// to 100% for the ring and put back afterwards. A ring nobody answers stops
// itself after alarmRingTimeout and is left in the notification centre.

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// alarmSnoozeMinutes is how long Snooze holds the alarm off. Nine is the
	// classic mechanical-alarm snooze, and long enough to be worth pressing.
	alarmSnoozeMinutes = 9
	// alarmRingTimeout ends a ring nobody answers: an empty room should not
	// hear a phone-loud alarm all morning, nor the battery pay for it.
	alarmRingTimeout = 10 * time.Minute
)

// ── Firing state ─────────────────────────────────────────────────────────

type alarmRingState struct {
	mu      sync.Mutex
	active  bool
	entry   alarmEntry
	started time.Time
	snoozes int
	focus   int // 0 = snooze, 1 = dismiss
	// snoozeUntil is when the last snooze re-rings: the count is kept for
	// that re-ring, and a later ring of the same alarm starts from zero.
	snoozeUntil time.Time
}

var alarmRing alarmRingState

// Button rects, written by the renderer and read by the mouse handler, so a tap
// lands on what the frame actually drew.
var (
	alarmRingSnoozeRect  sdl.Rect
	alarmRingDismissRect sdl.Rect
)

// alarmFiringActive reports whether the ring overlay is on screen.
func alarmFiringActive() bool {
	alarmRing.mu.Lock()
	defer alarmRing.mu.Unlock()
	return alarmRing.active
}

// alarmBegin takes over the screen for a. It refuses when an alarm is already
// ringing: two takeovers fighting over the screen would make both unreadable
// (alarmTrigger hands the running ring to the newer alarm instead).
func alarmBegin(a alarmEntry) bool {
	alarmRing.mu.Lock()
	defer alarmRing.mu.Unlock()
	if alarmRing.active {
		return false
	}
	// The snooze count is one alarm's morning: the re-ring its snooze set up
	// keeps it ("snoozed 2×"); another alarm, or this one ringing again after
	// that snooze lapsed (edited, or slept through), starts from zero.
	if a.ID != alarmRing.entry.ID || time.Now().After(alarmRing.snoozeUntil.Add(alarmCatchUp)) {
		alarmRing.snoozes = 0
	}
	alarmRing.active = true
	alarmRing.entry = a
	alarmRing.started = time.Now()
	alarmRing.focus = 0
	return true
}

// alarmRetarget hands a running ring to a, an alarm that came due while another
// was ringing: the takeover shows the newer alarm and the unanswered-ring
// timeout starts again for it, so one answer silences both. Waiting for the
// first ring to be answered instead could carry the newer alarm past its
// catch-up window, and an alarm that never rings is the one failure an alarm
// clock must not have. It returns the alarm that was ringing.
func alarmRetarget(a alarmEntry) (alarmEntry, bool) {
	alarmRing.mu.Lock()
	defer alarmRing.mu.Unlock()
	prev := alarmRing.entry
	if !alarmRing.active || prev.ID == a.ID {
		return prev, false
	}
	alarmRing.entry = a
	alarmRing.started = time.Now()
	alarmRing.snoozes = 0
	return prev, true
}

// alarmDisplayLabel is the alarm's label, or its time when it has none.
func alarmDisplayLabel(config *Config, a alarmEntry) string {
	if a.Label != "" {
		return a.Label
	}
	return clockDigitsHM(config, a.Hour, a.Minute)
}

// alarmTrigger starts the ring: the takeover, the screen woken, media and a
// running game paused, the speaker raised and the ringtone looping. It runs on
// the main thread (alarmCheck). The caller has already saved FiredOn.
func alarmTrigger(config *Config, a alarmEntry) {
	// A ringtone preview the music picker still had scheduled would replace
	// the ring's sound: the player plays one thing at a time.
	alarmUI.pickPreview = time.Time{}
	if !alarmBegin(a) {
		// Another alarm is ringing: this one takes the ring over, with its own
		// ringtone when that differs.
		if prev, ok := alarmRetarget(a); ok {
			if id := alarmSoundByID(a.Sound).ID; id != alarmSoundByID(prev.Sound).ID {
				alarmPlayStart(id, false)
			}
			notifRecord("Alarm "+clockDigitsHM(config, a.Hour, a.Minute)+alarmLabelSuffix(a), notifInfo)
		}
		return
	}
	// The idle screens give the panel back: the starfield, and the dock's
	// darkened panel, which is the common way to sleep with an alarm armed.
	screensaverNotifyInput()
	alarmHoldMedia()
	// A desktop window that is minimised or behind a game comes to the front.
	if mainWindow != nil {
		if mainWindow.GetFlags()&sdl.WINDOW_MINIMIZED != 0 {
			mainWindow.Restore()
		}
		mainWindow.Raise()
	}
	alarmVolumeRaise()
	alarmPlayStart(alarmSoundByID(a.Sound).ID, false)
	// The notification centre keeps a line for it; a toast would only play its
	// own sound over the ringtone.
	notifRecord("Alarm "+clockDigitsHM(config, a.Hour, a.Minute)+alarmLabelSuffix(a), notifInfo)
}

// alarmLabelSuffix is " · label" for an alarm with a label.
func alarmLabelSuffix(a alarmEntry) string {
	if a.Label == "" {
		return ""
	}
	return " · " + a.Label
}

// alarmEndRing gives back what the ring took: the sound stops, the mixer level
// and the paused media and game are restored.
func alarmEndRing() {
	alarmPlayStop()
	alarmVolumeRestore()
	alarmReleaseMedia()
	alarmRingSnoozeRect = sdl.Rect{}
	alarmRingDismissRect = sdl.Rect{}
}

// alarmStopRing ends a ring on the way out of the app, without touching the
// alarm's saved state (writing jukaalarm.json is another step's job). It runs
// on the main thread (ShowShutdown). A game the ring paused runs again — a
// stopped process would sit out the quit request until it was killed — but
// music and video it paused stay paused: the teardown stops them next, and
// resuming them first would only blip.
func alarmStopRing() {
	alarmRing.mu.Lock()
	wasActive := alarmRing.active
	alarmRing.active = false
	alarmRing.mu.Unlock()
	if !wasActive {
		// A preview from the editor may still be playing.
		alarmPlayStop()
		return
	}
	alarmHeld.music, alarmHeld.video = false, false
	alarmEndRing()
}

// alarmSnoozeFiring silences the ring and arms the same alarm again after
// alarmSnoozeMinutes. The ring already marked the day as fired, so the snooze
// is what has to bring it back — alarmDue knows how to read it.
func alarmSnoozeFiring() {
	alarmRing.mu.Lock()
	if !alarmRing.active {
		alarmRing.mu.Unlock()
		return
	}
	entry := alarmRing.entry
	alarmRing.snoozes++
	until := time.Now().Add(alarmSnoozeMinutes * time.Minute)
	alarmRing.snoozeUntil = until
	alarmRing.active = false
	alarmRing.mu.Unlock()
	alarmEndRing()

	for i := range alarmStore.Alarms {
		if alarmStore.Alarms[i].ID == entry.ID {
			alarmStore.Alarms[i].SnoozeUntil = until.Format(time.RFC3339)
			alarmStore.Alarms[i].Enabled = true
			break
		}
	}
	alarmSave()
	showToast(fmt.Sprintf("Snoozed %d minutes — rings again at %s", alarmSnoozeMinutes, clockDigits(appConfig, until)), ToastInfo())
	PlayActivateSound()
}

// alarmDismissFiring silences the ring and clears any pending snooze: the alarm
// is done until its next scheduled time.
func alarmDismissFiring() {
	alarmDismissWith("Alarm dismissed")
}

// alarmDismissWith is dismiss with the toast's wording, so the timeout can say
// why the ring stopped by itself.
func alarmDismissWith(message string) {
	alarmRing.mu.Lock()
	if !alarmRing.active {
		alarmRing.mu.Unlock()
		return
	}
	entry := alarmRing.entry
	alarmRing.active = false
	alarmRing.snoozes = 0
	alarmRing.snoozeUntil = time.Time{}
	alarmRing.mu.Unlock()
	alarmEndRing()

	for i := range alarmStore.Alarms {
		if alarmStore.Alarms[i].ID == entry.ID {
			alarmStore.Alarms[i].SnoozeUntil = ""
			break
		}
	}
	alarmSave()
	showToast(message, ToastInfo())
	PlayBackSound()
}

// alarmRingTick is the once-a-second upkeep of a ring (alarmCheck, main
// thread): the idle screens stay away for its whole length, and a ring nobody
// answers ends after alarmRingTimeout.
func alarmRingTick(now time.Time) {
	alarmRing.mu.Lock()
	active, started := alarmRing.active, alarmRing.started
	alarmRing.mu.Unlock()
	if !active {
		return
	}
	screensaverNotifyInput()
	if now.Sub(started) >= alarmRingTimeout {
		alarmDismissWith(fmt.Sprintf("Alarm stopped after %d minutes", int(alarmRingTimeout/time.Minute)))
	}
}

// alarmRingMoveFocus walks between Snooze and Dismiss.
func alarmRingMoveFocus(dir int) {
	alarmRing.mu.Lock()
	alarmRing.focus = (alarmRing.focus + dir + 2) % 2
	alarmRing.mu.Unlock()
	PlayNavSound()
}

// alarmRingActivate presses the focused button.
func alarmRingActivate() {
	alarmRing.mu.Lock()
	focus := alarmRing.focus
	alarmRing.mu.Unlock()
	if focus == 1 {
		alarmDismissFiring()
		return
	}
	alarmSnoozeFiring()
}

// ── Input ────────────────────────────────────────────────────────────────

// alarmHandleKey owns every key while the alarm rings. It returns true even for
// keys it ignores, because nothing behind the takeover may react to a press
// that was aimed at the alarm.
func alarmHandleKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !alarmFiringActive() {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_RIGHT, sdl.K_TAB:
		alarmRingMoveFocus(1) // two buttons: either direction swaps
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		alarmRingActivate()
	case sdl.K_s, sdl.K_x, sdl.K_y:
		alarmSnoozeFiring()
	case sdl.K_d, sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		alarmDismissFiring()
	}
	return true
}

// alarmHandleController owns the pad while the alarm rings: A presses the
// focused button, B (and Select) dismisses, X or Y snoozes.
func alarmHandleController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !alarmFiringActive() {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT,
		sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		alarmRingMoveFocus(1)
	case sdl.CONTROLLER_BUTTON_A:
		alarmRingActivate()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		alarmDismissFiring()
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		alarmSnoozeFiring()
	}
	return true
}

// alarmHandleMouse presses a button on the takeover. Taps anywhere else are
// swallowed: the alarm is modal, so the screen behind it cannot be pressed
// through the red wash.
func alarmHandleMouse(mx, my int32) bool {
	if !alarmFiringActive() {
		return false
	}
	switch {
	case alarmRingSnoozeRect.W > 0 && pointInRect(mx, my, alarmRingSnoozeRect):
		alarmSnoozeFiring()
	case alarmRingDismissRect.W > 0 && pointInRect(mx, my, alarmRingDismissRect):
		alarmDismissFiring()
	}
	return true
}

// ── Rendering ────────────────────────────────────────────────────────────

// Colours of the takeover. The red is the alarm's own, not the theme's: the
// one screen that must read as "alarm" on every preset.
var (
	alarmRed     = sdl.Color{R: 214, G: 34, B: 46, A: 255}
	alarmRedDeep = sdl.Color{R: 70, G: 6, B: 12, A: 255}
	alarmPink    = sdl.Color{R: 255, G: 196, B: 200, A: 255}
)

// alarmBlink is the wash's intensity at time t (seconds): a one-second blink
// with soft edges — on for half a second, off for half — rather than a pulse
// too gentle to notice from across a room.
func alarmBlink(t float64) float64 {
	v := 0.5 + 0.5*math.Cos(2*math.Pi*t)
	return bootSmoothstep(0.2, 0.8, v)
}

// renderAlarmRing paints the takeover over everything already drawn: a deep
// red base, the blinking red wash, the time now, the alarm's label and time,
// the two buttons and their hints.
func renderAlarmRing(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	alarmRing.mu.Lock()
	if !alarmRing.active {
		alarmRing.mu.Unlock()
		return
	}
	entry := alarmRing.entry
	snoozes := alarmRing.snoozes
	focus := alarmRing.focus
	started := alarmRing.started
	alarmRing.mu.Unlock()

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetClipRect(nil)
	now := time.Now()
	blink := alarmBlink(now.Sub(started).Seconds())
	full := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}

	// The base keeps whatever is behind from being read at all; the wash is the
	// blink itself.
	setDrawColor(renderer, WithAlpha(alarmRedDeep, 236))
	_ = renderer.FillRect(&full)
	setDrawColor(renderer, WithAlpha(alarmRed, uint8(70+130*blink)))
	_ = renderer.FillRect(&full)

	cx := screenWidth / 2
	glow := scaleY(520, screenHeight)
	homeGlowBlob(renderer, sdl.Rect{X: cx - glow, Y: screenHeight*2/5 - glow/2, W: 2 * glow, H: glow},
		sdl.Color{R: 255, G: 90, B: 90, A: 255}, uint8(60+80*blink))

	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		medium = small
	}
	clockFont := getFontSized(config, "big", 150)
	if clockFont == nil {
		clockFont = medium
	}
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}

	y := scaleY(70, screenHeight)
	if small != nil {
		kicker := "ALARM"
		kw, kh := measureText(small, kicker)
		renderText(renderer, config, small, kicker, alarmPink, cx-kw/2, y)
		y += kh + scaleY(4, screenHeight)
	}
	if clockFont != nil {
		timeStr := clockDigits(config, now)
		tw, th := measureText(clockFont, timeStr)
		renderText(renderer, config, clockFont, timeStr, white, cx-tw/2, y)
		y += th
	}
	if medium != nil {
		line := alarmDisplayLabel(config, entry)
		if entry.Label != "" {
			line += " · " + clockDigitsHM(config, entry.Hour, entry.Minute)
		}
		line = ellipsize(medium, line, screenWidth-2*scaleX(80, screenWidth))
		lw, lh := measureText(medium, line)
		renderText(renderer, config, medium, line, white, cx-lw/2, y)
		y += lh + scaleY(8, screenHeight)
	}
	if small != nil {
		meta := "Ringtone: " + alarmSoundName(entry.Sound)
		if snoozes > 0 {
			meta += fmt.Sprintf(" · snoozed %d×", snoozes)
		}
		meta = ellipsize(small, meta, screenWidth-2*scaleX(80, screenWidth))
		mw, _ := measureText(small, meta)
		renderText(renderer, config, small, meta, alarmPink, cx-mw/2, y)
	}

	// Two big buttons, side by side: snooze on the left, dismiss on the right,
	// the focused one lifted and ringed the way every glass button is.
	btnW := scaleX(300, screenWidth)
	btnH := scaleY(76, screenHeight)
	gap := scaleX(28, screenWidth)
	if 2*btnW+gap > screenWidth-2*scaleX(40, screenWidth) {
		btnW = (screenWidth - 2*scaleX(40, screenWidth) - gap) / 2
	}
	btnY := screenHeight - scaleY(190, screenHeight)
	alarmRingSnoozeRect = sdl.Rect{X: cx - gap/2 - btnW, Y: btnY, W: btnW, H: btnH}
	alarmRingDismissRect = sdl.Rect{X: cx + gap/2, Y: btnY, W: btnW, H: btnH}
	drawGlassButton(renderer, config, alarmRingSnoozeRect, fmt.Sprintf("Snooze %d min", alarmSnoozeMinutes), medium,
		sdl.Color{R: 255, G: 190, B: 90, A: 70}, glassState{Focused: focus == 0})
	drawGlassButton(renderer, config, alarmRingDismissRect, "Dismiss", medium,
		sdl.Color{R: 255, G: 255, B: 255, A: 60}, glassState{Focused: focus == 1})

	// The hint row, Home's footer badges: what each button does.
	if small != nil {
		alarmDrawHints(renderer, config, small, btnY+btnH+scaleY(30, screenHeight))
	}
}

// alarmRingHints are the takeover's hint badges.
var alarmRingHints = [3][2]string{{"A", "Select"}, {"X", "Snooze"}, {"B", "Dismiss"}}

// alarmDrawHints draws the hint badges centred on one line at y.
func alarmDrawHints(renderer *sdl.Renderer, config *Config, font *ttf.Font, y int32) {
	_, th := measureText(font, "Ag")
	badgeH := th + 6
	pad := int32(10)
	gap := scaleX(26, screenWidth)
	total := int32(0)
	for i, h := range alarmRingHints {
		// Measured as drawGlassHintBadge names it (Enter/Esc on a keyboard).
		bw, _ := measureText(font, hintKeyFor(h[0], h[1]))
		lw, _ := measureText(font, h[1])
		total += bw + 2*pad + 8 + lw
		if i > 0 {
			total += gap
		}
	}
	x := screenWidth/2 - total/2
	for _, h := range alarmRingHints {
		bw, _ := measureText(font, hintKeyFor(h[0], h[1]))
		badge := sdl.Rect{X: x, Y: y, W: bw + 2*pad, H: badgeH}
		x = drawGlassHintBadge(renderer, config, font, badge, h[0], h[1], x+badge.W+8, y+3) + gap
	}
}
