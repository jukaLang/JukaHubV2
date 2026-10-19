package main

// companionui.go — the on-device half of the Phone Companion: the QR overlay
// and the bridge that turns queued phone actions into real input events.
//
// The overlay is modal. Its QR code contains the LAN address plus a fresh
// six-digit PIN, so nothing has to be typed on the handheld to pair a phone.

import (
	"fmt"
	"path/filepath"
	"strings"

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

func init() {
	Cleanup = append(Cleanup, closeCompanionOverlay)
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

func closeCompanionOverlay() {
	if companion.srv != nil {
		companion.srv.Stop()
		companion.srv = nil
	}
	companion.open = false
	companion.pin = ""
	companion.url = ""
	companion.err = ""
	companion.qr = nil
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
	if companion.srv != nil {
		companion.srv.Stop()
		companion.srv = nil
	}
	companionStart()
	PlayNavSound()
}

func companionStart() {
	companion.err = ""
	companion.url = ""
	companion.qr = nil
	if localIPv4() == "" {
		companion.err = "No Wi-Fi address - connect the handheld to a network"
		return
	}
	pin, err := companionNewPIN()
	if err != nil {
		companion.err = "cannot generate a PIN: " + err.Error()
		return
	}
	companion.pin = pin
	companion.err = ""
	srv := newCompanionServer(pin, ftpRootDir(), enqueueCompanion)
	if err := srv.Start(); err != nil {
		companion.err = err.Error()
		return
	}
	companion.srv = srv
	companion.url = srv.URL()
	companion.qr = nil
	if code, err := qrcode.New(companion.url, qrcode.Low); err == nil {
		companion.qr = code.Bitmap()
	} else {
		companion.err = "cannot draw the QR code: " + err.Error()
	}
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

// renderCompanionOverlay draws the pairing card: QR on the left, everything a
// person needs to know on the right. Every offset comes from the measured font
// height, because the design tokens choose those sizes and a handheld font is
// not the same size as a desktop one.
func renderCompanionOverlay(renderer *sdl.Renderer, config *Config) {
	if !companion.open {
		return
	}
	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 0, 0, 0, 190)

	cardW := int32(780)
	cardH := int32(470)
	if cardW > screenWidth-24 {
		cardW = screenWidth - 24
	}
	if cardH > screenHeight-24 {
		cardH = screenHeight - 24
	}
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2
	drawRoundedRect(renderer, &sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, 18, 22, 22, 28, 248)

	const pad = int32(26)
	titleFont := getDisplayFont(config, 22)
	bodyFont := getBodyFont(config, 14)
	smallFont := getBodyFont(config, 12)
	pinFont := getDisplayFont(config, 30)
	accent := getAccentColor(config)
	dim := colorNew(139, 147, 167, 255)
	ink := colorNew(230, 232, 239, 255)
	red := colorNew(255, 107, 107, 255)
	green := colorNew(62, 207, 142, 255)

	line := companionFontLine(smallFont)
	titleY := cardY + pad
	drawText(renderer, titleFont, "Phone Companion", cardX+pad, titleY, accent, textAlignLeft)

	footerY := cardY + cardH - pad - companionFontLine(smallFont)
	contentY := titleY + companionFontLine(titleFont) + 16
	limit := footerY - 6

	// QR block: a square that fills the height left between the title and the
	// two caption lines, so a small screen shrinks the code instead of
	// overlapping the text.
	captionLines := int32(2)
	if cardH < 300 {
		captionLines = 1
	}
	qrSize := limit - contentY - captionLines*line - 8
	if qrSize > 320 {
		qrSize = 320
	}
	if qrSize < 64 {
		qrSize = 64
	}
	qrX := cardX + pad
	qrY := contentY
	drawRoundedRect(renderer, &sdl.Rect{X: qrX, Y: qrY, W: qrSize, H: qrSize}, 8, 255, 255, 255, 255)
	if modules := len(companion.qr); modules > 0 {
		module := qrSize / int32(modules)
		if module < 1 {
			module = 1
		}
		coded := module * int32(modules)
		ox := qrX + (qrSize-coded)/2
		oy := qrY + (qrSize-coded)/2
		renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetDrawColor(0, 0, 0, 255)
		for y := 0; y < modules; y++ {
			row := companion.qr[y]
			for x := 0; x < modules; x++ {
				if !row[x] {
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

	caption := "Scan with your phone camera"
	if companion.err != "" {
		caption = "Companion is offline"
	}
	captionY := qrY + qrSize + 6
	drawText(renderer, smallFont, caption, qrX, captionY, dim, textAlignLeft)
	if captionLines > 1 {
		drawText(renderer, smallFont, "Phone and handheld must share Wi-Fi", qrX, captionY+line, dim, textAlignLeft)
	}

	rightX := qrX + qrSize + pad + 6
	rightW := cardX + cardW - pad - rightX
	if rightW < 80 {
		rightW = 80
	}
	y := contentY

	drawText(renderer, smallFont, "PAIRING PIN", rightX, y, dim, textAlignLeft)
	y += line + 2
	pinText := companion.pin
	if pinText == "" {
		pinText = "------"
	}
	drawText(renderer, pinFont, pinText, rightX, y, accent, textAlignLeft)
	y += companionFontLine(pinFont) + 2

	status := "Listening"
	statusColor := green
	if companion.err != "" {
		status = "Offline"
		statusColor = red
	}
	drawText(renderer, bodyFont, status, rightX, y, statusColor, textAlignLeft)
	y += companionFontLine(bodyFont)
	if companion.srv != nil {
		drawText(renderer, smallFont, companion.url, rightX, y, ink, textAlignLeft)
		y += line
	}
	y += 8

	if companion.err != "" {
		y = drawCompanionParagraph(renderer, smallFont, companion.err, rightX, y, rightW, red, line, limit)
		y += 4
	}

	help := "Your phone gets a keyboard, a D-pad remote, a \"send link\" box and a file drop. " +
		"Files land in the PhoneDrop folder, links open in the player."
	drawCompanionParagraph(renderer, smallFont, help, rightX, y+2, rightW, dim, line, limit)

	drawText(renderer, smallFont, "Y  new PIN        B / Esc  close", rightX, footerY, dim, textAlignLeft)
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
			drawText(renderer, font, line, x, y, col, textAlignLeft)
			y += lineH
			line = word
			continue
		}
		line = candidate
	}
	if line != "" && y+lineH <= maxY {
		drawText(renderer, font, line, x, y, col, textAlignLeft)
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
	case sdl.K_y:
		companionRestart()
	}
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

// pushCompanionKey replays a phone button as a real SDL event. Going through
// the event queue means the phone gets exactly the same handling as the
// handheld's own pad, overlays and scene handlers included.
func pushCompanionKey(name string) {
	if btn, ok := companionControllerButtons[name]; ok {
		sdl.PushEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: btn})
		sdl.PushEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: btn})
		return
	}
	if key, ok := companionKeyboardKeys[name]; ok {
		sdl.PushEvent(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: key}})
		sdl.PushEvent(&sdl.KeyboardEvent{Type: sdl.KEYUP, Keysym: sdl.Keysym{Sym: key}})
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
			pushCompanionKey(ev.Text)
		case companionEventURL:
			playVideoURL(config, ev.Text)
			showToast("Phone sent a link", ColorToastSuccess)
		case companionEventUpload:
			showToast("Phone sent "+filepath.Base(ev.Text), ColorToastSuccess)
		}
	}
}

// companionStatusLine is used by tests and the Tools panel: a one-line summary
// of where the phone should connect.
func companionStatusLine() string {
	if !companion.open {
		return "Stopped"
	}
	if companion.err != "" {
		return "Error: " + companion.err
	}
	return fmt.Sprintf("Pairing PIN %s", companion.pin)
}
