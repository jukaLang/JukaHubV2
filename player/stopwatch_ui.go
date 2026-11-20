package main

// stopwatch_ui.go — the Stopwatch scene: two tabs over the clocks in
// stopwatch.go.
//
//   - Stopwatch: the reading in large digits, the lap running now under it,
//     and the laps newest first, each with its own time and the total, the
//     fastest and the slowest marked. A starts and pauses, X takes a lap while
//     it runs, Y clears it once it is paused.
//   - Countdown: a ring that empties as the time runs out, the time left in
//     the middle. While it is being set the middle holds the hours, minutes
//     and seconds (Left/Right choose one, Up/Down change it — held, they
//     repeat) and the presets sit under the ring (Y steps through them). A
//     starts and pauses, Y resets.
//
// L1/R1 (Tab on a keyboard) or a tap on the tab row switch tabs, and the tab
// used last is the one the app opens on. Every pill on screen can be tapped;
// the wheel scrolls the laps or changes the field being set.

import (
	"fmt"
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// swHit is one tappable thing drawn this frame.
type swHit struct {
	r    sdl.Rect
	kind int
	arg  int
}

const (
	swHitTab = iota
	swHitAction
	swHitField
	swHitPreset
)

// The actions behind the pills (swHitAction's arg).
const (
	swActToggle = iota
	swActLap
	swActReset
	swActCdToggle
	swActCdReset
)

// swHits are this frame's targets; swLapRows is how many lap rows fit, for
// the scroll. Main thread only.
var (
	swHits    []swHit
	swLapRows int
)

var (
	swFastInk = sdl.Color{R: 96, G: 214, B: 140, A: 255}
	swSlowInk = sdl.Color{R: 240, G: 146, B: 96, A: 255}
)

// ── Entering and leaving ──────────────────────────────────────────────────

// stopwatchEnter runs when the scene opens.
func stopwatchEnter(config *Config) {
	swEnsureLoaded()
	sw.lapScroll = 0
	swHits = nil
}

// stopwatchLeave runs when the scene closes: the clocks keep running, only
// the frame's tap targets go.
func stopwatchLeave() {
	swHits = nil
}

// ── Input ─────────────────────────────────────────────────────────────────

// swPressA starts or pauses the tab's clock.
func swPressA() {
	if sw.tab == swTabCountdown {
		swCdToggle()
		return
	}
	swToggle()
}

// swPressX takes a lap (the stopwatch, while it runs).
func swPressX() {
	if sw.tab == swTabStopwatch {
		swLap()
	}
}

// swPressY clears the stopwatch, or on the countdown steps the presets while
// it is being set and resets it once it has started (or run out).
func swPressY() {
	switch {
	case sw.tab == swTabStopwatch:
		swReset()
	case sw.cdDone || !swCdEditable():
		swCdReset()
	default:
		swCdPreset()
	}
}

// swPressDir is the D-pad: the laps scroll on the stopwatch; on a countdown
// being set Left/Right choose the field and Up/Down change it.
func swPressDir(dx, dy int) {
	if sw.tab == swTabStopwatch {
		if dy != 0 {
			swScrollLaps(dy)
		}
		return
	}
	switch {
	case dx != 0:
		swCdField(dx)
	case dy != 0:
		swCdAdjust(-dy)
	}
}

// swScrollLaps moves the lap list by delta rows (the drawing clamps the end).
func swScrollLaps(delta int) {
	next := min(max(sw.lapScroll+delta, 0), max(len(sw.laps)-max(swLapRows, 1), 0))
	if next != sw.lapScroll {
		sw.lapScroll = next
		PlayNavSound()
	}
}

// swHandleKey is the keyboard: Enter/Space = A, X, Y, Tab = L1/R1, the arrows,
// PgUp/PgDn through the laps, Esc/Backspace = B.
func swHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	swEnsureLoaded()
	repeat := e.Repeat != 0
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		if !repeat {
			swPressA()
		}
	case sdl.K_x:
		if !repeat {
			swPressX()
		}
	case sdl.K_y:
		if !repeat {
			swPressY()
		}
	case sdl.K_TAB:
		if !repeat {
			swSetTab(1 - sw.tab)
		}
	case sdl.K_LEFT:
		swPressDir(-1, 0)
	case sdl.K_RIGHT:
		swPressDir(1, 0)
	case sdl.K_UP:
		swPressDir(0, -1)
	case sdl.K_DOWN:
		swPressDir(0, 1)
	case sdl.K_PAGEUP:
		swScrollLaps(-max(swLapRows, 1))
	case sdl.K_PAGEDOWN:
		swScrollLaps(max(swLapRows, 1))
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		if !repeat {
			goBackScene(config)
		}
	default:
		return false
	}
	return true
}

// swHandleController is the pad. A held D-pad repeats through navrepeat.go.
func swHandleController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type == sdl.CONTROLLERBUTTONUP {
		return false
	}
	swEnsureLoaded()
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		swPressA()
	case sdl.CONTROLLER_BUTTON_X:
		swPressX()
	case sdl.CONTROLLER_BUTTON_Y:
		swPressY()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		swSetTab(1 - sw.tab)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		swPressDir(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		swPressDir(1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		swPressDir(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		swPressDir(0, 1)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// stopwatchClick handles a tap and reports whether it hit anything.
func stopwatchClick(mx, my int32, config *Config) bool {
	for _, h := range swHits {
		if !pointInRect(mx, my, h.r) {
			continue
		}
		switch h.kind {
		case swHitTab:
			swSetTab(h.arg)
		case swHitField:
			if swCdEditable() && sw.field != h.arg {
				sw.field = h.arg
				PlayNavSound()
				swSave()
			}
		case swHitPreset:
			if h.arg >= 0 && h.arg < len(swPresets) {
				swCdSetLength(swPresets[h.arg])
			}
		case swHitAction:
			switch h.arg {
			case swActToggle:
				swToggle()
			case swActLap:
				swLap()
			case swActReset:
				swReset()
			case swActCdToggle:
				swCdToggle()
			case swActCdReset:
				swCdReset()
			}
		}
		return true
	}
	return false
}

// stopwatchWheel scrolls the laps, or changes the field of a countdown being
// set. y > 0 is the wheel turned away from the user.
func stopwatchWheel(y int32) bool {
	if y == 0 {
		return false
	}
	step := 1
	if y > 0 {
		step = -1
	}
	if sw.tab == swTabStopwatch {
		if len(sw.laps) == 0 {
			return false
		}
		swScrollLaps(step)
		return true
	}
	if !swCdEditable() {
		return false
	}
	swCdAdjust(-step)
	return true
}

// stopwatchFooterHints are the footer's keys for the tab and its state.
func stopwatchFooterHints() []FooterHint {
	tab := FooterHint{Button: "L1/R1", Label: "Tab"}
	back := FooterHint{Button: "B", Label: "Back"}
	if sw.tab == swTabCountdown {
		switch {
		case sw.cdRunning:
			return []FooterHint{{Button: "A", Label: "Pause"}, {Button: "Y", Label: "Reset"}, tab, back}
		case sw.cdStarted:
			return []FooterHint{{Button: "A", Label: "Resume"}, {Button: "Y", Label: "Reset"}, tab, back}
		case sw.cdDone:
			return []FooterHint{{Button: "A", Label: "Restart"}, {Button: "Y", Label: "Reset"}, tab, back}
		}
		return []FooterHint{{Button: "A", Label: "Start"}, {Button: "Left/Right", Label: "Field"},
			{Button: "Up/Down", Label: "Change"}, {Button: "Y", Label: "Preset"}, tab, back}
	}
	switch {
	case sw.running:
		return []FooterHint{{Button: "A", Label: "Pause"}, {Button: "X", Label: "Lap"}, tab, back}
	case sw.banked > 0:
		hints := []FooterHint{{Button: "A", Label: "Resume"}, {Button: "Y", Label: "Reset"}}
		if len(sw.laps) > swLapRows {
			hints = append(hints, FooterHint{Button: "Up/Down", Label: "Laps"})
		}
		return append(hints, tab, back)
	}
	return []FooterHint{{Button: "A", Label: "Start"}, tab, back}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// swFonts are the scene's four sizes, each falling back to the next smaller.
type swFonts struct {
	small, medium, large, big *ttf.Font
}

// renderStopwatch draws the scene: the tab row, then the tab's clock.
func renderStopwatch(renderer *sdl.Renderer, config *Config) {
	swEnsureLoaded()
	// The countdown ends on the frame it runs out while it is on screen,
	// rather than on the next once-a-second tick.
	swCheck()
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	swHits = swHits[:0]
	var f swFonts
	f.small, _ = getCachedFont(config, "small")
	f.medium, _ = getCachedFont(config, "medium")
	f.large, _ = getCachedFont(config, "large")
	f.big, _ = getCachedFont(config, "big")
	if f.medium == nil {
		f.medium = f.small
	}
	if f.large == nil {
		f.large = f.medium
	}
	if f.big == nil {
		f.big = f.large
	}
	if f.small != nil {
		top := headerBarHeight()
		bottom := screenHeight - footerBarHeight()
		pad := max32(8, screenHeight/48)
		y := swDrawTabs(renderer, f, top+pad)
		body := sdl.Rect{X: 2 * pad, Y: y + pad, W: screenWidth - 4*pad, H: bottom - y - 2*pad}
		if body.W > 40 && body.H > 40 {
			if sw.tab == swTabCountdown {
				swDrawCountdown(renderer, f, body, pad)
			} else {
				swDrawStopwatch(renderer, f, body, pad)
			}
		}
	}
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// swPill draws one rounded button: filled when active, dimmed when it does
// nothing now. Its label is centred.
func swPill(renderer *sdl.Renderer, font *ttf.Font, r sdl.Rect, label string, active, enabled bool) {
	radius := r.H / 2
	accent := glassAccent()
	ink := ColorTextPrimary()
	switch {
	case active:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(accent, 90))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 2, accent)
	case enabled:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassA(22))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassSheetEdge())
		ink = ColorTextSecondary()
	default:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassA(10))
		ink = WithAlpha(ColorTextTertiary(), 160)
	}
	_, th := measureText(font, label)
	drawText(renderer, font, label, r.X+r.W/2, r.Y+(r.H-th)/2, ink, textAlignCenter)
}

// swPillRow lays pills out centred on cx at y and records them as actions.
// Each entry is a label, an action and whether it is live now; the first is
// the main one (filled).
func swPillRow(renderer *sdl.Renderer, font *ttf.Font, cx, y int32, labels []string, acts []int, live []bool) int32 {
	padX := max32(14, int32(font.Height())*3/4)
	h := int32(font.Height()) + 14
	gap := padX / 2
	widths := make([]int32, len(labels))
	total := int32(0)
	for i, l := range labels {
		w, _ := measureText(font, l)
		widths[i] = max32(w+2*padX, h*2)
		total += widths[i]
	}
	total += gap * int32(len(labels)-1)
	x := cx - total/2
	for i, l := range labels {
		r := sdl.Rect{X: x, Y: y, W: widths[i], H: h}
		swPill(renderer, font, r, l, i == 0 && live[i], live[i])
		if live[i] {
			swHits = append(swHits, swHit{r: r, kind: swHitAction, arg: acts[i]})
		}
		x += widths[i] + gap
	}
	return y + h
}

// swDrawTabs draws the tab row centred at y and answers where it ends.
func swDrawTabs(renderer *sdl.Renderer, f swFonts, y int32) int32 {
	labels := [2]string{"Stopwatch", "Countdown"}
	font := f.medium
	padX := max32(16, int32(font.Height()))
	h := int32(font.Height()) + 12
	var widths [2]int32
	for i, l := range labels {
		w, _ := measureText(font, l)
		widths[i] = w + 2*padX
	}
	gap := int32(8)
	x := (screenWidth - widths[0] - widths[1] - gap) / 2
	for i, l := range labels {
		r := sdl.Rect{X: x, Y: y, W: widths[i], H: h}
		swPill(renderer, font, r, l, sw.tab == i, true)
		swHits = append(swHits, swHit{r: r, kind: swHitTab, arg: i})
		x += widths[i] + gap
	}
	return y + h
}

// swCentered draws text exactly as written (digits need no translation),
// centred on cx with its top at y, and answers its height.
func swCentered(renderer *sdl.Renderer, font *ttf.Font, text string, cx, y int32, ink sdl.Color) int32 {
	drawTextRaw(renderer, font, text, cx, y, ink, textAlignCenter)
	return int32(font.Height())
}

// swDrawStopwatch draws the stopwatch tab: the reading, its state, the pills
// and the laps — beside the reading on a wide screen, under it otherwise.
func swDrawStopwatch(renderer *sdl.Renderer, f swFonts, body sdl.Rect, pad int32) {
	timeArea, lapArea := body, body
	wide := body.W >= 900 && body.W > body.H*3/2
	if wide {
		timeArea.W = body.W * 54 / 100
		lapArea.X = timeArea.X + timeArea.W + pad
		lapArea.W = body.X + body.W - lapArea.X
	} else {
		need := int32(f.big.Height()) + int32(f.small.Height()) + int32(f.medium.Height()) + 14 + 4*pad
		timeArea.H = min32(need, body.H*60/100)
		lapArea.Y = timeArea.Y + timeArea.H + pad/2
		lapArea.H = body.Y + body.H - lapArea.Y
	}
	cx := timeArea.X + timeArea.W/2
	elapsed := swElapsed()
	block := int32(f.big.Height()) + pad/2 + int32(f.small.Height()) + pad + int32(f.medium.Height()) + 14
	y := timeArea.Y + max32(0, (timeArea.H-block)/2)
	ink := ColorTextPrimary()
	if !sw.running && elapsed > 0 {
		ink = ColorTextSecondary()
	}
	y += swCentered(renderer, f.big, swClock(elapsed), cx, y, ink) + pad/2
	switch {
	case sw.running:
		lapStart := time.Duration(0)
		if n := len(sw.laps); n > 0 {
			lapStart = sw.laps[n-1]
		}
		// Only the word goes through the interface language: the line changes
		// ten times a second, and each new string would be a translation-cache
		// miss.
		status := uiT("Lap") + fmt.Sprintf(" %d · %s", len(sw.laps)+1, swClock(elapsed-lapStart))
		drawTextRaw(renderer, f.small, status, cx, y, ColorTextSecondary(), textAlignCenter)
	case elapsed > 0:
		drawText(renderer, f.small, "Paused", cx, y, ColorTextSecondary(), textAlignCenter)
	default:
		drawText(renderer, f.small, hintSentence("Press %s to start", "A", "Start"), cx, y, ColorTextSecondary(), textAlignCenter)
	}
	y += int32(f.small.Height()) + pad
	toggle := "Start"
	switch {
	case sw.running:
		toggle = "Pause"
	case elapsed > 0:
		toggle = "Resume"
	}
	swPillRow(renderer, f.medium, cx, y, []string{toggle, "Lap", "Reset"},
		[]int{swActToggle, swActLap, swActReset},
		[]bool{true, sw.running && len(sw.laps) < swMaxLaps, !sw.running && (elapsed > 0 || len(sw.laps) > 0)})
	if lapArea.H > int32(f.small.Height())*2 {
		swDrawLaps(renderer, f, lapArea)
	}
}

// swDrawLaps draws the lap list in r: newest first, each lap's number, its own
// time and the total, with the fastest and slowest marked.
func swDrawLaps(renderer *sdl.Renderer, f swFonts, r sdl.Rect) {
	fill := glassSheetFill()
	fill.A = 150
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, fill)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, 1, glassSheetEdge())
	font := f.small
	lineH := int32(font.Height())
	inner := sdl.Rect{X: r.X + 14, Y: r.Y + 10, W: r.W - 28, H: r.H - 20}
	title := "Laps"
	if n := len(sw.laps); n > 0 {
		title = fmt.Sprintf("Laps · %d", n)
	}
	drawText(renderer, font, title, inner.X, inner.Y, ColorTextSecondary(), textAlignLeft)
	totalW, _ := measureText(font, "00:00:00.0")
	colTotal := inner.X + inner.W
	colLap := colTotal - totalW - 24
	drawText(renderer, font, "Lap", colLap, inner.Y, ColorTextTertiary(), textAlignRight)
	drawText(renderer, font, "Total", colTotal, inner.Y, ColorTextTertiary(), textAlignRight)
	y := inner.Y + lineH + 8
	rowH := lineH + 8
	swLapRows = max(1, int((inner.Y+inner.H-y)/rowH))
	n := len(sw.laps)
	if n == 0 {
		drawText(renderer, font, hintSentence("No laps yet — %s takes one while it runs", "X", "Lap"),
			inner.X, y, ColorTextTertiary(), textAlignLeft)
		return
	}
	sw.lapScroll = min(max(sw.lapScroll, 0), max(n-swLapRows, 0))
	fast, slow := swLapExtremes()
	for row := 0; row < swLapRows; row++ {
		i := n - 1 - sw.lapScroll - row
		if i < 0 {
			break
		}
		label := fmt.Sprintf("Lap %d", i+1)
		ink := ColorTextPrimary()
		switch i {
		case fast:
			label += " · fastest"
			ink = swFastInk
		case slow:
			label += " · slowest"
			ink = swSlowInk
		}
		if row%2 == 1 {
			fillRoundedRect(renderer, inner.X-6, y-4, inner.W+12, rowH, 6, glassA(8))
		}
		drawText(renderer, font, label, inner.X, y, ink, textAlignLeft)
		drawTextRaw(renderer, font, swClock(swLapTime(i)), colLap, y, ink, textAlignRight)
		drawTextRaw(renderer, font, swClock(sw.laps[i]), colTotal, y, ColorTextSecondary(), textAlignRight)
		y += rowH
	}
	if sw.lapScroll > 0 || n > sw.lapScroll+swLapRows {
		more := fmt.Sprintf("%d-%d of %d", sw.lapScroll+1, min(n, sw.lapScroll+swLapRows), n)
		drawTextRaw(renderer, font, more, inner.X+inner.W/2, r.Y+r.H-lineH-4, ColorTextTertiary(), textAlignCenter)
	}
}

// swDrawCountdown draws the countdown tab: the ring with the time (or the
// fields being set) in it, the presets while it is set, and the pills.
func swDrawCountdown(renderer *sdl.Renderer, f swFonts, body sdl.Rect, pad int32) {
	pillH := int32(f.medium.Height()) + 14
	chipH := int32(f.small.Height()) + 12
	below := pillH + pad
	if swCdEditable() {
		below += chipH + pad
	}
	r := min32((body.H-below-pad)/2, body.W/4)
	r = max32(40, min32(r, 170))
	cx := body.X + body.W/2
	cy := body.Y + r + max32(0, (body.H-below-2*r-pad)/2)

	left := swCountdownLeft()
	accent := glassAccent()
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	thick := max32(6, r/14)
	renderCircleSectorColor(renderer, cx, cy, r, r-thick, 0, 2*math.Pi, glassA(30))
	frac := 0.0
	switch {
	case swCdEditable():
		frac = 1
	case sw.cdSet > 0:
		frac = math.Max(0, math.Min(1, float64(left)/float64(sw.cdSet)))
	}
	arc := accent
	if swCdEditable() {
		arc = WithAlpha(accent, 90)
	} else if !sw.cdRunning {
		arc = WithAlpha(ColorTextTertiary(), 200)
	}
	if frac > 0 && !sw.cdDone {
		renderCircleSectorColor(renderer, cx, cy, r, r-thick, -math.Pi/2, -math.Pi/2+2*math.Pi*frac, arc)
	}
	disk := glassSheetFill()
	disk.A = 120
	fillCircle(renderer, cx, cy, r-thick-2, disk)

	switch {
	case sw.cdDone:
		th := int32(f.large.Height())
		drawText(renderer, f.large, "Time's up", cx, cy-th/2-int32(f.small.Height())/2, accent, textAlignCenter)
		drawText(renderer, f.small, swClockShort(sw.cdSet)+" timer", cx, cy+th/2, ColorTextSecondary(), textAlignCenter)
	case swCdEditable():
		swDrawFields(renderer, f, cx, cy, 2*(r-thick)-16)
	default:
		text := swLeft(left)
		font := f.big
		if w, _ := measureText(font, text); w > 2*(r-thick)-12 {
			font = f.large
		}
		th := int32(font.Height())
		ink := ColorTextPrimary()
		if !sw.cdRunning {
			ink = ColorTextSecondary()
		}
		swCentered(renderer, font, text, cx, cy-th/2, ink)
		sub := "of " + swClockShort(sw.cdSet)
		if !sw.cdRunning {
			sub = "Paused · " + sub
		}
		drawText(renderer, f.small, sub, cx, cy+th/2+2, ColorTextSecondary(), textAlignCenter)
	}

	y := cy + r + pad
	if swCdEditable() {
		swDrawPresets(renderer, f.small, body, y, chipH)
		y += chipH + pad
	}
	toggle := "Start"
	switch {
	case sw.cdRunning:
		toggle = "Pause"
	case sw.cdStarted:
		toggle = "Resume"
	case sw.cdDone:
		toggle = "Restart"
	}
	swPillRow(renderer, f.medium, cx, y, []string{toggle, "Reset"},
		[]int{swActCdToggle, swActCdReset},
		[]bool{sw.cdStarted || sw.cdSet > 0, sw.cdStarted || sw.cdDone})
}

// swDrawFields draws the hours, minutes and seconds being set, the focused one
// boxed, in a row at most maxW wide centred on (cx, cy).
func swDrawFields(renderer *sdl.Renderer, f swFonts, cx, cy, maxW int32) {
	font := f.large
	digitsW, _ := measureText(font, "00")
	if 3*(digitsW+20)+16 > maxW {
		font = f.medium
		digitsW, _ = measureText(font, "00")
	}
	boxW := digitsW + 20
	boxH := int32(font.Height()) + 10
	gap := int32(8)
	total := 3*boxW + 2*gap
	x := cx - total/2
	y := cy - (boxH+int32(f.small.Height())+4)/2
	parts := swCdParts()
	units := [3]string{"h", "min", "s"}
	accent := glassAccent()
	for i := 0; i < 3; i++ {
		r := sdl.Rect{X: x, Y: y, W: boxW, H: boxH}
		if i == sw.field {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(accent, 70))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 2, accent)
		} else {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, glassA(16))
		}
		drawTextRaw(renderer, font, fmt.Sprintf("%02d", parts[i]), r.X+r.W/2, r.Y+5, ColorTextPrimary(), textAlignCenter)
		drawText(renderer, f.small, units[i], r.X+r.W/2, r.Y+r.H+4, ColorTextSecondary(), textAlignCenter)
		swHits = append(swHits, swHit{r: r, kind: swHitField, arg: i})
		x += boxW + gap
	}
}

// swDrawPresets draws the preset chips in a row centred in body at y, the one
// matching the length lit. Narrow screens get the short labels.
func swDrawPresets(renderer *sdl.Renderer, font *ttf.Font, body sdl.Rect, y, h int32) {
	labels := make([]string, len(swPresets))
	widths := make([]int32, len(swPresets))
	gap := int32(6)
	fit := func(short bool) int32 {
		total := gap * int32(len(swPresets)-1)
		for i, p := range swPresets {
			m := int(p / time.Minute)
			labels[i] = fmt.Sprintf("%d min", m)
			if short {
				labels[i] = fmt.Sprintf("%dm", m)
			}
			w, _ := measureText(font, labels[i])
			widths[i] = w + 20
			total += widths[i]
		}
		return total
	}
	total := fit(false)
	if total > body.W {
		total = fit(true)
	}
	x := body.X + (body.W-total)/2
	for i := range swPresets {
		r := sdl.Rect{X: x, Y: y, W: widths[i], H: h}
		swPill(renderer, font, r, labels[i], sw.cdSet == swPresets[i], true)
		swHits = append(swHits, swHit{r: r, kind: swHitPreset, arg: i})
		x += widths[i] + gap
	}
}

// ── Registration ──────────────────────────────────────────────────────────

type stopwatchScene struct{ sceneBase }

func (stopwatchScene) render(r *sdl.Renderer, c *Config) bool { renderStopwatch(r, c); return true }

func (stopwatchScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return swHandleKey(e, c) }

func (stopwatchScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return swHandleController(e, c)
}

func init() { registerScene(stopwatchScene{sceneBase{name: stopwatchSceneName}}) }
