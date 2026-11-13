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
//
// On the Linux handheld the card also offers Sleep, Restart and Power off
// (2026-11-11, after Mosaik CFW 0.9.11's Restart button): a row of three
// above Cancel / Exit. Restart and Power off are the same clean quit followed
// by busybox's reboot or poweroff; Sleep suspends the device and JukaHub
// carries on where it was (devpower.go). Exit stays the focused button, and
// every button but Cancel waits out homePowerArmMS. A desktop keeps the two.
// The Power quick tile (home_quicktiles.go) opens the same card on Power off:
// a tile pinned to turn the handheld off is pressed for that, and the second
// press still has to come after the arming delay.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The card's buttons. Cancel and Exit are on every platform; the other three
// only on the handheld (homePowerConfirm.full).
const (
	homePowerCancel = iota
	homePowerExit
	homePowerSleep
	homePowerRestart
	homePowerOff
	homePowerButtons
)

// homePowerOrderShort and homePowerOrderFull are the orders Left and Right
// walk the buttons in: reading order, the handheld's top row first.
var (
	homePowerOrderShort = []int{homePowerCancel, homePowerExit}
	homePowerOrderFull  = []int{homePowerSleep, homePowerRestart, homePowerOff, homePowerCancel, homePowerExit}
)

// homePowerEnterMS is the card's entrance: a short rise and fade, skipped
// under reduced motion.
const homePowerEnterMS = 140

// homePowerArmMS is how long Exit (and Sleep, Restart and Power off) ignore
// presses after the card opens, so a double press — or a held key's
// auto-repeat — on the power button cannot act without the card ever being
// read.
const homePowerArmMS = 300

// homePowerConfirm is the open confirmation card and the rects its last frame
// was drawn with, for the tap handler. Render/event thread only.
var homePowerConfirm struct {
	open     bool
	focus    int
	openedAt uint64
	enter    uiEnter
	// full is the handheld's card with Sleep, Restart and Power off, decided
	// when the card opens.
	full bool
	card sdl.Rect
	// btn holds each button's rect by its constant; a zero rect is a button
	// this card does not have (or a frame not drawn yet).
	btn [homePowerButtons]sdl.Rect
	// mouseX/mouseY are where the pointer was last frame: hovering a button
	// focuses it, but only when the pointer actually moved, so a pointer left
	// resting over Cancel cannot fight the D-pad every frame.
	mouseX, mouseY int32
}

// homePowerOpen shows the card with Exit focused.
func homePowerOpen() {
	homeCalendarOpen = false
	dismissWeatherPopover()
	dismissVolumePopover()
	dismissBrightnessPopover()
	homePowerConfirm.open = true
	homePowerConfirm.full = devPowerSupported()
	homePowerConfirm.focus = homePowerExit
	homePowerConfirm.openedAt = sdl.GetTicks64()
	homePowerConfirm.enter.begin(uiClockMS(), homePowerEnterMS)
	homePowerConfirm.card = sdl.Rect{}
	homePowerConfirm.btn = [homePowerButtons]sdl.Rect{}
	homePowerConfirm.mouseX, homePowerConfirm.mouseY = mouseX, mouseY
}

// homePowerOpenFromKey opens the card for the hardware power key (a long
// press, or a short one in the Exit-card mode: powerkey.go) from wherever
// JukaHub is: the idle screen gives the panel back, the Start menu and the
// notification list close (the card closes itself under them), and from
// another screen JukaHub goes Home first. It reports whether the card is up;
// powerKeyTick calls it again until it is, while the scene change runs.
// Main thread.
func homePowerOpenFromKey(config *Config) bool {
	screensaverNotifyInput()
	if homePowerConfirm.open {
		return true
	}
	if tb.startOpen {
		startMenuDismiss()
	}
	if nc.open {
		notifDismiss()
	}
	if !homeLayoutActive || homeLayout == nil {
		// switchScene ignores the call while a transition runs, so asking
		// again each frame until Home is up is harmless.
		if home := homeSceneIndex(config); home >= 0 && home != currentSceneIndex {
			changeSceneTo(config, home)
		}
		return false
	}
	homePowerOpen()
	return true
}

// homePowerClose dismisses the card without doing anything.
func homePowerClose() {
	homePowerConfirm.open = false
	homePowerConfirm.enter.cancel()
	homePowerConfirm.card = sdl.Rect{}
	homePowerConfirm.btn = [homePowerButtons]sdl.Rect{}
}

// homePowerOrder is the buttons this card has, in walking order.
func homePowerOrder() []int {
	if homePowerConfirm.full {
		return homePowerOrderFull
	}
	return homePowerOrderShort
}

// homePowerActivate runs the focused button.
func homePowerActivate() {
	homePowerPress(homePowerConfirm.focus)
}

// homePowerPress runs one button. Everything but Cancel is not armed until
// the card has been up for homePowerArmMS.
func homePowerPress(id int) {
	if id != homePowerCancel && sdl.GetTicks64()-homePowerConfirm.openedAt < homePowerArmMS {
		return
	}
	switch id {
	case homePowerExit:
		homePowerQuit()
	case homePowerSleep:
		homePowerClose()
		PlayActivateSound()
		devSleepNow("power card")
	case homePowerRestart, homePowerOff:
		homePowerClose()
		PlayActivateSound()
		if snapshotVideoState().phase != "idle" {
			StopEmbeddedPlayback()
		}
		if id == homePowerRestart {
			devPowerRequest(devPowerReboot)
		} else {
			devPowerRequest(devPowerPoweroff)
		}
	default:
		PlayBackSound()
		homePowerClose()
	}
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

// homePowerMove steps the card's focus along homePowerOrder; it does not
// wrap.
func homePowerMove(delta int) {
	order := homePowerOrder()
	at := 0
	for i, id := range order {
		if id == homePowerConfirm.focus {
			at = i
		}
	}
	next := at + delta
	if next < 0 || next >= len(order) {
		return
	}
	homePowerConfirm.focus = order[next]
	PlayNavSound()
}

// homePowerMoveRow is Up and Down. The two-button card has one row, so they
// step like Left and Right as they always did; the handheld's card moves
// between its rows to the button below or above (Restart lands on Cancel,
// the harmless one of the two under it).
func homePowerMoveRow(delta int) {
	if !homePowerConfirm.full {
		homePowerMove(delta)
		return
	}
	next := -1
	switch f := homePowerConfirm.focus; {
	case delta < 0 && f == homePowerCancel:
		next = homePowerSleep
	case delta < 0 && f == homePowerExit:
		next = homePowerOff
	case delta > 0 && (f == homePowerSleep || f == homePowerRestart):
		next = homePowerCancel
	case delta > 0 && f == homePowerOff:
		next = homePowerExit
	}
	if next < 0 {
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
	case sdl.K_LEFT:
		homePowerMove(-1)
	case sdl.K_RIGHT:
		homePowerMove(1)
	case sdl.K_UP:
		homePowerMoveRow(-1)
	case sdl.K_DOWN:
		homePowerMoveRow(1)
	case sdl.K_TAB:
		// Tab walks every button and comes round again.
		order := homePowerOrder()
		next := order[0]
		for i, id := range order {
			if id == homePowerConfirm.focus && i+1 < len(order) {
				next = order[i+1]
			}
		}
		homePowerConfirm.focus = next
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
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		homePowerMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		homePowerMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		homePowerMoveRow(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homePowerMoveRow(1)
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
	// Exact hits first: the grown touch targets of neighbouring buttons
	// overlap in the gaps between them.
	for _, grow := range []bool{false, true} {
		for _, id := range homePowerOrder() {
			r := c.btn[id]
			if grow {
				r = growTouchTarget(r)
			}
			if c.btn[id].W > 0 && pointInRect(mx, my, r) {
				c.focus = id
				homePowerPress(id)
				return
			}
		}
	}
	if c.card.W > 0 && pointInRect(mx, my, c.card) {
		return
	}
	PlayBackSound()
	homePowerClose()
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
		strokeRoundedRectAt(renderer, base, homeFocusGlide.apply(draw), radius, 3, ColorAccent)
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
// and kept inside the canvas, and its buttons along its bottom edge: Cancel
// and Exit, and on the full card Sleep, Restart and Power off in a row above
// them (the card is wider and one row taller; at 1280x720 it still ends
// above the pill and the footer).
func homePowerCardLayout(pill sdl.Rect, w, h int32, full bool) (card sdl.Rect, btn [homePowerButtons]sdl.Rect) {
	cardW := min32(scaleY(380, h), w-2*scaleX(homeSideMargin, w))
	cardH := scaleY(176, h)
	if full {
		cardW = min32(scaleY(500, h), w-2*scaleX(homeSideMargin, w))
		cardH = scaleY(232, h)
	}
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
	btn[homePowerCancel] = sdl.Rect{X: x + pad, Y: btnY, W: btnW, H: btnH}
	btn[homePowerExit] = sdl.Rect{X: x + pad + btnW + gap, Y: btnY, W: btnW, H: btnH}
	if full {
		topY := btnY - gap - btnH
		topW := (cardW - 2*pad - 2*gap) / 3
		for i, id := range []int{homePowerSleep, homePowerRestart, homePowerOff} {
			btn[id] = sdl.Rect{X: x + pad + int32(i)*(topW+gap), Y: topY, W: topW, H: btnH}
		}
	}
	return card, btn
}

// homePowerButtonLook is a button's label, glyph and whether it is drawn in
// the danger tint.
func homePowerButtonLook(id int) (label, icon string, danger bool) {
	switch id {
	case homePowerExit:
		return "Exit", "power", true
	case homePowerSleep:
		return "Sleep", "moon", false
	case homePowerRestart:
		return "Restart", "refresh", true
	case homePowerOff:
		return "Power off", "power", true
	}
	return "Cancel", "", false
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
	card, btn := homePowerCardLayout(pill, screenWidth, screenHeight, c.full)

	t := 1.0
	if uiMotionEnabled(config) {
		t = c.enter.progress(uiClockMS())
	} else {
		c.enter.cancel()
	}
	progress := uiEaseOutCubic(t)
	rise := int32(math.Round(uiPanelRise.at(t)))
	card.Y += rise
	for i := range btn {
		if btn[i].W > 0 {
			btn[i].Y += rise
		}
	}
	c.card, c.btn = card, btn

	// Hover focuses, but only for a pointer that moved since the last frame.
	if mouseX != c.mouseX || mouseY != c.mouseY {
		c.mouseX, c.mouseY = mouseX, mouseY
		for _, id := range homePowerOrder() {
			if pointInRect(mouseX, mouseY, btn[id]) {
				c.focus = id
			}
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
		heading := "Exit JukaHub?"
		if c.full {
			heading = "Sleep, restart or exit?"
		}
		drawText(renderer, title, ellipsize(title, heading, textW), textX, ty, ColorTextPrimary(), textAlignLeft)
		if note != nil {
			drawText(renderer, note, ellipsize(note, "Your settings and progress are saved.", textW), textX, ty+titleH, ColorTextMuted(), textAlignLeft)
		}
	}

	for _, id := range homePowerOrder() {
		label, icon, danger := homePowerButtonLook(id)
		homePowerDrawButton(renderer, fonts, btn[id], label, icon, c.focus == id, danger)
	}
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
		// The ring follows the glow when the focus glides (homefocusglide.go).
		out := homeFocusRingOutset()
		ring := sdl.Rect{X: rect.X - out, Y: rect.Y - out, W: rect.W + 2*out, H: rect.H + 2*out}
		strokeRoundedRectAt(renderer, ring, homeFocusGlide.apply(ring), radius+out, homeFocusRingWidth, ColorAccent)
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
