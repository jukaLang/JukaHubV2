package main

// castui.go — the on-device half of the remote cast: a panel with the share
// link, its QR code, how many browsers are watching, and the on/off switch.
//
// The cast itself lives in companion_cast.go and runs on the companion server
// that is already bound in the background. This file only shows it and flips
// the switch, following the same modal-panel pattern as the other overlays.

import (
	"fmt"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

type castPanelState struct {
	open bool
	url  string
	qr   [][]bool
	err  string
}

var castPanel castPanelState

// castShareURL builds the address a viewer opens. It needs the companion
// server's port and PIN, so it is empty until the server is up.
func castShareURL() string {
	if companion.srv == nil || !companion.srv.Running() {
		return ""
	}
	ip := localIPv4()
	if ip == "" {
		return ""
	}
	return castWatchURL(ip, companion.srv.Port(), companion.pin)
}

func openCastPanel(config *Config) {
	qs.open = false
	toolsPanel.open = false
	a11y.open = false
	musicSettings.open = false
	// The server normally comes up with the home screen; this makes the panel
	// usable even when the user opened it before Wi-Fi appeared.
	companionEnsureStarted()
	castPanel.open = true
	castPanelRefresh()
	PlayActivateSound()
}

func closeCastPanel() {
	if !castPanel.open {
		return
	}
	castPanel.open = false
	PlayBackSound()
}

// castPanelRefresh rebuilds the link and QR when the address or PIN changed.
func castPanelRefresh() {
	castPanel.err = ""
	url := castShareURL()
	if url == "" {
		castPanel.url = ""
		castPanel.qr = nil
		if companion.err != "" {
			castPanel.err = companion.err
		} else {
			castPanel.err = "Start the Phone Companion first (needs Wi-Fi)"
		}
		return
	}
	if url == castPanel.url && len(castPanel.qr) > 0 {
		return
	}
	castPanel.url = url
	code, err := qrcode.New(url, qrcode.Low)
	if err != nil {
		castPanel.qr = nil
		castPanel.err = "cannot draw the QR code: " + err.Error()
		return
	}
	castPanel.qr = code.Bitmap()
}

// castPanelToggle flips the stream and reports the new state.
func castPanelToggle() bool {
	on := !castIsEnabled()
	castSetEnabled(on)
	if on {
		showToast("Remote cast on — share the link", ToastSuccess())
	} else {
		showToast("Remote cast off", ToastInfo())
	}
	PlayNavSound()
	return on
}

// castStatusText is the one-line state used by the panel and the toast.
func castStatusText() string {
	if !castIsEnabled() {
		return "Off"
	}
	viewers := castViewerCount()
	switch viewers {
	case 0:
		return "On — waiting for viewers"
	case 1:
		return "Streaming to 1 viewer"
	}
	return fmt.Sprintf("Streaming to %d viewers", viewers)
}

func handleCastKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !castPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		closeCastPanel()
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		castPanelToggle()
	case sdl.K_y:
		// A new PIN invalidates the shared link, which is how a viewer is
		// kicked out: the old link stops authorising immediately.
		companionRestart()
		if castIsEnabled() {
			castSetEnabled(false)
		}
		castPanel.url = ""
		castPanelRefresh()
		showToast("New PIN — old links no longer work", ToastInfo())
		PlayNavSound()
	default:
		return false
	}
	return true
}

func ctrlCastPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		castPanelToggle()
	case sdl.CONTROLLER_BUTTON_Y:
		companionRestart()
		if castIsEnabled() {
			castSetEnabled(false)
		}
		castPanel.url = ""
		castPanelRefresh()
		showToast("New PIN — old links no longer work", ToastInfo())
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeCastPanel()
	}
	return true
}

func handleCastMouse(mx, my int32, config *Config) bool {
	if !castPanel.open {
		return false
	}
	// The whole card is a toggle except the title row, which keeps the panel
	// from closing on an accidental tap outside.
	if castToggleRect.W > 0 && pointInRect(mx, my, castToggleRect) {
		castPanelToggle()
		return true
	}
	closeCastPanel()
	return true
}

// castToggleRect is the on/off switch row, measured during render.
var castToggleRect sdl.Rect

// renderCastPanel draws the share card: QR on the left, link and state on the
// right, switch at the bottom.
func renderCastPanel(renderer *sdl.Renderer, config *Config) {
	if !castPanel.open || renderer == nil {
		return
	}
	castPanelRefresh()

	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 0, 0, 0, 190)

	cardW := int32(820)
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
	accent := getAccentColor(config)
	dim := colorNew(139, 147, 167, 255)
	ink := colorNew(230, 232, 239, 255)
	red := colorNew(255, 107, 107, 255)
	green := colorNew(62, 207, 142, 255)

	line := companionFontLine(smallFont)
	smallH := companionFontLine(bodyFont)
	titleY := cardY + pad
	drawText(renderer, titleFont, "Remote Cast", cardX+pad, titleY, accent, textAlignLeft)
	footerY := cardY + cardH - pad - line
	contentY := titleY + companionFontLine(titleFont) + 14

	// QR block.
	qrSize := footerY - contentY - 2*line - 24
	if qrSize > 300 {
		qrSize = 300
	}
	if qrSize < 96 {
		qrSize = 96
	}
	qrX := cardX + pad
	qrY := contentY
	drawRoundedRect(renderer, &sdl.Rect{X: qrX, Y: qrY, W: qrSize, H: qrSize}, 8, 255, 255, 255, 255)
	if len(castPanel.qr) > 0 {
		drawCompanionQR(renderer, sdl.Rect{X: qrX, Y: qrY, W: qrSize, H: qrSize}, castPanel.qr)
	}
	caption := "Scan to watch on a phone or laptop"
	drawText(renderer, smallFont, caption, qrX, qrY+qrSize+6, dim, textAlignLeft)
	drawText(renderer, smallFont, "Viewers must be on the same Wi-Fi", qrX, qrY+qrSize+6+line, dim, textAlignLeft)

	// Right column: state, link, PIN.
	rightX := qrX + qrSize + pad + 4
	rightW := cardX + cardW - pad - rightX
	if rightW < 120 {
		rightW = 120
	}
	y := contentY

	status := castStatusText()
	statusColor := dim
	if castIsEnabled() {
		statusColor = green
	}
	drawText(renderer, bodyFont, status, rightX, y, statusColor, textAlignLeft)
	y += smallH + 6

	if castPanel.err != "" {
		y = drawCompanionParagraph(renderer, smallFont, castPanel.err, rightX, y, rightW, red, line, footerY)
	} else {
		drawText(renderer, smallFont, "WATCH LINK", rightX, y, dim, textAlignLeft)
		y += line + 2
		y = drawCompanionParagraph(renderer, bodyFont, castPanel.url, rightX, y, rightW, ink, smallH, footerY)
		y += 8
		if companion.pin != "" {
			drawText(renderer, smallFont, "PIN  "+companion.pin, rightX, y, dim, textAlignLeft)
			y += line + 4
		}
		note := "Anyone with this link can watch the screen while the cast is on. " +
			"Turning it off disconnects them; Y makes a new PIN."
		drawCompanionParagraph(renderer, smallFont, note, rightX, y+2, rightW, dim, line, footerY)
	}

	// On/off switch.
	castToggleRect = sdl.Rect{X: rightX, Y: footerY - 54, W: rightW, H: 44}
	if castToggleRect.Y < y {
		castToggleRect.Y = y
		castToggleRect.H = 36
	}
	if castToggleRect.Y+castToggleRect.H < footerY {
		on := castIsEnabled()
		fill := colorNew(38, 44, 58, 255)
		fg := ink
		label := "A   Start casting"
		if on {
			fill = accent
			fg = sdl.Color{R: 10, G: 14, B: 22, A: 255}
			label = "A   Stop casting"
		}
		UIButton(renderer, config, castToggleRect, label, UIButtonStyle{
			Variant:    UIButtonCustom,
			Fill:       fill,
			Border:     WithAlpha(accent, 160),
			LabelColor: fg,
			Font:       bodyFont,
			Radius:     RadiusSM,
			NoShadow:   true,
		}, UIButtonState{})
	}

	hints := "A / Enter  toggle cast      Y  new PIN      B / Esc  close"
	hw, _, _ := smallFont.SizeUTF8(hints)
	drawText(renderer, smallFont, hints, cardX+(cardW-int32(hw))/2, footerY, dim, textAlignLeft)
}
