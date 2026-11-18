package main

// bootlock_ui.go — the startup lock's screen (bootlock.go): the time, the
// user's name, the entry dots or the password keyboard, what went wrong, how
// to remove a forgotten code, and the power choices.
//
// It is drawn over everything but a ringing alarm (main.go calls
// bootLockRender after the toasts and the trophy card), on an opaque base and
// the app backdrop, so nothing of the screen under it can be read — not a
// toast, not a notification. The password keyboard is drawn here too, on top
// of the lock, rather than where main.go draws it under the toasts.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// bootLockForgotCache is bootLockForgotText's answer; the folder does not
// change while JukaHub runs.
var bootLockForgotCache string

// bootLockUserFile is where jukauser.json is, as the owner would find it: on
// the handheld relative to the SD card's root ("Apps/JukaHub/jukauser.json").
func bootLockUserFile() string {
	path := "jukauser.json"
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if IsTrimuiDevice() || runtime.GOOS == "linux" {
		for _, root := range []string{"/mnt/SDCARD/", "/mnt/sdcard/"} {
			if strings.HasPrefix(path, root) {
				return strings.TrimPrefix(path, root)
			}
		}
	}
	return path
}

// bootLockForgotText says how to remove a forgotten code. Setting the value
// to "" (rather than deleting the line) keeps the file valid JSON: a broken
// file would be replaced by jukauser.json.bak, lock and all.
func bootLockForgotText() string {
	if bootLockForgotCache != "" {
		return bootLockForgotCache
	}
	file := bootLockUserFile()
	switch {
	case IsTrimuiDevice():
		bootLockForgotCache = "Forgot it? Put the SD card in a computer, open " + file +
			" and change \"" + bootLockSecretKey + "\" to \"\"."
	case IsWindows():
		bootLockForgotCache = "Forgot it? Close JukaHub, open " + file +
			" in Notepad and change \"" + bootLockSecretKey + "\" to \"\"."
	default:
		bootLockForgotCache = "Forgot it? Close JukaHub, open " + file +
			" and change \"" + bootLockSecretKey + "\" to \"\"."
	}
	if _, err := os.Stat("jukauser.json"); err != nil {
		logInfo("lock", "startup lock: jukauser.json not found in the working folder (%v)", err)
	}
	return bootLockForgotCache
}

// bootLockFonts are the screen's type sizes.
type bootLockFontSet struct {
	clock, title, body, small *ttf.Font
}

func bootLockFonts(config *Config) bootLockFontSet {
	// The clock is a display numeral, so it keeps its own token; every piece of
	// text around it follows the same five-role scale as the rest of the UI.
	clockSize, titleRole := FontSizeDisplay, TypeTitle
	if screenHeight < 600 {
		clockSize, titleRole = FontSizeDisplayCompact, TypeHeading
	}
	return bootLockFontSet{
		clock: getFontSized(config, "big", int(clockSize)),
		title: FontForRole(config, titleRole),
		body:  FontForRole(config, TypeBody),
		small: FontForRole(config, TypeCaption),
	}
}

// bootLockRender is main.go's hook once a frame: the tick, then the screen
// while the lock or its setup is up.
func bootLockRender(renderer *sdl.Renderer, config *Config) {
	bootLockTick(config)
	if bootLock.mode == bootLockModeOff || renderer == nil || config == nil {
		return
	}
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	base := ColorBackground
	base.A = 255
	setDrawColor(renderer, base)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	fonts := bootLockFonts(config)
	if bootLockTyping() {
		renderKeyboard(renderer, config)
		bootLockDrawAbove(renderer, config, fonts, vkPanelRect.Y)
		return
	}
	bootLockDrawCard(renderer, config, fonts)
	bootLockDrawFooter(renderer, config)
	if bootLock.power {
		bootLockDrawPower(renderer, config, fonts)
	}
}

// bootLockTitle is the card's heading: the user's name on the lock screen.
func bootLockTitle(config *Config) string {
	if bootLock.mode == bootLockModeLocked {
		if name := userName(config); name != "" {
			return name
		}
		return "JukaHub is locked"
	}
	return "Startup lock"
}

// bootLockPrompt is what to do now.
func bootLockPrompt() string {
	if p := bootLockVoicePrompt(); p != "" {
		return p // the phrase is wanted (bootlock_voice.go)
	}
	code := bootLock.kind != bootLockKindPassword
	switch {
	case bootLock.mode == bootLockModeLocked && code:
		return "Enter your button code"
	case bootLock.mode == bootLockModeLocked:
		return "Enter your password"
	case bootLock.step == bootLockStepCurrent && code:
		return "First, the current button code"
	case bootLock.step == bootLockStepCurrent:
		return "First, the current password"
	case bootLock.step == bootLockStepConfirm:
		return "Enter it again to confirm"
	case code:
		return "Choose a button code: " + strconv.Itoa(bootLockCodeMin) + " to " + strconv.Itoa(bootLockCodeMax) + " presses"
	}
	return "Choose a password: at least " + strconv.Itoa(bootLockPassMin) + " characters"
}

// bootLockStatus is the line under the entry, and whether it is a warning.
func bootLockStatus() (string, bool) {
	if bootLock.checking {
		return "Checking…", false
	}
	if left := bootLockWaitLeft(); left > 0 && (bootLock.mode == bootLockModeLocked || bootLock.step == bootLockStepCurrent) {
		secs := int((left + time.Second - 1) / time.Second)
		return "Too many tries · try again in " + strconv.Itoa(secs/60) + ":" + twoDigitsLock(secs%60), true
	}
	if bootLock.note == "Enter it again to confirm" {
		// The prompt says it already.
		return "", false
	}
	return bootLock.note, bootLock.warn
}

func twoDigitsLock(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// bootLockHelpLine is the small print: the buttons a code uses, and on the
// lock screen how to remove a forgotten one.
func bootLockHelpLine() string {
	if s := bootLockVoiceHelp(); s != "" {
		return s
	}
	if bootLock.mode == bootLockModeLocked {
		return bootLockForgotText()
	}
	if bootLock.kind == bootLockKindPassword {
		return "A lock for the screen, not encryption: the SD card can still be read in a computer."
	}
	return "Use the D-pad, A, B, X, Y, L1 and R1. Start goes on, Select deletes the last press."
}

// bootLockDrawCard is the lock without the keyboard: the clock above a glass
// card.
func bootLockDrawCard(renderer *sdl.Renderer, config *Config, f bootLockFontSet) {
	band := sceneFooterBand()
	top := int32(0)
	if f.clock != nil && bootLock.mode == bootLockModeLocked {
		clock := clockDigits(config, time.Now())
		y := screenHeight / 12
		drawText(renderer, f.clock, clock, screenWidth/2, y, ColorTextPrimary(), textAlignCenter)
		top = y + int32(f.clock.Height())
	}
	if f.title == nil || f.body == nil || f.small == nil {
		return
	}
	w := int32(620)
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	pad := int32(26)
	inner := w - 2*pad
	help := wrapTextLines(f.small, bootLockHelpLine(), inner, 3)
	icon := int32(40)
	dotsH := int32(30)
	voice := bootLockVoiceLine(config) // Voice unlock's line (bootlock_voice.go)
	h := pad + icon + 12 + int32(f.title.Height()) + 4 + int32(f.body.Height()) + 18 + dotsH + 14 +
		int32(f.body.Height()) + 10 + int32(len(help))*(int32(f.small.Height())+2) + pad
	if voice != "" {
		h += int32(f.small.Height()) + 8
	}
	avail := band.Y - top
	y := top + (avail-h)/2
	if y < top+8 {
		y = top + 8
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: y, W: w, H: h}
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	cx := card.X + card.W/2
	cy := card.Y + pad
	drawHomeGlyphTinted(renderer, "lock", sdl.Rect{X: cx - icon/2, Y: cy, W: icon, H: icon}, ColorTextPrimary())
	cy += icon + 12
	drawText(renderer, f.title, ellipsize(f.title, bootLockTitle(config), inner), cx, cy, ColorTextPrimary(), textAlignCenter)
	cy += int32(f.title.Height()) + 4
	drawText(renderer, f.body, ellipsize(f.body, bootLockPrompt(), inner), cx, cy, ColorTextSecondary(), textAlignCenter)
	cy += int32(f.body.Height()) + 18
	bootLockDrawEntry(renderer, f, cx, cy, dotsH)
	cy += dotsH + 14
	if msg, warn := bootLockStatus(); msg != "" {
		ink := ColorTextSecondary()
		if warn {
			ink = WarningInk()
		}
		drawText(renderer, f.body, ellipsize(f.body, msg, inner), cx, cy, ink, textAlignCenter)
	}
	cy += int32(f.body.Height()) + 10
	if voice != "" {
		drawText(renderer, f.small, ellipsize(f.small, voice, inner), cx, cy, ColorTextSecondary(), textAlignCenter)
		cy += int32(f.small.Height()) + 8
	}
	for _, line := range help {
		drawText(renderer, f.small, line, cx, cy, ColorTextTertiary(), textAlignCenter)
		cy += int32(f.small.Height()) + 2
	}
}

// bootLockDrawEntry is the dots of a code (hollow ones up to the shortest
// code, so the length typed so far is all that shows), or, for a password,
// the press that opens the keyboard.
func bootLockDrawEntry(renderer *sdl.Renderer, f bootLockFontSet, cx, y, h int32) {
	if bootLockVoiceStepActive() {
		bootLockVoiceDrawPill(renderer, f, cx, y, h)
		return
	}
	if bootLock.kind == bootLockKindPassword {
		label := "Press A to type"
		if bootLockWaitLeft() > 0 && (bootLock.mode == bootLockModeLocked || bootLock.step == bootLockStepCurrent) {
			label = "Wait for the timer"
		}
		pillW := int32(260)
		fillRoundedRect(renderer, cx-pillW/2, y, pillW, h, h/2, glassA(18))
		strokeRoundedRect(renderer, cx-pillW/2, y, pillW, h, h/2, 1, glassA(40))
		drawText(renderer, f.small, label, cx, y+(h-int32(f.small.Height()))/2, ColorTextSecondary(), textAlignCenter)
		return
	}
	n := len(bootLock.entry)
	slots := n
	if slots < bootLockCodeMin {
		slots = bootLockCodeMin
	}
	r := int32(9)
	gap := int32(16)
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

// bootLockDrawAbove is the lock while the password keyboard is up: the name,
// the prompt and the status above the keyboard's sheet.
func bootLockDrawAbove(renderer *sdl.Renderer, config *Config, f bootLockFontSet, limit int32) {
	if f.title == nil || f.body == nil || f.small == nil {
		return
	}
	if limit <= 0 || limit > screenHeight {
		limit = screenHeight / 2
	}
	inner := screenWidth - 64
	help := []string(nil)
	if bootLock.mode == bootLockModeLocked {
		help = wrapTextLines(f.small, bootLockForgotText(), inner, 2)
	}
	if voice := bootLockVoiceLine(config); voice != "" {
		// Still listening for the phrase while the password is typed.
		help = append([]string{ellipsize(f.small, voice, inner)}, help...)
	}
	h := int32(f.title.Height()) + 4 + int32(f.body.Height()) + 8 + int32(f.body.Height()) +
		6 + int32(len(help))*(int32(f.small.Height())+2)
	y := (limit - h) / 2
	if y < 8 {
		y = 8
	}
	cx := screenWidth / 2
	drawText(renderer, f.title, ellipsize(f.title, bootLockTitle(config), inner), cx, y, ColorTextPrimary(), textAlignCenter)
	y += int32(f.title.Height()) + 4
	drawText(renderer, f.body, ellipsize(f.body, bootLockPrompt(), inner), cx, y, ColorTextSecondary(), textAlignCenter)
	y += int32(f.body.Height()) + 8
	if msg, warn := bootLockStatus(); msg != "" {
		ink := ColorTextSecondary()
		if warn {
			ink = WarningInk()
		}
		drawText(renderer, f.body, ellipsize(f.body, msg, inner), cx, y, ink, textAlignCenter)
	}
	y += int32(f.body.Height()) + 6
	for _, line := range help {
		drawText(renderer, f.small, line, cx, y, ColorTextTertiary(), textAlignCenter)
		y += int32(f.small.Height()) + 2
	}
}

// bootLockHints is the footer for what is on screen.
func bootLockHints(config *Config) []FooterHint {
	if bootLock.power {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	}
	if hints := bootLockVoiceHints(); hints != nil {
		return hints
	}
	locked := bootLock.mode == bootLockModeLocked
	// An "either" voice lock: R2 says the phrase (V on a keyboard).
	speak := locked && bootLockVoiceEitherUp(config) && !bootLockVoice.paused
	if bootLock.kind == bootLockKindPassword {
		if locked && speak {
			return []FooterHint{{Button: "A", Label: "Type"}, {Button: "R2 / V", Label: "Speak"}, {Button: "Menu", Label: "Power"}}
		}
		if locked {
			return []FooterHint{{Button: "A", Label: "Type"}, {Button: "Menu", Label: "Power"}}
		}
		return []FooterHint{{Button: "A", Label: "Type"}, {Button: "B", Label: "Cancel"}}
	}
	sel := FooterHint{Button: "Select", Label: "Delete"}
	if len(bootLock.entry) == 0 && !locked {
		sel.Label = "Cancel"
	}
	start := FooterHint{Button: "Start", Label: "Unlock"}
	if !locked {
		start.Label = "Next"
		if bootLock.step == bootLockStepConfirm {
			start.Label = "Save"
		}
	}
	if locked && speak {
		return []FooterHint{start, sel, {Button: "R2 / V", Label: "Speak"}, {Button: "Menu", Label: "Power"}}
	}
	if locked {
		return []FooterHint{start, sel, {Button: "Menu", Label: "Power"}}
	}
	return []FooterHint{start, sel}
}

func bootLockDrawFooter(renderer *sdl.Renderer, config *Config) {
	band := sceneFooterBand()
	drawGlassFooterBand(renderer, band)
	pad := scaleX(16, screenWidth)
	plan := cachedFooterPlan(config, bootLockHints(config), band.W-2*pad)
	vkDrawHintPlan(renderer, config, plan, band)
}

// bootLockDrawPower is the power choices over the lock.
func bootLockDrawPower(renderer *sdl.Renderer, config *Config, f bootLockFontSet) {
	if f.title == nil || f.body == nil {
		return
	}
	drawOverlayScrim(renderer)
	items := bootLockPowerItems()
	rowH, gap, pad := int32(52), int32(8), int32(22)
	w := int32(380)
	if w > screenWidth-40 {
		w = screenWidth - 40
	}
	h := pad + int32(f.title.Height()) + 14 + int32(len(items))*(rowH+gap) - gap + pad
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	y := card.Y + pad
	drawText(renderer, f.title, "Power", card.X+card.W/2, y, ColorTextPrimary(), textAlignCenter)
	y += int32(f.title.Height()) + 14
	if cap(bootLock.powerRects) < len(items) {
		bootLock.powerRects = make([]sdl.Rect, len(items))
	}
	bootLock.powerRects = bootLock.powerRects[:len(items)]
	for i, it := range items {
		r := sdl.Rect{X: card.X + pad, Y: y, W: card.W - 2*pad, H: rowH}
		bootLock.powerRects[i] = r
		UIListRow(renderer, r, glassRowRadius, i == bootLock.powerFocus, false)
		drawText(renderer, f.body, bootLockPowerLabel(it), r.X+r.W/2, r.Y+(r.H-int32(f.body.Height()))/2, ColorTextPrimary(), textAlignCenter)
		y += rowH + gap
	}
}
