package main

// qrzoom.go — the full-screen QR card.
//
// Every QR code in the app exists to be scanned by another device: the Phone
// Companion pairing code, the Remote Cast watch link and the diagnostics
// export link. Inside their panels the code is a few hundred pixels square, in
// a card over a dimmed screen — the wrong size and the wrong contrast for a
// phone camera held over a handheld on a desk. Selecting the code (A while it
// is focused, or a tap) opens this overlay instead: the code fills the screen
// at the largest size that fits and the backlight goes to full while it is up,
// then returns to exactly the level the device had when the card closes.

import (
	"net/url"

	"github.com/veandco/go-sdl2/sdl"
)

type qrZoomState struct {
	open    bool
	title   string
	modules [][]bool
	// detail is an optional caption under the code (the pairing PIN and
	// address for the Phone Companion); empty keeps the generic hint.
	detail string
	// prevPct is the backlight the device had before the card went up.
	// restore stays false when the device has no readable backlight, so a
	// platform without one (the desktop builds) never writes a guess back.
	prevPct int
	restore bool
}

var qrZoom qrZoomState

// The backlight seam. The real readers and writers are the backlight sysfs
// helpers; tests swap these so a card can be opened without touching a device.
var (
	qrZoomReadBrightness  = screenBrightness
	qrZoomWriteBrightness = setScreenBrightness
)

// qrZoomBrightness is how bright the screen goes while a code is up: a phone
// camera reads a bright code far more reliably than a dim one.
const qrZoomBrightness = 100

func qrZoomActive() bool { return qrZoom.open }

// qrZoomOpenCard shows modules full screen under title. It reports whether the
// card opened: a surface with no code yet (Wi-Fi down, no companion server)
// says so and leaves the screen alone rather than showing an empty square.
func qrZoomOpenCard(modules [][]bool, title string) bool {
	return qrZoomOpenCardDetail(modules, title, "")
}

// qrZoomOpenCardDetail is qrZoomOpenCard with a caption under the code: what a
// person may need beside the camera (the Home pairing code shows its PIN and
// address, for a phone that cannot scan).
func qrZoomOpenCardDetail(modules [][]bool, title, detail string) bool {
	if len(modules) == 0 {
		ShowToast("No QR code to enlarge yet", ToastKindInfo)
		return false
	}
	qrZoom.open = true
	qrZoom.title = title
	qrZoom.modules = modules
	qrZoom.detail = detail
	qrZoom.restore = false
	if pct, ok := qrZoomReadBrightness(); ok {
		qrZoom.prevPct, qrZoom.restore = pct, true
		if pct < qrZoomBrightness {
			_ = qrZoomWriteBrightness(qrZoomBrightness)
		}
	}
	PlayActivateSound()
	return true
}

// qrZoomCloseCard puts the backlight back and forgets the code. It is a no-op
// when no card is up, so every dismissal path can call it.
func qrZoomCloseCard() {
	if !qrZoom.open {
		return
	}
	if qrZoom.restore {
		_ = qrZoomWriteBrightness(qrZoom.prevPct)
	}
	qrZoom = qrZoomState{}
	PlayBackSound()
}

// handleQRZoomKey owns every key while the card is up: it is the topmost
// overlay, so nothing behind it may answer. Both A and B dismiss it — the
// card is read-only, and either button is how a person gets back to the panel.
func handleQRZoomKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !qrZoom.open {
		return false
	}
	// The key that opened the card (Enter on the focused Home code) may still
	// be held: its repeats are swallowed, or they closed the card, the next
	// one opened it again, and the backlight flipped with every repeat.
	if e.Repeat != 0 {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE, sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		qrZoomCloseCard()
	}
	return true
}

// ctrlQRZoom is the pad's half of the same rule.
func ctrlQRZoom(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !qrZoom.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK,
		sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		qrZoomCloseCard()
	}
	return true
}

// qrZoomLayout centres the code at the largest square the screen allows,
// leaving one text band above and one below it. The white card is returned
// with the module grid inside it: the gap between them is the quiet zone, four
// modules wide as the spec requires, so a scanner can find the code's edges.
func qrZoomLayout(w, h int32, modules int) (card sdl.Rect, grid sdl.Rect) {
	if modules < 1 {
		modules = 1
	}
	side := w - 2*qrZoomMargin
	if room := h - 2*qrZoomTextBand; room < side {
		side = room
	}
	if side < 64 {
		side = 64
	}
	card = sdl.Rect{X: (w - side) / 2, Y: (h - side) / 2, W: side, H: side}
	// Four modules of quiet zone, solved together with the module size:
	// quiet = 4 * (side - 2*quiet) / modules, so quiet = 4*side / (modules+8).
	quiet := 4 * side / int32(modules+8)
	if quiet < 4 {
		quiet = 4
	}
	if 2*quiet >= side {
		quiet = side / 4
	}
	grid = sdl.Rect{X: card.X + quiet, Y: card.Y + quiet, W: side - 2*quiet, H: side - 2*quiet}
	return card, grid
}

// The card keeps a thin margin from the screen edges and leaves one text band
// above and below for the title and the dismissal hint — just enough for a
// 12-14px line, because every pixel here is code the camera has to read.
const (
	qrZoomMargin   = int32(16)
	qrZoomTextBand = int32(34)
)

// qrZoomTitle is the caption above the code: the surface's own name, so the
// full-screen card still says what the person is looking at.
func qrZoomTitle() string {
	if qrZoom.title != "" {
		return qrZoom.title
	}
	return "QR code"
}

// renderQRZoom paints the code over everything. The backdrop is opaque, not a
// scrim: the panel behind is what made the code hard to scan, so it is hidden
// rather than dimmed.
func renderQRZoom(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !qrZoom.open {
		return
	}
	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 6, 7, 10, 255)
	if len(qrZoom.modules) == 0 {
		return
	}

	titleFont := getBodyFont(config, 14)
	title := getDisplayFont(config, 18)
	hintFont := getBodyFont(config, 12)
	line := companionFontLine(titleFont)

	card, grid := qrZoomLayout(screenWidth, screenHeight, len(qrZoom.modules))
	// The white card is the scanner's "paper": a code drawn straight on the
	// dark backdrop reads as an inverted code and will not scan.
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, 12, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	drawCompanionQR(renderer, grid, qrZoom.modules)

	ink := sdl.Color{R: 232, G: 236, B: 244, A: 255}
	dim := ColorTextTertiary()
	top := card.Y - 8 - line
	if top < 4 {
		top = 4
	}
	if title != nil {
		w, _, _ := title.SizeUTF8(qrZoomTitle())
		drawText(renderer, title, qrZoomTitle(), (screenWidth-int32(w))/2, top, ink, textAlignLeft)
	}
	bottom := card.Y + card.H + 10
	if bottom+line > screenHeight-2 {
		bottom = screenHeight - 2 - line
	}
	hint := qrZoomHint()
	if hintFont != nil {
		w, _, _ := hintFont.SizeUTF8(hint)
		drawText(renderer, hintFont, hint, (screenWidth-int32(w))/2, bottom, dim, textAlignLeft)
	}
}

// qrZoomHint is the one dismissal line every surface shows: either button
// closes, and the second half says where to point the phone — or, when the
// surface gave one, its caption (qrZoomOpenCardDetail).
func qrZoomHint() string {
	if qrZoom.detail != "" {
		return hintLine(FooterHint{"A / B", "Close"}, FooterHint{"", qrZoom.detail})
	}
	return hintLine(FooterHint{"A / B", "Close"}, FooterHint{"", "Hold the phone steady over the code"})
}

// homeShowPairingQR shows the Home header's pairing code full screen (A, a
// click or a tap on it). The header tile is a scaled-down copy of the code
// on the Phone Companion card, so this is the same symbol at the size and
// backlight a phone camera reads from arm's length, with the PIN and address
// under it for a phone that cannot scan.
func homeShowPairingQR() {
	qrZoomOpenCardDetail(companion.qr, "Phone Companion", companionPairingCaption(companion.url))
}

// companionPairingCaption is "PIN 123456 · 192.168.1.5:8765" for a pairing
// link, read out of the link itself so the caption always matches the code
// (and its screenshot stand-in, companionURL). A link without a PIN gives the
// address alone; no link gives "". Pure.
func companionPairingCaption(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	if pin := u.Query().Get("p"); pin != "" {
		return "PIN " + pin + " · " + u.Host
	}
	return u.Host
}
