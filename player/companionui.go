package main

// companionui.go — the on-device half of the Phone Companion: the compact QR
// in the home header, the full pairing card, and the bridge that turns queued
// phone actions into real input events.
//
// The listener lives for the whole session so the header QR is always
// scannable. The full card is modal. Both codes contain the LAN address plus a
// fresh six-digit PIN, so nothing has to be typed on the handheld to pair.

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type companionOverlay struct {
	open bool
	pin  string
	url  string
	err  string
	qr   [][]bool
	srv  *companionServer
}

var companion companionOverlay

// companionQRRect is the pairing code as drawn in the card, so a tap can
// enlarge it. Render/event thread only; empty when no card is up.
var companionQRRect sdl.Rect

// companionHeaderRect is the QR tile in the home header. It is set by the
// renderer and read by the home click handler, so the visible code and its hit
// target can never drift apart.
var companionHeaderRect sdl.Rect

// companionNextRefresh throttles the LAN-address poll. The companion server
// stays up for the whole session; this only notices a DHCP change and rebuilds
// the QR for the new address.
var companionNextRefresh time.Time

// companionNextRetry throttles automatic startup while the handheld is waiting
// for Wi-Fi, so returning to Home retries without enumerating interfaces every
// frame.
var companionNextRetry time.Time

func init() {
	Cleanup = append(Cleanup, shutdownCompanion)
}

func openCompanionOverlay() {
	if companion.open {
		closeCompanionOverlay()
		return
	}
	companion.open = true
	resetCompanionQueue()
	companionStart()
	PlayActivateSound()
}

// closeCompanionOverlay only dismisses the pairing card. The listener and its
// PIN stay alive so the QR code on the home screen keeps working; shutting the
// server down is reserved for app exit and "new PIN".
func closeCompanionOverlay() {
	companion.open = false
	companionQRRect = sdl.Rect{}
	resetCompanionQueue()
}

func toggleCompanionOverlay() {
	if companion.open {
		closeCompanionOverlay()
		PlayBackSound()
		return
	}
	openCompanionOverlay()
}

// companionRestart issues a new PIN and rebinds the listener. The QR code on
// screen is the only copy of the old PIN, so rotating it locks out any phone
// that was left connected.
func companionRestart() {
	stopCompanionServer()
	companion.pin = ""
	companionStart()
	PlayNavSound()
}

// stopCompanionServer releases the listener and clears the pairing material.
// The overlay may stay open so the error state is visible.
func stopCompanionServer() {
	if companion.srv != nil {
		companion.srv.Stop()
		companion.srv = nil
	}
	companion.pin = ""
	companion.url = ""
	companion.err = ""
	companion.qr = nil
	companionHeaderRect = sdl.Rect{}
}

// shutdownCompanion is the app-exit path: close the card and release the port.
func shutdownCompanion() {
	closeCompanionOverlay()
	stopCompanionServer()
}

// companionStart binds the server once. Calling it again is a cheap no-op that
// only refreshes the QR if the handheld's LAN address changed.
func companionStart() {
	if companion.srv != nil && companion.srv.Running() {
		companionRefreshURL()
		return
	}
	if !safeModeAllows(SafeFeatureNetwork) {
		companion.err = "Network is disabled in safe mode"
		return
	}
	companion.err = ""
	companion.url = ""
	companion.qr = nil
	if localIPv4() == "" {
		companion.err = "No Wi-Fi address - connect the handheld to a network"
		return
	}
	pin := companion.pin
	if len(pin) != 6 {
		var err error
		pin, err = companionNewPIN()
		if err != nil {
			companion.err = "cannot generate a PIN: " + err.Error()
			return
		}
	}
	companion.pin = pin
	srv := newCompanionServer(pin, ftpRootDir(), enqueueCompanion)
	if err := srv.Start(); err != nil {
		companion.err = err.Error()
		return
	}
	companion.srv = srv
	companionRefreshURL()
}

// companionEnsureStarted brings the companion up in the background so the home
// QR can be scanned without opening the pairing card first. It is safe to call
// from the render loop: while the server is healthy it only checks the LAN
// address every few seconds, and when Wi-Fi is absent it retries slowly.
func companionEnsureStarted() {
	if !safeModeAllows(SafeFeatureNetwork) {
		return
	}
	now := time.Now()
	if companion.srv != nil && companion.srv.Running() {
		if now.Before(companionNextRefresh) {
			return
		}
		companionNextRefresh = now.Add(3 * time.Second)
		companionRefreshURL()
		return
	}
	if now.Before(companionNextRetry) {
		return
	}
	companionNextRetry = now.Add(5 * time.Second)
	companionStart()
}

// companionRefreshURL keeps the QR in sync with the current LAN address. The
// listener stays bound across DHCP changes; only the displayed URL changes.
func companionRefreshURL() {
	if companion.srv == nil {
		return
	}
	if localIPv4() == "" {
		companion.err = "No Wi-Fi address - connect the handheld to a network"
		companion.url = ""
		companion.qr = nil
		return
	}
	url := companion.srv.URL()
	if url == companion.url && len(companion.qr) > 0 {
		companion.err = ""
		return
	}
	companion.url = url
	companion.err = ""
	code, err := qrcode.New(url, qrcode.Low)
	if err != nil {
		companion.qr = nil
		companion.err = "cannot draw the QR code: " + err.Error()
		return
	}
	companion.qr = code.Bitmap()
}

// drawCompanionQR paints the module bitmap inside rect, centring the code and
// leaving the leftover space as a quiet zone. The caller owns the white tile.
func drawCompanionQR(renderer *sdl.Renderer, rect sdl.Rect, modules [][]bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || len(modules) == 0 {
		return
	}
	module := rect.W / int32(len(modules))
	if module < 1 {
		module = 1
	}
	coded := module * int32(len(modules))
	ox := rect.X + (rect.W-coded)/2
	oy := rect.Y + (rect.H-coded)/2
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 255)
	for y, row := range modules {
		for x, on := range row {
			if !on {
				continue
			}
			renderer.FillRect(&sdl.Rect{
				X: ox + int32(x)*module,
				Y: oy + int32(y)*module,
				W: module,
				H: module,
			})
		}
	}
}

// companionHeaderQRQuiet is the quiet zone the header code keeps inside its
// white tile, in modules. The standard asks for four; phone cameras read two
// reliably, and every module the margin does not take goes to the code itself.
const companionHeaderQRQuiet = 2

// companionHeaderQR caches the header code between frames: the symbol without
// the quiet zone the bitmap carries, and the rects one frame fills, so the tile
// costs one FillRects call instead of a FillRect per dark module. Render
// thread only.
var companionHeaderQR struct {
	src    [][]bool // the companion.qr the fields below were built from
	core   [][]bool
	origin sdl.Point
	module int32
	rects  []sdl.Rect
}

// qrCoreModules strips the all-light rows and columns around a QR bitmap — the
// quiet zone go-qrcode adds — and returns the symbol itself. A bitmap with no
// dark module, or with ragged rows, comes back unchanged.
func qrCoreModules(bitmap [][]bool) [][]bool {
	top, bottom, left, right := -1, -1, -1, -1
	for y, row := range bitmap {
		if len(row) != len(bitmap[0]) {
			return bitmap
		}
		for x, on := range row {
			if !on {
				continue
			}
			if top < 0 {
				top = y
			}
			bottom = y
			if left < 0 || x < left {
				left = x
			}
			if x > right {
				right = x
			}
		}
	}
	if top < 0 {
		return bitmap
	}
	core := make([][]bool, 0, bottom-top+1)
	for y := top; y <= bottom; y++ {
		core = append(core, bitmap[y][left:right+1])
	}
	return core
}

// qrModuleRects lays a symbol out at (x, y) with square modules of the given
// size. Neighbouring dark modules on a row merge into one rect, so a pairing
// code is a couple of hundred rects for a single FillRects call.
func qrModuleRects(core [][]bool, x, y, module int32) []sdl.Rect {
	rects := make([]sdl.Rect, 0, len(core)*len(core)/3)
	for r, row := range core {
		for c := 0; c < len(row); {
			if !row[c] {
				c++
				continue
			}
			start := c
			for c < len(row) && row[c] {
				c++
			}
			rects = append(rects, sdl.Rect{
				X: x + int32(start)*module,
				Y: y + int32(r)*module,
				W: int32(c-start) * module,
				H: module,
			})
		}
	}
	return rects
}

// companionHeaderQRCore is the current code's symbol, recomputed only when the
// code itself changes (a new PIN or LAN address).
func companionHeaderQRCore() [][]bool {
	c := &companionHeaderQR
	qr := companion.qr
	if len(qr) == 0 {
		return nil
	}
	if len(c.src) != len(qr) || &c.src[0] != &qr[0] {
		c.src = qr
		c.core = qrCoreModules(qr)
		c.rects = nil
	}
	return c.core
}

// headerQRModule is the module size, in pixels, of a symbol of n modules drawn
// in a square tile of the given side with the header's quiet zone: the largest
// whole number that fits, so every module is the same crisp square. Zero when
// not even one-pixel modules fit.
func headerQRModule(side, n int32) int32 {
	if n <= 0 || side <= 0 {
		return 0
	}
	return side / (n + 2*companionHeaderQRQuiet)
}

// renderCompanionHeaderQR draws the always-available pairing code in the home
// header: a white tile, right-aligned in rect, that the code fills edge to
// edge apart from a two-module quiet zone. The old tile drew the bitmap with
// its own four-module margin and a pixel per module, so the 72px tile held a
// 37px code; the module size now comes from the tile (headerQRModule), which
// is what makes the code bigger, not just its frame. It returns an empty rect
// when there is no code to show or no room for a readable one.
func renderCompanionHeaderQR(renderer *sdl.Renderer, fonts HomeFonts, rect sdl.Rect) sdl.Rect {
	companionHeaderRect = sdl.Rect{}
	if renderer == nil || len(companion.qr) == 0 || rect.W <= 0 || rect.H <= 0 {
		return sdl.Rect{}
	}
	core := companionHeaderQRCore()
	n := int32(len(core))
	side := min32(rect.W, rect.H)
	module := headerQRModule(side, n)
	if module < 1 {
		return sdl.Rect{}
	}
	tile := sdl.Rect{X: rect.X + rect.W - side, Y: rect.Y, W: side, H: side}
	radius := max32(6, side/10)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// A plain white card: on the navy backdrop it already reads as the one
	// tappable code on the screen, and every pixel of it is quiet zone or code.
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, radius, sdl.Color{R: 255, G: 255, B: 255, A: 255})

	code := module * n
	origin := sdl.Point{X: tile.X + (tile.W-code)/2, Y: tile.Y + (tile.H-code)/2}
	c := &companionHeaderQR
	if c.rects == nil || c.origin != origin || c.module != module {
		c.rects = qrModuleRects(core, origin.X, origin.Y, module)
		c.origin, c.module = origin, module
	}
	if len(c.rects) > 0 {
		renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRects(c.rects)
	}
	companionHeaderRect = tile
	return tile
}

// companionFontLine is the height of one line of a cached font, falling back
// to a readable default when the environment has no fonts at all.
func companionFontLine(font *ttf.Font) int32 {
	if font == nil {
		return 14
	}
	if h := font.Height(); h > 0 {
		return int32(h)
	}
	return 14
}

// companionCardLayout keeps the QR captions in their own column and reserves
// a full-width footer, even when the selected font is larger than expected.
type companionCardLayout struct {
	card, qr, left, right sdl.Rect
	titleY, footerY       int32
	contentBottom         int32
}

func companionCardLayoutFor(width, height, titleH, lineH int32) companionCardLayout {
	cardW := min32(780, max32(1, width-24))
	cardH := min32(470, max32(1, height-24))
	card := sdl.Rect{X: (width - cardW) / 2, Y: (height - cardH) / 2, W: cardW, H: cardH}
	pad := int32(26)
	titleY := card.Y + pad
	contentY := titleY + titleH + 16
	footerY := card.Y + card.H - pad - lineH
	bottom := footerY - 8
	innerW := max32(1, cardW-2*pad)
	// Reserve three measured lines under the QR for scanning, enlargement
	// and the shared-Wi-Fi instruction.
	qrSize := min32(320, innerW*36/100)
	qrSize = min32(qrSize, max32(0, bottom-contentY-3*lineH-6))
	leftW := qrSize
	rightX := card.X + pad + leftW + 16
	rightW := max32(0, card.X+card.W-pad-rightX)
	return companionCardLayout{
		card:   card,
		qr:     sdl.Rect{X: card.X + pad, Y: contentY, W: qrSize, H: qrSize},
		left:   sdl.Rect{X: card.X + pad, Y: contentY, W: leftW, H: max32(0, bottom-contentY)},
		right:  sdl.Rect{X: rightX, Y: contentY, W: rightW, H: max32(0, bottom-contentY)},
		titleY: titleY, footerY: footerY, contentBottom: bottom,
	}
}

// companionWrapAddress breaks an address at rune boundaries rather than
// ellipsizing it: users who cannot scan the QR need the entire address.
func companionWrapAddress(font *ttf.Font, text string, width int32) []string {
	if font == nil || width <= 0 {
		return nil
	}
	runes := []rune(text)
	var lines []string
	for len(runes) > 0 {
		end := 0
		for end < len(runes) {
			w, _, err := font.SizeUTF8(string(runes[:end+1]))
			if err != nil || int32(w) > width {
				break
			}
			end++
		}
		if end == 0 {
			return nil
		}
		lines = append(lines, string(runes[:end]))
		runes = runes[end:]
	}
	return lines
}

// renderCompanionOverlay draws the pairing card: QR on the left, everything a
// person needs to know on the right. Every offset comes from the measured font
// height, because the design tokens choose those sizes and a handheld font is
// not the same size as a desktop one.
func renderCompanionOverlay(renderer *sdl.Renderer, config *Config) {
	if !companion.open {
		return
	}
	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 0, 0, 0, 190)

	const pad = int32(26)
	titleFont := getDisplayFont(config, 22)
	bodyFont := getBodyFont(config, 14)
	smallFont := getSmallFont(config)
	pinFont := getDisplayFont(config, 30)
	accent := getAccentColor(config)
	dim := colorNew(160, 170, 190, 255)
	ink := colorNew(230, 232, 239, 255)
	red := colorNew(255, 107, 107, 255)
	green := colorNew(62, 207, 142, 255)

	line := companionFontLine(smallFont)
	layout := companionCardLayoutFor(screenWidth, screenHeight, companionFontLine(titleFont), line)
	card := layout.card
	drawRoundedRect(renderer, &card, 18, 22, 22, 28, 248)
	drawText(renderer, titleFont, ellipsize(titleFont, "Phone Companion", max32(1, card.W-2*pad)), card.X+pad, layout.titleY, ink, textAlignLeft)

	qr := layout.qr
	companionQRRect = qr
	if qr.W > 0 {
		drawRoundedRect(renderer, &qr, 8, 255, 255, 255, 255)
		// A QR smaller than its module grid cannot be scanned; leave the tile
		// blank rather than drawing modules outside the column.
		if qr.W >= int32(len(companion.qr)) {
			drawCompanionQR(renderer, qr, companion.qr)
		}
		if len(companion.qr) > 0 {
			strokeRoundedRect(renderer, qr.X-3, qr.Y-3, qr.W+6, qr.H+6, 10, 3, accent)
		}
	}
	// Short, individually fitted lines cannot spill into the details column.
	captionY := qr.Y + qr.H + 6
	captions := []string{"Scan with phone", "A Enlarge QR", "Use same Wi-Fi"}
	if companion.err != "" {
		captions = []string{"Companion offline", "", "Use same Wi-Fi"}
	} else if len(companion.qr) == 0 {
		captions[1] = ""
	}
	if layout.left.W > 0 && layout.left.H > 0 {
		renderWithClip(renderer, layout.left.X, layout.left.Y, layout.left.W, layout.left.H, func(r *sdl.Renderer) {
			for i, caption := range captions {
				drawText(r, smallFont, ellipsize(smallFont, caption, layout.left.W), layout.left.X, captionY+int32(i)*line, dim, textAlignLeft)
			}
		})
	}

	// The column is clipped as a final guard against very small displays or
	// unusually large accessibility fonts. Essential pairing details come first.
	if layout.right.W > 0 && layout.right.H > 0 {
		renderWithClip(renderer, layout.right.X, layout.right.Y, layout.right.W, layout.right.H, func(r *sdl.Renderer) {
			y := layout.right.Y
			drawText(r, smallFont, "PAIRING PIN", layout.right.X, y, dim, textAlignLeft)
			y += line + 2
			pinText := companion.pin
			if pinText == "" {
				pinText = "------"
			}
			drawText(r, pinFont, pinText, layout.right.X, y, ink, textAlignLeft)
			y += companionFontLine(pinFont) + 2
			status, statusColor := "Listening", green
			if companion.err != "" {
				status, statusColor = "Offline", red
			}
			drawText(r, bodyFont, status, layout.right.X, y, statusColor, textAlignLeft)
			y += companionFontLine(bodyFont) + 4
			if companion.srv != nil {
				for _, addressLine := range companionWrapAddress(smallFont, companion.url, layout.right.W) {
					if y+line > layout.contentBottom {
						break
					}
					drawText(r, smallFont, addressLine, layout.right.X, y, ink, textAlignLeft)
					y += line
				}
			}
			y += 8
			if companion.err != "" {
				y = drawCompanionParagraph(r, smallFont, companion.err, layout.right.X, y, layout.right.W, red, line, layout.contentBottom)
				y += 4
			}
			help := "Your phone gets a keyboard, remote, link sender and file drop. Files land in PhoneDrop."
			drawCompanionParagraph(r, smallFont, help, layout.right.X, y, layout.right.W, dim, line, layout.contentBottom)
		})
	}
	footer := hintLine(FooterHint{"Y", "New PIN"}, FooterHint{"B / Esc", "Close"})
	drawText(renderer, smallFont, ellipsize(smallFont, footer, max32(1, card.W-2*pad)), card.X+pad, layout.footerY, dim, textAlignLeft)
}

// drawCompanionParagraph wraps text at word boundaries and returns the y of
// the line after the last one. Lines that would cross maxY are dropped rather
// than drawn over the footer.
func drawCompanionParagraph(renderer *sdl.Renderer, font *ttf.Font, text string, x, y, maxW int32, col sdl.Color, lineH, maxY int32) int32 {
	if font == nil {
		return y
	}
	words := strings.Fields(text)
	line := ""
	for _, word := range words {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if width, _, _ := font.SizeUTF8(candidate); int32(width) > maxW && line != "" {
			if y+lineH > maxY {
				return y
			}
			drawText(renderer, font, ellipsize(font, line, maxW), x, y, col, textAlignLeft)
			y += lineH
			line = word
			continue
		}
		line = candidate
	}
	if line != "" && y+lineH <= maxY {
		drawText(renderer, font, ellipsize(font, line, maxW), x, y, col, textAlignLeft)
		y += lineH
	}
	return y
}

// handleCompanionKey consumes every key while the overlay is up: it is modal,
// and a stray keystroke must not reach the scene behind the QR code.
func handleCompanionKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !companion.open || e == nil {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_RETURN, sdl.K_KP_ENTER:
		closeCompanionOverlay()
		PlayBackSound()
	case sdl.K_a:
		// A enlarges the pairing code; Escape/Enter remain the way out, so
		// the card's existing dismissal keys do not change meaning.
		qrZoomOpenCard(companion.qr, "Phone Companion")
	case sdl.K_y:
		companionRestart()
	}
	return true
}

// handleCompanionMouse is the card's tap path: the code enlarges, anything
// else on the card dismisses it. It reports whether the click was consumed.
func handleCompanionMouse(mx, my int32) bool {
	if !companion.open {
		return false
	}
	if companionQRRect.W > 0 && pointInRect(mx, my, companionQRRect) {
		qrZoomOpenCard(companion.qr, "Phone Companion")
		return true
	}
	closeCompanionOverlay()
	return true
}

// companionControllerButtons maps the phone's remote buttons to SDL.
var companionControllerButtons = map[string]uint8{
	"up":    sdl.CONTROLLER_BUTTON_DPAD_UP,
	"down":  sdl.CONTROLLER_BUTTON_DPAD_DOWN,
	"left":  sdl.CONTROLLER_BUTTON_DPAD_LEFT,
	"right": sdl.CONTROLLER_BUTTON_DPAD_RIGHT,
	"a":     sdl.CONTROLLER_BUTTON_A,
	"b":     sdl.CONTROLLER_BUTTON_B,
	"x":     sdl.CONTROLLER_BUTTON_X,
	"y":     sdl.CONTROLLER_BUTTON_Y,
	"start": sdl.CONTROLLER_BUTTON_START,
	"back":  sdl.CONTROLLER_BUTTON_BACK,
	"lb":    sdl.CONTROLLER_BUTTON_LEFTSHOULDER,
	"rb":    sdl.CONTROLLER_BUTTON_RIGHTSHOULDER,
}

var companionKeyboardKeys = map[string]sdl.Keycode{
	"enter":     sdl.K_RETURN,
	"escape":    sdl.K_ESCAPE,
	"space":     sdl.K_SPACE,
	"tab":       sdl.K_TAB,
	"backspace": sdl.K_BACKSPACE,
}

// companionButtonFor resolves the SDL button a phone label should press.
//
// The phone labels describe what the user means ("A" = confirm, "X" = the X
// action), while the event queue is later normalized by applyButtonSwap for
// firmware that reports the physical face buttons mirrored (A/B and X/Y on the
// TrimUI handhelds). Pre-flipping through the same padFaceButton cancels that
// second swap, so a phone A stays A and a phone X stays X on every device
// instead of becoming Back or Y.
func companionButtonFor(name string, config *Config) (uint8, bool) {
	btn, ok := companionControllerButtons[name]
	if !ok {
		return 0, false
	}
	return padFaceButton(config, btn), true
}

// pushCompanionKey replays a phone button as a real SDL event. Going through
// the event queue means the phone gets exactly the same handling as the
// handheld's own pad, overlays and scene handlers included.
func pushCompanionKey(name string, config *Config) {
	if btn, ok := companionButtonFor(name, config); ok {
		// Pushed events carry their press state: the phone's A used to arrive
		// with State 0, which reads as a release.
		_ = pushControllerButton(sdl.CONTROLLERBUTTONDOWN, 0, btn, sdl.PRESSED)
		_ = pushControllerButton(sdl.CONTROLLERBUTTONUP, 0, btn, sdl.RELEASED)
		return
	}
	if key, ok := companionKeyboardKeys[name]; ok {
		_ = pushSyntheticKey(sdl.KEYDOWN, key, sdl.PRESSED)
		_ = pushSyntheticKey(sdl.KEYUP, key, sdl.RELEASED)
	}
}

// companionTextEvent packs one chunk into the SDL text event real typing
// produces. Chunks are kept under 32 bytes by companionTextChunks.
func companionTextEvent(text string) *sdl.TextInputEvent {
	e := &sdl.TextInputEvent{Type: sdl.TEXTINPUT}
	copy(e.Text[:], text)
	return e
}

// drainCompanionQueue runs once per frame on the main loop. Everything the
// phone sent since the last frame is applied here, in order.
func drainCompanionQueue(config *Config) {
	events := drainCompanionEvents()
	if len(events) == 0 {
		return
	}
	for _, ev := range events {
		switch ev.Kind {
		case companionEventText:
			for _, chunk := range companionTextChunks(ev.Text, 31) {
				handleTextInputEvent(companionTextEvent(chunk), config)
			}
		case companionEventKey:
			pushCompanionKey(ev.Text, config)
		case companionEventURL:
			playVideoURL(config, ev.Text)
			showToast("Phone sent a link", ColorToastSuccess)
		case companionEventUpload:
			showToast("Phone sent "+filepath.Base(ev.Text), ColorToastSuccess)
		case companionEventPSN:
			// A PlayStation sign-in (the phone's /psn page, or a "redirect"
			// address pasted on this device) produced the PSN Account-ID.
			psPanelAccountArrived(ev.Text)
		}
	}
}
