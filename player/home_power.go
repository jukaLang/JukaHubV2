package main

// home_power.go — the power button at the left end of Home's footer, and the
// confirmation card it opens.
//
// The Exit pill left Home in the November UX pass (a quit control one Down
// press from the bottom row, with no confirmation, dressed like the footer
// hints beside it). It is back at the owner's request (2026-11-09) in the
// design sheet's form — a glass pill with the power glyph at the band's left
// end — and the missing confirmation is what changed: pressing it opens a
// small "Exit JukaHub?" card above the pill instead of quitting on the spot.
// Exit is focused on the card, so a deliberate quit is still two presses of
// the same button; Cancel, B, Escape or a tap anywhere else closes it.
//
// Quitting runs the main loop's clean shutdown (requestAppExit), which draws
// the glass teardown — on the TrimUI that hands the screen back to the stock
// launcher.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The card's two buttons, in the order the D-pad walks them.
const (
	homePowerCancel = 0
	homePowerExit   = 1
)

// homePowerEnterMS is the card's entrance: a short rise and fade, skipped
// under reduced motion.
const homePowerEnterMS = 140

// homePowerArmMS is how long Exit ignores presses after the card opens, so a
// double press — or a held key's auto-repeat — on the power button cannot quit
// without the card ever being read.
const homePowerArmMS = 300

// homePowerConfirm is the open confirmation card and the rects its last frame
// was drawn with, for the tap handler. Render/event thread only.
var homePowerConfirm struct {
	open     bool
	focus    int
	openedAt uint64
	card     sdl.Rect
	cancel   sdl.Rect
	exit     sdl.Rect
	// mouseX/mouseY are where the pointer was last frame: hovering a button
	// focuses it, but only when the pointer actually moved, so a pointer left
	// resting over Cancel cannot fight the D-pad every frame.
	mouseX, mouseY int32
}

// homePowerOpen shows the card with Exit focused.
func homePowerOpen() {
	homeCalendarOpen = false
	dismissWeatherPopover()
	homePowerConfirm.open = true
	homePowerConfirm.focus = homePowerExit
	homePowerConfirm.openedAt = sdl.GetTicks64()
	homePowerConfirm.card = sdl.Rect{}
	homePowerConfirm.cancel = sdl.Rect{}
	homePowerConfirm.exit = sdl.Rect{}
	homePowerConfirm.mouseX, homePowerConfirm.mouseY = mouseX, mouseY
}

// homePowerClose dismisses the card without doing anything.
func homePowerClose() {
	homePowerConfirm.open = false
	homePowerConfirm.card = sdl.Rect{}
	homePowerConfirm.cancel = sdl.Rect{}
	homePowerConfirm.exit = sdl.Rect{}
}

// homePowerActivate runs the focused button. Exit is not armed until the card
// has been up for homePowerArmMS.
func homePowerActivate() {
	if homePowerConfirm.focus == homePowerExit {
		if sdl.GetTicks64()-homePowerConfirm.openedAt < homePowerArmMS {
			return
		}
		homePowerQuit()
		return
	}
	PlayBackSound()
	homePowerClose()
}

// homePowerQuit leaves JukaHub the way the Exit pill always did: embedded
// playback stops first, then the main loop's clean shutdown draws the glass
// teardown.
func homePowerQuit() {
	homePowerClose()
	PlayActivateSound()
	if snapshotVideoState().phase != "idle" {
		StopEmbeddedPlayback()
	}
	requestAppExit()
}

// homePowerMove steps the card's focus; the two buttons do not wrap.
func homePowerMove(delta int) {
	next := homePowerConfirm.focus + delta
	if next < homePowerCancel || next > homePowerExit {
		return
	}
	homePowerConfirm.focus = next
	PlayNavSound()
}

// homePowerKey is the keyboard while the card is open. Every key is the
// card's: it is modal over Home. A held key's auto-repeat is ignored, so
// holding Enter on the power button cannot run through the question. A and B
// confirm and cancel the way the other two-button cards on Home do
// (hoffcard.go).
func homePowerKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || e.Repeat != 0 {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_UP:
		homePowerMove(-1)
	case sdl.K_RIGHT, sdl.K_DOWN:
		homePowerMove(1)
	case sdl.K_TAB:
		homePowerConfirm.focus = homePowerExit - homePowerConfirm.focus
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		homePowerActivate()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		PlayBackSound()
		homePowerClose()
	}
	return true
}

// homePowerPad is the controller while the card is open. Buttons the card
// does not use fall through, so Start still opens the menu (which closes the
// card) and the Select + Start chord still quits.
func homePowerPad(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_UP:
		homePowerMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homePowerMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		homePowerActivate()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		PlayBackSound()
		homePowerClose()
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		// Home's own X and Y (close the mini player, open search) must not
		// act behind the card.
	default:
		return false
	}
	return true
}

// homePowerClick is a tap while the card is open: a button acts, the card
// itself swallows the tap, anywhere else closes it.
func homePowerClick(mx, my int32) {
	c := &homePowerConfirm
	switch {
	case c.exit.W > 0 && pointInRect(mx, my, growTouchTarget(c.exit)):
		c.focus = homePowerExit
		homePowerQuit()
	case c.cancel.W > 0 && pointInRect(mx, my, growTouchTarget(c.cancel)):
		c.focus = homePowerCancel
		PlayBackSound()
		homePowerClose()
	case c.card.W > 0 && pointInRect(mx, my, c.card):
	default:
		PlayBackSound()
		homePowerClose()
	}
}

// homePowerIcon is the pill's glyph. Shipped configs name the Exit tile's
// icon "exit" (a door), which is what made the pill read as one more footer
// hint; it is a power button, so it draws the power glyph unless a config
// picked something else on purpose.
func homePowerIcon(icon string) string {
	switch strings.ToLower(strings.TrimSpace(icon)) {
	case "", "exit", "quit", "logout", "log-out":
		return "power"
	}
	return icon
}

// homePowerPillSize is the pill's natural size: the glyph, a gap and the label
// between Home's pill padding, never wider than the slot the layout (or a
// config tile) gives it.
func (h *HomeLayout) homePowerPillSize(fonts HomeFonts, item HomeItem) (int32, int32) {
	slot := item.Rect
	if fonts.Small == nil || slot.W <= 0 || slot.H <= 0 {
		return 0, 0
	}
	glyph := min32(scaleY(18, h.Height), slot.H-scaleY(12, h.Height))
	lw, _ := measureText(fonts.Small, strings.TrimSpace(item.Title))
	w := scaleX(14, h.Width) + glyph + scaleX(8, h.Width) + lw + scaleX(16, h.Width)
	if lw == 0 {
		w = slot.H // a glyph-only round button
	}
	return min32(w, slot.W), slot.H
}

// sizePowerPill fits the power button's rect to the pill drawn in it, so a tap,
// the hover focus and the ring all use the drawn shape, and enables the button
// only while it can be drawn. Render calls it every frame, after ApplyConfig
// has put the config's slot back.
func (h *HomeLayout) sizePowerPill(fonts HomeFonts) {
	item, ok := h.Items[HomeFocusExit]
	if !ok {
		return
	}
	w, ph := h.homePowerPillSize(fonts, item)
	if w <= 0 || ph <= 0 {
		h.Focus.SetEnabled(HomeFocusExit, false)
		return
	}
	item.Rect.W, item.Rect.H = w, ph
	h.Items[HomeFocusExit] = item
	h.Focus.SetEnabled(HomeFocusExit, true)
}

// drawPowerPill paints the power button: Home's glass pill with the power
// glyph and its label, the search pill's focus treatment (bloom, lift, ring)
// and a danger tint on the glyph while it is focused, so the one control that
// leaves the app says so before it is pressed.
func (h *HomeLayout) drawPowerPill(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth, lift float64) {
	w, ph := h.homePowerPillSize(fonts, item)
	if renderer == nil || w <= 0 || ph <= 0 {
		return
	}
	base := sdl.Rect{X: item.Rect.X, Y: item.Rect.Y, W: w, H: ph}
	draw := scaleHomeCard(base, lift)
	draw.Y += homePressOffset(pressDepth, h.Height)
	radius := base.H / 2
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	fill, edge := homeGlass(16), homeGlass(36)
	switch {
	case pressed:
		fill = homeGlass(32)
	case focused:
		fill = homeGlass(26)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// The shape comes from the settled pill and follows the lifted one, the
	// way the search pill and the cards animate (fillRoundedRectAt).
	fillRoundedRectAt(renderer, base, draw, radius, fill)
	if focused {
		strokeRoundedRectAt(renderer, base, draw, radius, 3, ColorAccent)
	} else {
		strokeRoundedRectAt(renderer, base, draw, radius, 1, edge)
	}

	glyph := min32(scaleY(18, h.Height), draw.H-scaleY(12, h.Height))
	if glyph < 10 {
		glyph = 10
	}
	padX := scaleX(14, h.Width)
	label := strings.TrimSpace(item.Title)
	if label == "" {
		padX = (draw.W - glyph) / 2
	}
	icon := sdl.Rect{X: draw.X + padX, Y: draw.Y + (draw.H-glyph)/2, W: glyph, H: glyph}
	ink := ColorTextPrimary()
	if focused || pressed {
		ink = DangerInk()
	}
	drawHomeGlyphTinted(renderer, homePowerIcon(item.Icon), icon, ink)
	if label == "" || fonts.Small == nil {
		return
	}
	textX := icon.X + icon.W + scaleX(8, h.Width)
	maxW := draw.X + draw.W - scaleX(12, h.Width) - textX
	if maxW < scaleY(12, h.Height) {
		return
	}
	labelInk := ColorTextMuted()
	if focused {
		labelInk = ColorTextPrimary()
	}
	drawText(renderer, fonts.Small, ellipsize(fonts.Small, label, maxW), textX,
		draw.Y+(draw.H-int32(fonts.Small.Height()))/2, labelInk, textAlignLeft)
}

// homePowerCardLayout places the card above the pill, left-aligned with it
// and kept inside the canvas, and its two buttons along its bottom edge.
func homePowerCardLayout(pill sdl.Rect, w, h int32) (card, cancel, exit sdl.Rect) {
	cardW := min32(scaleY(380, h), w-2*scaleX(homeSideMargin, w))
	cardH := scaleY(176, h)
	x := pill.X
	if x+cardW > w-scaleX(homeSideMargin, w) {
		x = w - scaleX(homeSideMargin, w) - cardW
	}
	if x < 0 {
		x = 0
	}
	y := pill.Y - scaleY(14, h) - cardH
	if y < scaleY(8, h) {
		y = scaleY(8, h)
	}
	card = sdl.Rect{X: x, Y: y, W: cardW, H: cardH}
	pad := scaleY(20, h)
	btnH := scaleY(44, h)
	gap := scaleY(12, h)
	btnW := (cardW - 2*pad - gap) / 2
	btnY := y + cardH - pad - btnH
	cancel = sdl.Rect{X: x + pad, Y: btnY, W: btnW, H: btnH}
	exit = sdl.Rect{X: x + pad + btnW + gap, Y: btnY, W: btnW, H: btnH}
	return card, cancel, exit
}

// renderHomePowerConfirm draws the card over Home while it is open: a dim
// over the screen, the glass card with the power badge, the question and what
// leaving keeps, and the Cancel / Exit pair. renderHomeModern calls it last,
// so nothing on Home can paint over it.
func renderHomePowerConfirm(renderer *sdl.Renderer, config *Config) {
	c := &homePowerConfirm
	if !c.open || renderer == nil || homeLayout == nil {
		return
	}
	// Another overlay took the screen (the Start menu, the notification list):
	// the question is no longer the thing in front of the user.
	if tb.startOpen || nc.open {
		homePowerClose()
		return
	}
	pill := homeLayout.Items[HomeFocusExit].Rect
	if pill.W <= 0 {
		pill = sdl.Rect{X: scaleX(48, screenWidth), Y: scaleY(674, screenHeight), W: scaleX(116, screenWidth), H: scaleY(36, screenHeight)}
	}
	card, cancel, exit := homePowerCardLayout(pill, screenWidth, screenHeight)

	progress := 1.0
	if uiMotionEnabled(config) {
		progress = uiEaseOutCubic(float64(sdl.GetTicks64()-c.openedAt) / homePowerEnterMS)
	}
	rise := int32((1 - progress) * float64(scaleY(10, screenHeight)))
	card.Y += rise
	cancel.Y += rise
	exit.Y += rise
	c.card, c.cancel, c.exit = card, cancel, exit

	// Hover focuses, but only for a pointer that moved since the last frame.
	if mouseX != c.mouseX || mouseY != c.mouseY {
		c.mouseX, c.mouseY = mouseX, mouseY
		switch {
		case pointInRect(mouseX, mouseY, exit):
			c.focus = homePowerExit
		case pointInRect(mouseX, mouseY, cancel):
			c.focus = homePowerCancel
		}
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	scrim := glassScrimColor(progress)
	scrim.A = uint8(float64(scrim.A) * 0.7)
	setDrawColor(renderer, scrim)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// A soft shadow under the card, then the opaque glass sheet.
	fillRoundedRect(renderer, card.X+3, card.Y+8, card.W, card.H, OverlayCardRadius, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	drawGlassSheet(renderer, card, glassSheetFill())

	fonts := loadHomeFonts()
	pad := scaleY(20, screenHeight)
	badge := sdl.Rect{X: card.X + pad, Y: card.Y + pad, W: scaleY(44, screenHeight), H: scaleY(44, screenHeight)}
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.W/2, WithAlpha(ColorDanger, 46))
	strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.W/2, 1, WithAlpha(ColorDanger, 120))
	g := badge.W * 11 / 20
	drawHomeGlyphTinted(renderer, "power", sdl.Rect{X: badge.X + (badge.W-g)/2, Y: badge.Y + (badge.H-g)/2, W: g, H: g}, DangerInk())

	textX := badge.X + badge.W + scaleX(14, screenWidth)
	textW := card.X + card.W - pad - textX
	title := fonts.Tile
	if title == nil {
		title = fonts.Card
	}
	note := fonts.Meta
	if note == nil {
		note = fonts.Small
	}
	if title != nil {
		titleH := int32(title.Height())
		noteH := int32(0)
		if note != nil {
			noteH = int32(note.Height())
		}
		ty := badge.Y + (badge.H-titleH-noteH)/2
		drawText(renderer, title, ellipsize(title, "Exit JukaHub?", textW), textX, ty, ColorTextPrimary(), textAlignLeft)
		if note != nil {
			drawText(renderer, note, ellipsize(note, "Your settings and progress are saved.", textW), textX, ty+titleH, ColorTextMuted(), textAlignLeft)
		}
	}

	homePowerDrawButton(renderer, fonts, cancel, "Cancel", "", c.focus == homePowerCancel, false)
	homePowerDrawButton(renderer, fonts, exit, "Exit", "power", c.focus == homePowerExit, true)
}

// homePowerDrawButton is one of the card's buttons: glass, or the danger tint
// for Exit, with Home's ring and bloom on the focused one.
func homePowerDrawButton(renderer *sdl.Renderer, fonts HomeFonts, rect sdl.Rect, label, icon string, focused, danger bool) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	radius := rect.H / 2
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	fill, edge := homeGlass(16), homeGlass(36)
	ink := ColorTextPrimary()
	if danger {
		fill, edge = WithAlpha(ColorDanger, 40), WithAlpha(ColorDanger, 110)
		ink = DangerInk()
		if focused {
			fill = WithAlpha(ColorDanger, 230)
			ink = contrastText(ColorDanger)
		}
	} else if focused {
		fill = homeGlass(30)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, fill)
	if focused {
		out := homeFocusRingOutset()
		strokeRoundedRect(renderer, rect.X-out, rect.Y-out, rect.W+2*out, rect.H+2*out, radius+out, homeFocusRingWidth, ColorAccent)
	} else {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, edge)
	}
	font := fonts.Helper
	if font == nil {
		font = fonts.Small
	}
	if font == nil {
		return
	}
	lw, lh := measureText(font, label)
	glyph := int32(0)
	gap := int32(0)
	if icon != "" {
		glyph = min32(scaleY(18, screenHeight), rect.H-scaleY(16, screenHeight))
		gap = scaleX(8, screenWidth)
	}
	x := rect.X + (rect.W-(glyph+gap+lw))/2
	if glyph > 0 {
		drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: x, Y: rect.Y + (rect.H-glyph)/2, W: glyph, H: glyph}, ink)
	}
	drawText(renderer, font, label, x+glyph+gap, rect.Y+(rect.H-lh)/2, ink, textAlignLeft)
}
