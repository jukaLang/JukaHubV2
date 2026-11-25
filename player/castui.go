package main

// castui.go — Screen Share's panel: the watch link and its QR code, the on/off
// switch, the picture quality, whether games are shared, whether viewers hear
// the handheld and may ask to play it, and who is watching.
//
// The share itself is companion_cast.go (the viewer page and its streams),
// castcap.go (where the pictures come from), castaudio*.go (the sound) and
// castinput*.go (a viewer playing). This file only shows it and flips its
// switches, as one modal card on Home's glass — and it is where a viewer's
// request to play is answered: the Controllers row turns into the request
// (A allows, X says no), and X takes a playing viewer's turn back.

import (
	"fmt"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The panel's controls, in focus order.
const (
	castFocusQR = iota
	castFocusShare
	castFocusQuality
	castFocusGames
	castFocusSound
	castFocusControl
	castFocusCount
)

// castFocusRequest is where a viewer's request to play waits: the
// Controllers row, which answers it while it waits.
const castFocusRequest = castFocusControl

type castPanelState struct {
	open bool
	url  string
	qr   [][]bool
	err  string
	// focus is the selected control. The share switch keeps the selection when
	// the panel opens, so A means what it has always meant there.
	focus int
	// refreshAt throttles the LAN-address check: enumerating the interfaces
	// every frame cost more than the whole card.
	refreshAt time.Time
	// shot is the screenshot mode the link was built for: a change rebuilds
	// it at once, stand-in address and token included.
	shot bool
}

var castPanel castPanelState

// castPanelRects are the controls as last drawn, by castFocus*, so a click
// and the drawn card agree. Render/event thread only.
var castPanelRects [castFocusCount]sdl.Rect

// castPanelCard is the card as last drawn: a click outside it closes the panel.
var castPanelCard sdl.Rect

// castPanelInfoRect is the link and live-line block as last drawn; the share
// paints it over in the viewers' picture (castShareRedactions).
var castPanelInfoRect sdl.Rect

// castShareURL builds the address a viewer opens. It needs the companion
// server's port, so it is empty until the server is up. Screenshot mode names
// the stand-in address and token, like the pairing link (companionURL).
func castShareURL() string {
	if companion.srv == nil || !companion.srv.Running() {
		return ""
	}
	ip := localIPv4()
	if shotModeOn() {
		ip = shotLANAddress
	}
	if ip == "" {
		return ""
	}
	return castWatchURL(ip, companion.srv.Port(), castLinkToken())
}

// castShareBlocked is why sharing cannot run right now, "" when it can: the
// link is served by the Phone Companion's server, which safe mode keeps off
// and which needs a network address.
func castShareBlocked() string {
	if !safeModeAllows(SafeFeatureNetwork) {
		return "Screen Share needs the network, which is off in safe mode"
	}
	if companion.srv == nil || !companion.srv.Running() {
		if companion.err != "" {
			return "Screen Share can't start: " + companion.err
		}
		return "Screen Share needs Wi-Fi: connect the handheld to a network"
	}
	return ""
}

func openCastPanel(config *Config) {
	qs.open = false
	toolsPanel.open = false
	a11y.open = false
	smartHomePanel.open = false
	musicSettings.open = false
	// The server normally comes up with the home screen; this makes the panel
	// usable even when it was opened before Wi-Fi appeared.
	companionEnsureStarted()
	castSyncSettings(config)
	castPanel.open = true
	castPanel.focus = castFocusShare
	if _, asking, _ := castInputStatus(); asking != "" {
		// Opened to answer a viewer who asks to play (the toast says so).
		castPanel.focus = castFocusRequest
	}
	castPanel.refreshAt = time.Time{}
	castPanelRefresh()
	PlayActivateSound()
}

func closeCastPanel() {
	if !castPanel.open {
		return
	}
	castPanel.open = false
	castPanelRects = [castFocusCount]sdl.Rect{}
	castPanelCard = sdl.Rect{}
	castPanelInfoRect = sdl.Rect{}
	PlayBackSound()
}

// castPanelRefresh rebuilds the link and its QR when the address, the port or
// the token changed. The address is looked up at most every two seconds.
func castPanelRefresh() {
	now := time.Now()
	shot := shotModeOn()
	if shot == castPanel.shot && !castPanel.refreshAt.IsZero() && now.Before(castPanel.refreshAt) {
		return
	}
	castPanel.shot = shot
	castPanel.refreshAt = now.Add(2 * time.Second)
	castPanel.err = ""
	if why := castShareBlocked(); why != "" {
		castPanel.url = ""
		castPanel.qr = nil
		castPanel.err = why
		return
	}
	url := castShareURL()
	if url == "" {
		castPanel.url = ""
		castPanel.qr = nil
		castPanel.err = "Screen Share needs Wi-Fi: connect the handheld to a network"
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

// castPanelControls lists the controls this device offers, in focus order.
func castPanelControls() []int {
	if castGamesSupported() {
		return []int{castFocusQR, castFocusShare, castFocusQuality, castFocusGames, castFocusSound, castFocusControl}
	}
	return []int{castFocusQR, castFocusShare, castFocusQuality, castFocusSound, castFocusControl}
}

// castPanelMoveFocus selects the previous or next control, wrapping.
func castPanelMoveFocus(delta int) {
	controls := castPanelControls()
	at := 0
	for i, c := range controls {
		if c == castPanel.focus {
			at = i
		}
	}
	next := (at + delta + len(controls)) % len(controls)
	if controls[next] != castPanel.focus {
		castPanel.focus = controls[next]
		PlayNavSound()
	}
}

// castPanelSide handles Left/Right. On Quality they step the preset; the code
// sits left of the rows, so on Share or Games too Left reaches it, and Right
// comes back from it (from Quality, Up to Share and then Left).
func castPanelSide(config *Config, delta int) {
	switch castPanel.focus {
	case castFocusQuality:
		castPanelStepQuality(config, delta, false)
	case castFocusQR:
		if delta > 0 {
			castPanel.focus = castFocusShare
			PlayNavSound()
		}
	default:
		if delta < 0 {
			castPanel.focus = castFocusQR
			PlayNavSound()
		}
	}
}

// castPanelActivate runs the selected control.
func castPanelActivate(config *Config) {
	switch castPanel.focus {
	case castFocusQR:
		if len(castPanel.qr) > 0 {
			qrZoomOpenCard(castPanel.qr, "Screen Share")
		}
	case castFocusShare:
		castPanelToggle()
	case castFocusQuality:
		castPanelStepQuality(config, 1, true)
	case castFocusGames:
		castPanelToggleGames(config)
	case castFocusSound:
		castPanelToggleSound(config)
	case castFocusControl:
		// A request waiting turns the row into its answer.
		if castInputAnswer(true) {
			ShowToast("The viewer plays now — X in this panel takes it back", ToastKindSuccess)
			PlayActivateSound()
			return
		}
		castPanelToggleControl(config)
	}
}

// castPanelX is X: no to a viewer who asks to play, or the handheld takes a
// playing viewer's turn back.
func castPanelX() {
	if castInputAnswer(false) {
		ShowToast("Said no to the viewer", ToastKindInfo)
		PlayBackSound()
		return
	}
	if castInputStopOwner() {
		ShowToast("Took control back from the viewer", ToastKindInfo)
		PlayBackSound()
	}
}

// castPanelToggleSound flips Sound.
func castPanelToggleSound(config *Config) {
	on := !castSoundFor(config)
	setStoreBool(config, castSoundKey, on)
	castSyncSettings(config)
	if on {
		if castIsEnabled() {
			castTapProbeKick()
		}
		ShowToast("Viewers can hear the handheld — the Sound button on their page", ToastKindInfo)
	} else {
		ShowToast("Sound stays on the handheld", ToastKindInfo)
	}
	PlayNavSound()
}

// castPanelToggleControl flips Controllers between Ask and Off; Off also ends
// a viewer's turn (castSyncSettings).
func castPanelToggleControl(config *Config) {
	value := "off"
	if !castControlFor(config) {
		value = "ask"
	}
	setStoreString(config, castControlKey, value)
	castSyncSettings(config)
	if value == "ask" {
		ShowToast("Viewers can ask to play; you allow each one here", ToastKindInfo)
	} else {
		ShowToast("Viewers only watch", ToastKindInfo)
	}
	PlayNavSound()
}

// castPanelToggle flips sharing and reports the new state. It will not switch
// on while there is nothing to serve the link (safe mode, no network): the
// switch would claim a share nobody can open.
func castPanelToggle() bool {
	on := !castIsEnabled()
	if on {
		if why := castShareBlocked(); why != "" {
			ShowToast(why, ToastKindWarning)
			PlayErrorSound()
			return false
		}
	}
	castSetEnabled(on)
	if on {
		trophyEvent(tkCast)
		ShowToast("Screen Share on — open the link on a TV, laptop or phone", ToastKindSuccess)
	} else {
		ShowToast("Screen Share off", ToastKindInfo)
	}
	PlayNavSound()
	return on
}

// castPanelStepQuality moves the Quality setting by delta stops; wrap lets A
// cycle past the last stop, while Left/Right stop at the ends.
func castPanelStepQuality(config *Config, delta int, wrap bool) {
	at := castPresetIndex(castPresetFor(config).Name)
	next := at + delta
	switch {
	case wrap:
		next = (next + len(castPresets)) % len(castPresets)
	case next < 0 || next >= len(castPresets):
		return
	}
	setStoreString(config, castQualityKey, castPresets[next].Name)
	castSyncSettings(config)
	PlayNavSound()
}

// castPanelToggleGames flips "Games too".
func castPanelToggleGames(config *Config) {
	if !castGamesSupported() {
		return
	}
	on := !castGamesFor(config)
	setStoreBool(config, castGamesKey, on)
	castSyncSettings(config)
	if on {
		ShowToast("Games are shared too", ToastKindInfo)
	} else {
		ShowToast("Games are not shared — viewers see a pause card", ToastKindInfo)
	}
	PlayNavSound()
}

// castPanelNewLink retires the link everyone has and shows the new one.
// Open streams end at their next frame; the Phone Companion's PIN is
// untouched, so a paired phone stays paired.
func castPanelNewLink() {
	castRotateToken()
	// Saved at once, even with no network to show it on: a restart must not
	// bring the retired link back.
	castEnsureToken(appConfig)
	// Whoever held the old link stops playing too, and asks again.
	castInputRevoke("The handheld made a new link.")
	castPanel.url = ""
	castPanel.refreshAt = time.Time{}
	castPanelRefresh()
	ShowToast("New link — the old one stopped working", ToastKindInfo)
	PlayNavSound()
}

// castStatusText is the one-line state the panel shows.
func castStatusText() string {
	if !castIsEnabled() {
		return "Off"
	}
	viewers := castWatcherCount()
	switch viewers {
	case 0:
		return "On — waiting for viewers"
	case 1:
		return "Live · 1 watching"
	}
	return fmt.Sprintf("Live · %d watching", viewers)
}

// castLiveLine is the panel's measurements: the frame rate, the bandwidth one
// viewer takes, and where the picture comes from.
func castLiveLine() string {
	if !castIsEnabled() || castWatcherCount() == 0 {
		return ""
	}
	fps, kbps := castRates()
	w, h := castLastFrameSize()
	line := fmt.Sprintf("%.0f fps · %.0f kB/s", fps, kbps)
	if w > 0 && h > 0 {
		line += fmt.Sprintf(" · %dx%d", w, h)
	}
	if listeners, flowing, _ := castAudioInfo(); listeners > 0 {
		line += fmt.Sprintf(" · %d listening", listeners)
		if flowing {
			line += " (sound on)"
		}
	}
	return line
}

func handleCastKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !castPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		closeCastPanel()
	case sdl.K_UP:
		castPanelMoveFocus(-1)
	case sdl.K_DOWN:
		castPanelMoveFocus(1)
	case sdl.K_LEFT:
		castPanelSide(config, -1)
	case sdl.K_RIGHT:
		castPanelSide(config, 1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		castPanelActivate(config)
	case sdl.K_x:
		castPanelX()
	case sdl.K_y:
		castPanelNewLink()
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
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		castPanelMoveFocus(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		castPanelMoveFocus(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		castPanelSide(config, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		castPanelSide(config, 1)
	case sdl.CONTROLLER_BUTTON_A:
		castPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		castPanelX()
	case sdl.CONTROLLER_BUTTON_Y:
		castPanelNewLink()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeCastPanel()
	}
	return true
}

func handleCastMouse(mx, my int32, config *Config) bool {
	if !castPanel.open {
		return false
	}
	for id, r := range castPanelRects {
		if r.W <= 0 || !pointInRect(mx, my, r) {
			continue
		}
		castPanel.focus = id
		if id == castFocusQuality {
			// The left half steps down, the right half up.
			delta := 1
			if mx < r.X+r.W/2 {
				delta = -1
			}
			castPanelStepQuality(config, delta, false)
			return true
		}
		castPanelActivate(config)
		return true
	}
	// A click on the card's empty space is not a dismissal; one outside it is.
	if castPanelCard.W > 0 && pointInRect(mx, my, castPanelCard) {
		return true
	}
	closeCastPanel()
	return true
}

// castPanelLayout is the card's geometry.
type castPanelLayout struct {
	card    sdl.Rect
	titleY  int32
	qr      sdl.Rect
	caption sdl.Rect                 // under the code
	rows    [castFocusCount]sdl.Rect // by control; zero for the code and controls not offered
	info    sdl.Rect                 // the link, the live line and the privacy note
	footerY int32
}

// castPanelLayoutFor lays the card out on a w×h canvas: the Smart Pro's
// 1280x720 and the Brick's 960x720 both fit the same 880-wide card, and a
// smaller window shrinks it. controls are the rows in order (the code among
// them is skipped).
func castPanelLayoutFor(w, h int32, controls []int, titleH, lineH int32) castPanelLayout {
	const pad = int32(26)
	cardW := min32(w-32, 880)
	cardH := min32(h-32, 640)
	card := sdl.Rect{X: (w - cardW) / 2, Y: (h - cardH) / 2, W: cardW, H: cardH}
	l := castPanelLayout{card: card}
	l.titleY = card.Y + pad
	contentY := l.titleY + titleH + 18
	l.footerY = card.Y + card.H - pad - lineH
	contentBottom := l.footerY - 14
	q := min32(contentBottom-contentY-2*lineH-10, cardW*36/100)
	if q > 300 {
		q = 300
	}
	if q < 96 {
		q = 96
	}
	l.qr = sdl.Rect{X: card.X + pad, Y: contentY, W: q, H: q}
	l.caption = sdl.Rect{X: l.qr.X, Y: l.qr.Y + q + 8, W: q, H: max32(0, contentBottom-(l.qr.Y+q+8))}
	rx := l.qr.X + q + 28
	rw := max32(120, card.X+card.W-pad-rx)
	rowH, gap := int32(54), int32(8)
	if cardH < 600 {
		// A short window: the rows give the link and the note their room.
		rowH, gap = 46, 6
	}
	y := contentY
	for _, c := range controls {
		if c <= castFocusQR || c >= castFocusCount {
			continue
		}
		l.rows[c] = sdl.Rect{X: rx, Y: y, W: rw, H: rowH}
		y += rowH + gap
	}
	l.info = sdl.Rect{X: rx, Y: y + 4, W: rw, H: max32(0, contentBottom-(y+4))}
	return l
}

// renderCastPanel draws the share card: the code on the left; the switches,
// the link and the live line on the right; the hints at the bottom.
func renderCastPanel(renderer *sdl.Renderer, config *Config) {
	if !castPanel.open || renderer == nil {
		return
	}
	castPanelRefresh()
	fonts := loadHomeFonts()
	title, rowTitle, body, small := fonts.Card, fonts.Helper, fonts.Small, fonts.Meta
	if title == nil || rowTitle == nil || body == nil || small == nil {
		return
	}
	titleH := companionFontLine(title)
	line := companionFontLine(small)
	games := castGamesSupported()
	l := castPanelLayoutFor(screenWidth, screenHeight, castPanelControls(), titleH, line)
	castPanelCard = l.card

	drawOverlayScrim(renderer)
	fillRoundedRect(renderer, l.card.X+3, l.card.Y+8, l.card.W, l.card.H, OverlayCardRadius, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	drawOverlayCard(renderer, l.card)

	const pad = int32(26)
	ink, muted := ColorTextPrimary(), ColorTextMuted()
	drawText(renderer, title, "Screen Share", l.card.X+pad, l.titleY, ink, textAlignLeft)

	// The state chip, right-aligned in the title row.
	status := castStatusText()
	statusInk := muted
	if castIsEnabled() {
		statusInk = glassAccent()
		if castWatcherCount() > 0 {
			statusInk = ColorDanger
		}
	}
	sw, sh := measureText(small, status)
	chip := sdl.Rect{X: l.card.X + l.card.W - pad - sw - 24, Y: l.titleY + (titleH-sh-10)/2, W: sw + 24, H: sh + 10}
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, glassChipFill())
	strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, 1, WithAlpha(statusInk, 140))
	drawText(renderer, small, status, chip.X+12, chip.Y+5, statusInk, textAlignLeft)

	// The code: a white tile, the selection ring when focused.
	qr := l.qr
	castPanelRects[castFocusQR] = qr
	if castPanel.focus == castFocusQR {
		drawGlassFocusGlow(renderer, qr)
	}
	fillRoundedRect(renderer, qr.X, qr.Y, qr.W, qr.H, 10, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if len(castPanel.qr) > 0 && qr.W >= int32(len(castPanel.qr)) {
		drawCompanionQR(renderer, qr, castPanel.qr)
	}
	if castPanel.focus == castFocusQR {
		drawGlassFocusRing(renderer, glassLifted(qr), 10+glassFocusLift)
	}
	captions := []string{"Scan with a phone, or type the", "link into a TV or laptop browser"}
	if castPanel.err != "" {
		captions = []string{"No link yet", ""}
	}
	if l.caption.H > 0 {
		renderWithClip(renderer, l.caption.X, l.caption.Y, l.caption.W, l.caption.H, func(r *sdl.Renderer) {
			for i, caption := range captions {
				drawText(r, small, ellipsize(small, caption, l.caption.W), l.caption.X, l.caption.Y+int32(i)*line, muted, textAlignLeft)
			}
		})
	}

	// The rows.
	on := castIsEnabled()
	shareSub := "Off — nobody can watch"
	if on {
		shareSub = "On — anyone with the link can watch"
	}
	castDrawToggleRow(renderer, l.rows[castFocusShare], rowTitle, small, "Share this screen", shareSub, on, castPanel.focus == castFocusShare)

	p := castPresetFor(config)
	castDrawQualityRow(renderer, l.rows[castFocusQuality], rowTitle, small, p, castPanel.focus == castFocusQuality)

	if games {
		gamesSub := "Viewers see a pause card while you play"
		if castGamesFor(config) {
			gamesSub = "Your games show too; uses some CPU while you play"
		}
		castDrawToggleRow(renderer, l.rows[castFocusGames], rowTitle, small, "Games too", gamesSub, castGamesFor(config), castPanel.focus == castFocusGames)
	}

	castDrawToggleRow(renderer, l.rows[castFocusSound], rowTitle, small, "Sound", castPanelSoundSub(config),
		castSoundFor(config), castPanel.focus == castFocusSound)

	// Controllers: the setting, or the viewer asking to play, or the one
	// playing.
	owner, asking, waiting := castInputStatus()
	ctlRow := l.rows[castFocusControl]
	ctlFocus := castPanel.focus == castFocusControl
	switch {
	case asking != "":
		sub := "A lets them play · X says no"
		if waiting > 1 {
			sub += fmt.Sprintf(" · %d waiting", waiting)
		}
		castDrawRequestRow(renderer, ctlRow, rowTitle, small, asking+" asks to play", sub, ctlFocus)
	case owner != "":
		castDrawToggleRow(renderer, ctlRow, rowTitle, small, "Controllers", owner+" is playing · X takes it back", true, ctlFocus)
	case castControlFor(config):
		castDrawToggleRow(renderer, ctlRow, rowTitle, small, "Controllers", "Viewers can ask to play; you allow each one here", true, ctlFocus)
	default:
		castDrawToggleRow(renderer, ctlRow, rowTitle, small, "Controllers", "Off — viewers only watch and listen", false, ctlFocus)
	}
	castPanelRects = l.rows
	castPanelRects[castFocusQR] = qr

	// The link, the live line and the privacy note.
	castPanelInfoRect = l.info
	if l.info.H > 0 {
		renderWithClip(renderer, l.info.X, l.info.Y, l.info.W, l.info.H, func(r *sdl.Renderer) {
			y := l.info.Y
			bottom := l.info.Y + l.info.H
			if castPanel.err != "" {
				drawCompanionParagraph(r, body, castPanel.err, l.info.X, y, l.info.W, ColorDanger, companionFontLine(body), bottom)
				return
			}
			drawText(r, small, "WATCH LINK", l.info.X, y, muted, textAlignLeft)
			y += line + 2
			for _, part := range castPanelWrappedURL(body, l.info.W) {
				if y+companionFontLine(body) > bottom {
					break
				}
				drawText(r, body, part, l.info.X, y, ink, textAlignLeft)
				y += companionFontLine(body)
			}
			y += 6
			if live := castLiveLine(); live != "" {
				drawText(r, small, live, l.info.X, y, glassAccent(), textAlignLeft)
				y += line + 4
			}
			note := "Anyone with this link can watch and listen while sharing is on; playing needs your yes here. " +
				"Everything on screen is shown except the phone pairing code. Y makes a new link."
			drawCompanionParagraph(r, small, note, l.info.X, y, l.info.W, muted, line, bottom)
		})
	}

	hints := hintLine(FooterHint{"Up/Down", "Select"}, FooterHint{"Left/Right", "Change"}, FooterHint{"A", "Choose"},
		FooterHint{"Y", "New link"}, FooterHint{"B", "Close"})
	switch {
	case asking != "":
		hints = hintLine(FooterHint{"A", "Let them play"}, FooterHint{"X", "No"}, FooterHint{"Up/Down", "Select"},
			FooterHint{"Y", "New link"}, FooterHint{"B", "Close"})
	case owner != "":
		hints = hintLine(FooterHint{"Up/Down", "Select"}, FooterHint{"A", "Choose"}, FooterHint{"X", "Take control back"},
			FooterHint{"Y", "New link"}, FooterHint{"B", "Close"})
	}
	hints = ellipsize(small, hints, l.card.W-2*pad)
	hw, _ := measureText(small, hints)
	drawText(renderer, small, hints, l.card.X+(l.card.W-hw)/2, l.footerY, muted, textAlignLeft)
}

// castPanelWrap caches the link broken into lines: measuring it rune by rune
// every frame cost more than drawing it.
var castPanelWrap struct {
	url   string
	width int32
	font  *ttf.Font
	lines []string
}

// castPanelWrappedURL is the panel's link broken to width.
func castPanelWrappedURL(font *ttf.Font, width int32) []string {
	c := &castPanelWrap
	if c.url != castPanel.url || c.width != width || c.font != font {
		c.url, c.width, c.font = castPanel.url, width, font
		c.lines = companionWrapAddress(font, castPanel.url, width)
	}
	return c.lines
}

// castDrawToggleRow is a switch row: title, a line under it, the switch on
// the right.
func castDrawToggleRow(renderer *sdl.Renderer, r sdl.Rect, titleFont, subFont *ttf.Font, title, sub string, on, focused bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	UIListRow(renderer, r, glassRowRadius, focused, false)
	toggle := tbxDrawToggle(renderer, r.X+r.W-16, r.Y+r.H/2, on)
	castDrawRowText(renderer, r, toggle.X-r.X-30, titleFont, subFont, title, sub)
}

// castDrawRequestRow is the Controllers row while a viewer asks to play: the
// request in the accent colour, and the answer's buttons on the right.
func castDrawRequestRow(renderer *sdl.Renderer, r sdl.Rect, titleFont, subFont *ttf.Font, title, sub string, focused bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	UIListRow(renderer, r, glassRowRadius, focused, false)
	accent := glassAccent()
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, 2, WithAlpha(accent, 200))
	label := "Allow"
	lw, lh := measureText(subFont, label)
	chip := sdl.Rect{X: r.X + r.W - 16 - lw - 24, Y: r.Y + (r.H-lh-10)/2, W: lw + 24, H: lh + 10}
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, WithAlpha(accent, 60))
	drawText(renderer, subFont, label, chip.X+12, chip.Y+5, accent, textAlignLeft)
	th := companionFontLine(titleFont)
	sh := companionFontLine(subFont)
	textW := max32(40, chip.X-r.X-30)
	ty := r.Y + (r.H-th-sh-2)/2
	drawText(renderer, titleFont, ellipsize(titleFont, title, textW), r.X+18, ty, accent, textAlignLeft)
	drawText(renderer, subFont, ellipsize(subFont, sub, textW), r.X+18, ty+th+2, ColorTextMuted(), textAlignLeft)
}

// castPanelSoundSub is the Sound row's line: what viewers hear, and why a
// game's own sound may stay on the handheld.
func castPanelSoundSub(config *Config) string {
	if !castSoundFor(config) {
		return "Off — sound stays on the handheld"
	}
	listeners, flowing, game := castAudioInfo()
	switch {
	case game:
		return "Viewers hear the game"
	case flowing:
		return "Viewers hear the handheld"
	case listeners > 0:
		return "A viewer listens; nothing is playing"
	}
	if !castTapApplies() {
		return "Viewers can listen to JukaHub's own sound"
	}
	if ok, why, known := castTapProbeState(); known && !ok {
		return "JukaHub's sound only: " + why
	}
	return "Viewers can listen, games started from now on too"
}

// castDrawQualityRow is the Quality row: the preset's name between arrows on
// the right, what it means under the title.
func castDrawQualityRow(renderer *sdl.Renderer, r sdl.Rect, titleFont, subFont *ttf.Font, p castPreset, focused bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	UIListRow(renderer, r, glassRowRadius, focused, false)
	size := "full size"
	if p.Width > 0 {
		size = fmt.Sprintf("%d px wide", p.Width)
	}
	sub := fmt.Sprintf("%s · up to %d fps", size, p.FPS)
	value := p.Label
	vw, vh := measureText(titleFont, value)
	arrow := int32(6)
	right := r.X + r.W - 18
	valueX := right - arrow - 10 - vw
	cy := r.Y + r.H/2
	ink := ColorTextPrimary()
	if focused {
		ink = glassAccent()
	}
	at := castPresetIndex(p.Name)
	if at > 0 {
		drawChevronLeft(renderer, valueX-10-arrow/2, cy, arrow, ink)
		drawChevronLeft(renderer, valueX-9-arrow/2, cy, arrow, ink)
	}
	drawText(renderer, titleFont, value, valueX, cy-vh/2, ink, textAlignLeft)
	if at < len(castPresets)-1 {
		drawChevronRight(renderer, right-arrow/2, cy, arrow, ink)
		drawChevronRight(renderer, right-arrow/2+1, cy, arrow, ink)
	}
	castDrawRowText(renderer, r, valueX-r.X-40, titleFont, subFont, "Quality", sub)
}

// castDrawRowText draws a row's title and the line under it, left-aligned and
// cut to textW.
func castDrawRowText(renderer *sdl.Renderer, r sdl.Rect, textW int32, titleFont, subFont *ttf.Font, title, sub string) {
	if textW < 40 {
		textW = 40
	}
	th := companionFontLine(titleFont)
	sh := companionFontLine(subFont)
	ty := r.Y + (r.H-th-sh-2)/2
	drawText(renderer, titleFont, ellipsize(titleFont, title, textW), r.X+18, ty, ColorTextPrimary(), textAlignLeft)
	drawText(renderer, subFont, ellipsize(subFont, sub, textW), r.X+18, ty+th+2, ColorTextMuted(), textAlignLeft)
}
