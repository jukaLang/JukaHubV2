package main

// uimode_unlock.go — the parent code card and the Konami code (uimode.go).
//
// One card does two jobs:
//   - turning Kiosk or Kid mode on (Settings → Kid & Kiosk mode): choose a
//     parent code of 4 to 8 presses, then enter it again. Start with nothing
//     typed is "no code": then the Konami code leaves;
//   - leaving (Start menu → Unlock, Settings → Leave, or the Konami code when
//     a code is set): enter the code. Without a code the card takes the
//     Konami code itself.
//
// A code is the startup lock's button code: the D-pad, A, B, X, Y, L1 and R1
// as the physical buttons (before the A/B and X/Y swaps), hashed with
// bootLockHash and checked with bootLockVerify off the main thread. Start
// goes on, Select deletes the last press (and, with none, closes the card),
// Menu closes it. On a keyboard: the arrows and A, B, X, Y, L, R type, Enter
// goes on, Backspace deletes, Esc closes. Wrong codes wait like the startup
// lock's (bootLockWaitFor: five free, then 30 s doubling to 5 minutes),
// counted in data/ui-mode.json and timed by SDL's monotonic ticks.
//
// The Konami code is watched while a lock is on and the card is closed, on
// every screen JukaHub draws: Up Up Down Down Left Right Left Right B A on the
// pad (the printed buttons or their meaning after a swap, either counts, and
// B A or A B), or on a keyboard the arrows then B A, or Esc Enter. The presses
// still do what they do on the screen, except the last, which is taken.
//
// main.go calls uiModeEvent first thing after the startup lock and Controller
// Mode, and uiModeRender before the toasts.

import (
	"crypto/subtle"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// uiKonamiCode is the Konami code in the card's letters (bootLockPadChar);
// uiKonamiCodeSwapped is it with A and B the other way round.
const (
	uiKonamiCode        = "UUDDLRLRBA"
	uiKonamiCodeSwapped = "UUDDLRLRAB"
)

// The card's steps.
const (
	uiUnlockLeave   = iota // the code in force (or the Konami code), to leave
	uiUnlockNew            // turning a mode on: the new code
	uiUnlockConfirm        // the new code again
)

// uiUnlockResult is a check or a hash made off the main thread.
type uiUnlockResult struct {
	step int
	ok   bool
	hash string
}

// uiUnlock is the card. Main thread only.
var uiUnlock struct {
	open   bool
	step   int
	target string // the mode being turned on (uiUnlockNew, uiUnlockConfirm)
	entry  []byte
	first  string
	note   string
	warn   bool

	checking bool
	result   chan uiUnlockResult
	// waitUntil is the SDL tick before which no attempt counts.
	waitUntil uint64
}

// uiKonami holds the last presses while a lock is on: as pressed and as
// meant after the A/B and X/Y swaps.
var uiKonami struct {
	raw, meant []byte
}

// ── Opening and closing ───────────────────────────────────────────────────

// uiModeOpenUnlock opens the card to leave the mode in force.
func uiModeOpenUnlock(config *Config) {
	if !uiModeLocked() {
		ShowToast("Already in Full mode", ToastKindInfo)
		return
	}
	uiUnlockReset()
	uiUnlock.open = true
	uiUnlock.step = uiUnlockLeave
	uiUnlockImposeWait()
	startMenuDismiss()
	PlayActivateSound()
}

// uiModeBeginTurnOn opens the card to choose the code for mode.
func uiModeBeginTurnOn(config *Config, mode string) {
	if mode != uiModeKiosk && mode != uiModeKid {
		return
	}
	if uiModeLocked() {
		ShowToast("Leave "+uiModeLabel(uiModeNow())+" mode first", ToastKindInfo)
		return
	}
	uiUnlockReset()
	uiUnlock.open = true
	uiUnlock.step = uiUnlockNew
	uiUnlock.target = mode
	uiUnlock.waitUntil = 0
	PlayActivateSound()
}

// uiUnlockReset clears what was typed and said.
func uiUnlockReset() {
	for i := range uiUnlock.entry {
		uiUnlock.entry[i] = 0
	}
	uiUnlock.entry = uiUnlock.entry[:0]
	uiUnlock.first = ""
	uiUnlock.note, uiUnlock.warn = "", false
	uiUnlock.target = ""
}

// uiUnlockClose takes the card down.
func uiUnlockClose(sound bool) {
	if uiUnlock.checking {
		return
	}
	uiUnlockReset()
	uiUnlock.open = false
	if sound {
		PlayBackSound()
	}
}

// uiUnlockImposeWait starts the wait the counted wrong codes ask for.
func uiUnlockImposeWait() {
	uiUnlock.waitUntil = 0
	if d := bootLockWaitFor(uiModeSnapshot().Fails); d > 0 {
		uiUnlock.waitUntil = sdl.GetTicks64() + uint64(d.Milliseconds())
	}
}

// uiUnlockWaitLeft is the wait still to sit out, or 0.
func uiUnlockWaitLeft() time.Duration {
	now := sdl.GetTicks64()
	if uiUnlock.waitUntil <= now {
		return 0
	}
	return time.Duration(uiUnlock.waitUntil-now) * time.Millisecond
}

// uiUnlockMax is the longest entry the card takes now.
func uiUnlockMax() int {
	if uiUnlock.step == uiUnlockLeave && uiModeSnapshot().Code == "" {
		return len(uiKonamiCode)
	}
	return bootLockCodeMax
}

// ── Entry ─────────────────────────────────────────────────────────────────

func uiUnlockPress(c byte) {
	if uiUnlock.checking {
		return
	}
	if uiUnlock.step == uiUnlockLeave && uiUnlockWaitLeft() > 0 {
		PlayErrorSound()
		return
	}
	if len(uiUnlock.entry) >= uiUnlockMax() {
		PlayErrorSound()
		return
	}
	uiUnlock.entry = append(uiUnlock.entry, c)
	if uiUnlock.warn {
		uiUnlock.note, uiUnlock.warn = "", false
	}
	PlayNavSound()
}

// uiUnlockBackspace is Select: the last press goes; with none, the card.
func uiUnlockBackspace() {
	if n := len(uiUnlock.entry); n > 0 {
		uiUnlock.entry[n-1] = 0
		uiUnlock.entry = uiUnlock.entry[:n-1]
		PlayBackSound()
		return
	}
	uiUnlockClose(true)
}

func uiUnlockSay(msg string, warn bool) {
	uiUnlock.note, uiUnlock.warn = msg, warn
}

func uiUnlockClearEntry() {
	for i := range uiUnlock.entry {
		uiUnlock.entry[i] = 0
	}
	uiUnlock.entry = uiUnlock.entry[:0]
}

// uiUnlockSubmit is Start (Enter): the entry is complete.
func uiUnlockSubmit(config *Config) {
	if uiUnlock.checking {
		return
	}
	secret := string(uiUnlock.entry)
	switch uiUnlock.step {
	case uiUnlockLeave:
		if uiUnlockWaitLeft() > 0 {
			PlayErrorSound()
			return
		}
		stored := uiModeSnapshot().Code
		if stored == "" {
			// No code was chosen: the Konami code is the key. The card reads
			// the buttons as pressed (before the A/B swap), so B A typed by
			// the printed letters may come as A B: either counts, as in the
			// watcher — the combination is public anyway.
			uiUnlockClearEntry()
			if subtle.ConstantTimeCompare([]byte(secret), []byte(uiKonamiCode)) == 1 ||
				subtle.ConstantTimeCompare([]byte(secret), []byte(uiKonamiCodeSwapped)) == 1 {
				uiUnlockClose(false)
				uiModeLeave(config)
				return
			}
			uiUnlockSay("That is not it", true)
			PlayErrorSound()
			return
		}
		if len(secret) < bootLockCodeMin {
			// No code is that short: a stray Start is not counted.
			uiUnlockSay("The code is "+strconv.Itoa(bootLockCodeMin)+" to "+strconv.Itoa(bootLockCodeMax)+" presses", true)
			PlayErrorSound()
			return
		}
		// Counted and saved before the check: power pulled while it runs
		// cannot keep a wrong one off the count.
		uiModeUpdate(func(f *uiModeFile) { f.Fails++ })
		uiUnlockRun(uiUnlockLeave, func() uiUnlockResult {
			return uiUnlockResult{ok: bootLockVerify(stored, secret)}
		})
	case uiUnlockNew:
		if secret == "" {
			// Start with nothing typed: no code.
			mode := uiUnlock.target
			uiUnlockClose(false)
			uiModeTurnOn(config, mode, "")
			return
		}
		if msg := bootLockTooShort(bootLockKindCode, secret); msg != "" {
			uiUnlockSay(msg, true)
			PlayErrorSound()
			return
		}
		uiUnlock.first = secret
		uiUnlockClearEntry()
		uiUnlock.step = uiUnlockConfirm
		uiUnlockSay("", false)
		PlayNavSound()
	case uiUnlockConfirm:
		if subtle.ConstantTimeCompare([]byte(secret), []byte(uiUnlock.first)) != 1 {
			uiUnlock.first = ""
			uiUnlockClearEntry()
			uiUnlock.step = uiUnlockNew
			uiUnlockSay("The two did not match. Choose it again", true)
			PlayErrorSound()
			return
		}
		uiUnlock.first = ""
		uiUnlockRun(uiUnlockConfirm, func() uiUnlockResult {
			h, err := bootLockHash(bootLockKindCode, secret)
			return uiUnlockResult{ok: err == nil, hash: h}
		})
	}
}

// uiUnlockRun does the slow part on its own goroutine; the frame picks the
// answer up (uiUnlockTick).
func uiUnlockRun(step int, work func() uiUnlockResult) {
	uiUnlock.checking = true
	ch := make(chan uiUnlockResult, 1)
	uiUnlock.result = ch
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- uiUnlockResult{step: step}
			}
		}()
		r := work()
		r.step = step
		ch <- r
	}()
}

// uiUnlockTick applies a finished check or hash. Main thread, once a frame.
func uiUnlockTick(config *Config) {
	if !uiUnlock.checking {
		return
	}
	var r uiUnlockResult
	select {
	case r = <-uiUnlock.result:
	default:
		return
	}
	uiUnlock.checking = false
	uiUnlockClearEntry()
	switch r.step {
	case uiUnlockLeave:
		if r.ok {
			uiUnlockClose(false)
			uiModeLeave(config)
			return
		}
		uiUnlockImposeWait()
		PlayErrorSound()
		if uiUnlockWaitLeft() > 0 {
			uiUnlockSay("Too many tries", true)
		} else {
			uiUnlockSay("Wrong code", true)
		}
	case uiUnlockConfirm:
		if !r.ok || r.hash == "" {
			uiUnlock.step = uiUnlockNew
			uiUnlockSay("The code could not be saved. Try again", true)
			PlayErrorSound()
			return
		}
		mode := uiUnlock.target
		uiUnlockClose(false)
		uiModeTurnOn(config, mode, r.hash)
	}
}

// ── Konami ────────────────────────────────────────────────────────────────

// uiKonamiPush adds one press to a watch buffer and reports the code typed.
func uiKonamiPush(buf *[]byte, c byte) bool {
	*buf = append(*buf, c)
	if n := len(*buf); n > len(uiKonamiCode) {
		*buf = append((*buf)[:0], (*buf)[n-len(uiKonamiCode):]...)
	}
	s := string(*buf)
	return s == uiKonamiCode || s == uiKonamiCodeSwapped
}

// uiKonamiNote is one press while locked; true when it ended the code.
func uiKonamiNote(raw, meant byte) bool {
	a := uiKonamiPush(&uiKonami.raw, raw)
	b := uiKonamiPush(&uiKonami.meant, meant)
	if a || b {
		uiKonami.raw, uiKonami.meant = uiKonami.raw[:0], uiKonami.meant[:0]
		return true
	}
	return false
}

// uiKonamiDone is the Konami code typed: without a parent code it leaves the
// mode, with one it opens the card that asks for it.
func uiKonamiDone(config *Config) {
	if uiModeSnapshot().Code == "" {
		uiModeLeave(config)
		return
	}
	uiModeOpenUnlock(config)
	uiUnlockSay("Enter the parent code", false)
}

// uiKonamiKeyChar is a key's letter in the watch ('.' for any other key).
func uiKonamiKeyChar(sym sdl.Keycode) byte {
	switch sym {
	case sdl.K_UP:
		return 'U'
	case sdl.K_DOWN:
		return 'D'
	case sdl.K_LEFT:
		return 'L'
	case sdl.K_RIGHT:
		return 'R'
	case sdl.K_b, sdl.K_ESCAPE:
		return 'B'
	case sdl.K_a, sdl.K_RETURN, sdl.K_KP_ENTER:
		return 'A'
	}
	return '.'
}

// ── Input ─────────────────────────────────────────────────────────────────

// uiModeEvent is main.go's hook for every event: the open card takes all
// input; otherwise, while locked, the Konami code is watched and its last
// press taken. Quit, window and other events pass.
func uiModeEvent(config *Config, event sdl.Event) bool {
	if event == nil {
		return false
	}
	if !uiUnlock.open {
		// The in-app HTML5 game draws over the card: it owns its presses.
		if !uiModeLocked() || html5GameActive() {
			return false
		}
		switch e := event.(type) {
		case *sdl.ControllerButtonEvent:
			if e.Type != sdl.CONTROLLERBUTTONDOWN || e.Which == padRepeatWhich {
				return false
			}
			raw := uint8(e.Button)
			if e.Which != inmapSynthWhich && e.Which != menuBackWhich {
				raw = padEventRaw
			}
			rc, ok := bootLockPadChar(raw)
			if !ok {
				rc = '.'
			}
			mc, ok := bootLockPadChar(e.Button)
			if !ok {
				mc = '.'
			}
			if uiKonamiNote(rc, mc) {
				uiKonamiDone(config)
				return true
			}
		case *sdl.KeyboardEvent:
			if e.Type != sdl.KEYDOWN || e.Repeat != 0 {
				return false
			}
			c := uiKonamiKeyChar(e.Keysym.Sym)
			if uiKonamiNote(c, c) {
				uiKonamiDone(config)
				return true
			}
		}
		return false
	}
	if alarmFiringActive() {
		// A ringing alarm's card answers its presses first.
		switch event.(type) {
		case *sdl.ControllerButtonEvent, *sdl.KeyboardEvent, *sdl.TextInputEvent, *sdl.MouseButtonEvent:
			return false
		}
	}
	switch e := event.(type) {
	case *sdl.ControllerButtonEvent:
		if e.Type != sdl.CONTROLLERBUTTONDOWN {
			wakeSwallowRelease(wakeInputPad, int32(e.Button))
			return true
		}
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		inputHintsNotePad()
		if wakeSwallowPress(wakeInputPad, int32(e.Button), false) {
			return true
		}
		uiUnlockPad(config, e)
	case *sdl.KeyboardEvent:
		if e.Type != sdl.KEYDOWN {
			wakeSwallowRelease(wakeInputKey, int32(e.Keysym.Scancode))
			return true
		}
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		inputHintsNoteKey(e)
		if wakeSwallowPress(wakeInputKey, int32(e.Keysym.Scancode), e.Repeat != 0) {
			return true
		}
		uiUnlockKey(config, e)
	case *sdl.TextInputEvent, *sdl.TextEditingEvent, *sdl.MouseButtonEvent, *sdl.MouseMotionEvent,
		*sdl.MouseWheelEvent, *sdl.ControllerAxisEvent, *sdl.JoyButtonEvent, *sdl.JoyAxisEvent,
		*sdl.JoyHatEvent, *sdl.TouchFingerEvent, *sdl.MultiGestureEvent:
		// Nothing behind the card may see them.
	default:
		return false
	}
	return true
}

// uiUnlockPad is one pad press on the card.
func uiUnlockPad(config *Config, e *sdl.ControllerButtonEvent) {
	// A code is pressed, not held: a held D-pad's repeats are not presses.
	if e.Which == padRepeatWhich || uiUnlock.checking {
		return
	}
	b := sdl.GameControllerButton(e.Button)
	synthetic := e.Which == inmapSynthWhich || e.Which == menuBackWhich
	if b == sdl.CONTROLLER_BUTTON_BACK && (e.Which == menuBackWhich || (!synthetic && padEventRaw == sdl.CONTROLLER_BUTTON_GUIDE)) {
		// Menu is still Menu: on a handheld applyButtonSwap folds GUIDE
		// into BACK (padEventRaw keeps it).
		b = sdl.CONTROLLER_BUTTON_GUIDE
	}
	raw := uint8(b)
	if !synthetic {
		raw = padEventRaw
	}
	switch b {
	case sdl.CONTROLLER_BUTTON_START:
		uiUnlockSubmit(config)
	case sdl.CONTROLLER_BUTTON_BACK:
		uiUnlockBackspace()
	case sdl.CONTROLLER_BUTTON_GUIDE:
		uiUnlockClose(true)
	default:
		if c, ok := bootLockPadChar(raw); ok {
			uiUnlockPress(c)
		}
	}
}

// uiUnlockKey is one key press on the card.
func uiUnlockKey(config *Config, e *sdl.KeyboardEvent) {
	if e.Repeat != 0 || uiUnlock.checking {
		return
	}
	var c byte
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		uiUnlockSubmit(config)
		return
	case sdl.K_BACKSPACE, sdl.K_DELETE:
		uiUnlockBackspace()
		return
	case sdl.K_ESCAPE:
		uiUnlockClose(true)
		return
	case sdl.K_UP:
		c = 'U'
	case sdl.K_DOWN:
		c = 'D'
	case sdl.K_LEFT:
		c = 'L'
	case sdl.K_RIGHT:
		c = 'R'
	case sdl.K_a:
		c = 'A'
	case sdl.K_b:
		c = 'B'
	case sdl.K_x:
		c = 'X'
	case sdl.K_y:
		c = 'Y'
	case sdl.K_l:
		c = 'l'
	case sdl.K_r:
		c = 'r'
	default:
		return
	}
	uiUnlockPress(c)
}

// ── Drawing ───────────────────────────────────────────────────────────────

// uiUnlockTitle is the card's heading.
func uiUnlockTitle() string {
	if uiUnlock.step == uiUnlockLeave {
		return "Leave " + uiModeLabel(uiModeNow()) + " mode"
	}
	return uiModeLabel(uiUnlock.target) + " mode: parent code"
}

// uiUnlockPrompt is what to do now.
func uiUnlockPrompt() string {
	switch uiUnlock.step {
	case uiUnlockNew:
		return "Choose a code: " + strconv.Itoa(bootLockCodeMin) + " to " + strconv.Itoa(bootLockCodeMax) + " presses"
	case uiUnlockConfirm:
		return "Enter it again to confirm"
	}
	if uiModeSnapshot().Code == "" {
		return "Enter the unlock combination"
	}
	return "Enter the parent code"
}

// uiUnlockStatus is the line under the dots, and whether it warns.
func uiUnlockStatus() (string, bool) {
	if uiUnlock.checking {
		return "Checking…", false
	}
	if left := uiUnlockWaitLeft(); left > 0 && uiUnlock.step == uiUnlockLeave {
		secs := int((left + time.Second - 1) / time.Second)
		return "Too many tries · try again in " + strconv.Itoa(secs/60) + ":" + twoDigitsLock(secs%60), true
	}
	return uiUnlock.note, uiUnlock.warn
}

// uiUnlockHelp is the small print.
func uiUnlockHelp() string {
	switch {
	case uiUnlock.step == uiUnlockNew:
		return "The D-pad, A, B, X, Y, L1 and R1. Start with nothing typed sets no code: then Up Up Down Down Left Right Left Right B A leaves."
	case uiUnlock.step == uiUnlockConfirm:
		return "Keep it to yourself. Forgot it later? Delete ui-mode.json in JukaHub's data folder from a computer."
	case uiModeSnapshot().Code == "":
		return "No parent code was set. The combination is in the manual (Kid mode)."
	}
	return "Forgot it? Delete ui-mode.json in JukaHub's data folder from a computer."
}

// uiUnlockHints is the card's footer.
func uiUnlockHints() []FooterHint {
	start := FooterHint{Button: "Start", Label: "Unlock"}
	switch uiUnlock.step {
	case uiUnlockNew:
		start.Label = "Next"
		if len(uiUnlock.entry) == 0 {
			start.Label = "No code"
		}
	case uiUnlockConfirm:
		start.Label = "Save"
	}
	sel := FooterHint{Button: "Select", Label: "Delete"}
	if len(uiUnlock.entry) == 0 {
		sel.Label = "Cancel"
	}
	return []FooterHint{start, sel, {Button: "Menu", Label: "Close"}}
}

// uiModeRender is main.go's hook once a frame: the checks' answers, then the
// card while it is up.
func uiModeRender(renderer *sdl.Renderer, config *Config) {
	uiUnlockTick(config)
	if !uiUnlock.open || renderer == nil || config == nil {
		return
	}
	f := bootLockFonts(config)
	if f.title == nil || f.body == nil || f.small == nil {
		return
	}
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	drawOverlayScrim(renderer)
	band := sceneFooterBand()
	w := int32(600)
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	pad := int32(24)
	inner := w - 2*pad
	help := wrapTextLines(f.small, uiUnlockHelp(), inner, 3)
	icon, dotsH := int32(36), int32(28)
	h := pad + icon + 10 + int32(f.title.Height()) + 4 + int32(f.body.Height()) + 16 + dotsH + 12 +
		int32(f.body.Height()) + 8 + int32(len(help))*(int32(f.small.Height())+2) + pad
	y := (band.Y - h) / 2
	if y < 8 {
		y = 8
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: y, W: w, H: h}
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	cx := card.X + card.W/2
	cy := card.Y + pad
	drawHomeGlyphTinted(renderer, "lock", sdl.Rect{X: cx - icon/2, Y: cy, W: icon, H: icon}, ColorTextPrimary())
	cy += icon + 10
	drawText(renderer, f.title, ellipsize(f.title, uiUnlockTitle(), inner), cx, cy, ColorTextPrimary(), textAlignCenter)
	cy += int32(f.title.Height()) + 4
	drawText(renderer, f.body, ellipsize(f.body, uiUnlockPrompt(), inner), cx, cy, ColorTextSecondary(), textAlignCenter)
	cy += int32(f.body.Height()) + 16
	uiUnlockDrawDots(renderer, cx, cy, dotsH)
	cy += dotsH + 12
	if msg, warn := uiUnlockStatus(); msg != "" {
		ink := ColorTextSecondary()
		if warn {
			ink = WarningInk()
		}
		drawText(renderer, f.body, ellipsize(f.body, msg, inner), cx, cy, ink, textAlignCenter)
	}
	cy += int32(f.body.Height()) + 8
	for _, line := range help {
		drawText(renderer, f.small, line, cx, cy, ColorTextTertiary(), textAlignCenter)
		cy += int32(f.small.Height()) + 2
	}
	drawGlassFooterBand(renderer, band)
	plan := cachedFooterPlan(config, uiUnlockHints(), band.W-2*scaleX(16, screenWidth))
	vkDrawHintPlan(renderer, config, plan, band)
}

// uiUnlockDrawDots draws one dot per press, hollow ones up to the shortest
// code, so only the length typed shows.
func uiUnlockDrawDots(renderer *sdl.Renderer, cx, y, h int32) {
	n := len(uiUnlock.entry)
	slots := n
	if slots < bootLockCodeMin {
		slots = bootLockCodeMin
	}
	r, gap := int32(8), int32(14)
	total := int32(slots)*(2*r) + int32(slots-1)*gap
	x := cx - total/2 + r
	accent := glassAccent()
	for i := 0; i < slots; i++ {
		if i < n {
			fillCircle(renderer, x, y+h/2, r, accent)
		} else {
			fillCircle(renderer, x, y+h/2, r, glassA(26))
		}
		x += 2*r + gap
	}
}
