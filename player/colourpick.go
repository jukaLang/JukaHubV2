package main

// colourpick.go — the RGB colour picker every colour row in Settings →
// Theme & look opens on A: a live swatch beside
// the colour it replaces, Red, Green and Blue sliders from 0 to 255, the
// #RRGGBB readout and, where the colour is read against another one, their
// contrast.
//
// Up/Down picks a slider (and the Apply / Cancel buttons under them),
// Left/Right moves it by 5 — a held D-pad repeats like everywhere else
// (navrepeat.go) — and L1/R1 by 1, repeating while held. X types the colour
// on the keyboard (#RRGGBB or R,G,B, the forms the colour rows always took),
// Y steps through the row's presets — the theme's own colour, the colour
// palettes' and the row's swatches — A applies and B cancels. Nothing behind
// the sheet changes before A: the swatch is the preview, so a slider step
// never repaints the theme (which drops the text and shape caches).
//
// The sheet belongs to the Settings hub: setui.go draws it after the page and
// hands it every key, button, tap and wheel step while it is open.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// colourPickSlot is what a colour row hands the sheet.
type colourPickSlot struct {
	title string
	note  string
	// current is the colour on screen now (the theme's when not overridden).
	current func(c *Config) sdl.Color
	// value is the row's stored value, matched against the presets' Values
	// so Y starts from the stop the row is on.
	value func(c *Config) string
	// presets are Y's stops; each one's Swatch is the colour it loads.
	presets func(c *Config) []setOption
	// apply stores a preset's Value, or the "#rrggbb" the sliders made.
	apply func(c *Config, value string)
	// against is the colour this one is read with ("the background" for
	// text) and minContrast the ratio it should reach; nil shows no line.
	against     func() (sdl.Color, string)
	minContrast float64
}

// The sheet's focus stops, top to bottom.
const (
	colourPickRowRed = iota
	colourPickRowGreen
	colourPickRowBlue
	colourPickRowButtons
)

// colourPickCoarse is a Left/Right step; L1/R1 step by one.
const colourPickCoarse = 5

// colourPick is the open sheet. Main thread only.
var colourPick struct {
	open    bool
	slot    colourPickSlot
	cfg     *Config
	rgb     [3]int
	before  sdl.Color
	focus   int
	button  int // 0 Apply, 1 Cancel
	presets []setOption
	preset  int // the preset Y loaded last (-1 none)
	changed bool
	hold    navRepeatTracker // L1/R1 held
	// The last frame's geometry, for taps.
	card    sdl.Rect
	rows    [3]sdl.Rect
	tracks  [3]sdl.Rect
	buttons [2]sdl.Rect
}

// colourPickOpen opens the sheet on the row's colour as it is now.
func colourPickOpen(c *Config, slot colourPickSlot) {
	if c == nil || slot.current == nil || slot.apply == nil {
		return
	}
	col := slot.current(c)
	colourPick.open = true
	colourPick.slot = slot
	colourPick.cfg = c
	colourPick.rgb = [3]int{int(col.R), int(col.G), int(col.B)}
	colourPick.before = sdl.Color{R: col.R, G: col.G, B: col.B, A: 255}
	colourPick.focus = colourPickRowRed
	colourPick.button = 0
	colourPick.changed = false
	colourPick.hold = navRepeatTracker{}
	colourPick.presets = nil
	if slot.presets != nil {
		colourPick.presets = slot.presets(c)
	}
	colourPick.preset = -1
	if slot.value != nil {
		v := slot.value(c)
		for i, o := range colourPick.presets {
			if strings.EqualFold(o.Value, v) {
				colourPick.preset = i
				break
			}
		}
	}
	PlayActivateSound()
}

// colourPickClose dismisses the sheet without changing anything.
func colourPickClose() {
	if !colourPick.open {
		return
	}
	colourPick.open = false
	PlayBackSound()
}

// colourPickHubEnter is the hub's new-visit hook: a sheet left open on an
// earlier visit does not reappear.
func colourPickHubEnter() { colourPick.open = false }

// colourPickColor is the sliders' colour.
func colourPickColor() sdl.Color {
	return sdl.Color{R: uint8(colourPick.rgb[0]), G: uint8(colourPick.rgb[1]), B: uint8(colourPick.rgb[2]), A: 255}
}

// colourPickHex is the sliders' colour as "#rrggbb".
func colourPickHex() string { return colourHexOf(colourPickColor()) }

// colourPickPresetLoaded is the preset the sliders still show, or -1.
func colourPickPresetLoaded() int {
	i := colourPick.preset
	if i < 0 || i >= len(colourPick.presets) {
		return -1
	}
	col, ok := parseHexColor(colourPick.presets[i].Swatch)
	if !ok || col != colourPickColor() {
		return -1
	}
	return i
}

// colourPickNudge moves the focused slider by dir*step; on the buttons
// Left/Right moves between them.
func colourPickNudge(dir, step int) {
	f := colourPick.focus
	if f == colourPickRowButtons {
		if step == colourPickCoarse {
			b := colourPick.button + dir
			if b >= 0 && b <= 1 && b != colourPick.button {
				colourPick.button = b
				PlayNavSound()
			}
		}
		return
	}
	if f < 0 || f > colourPickRowBlue {
		return
	}
	v := colourPick.rgb[f] + dir*step
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	if v == colourPick.rgb[f] {
		return
	}
	colourPick.rgb[f] = v
	colourPick.changed = true
	PlayNavSound()
}

// colourPickMove is Up/Down between the sliders and the buttons.
func colourPickMove(dir int) {
	f := colourPick.focus + dir
	if f < colourPickRowRed || f > colourPickRowButtons {
		return
	}
	colourPick.focus = f
	if f == colourPickRowButtons {
		colourPick.button = 0
	}
	PlayNavSound()
}

// colourPickPreset is Y: the next preset's colour on the sliders.
func colourPickPreset(dir int) {
	n := len(colourPick.presets)
	if n == 0 {
		ShowToast("This colour has no presets", ToastKindInfo)
		return
	}
	for tries := 0; tries < n; tries++ {
		i := colourPick.preset + dir
		if colourPick.preset < 0 && dir < 0 {
			i = n - 1
		}
		colourPick.preset = (i%n + n) % n
		col, ok := parseHexColor(colourPick.presets[colourPick.preset].Swatch)
		if !ok {
			continue
		}
		colourPick.rgb = [3]int{int(col.R), int(col.G), int(col.B)}
		colourPick.changed = true
		PlayNavSound()
		return
	}
}

// colourPickType is X: the colour typed on the keyboard, onto the sliders.
// A applies it as usual, so a typo is seen before it lands.
func colourPickType() {
	seed := strings.ToUpper(colourPickHex())
	osPromptOpen(colourPick.slot.title, "#RRGGBB or R,G,B", "Use", seed, 16, func(typed string) {
		if !colourPick.open || typed == "" {
			return
		}
		rgb, ok := parseColorValue(typed)
		if !ok {
			ShowToast("Not a colour: type #RRGGBB or R,G,B", ToastKindWarning)
			PlayErrorSound()
			return
		}
		colourPick.rgb = [3]int{rgb.R, rgb.G, rgb.B}
		colourPick.changed = true
	})
}

// colourPickApply is A: the row stores the colour (a preset's own value when
// the sliders still show it, so "Theme's own" stays the default and a named
// menu colour keeps its name), and the sheet closes.
func colourPickApply() {
	c, slot := colourPick.cfg, colourPick.slot
	colourPick.open = false
	if !colourPick.changed || c == nil || slot.apply == nil {
		PlayBackSound()
		return
	}
	value := colourPickHex()
	if i := colourPickPresetLoaded(); i >= 0 {
		value = colourPick.presets[i].Value
	}
	PlayActivateSound()
	slot.apply(c, value)
	setHub.dirty = true
}

// colourPickActivate is A or Enter on the focused stop.
func colourPickActivate() {
	if colourPick.focus == colourPickRowButtons && colourPick.button == 1 {
		colourPickClose()
		return
	}
	colourPickApply()
}

// colourPickTickHold repeats L1/R1 while held (the D-pad repeats through
// navrepeat.go; the shoulders only send one press).
func colourPickTickHold() {
	dx := 0
	if !virtualKeyboardActive {
		if vkLogicalHeld(uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER)) {
			dx = -1
		} else if vkLogicalHeld(uint8(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER)) {
			dx = 1
		}
	}
	if mx, _ := colourPick.hold.step(sdl.GetTicks64(), dx, 0); mx != 0 {
		colourPickNudge(mx, 1)
	}
}

// ── Input ─────────────────────────────────────────────────────────────────

// colourPickKey handles a key while the sheet is open.
func colourPickKey(e *sdl.KeyboardEvent) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		colourPickMove(-1)
	case sdl.K_DOWN, sdl.K_TAB:
		colourPickMove(1)
	case sdl.K_LEFT:
		colourPickNudge(-1, colourPickCoarse)
	case sdl.K_RIGHT:
		colourPickNudge(1, colourPickCoarse)
	case sdl.K_PAGEDOWN:
		colourPickNudge(-1, 1)
	case sdl.K_PAGEUP:
		colourPickNudge(1, 1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			colourPickActivate()
		}
	case sdl.K_x:
		if e.Repeat == 0 {
			colourPickType()
		}
	case sdl.K_y:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			colourPickPreset(-1)
		} else {
			colourPickPreset(1)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		colourPickClose()
	}
}

// colourPickPad handles a pad button while the sheet is open.
func colourPickPad(button uint8) {
	switch sdl.GameControllerButton(button) {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		colourPickMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		colourPickMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		colourPickNudge(-1, colourPickCoarse)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		colourPickNudge(1, colourPickCoarse)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		colourPickNudge(-1, 1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		colourPickNudge(1, 1)
	case sdl.CONTROLLER_BUTTON_A:
		colourPickActivate()
	case sdl.CONTROLLER_BUTTON_X:
		colourPickType()
	case sdl.CONTROLLER_BUTTON_Y:
		colourPickPreset(1)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		colourPickClose()
	}
}

// colourPickMouse handles a tap: a track sets its slider where it was
// touched, the buttons press, and a tap outside the card closes it.
func colourPickMouse(mx, my int32) {
	if !pointInRect(mx, my, colourPick.card) {
		colourPickClose()
		return
	}
	for i := colourPickRowRed; i <= colourPickRowBlue; i++ {
		if !pointInRect(mx, my, colourPick.rows[i]) {
			continue
		}
		colourPick.focus = i
		t := colourPick.tracks[i]
		if t.W > 0 && mx >= t.X-12 && mx <= t.X+t.W+12 {
			v := int((mx - t.X) * 255 / t.W)
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			if v != colourPick.rgb[i] {
				colourPick.rgb[i] = v
				colourPick.changed = true
			}
		}
		PlayNavSound()
		return
	}
	for b, r := range colourPick.buttons {
		if r.W > 0 && pointInRect(mx, my, r) {
			colourPick.focus, colourPick.button = colourPickRowButtons, b
			colourPickActivate()
			return
		}
	}
}

// colourPickWheel steps the focused slider by one.
func colourPickWheel(dy int32) {
	switch {
	case dy > 0:
		colourPickNudge(1, 1)
	case dy < 0:
		colourPickNudge(-1, 1)
	}
}

// colourPickHints is the footer while the sheet is open.
func colourPickHints() []FooterHint {
	fine := FooterHint{Button: "L1/R1", Label: "Fine"}
	if hintsUseKeyboard() {
		fine.Button = "PgUp/PgDn"
	}
	return []FooterHint{{Button: "Left/Right", Label: "Adjust"}, fine,
		{Button: "X", Label: "Type"}, {Button: "Y", Label: "Presets"},
		{Button: "A", Label: "Apply"}, {Button: "B", Label: "Cancel"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// colourPickChannelNames are the sliders' captions.
var colourPickChannelNames = [3]string{"Red", "Green", "Blue"}

// colourPickDraw paints the sheet over the Settings page.
func colourPickDraw(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !colourPick.open {
		return
	}
	colourPickTickHold()
	body := FontForRole(config, TypeBody)
	small := FontForRole(config, TypeLabel)
	if body == nil || small == nil {
		return
	}
	drawOverlayScrim(renderer)

	w := int32(640)
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	top := headerBarHeight() + 8
	avail := screenHeight - top - bottomBarHeight("Settings") - 16
	// Six rows of at most 46 px under the header fill about this much.
	h := avail
	if h > 460 {
		h = 460
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top + (avail-h)/2, W: w, H: h}
	colourPick.card = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, colourPick.slot.title, colourPick.slot.note, "palette")
	y := UIPageHeaderDraw(renderer, config, header) + 6

	x := card.X + OverlayPadX
	inner := card.W - 2*OverlayPadX
	bottom := card.Y + card.H - OverlayPadX
	gap := int32(8)
	// The swatch takes two rows, the three sliders and the buttons one each.
	rowH := (bottom - y - 5*gap) / 6
	if rowH > 46 {
		rowH = 46
	}
	if rowH < 22 {
		rowH = 22
	}

	y = colourPickDrawSwatch(renderer, body, small, sdl.Rect{X: x, Y: y, W: inner, H: 2 * rowH}) + gap
	for i := colourPickRowRed; i <= colourPickRowBlue; i++ {
		r := sdl.Rect{X: x, Y: y, W: inner, H: rowH}
		colourPickDrawSlider(renderer, body, small, r, i)
		y += rowH + gap
	}

	half := (inner - gap) / 2
	apply := sdl.Rect{X: x, Y: y, W: half, H: rowH}
	cancel := sdl.Rect{X: x + half + gap, Y: y, W: inner - half - gap, H: rowH}
	onButtons := colourPick.focus == colourPickRowButtons
	drawGlassButton(renderer, config, apply, "Apply", body, WithAlpha(glassAccent(), 60),
		glassState{Focused: onButtons && colourPick.button == 0})
	drawGlassButton(renderer, config, cancel, "Cancel", body, sdl.Color{},
		glassState{Focused: onButtons && colourPick.button == 1})
	colourPick.buttons = [2]sdl.Rect{apply, cancel}
}

// colourPickDrawSwatch paints the colour it replaces and the new one side by
// side, with the hex, the preset it came from and the contrast beside them.
// It returns the band's bottom.
func colourPickDrawSwatch(renderer *sdl.Renderer, body, small *ttf.Font, r sdl.Rect) int32 {
	col := colourPickColor()
	sw := r.W * 46 / 100
	old := sdl.Rect{X: r.X, Y: r.Y, W: sw / 3, H: r.H}
	now := sdl.Rect{X: old.X + old.W + 6, Y: r.Y, W: sw - old.W - 6, H: r.H}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, old.X, old.Y, old.W, old.H, 12, colourPick.before)
	fillRoundedRect(renderer, now.X, now.Y, now.W, now.H, 12, col)
	edge := glassA(60)
	strokeRoundedRect(renderer, old.X, old.Y, old.W, old.H, 12, 1, edge)
	strokeRoundedRect(renderer, now.X, now.Y, now.W, now.H, 12, 1, edge)
	sh, bh := int32(small.Height()), int32(body.Height())
	drawText(renderer, small, "Now", old.X+old.W/2, old.Y+old.H-sh-6, contrastText(colourPick.before), textAlignCenter)
	drawText(renderer, body, "Aa", now.X+now.W/2, now.Y+(now.H-bh)/2, contrastText(col), textAlignCenter)

	tx := now.X + now.W + 18
	tw := r.X + r.W - tx
	ty := r.Y + 2
	drawText(renderer, body, strings.ToUpper(colourPickHex()), tx, ty, ColorTextPrimary(), textAlignLeft)
	ty += bh + 2
	from := "Your own colour"
	if i := colourPickPresetLoaded(); i >= 0 {
		o := colourPick.presets[i]
		from = o.Label
		if o.Note != "" && !strings.EqualFold(o.Note, o.Label) {
			from += " · " + o.Note
		}
	}
	drawText(renderer, small, ellipsize(small, from, tw), tx, ty, ColorTextSecondary(), textAlignLeft)
	ty += sh + 2
	if line := colourPickContrastLine(col); line != "" && ty+sh <= r.Y+r.H {
		drawText(renderer, small, ellipsize(small, line, tw), tx, ty, ColorTextSecondary(), textAlignLeft)
	}
	return r.Y + r.H
}

// colourPickContrastLine is "7.2:1 on the background · reads well".
func colourPickContrastLine(col sdl.Color) string {
	if colourPick.slot.against == nil {
		return ""
	}
	other, word := colourPick.slot.against()
	other.A = 255
	ratio := wcagContrast(col, other)
	verdict := "reads well"
	switch want := colourPick.slot.minContrast; {
	case want > 0 && ratio < 3:
		verdict = "hard to see"
	case want > 3 && ratio < want:
		verdict = "large text only"
	}
	return fmt.Sprintf("%.1f:1 against %s · %s", ratio, word, verdict)
}

// colourPickDrawSlider paints one channel's row: caption, a track shaded
// from 0 to 255 of that channel with the other two as they are, the knob and
// the value.
func colourPickDrawSlider(renderer *sdl.Renderer, body, small *ttf.Font, r sdl.Rect, ch int) {
	focused := colourPick.focus == ch
	drawGlassSurface(renderer, r, 10, glassState{Focused: focused, NoLift: true})
	capInk := WithAlpha(ColorTextSecondary(), 210)
	if focused {
		capInk = ColorTextPrimary()
	}
	sh, bh := int32(small.Height()), int32(body.Height())
	drawText(renderer, small, colourPickChannelNames[ch], r.X+16, r.Y+(r.H-sh)/2, capInk, textAlignLeft)
	value := strconv.Itoa(colourPick.rgb[ch])
	drawText(renderer, body, value, r.X+r.W-16, r.Y+(r.H-bh)/2, ColorTextPrimary(), textAlignRight)

	trackH := r.H / 4
	if trackH < 6 {
		trackH = 6
	}
	track := sdl.Rect{X: r.X + 96, Y: r.Y + (r.H-trackH)/2, W: r.W - 96 - 72, H: trackH}
	colourPick.rows[ch], colourPick.tracks[ch] = r, track
	if track.W <= 0 {
		return
	}
	const segs = 32
	shade := colourPick.rgb
	for s := int32(0); s < segs; s++ {
		x0 := track.X + track.W*s/segs
		x1 := track.X + track.W*(s+1)/segs
		shade[ch] = int(255 * (2*s + 1) / (2 * segs))
		setDrawColor(renderer, sdl.Color{R: uint8(shade[0]), G: uint8(shade[1]), B: uint8(shade[2]), A: 255})
		_ = renderer.FillRect(&sdl.Rect{X: x0, Y: track.Y, W: x1 - x0, H: track.H})
	}
	strokeRoundedRect(renderer, track.X-1, track.Y-1, track.W+2, track.H+2, 2, 1, glassA(50))

	knobX := track.X + track.W*int32(colourPick.rgb[ch])/255
	knobR := trackH + 3
	ring := ColorTextPrimary()
	if focused {
		ring = glassAccent()
	}
	fillCircle(renderer, knobX, track.Y+track.H/2, knobR, ring)
	fillCircle(renderer, knobX, track.Y+track.H/2, knobR-3, colourPickColor())
}
