package main

// partymode_ui.go — the Party Mode screen on the handheld: the lobby with
// its QR code, the rounds of Guess the Game, and the winner screen.
//
// The room lives in partymode.go and is served by partymode_net.go; this
// file only draws it and turns the pad into the host's controls (and, when
// the host takes the handheld seat, into a buzzer and four answer buttons).
// The view is copied from the room when its version changes (or once a
// second), never rebuilt per frame; timer bars count down from the copy.
//
// Leaving the screen by any route ends the party (the scene task stops the
// listener), so B asks first while phones are seated.

import (
	"image"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// pmodeSceneName is the scene the Tools hub and Home search open.
const pmodeSceneName = "PartyMode"

// pmodePalette is the player colours, the same eight the phone page uses.
var pmodePalette = [pmodeColors]sdl.Color{
	{R: 255, G: 93, B: 115, A: 255},
	{R: 255, G: 184, B: 77, A: 255},
	{R: 255, G: 228, B: 94, A: 255},
	{R: 94, G: 224, B: 122, A: 255},
	{R: 77, G: 212, B: 255, A: 255},
	{R: 110, G: 139, B: 255, A: 255},
	{R: 199, G: 125, B: 255, A: 255},
	{R: 255, G: 122, B: 217, A: 255},
}

// pmodeChoiceColors are the four answer tiles (A red, B blue, C gold, D
// green), dark enough for white text; the phone page uses the same ones.
var pmodeChoiceColors = [pmodeChoiceCount]sdl.Color{
	{R: 226, G: 52, B: 79, A: 255},
	{R: 47, G: 111, B: 228, A: 255},
	{R: 154, G: 100, B: 0, A: 255},
	{R: 23, G: 128, B: 74, A: 255},
}

func pmodeColor(i int) sdl.Color {
	return pmodePalette[((i%pmodeColors)+pmodeColors)%pmodeColors]
}

type pmodeConfetti struct {
	x, y, vx, vy float64
	spin         float64
	color        int
	size         int32
}

// pmodeScreenState is the screen's own state; the game is in the room.
type pmodeScreenState struct {
	visit tbxVisit
	nav   tbxNav
	view  pmodeView
	// ranked is view.Players best first, sorted when the view is copied.
	ranked  []pmodePlayerView
	viewAt  time.Time
	viewFor *pmodeRoom
	events  uint64
	// The cover texture and the crop being eased towards.
	tex      *sdl.Texture
	texPic   *image.NRGBA
	cropFrom image.Rectangle
	cropTo   image.Rectangle
	cropAt   time.Time
	// The join address and the QR codes, refreshed once a second.
	netAt    time.Time
	host     string
	joinURL  string
	qr       [][]bool
	qrFor    string
	wifiQR   [][]bool
	wifiFor  string
	choiceAt string
	confetti []pmodeConfetti
	confAt   time.Time
	wrap     tbxWrapCache
}

var pmodeScreen pmodeScreenState

// pmodeOpenScreen opens Party Mode. The Tools hub and Home search come
// through here.
func pmodeOpenScreen(config *Config) {
	tbxOpenScene(config, pmodeSceneName)
}

type pmodeScene struct{ sceneBase }

func (pmodeScene) render(r *sdl.Renderer, c *Config) bool {
	pmodeRenderScreen(r, c)
	return true
}

func (pmodeScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), pmodeSceneAction)
}

func (pmodeScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), pmodeSceneAction)
}

func init() { registerScene(pmodeScene{sceneBase{name: pmodeSceneName}}) }

// pmodeHubTile is the Tools hub's tile, beside Watch Party. It has no
// network need (the screen itself offers Wi-Fi and the hotspot) and no live
// state: the party only exists while its screen is up.
func pmodeHubTile() toolsHubTile {
	return toolsHubTile{
		id: "partymode", label: "Party Mode", icon: "sparkles",
		value: "Guess the Game — friends play from their phones",
	}
}

// pmodeScreenEnter runs once per visit: a fresh room and listener (the scene
// task), and a clean screen.
func pmodeScreenEnter() {
	pmodeScreen.nav.scroll = 0
	pmodeScreen.nav.focus = "start"
	pmodeScreen.view = pmodeView{}
	pmodeScreen.ranked = nil
	pmodeScreen.viewAt = time.Time{}
	pmodeScreen.viewFor = nil
	pmodeScreen.events = 0
	pmodeScreen.netAt = time.Time{}
	pmodeScreen.confetti = nil
	pmodeScreen.choiceAt = ""
	pmodeDropTexture()
	runSceneTask(pmodeRun)
}

func pmodeDropTexture() {
	if pmodeScreen.tex != nil {
		pmodeScreen.tex.Destroy()
	}
	pmodeScreen.tex = nil
	pmodeScreen.texPic = nil
}

// pmodeReleaseMemory runs when the screen is left (completeSceneTransition,
// core.go): the cover texture, the last view (which holds the closed room's
// picture) and the QR codes (one may encode the hotspot password) go. Main
// thread.
func pmodeReleaseMemory() {
	pmodeDropTexture()
	pmodeScreen.view, pmodeScreen.ranked, pmodeScreen.viewFor = pmodeView{}, nil, nil
	pmodeScreen.confetti = nil
	pmodeScreen.qr, pmodeScreen.qrFor, pmodeScreen.wifiQR, pmodeScreen.wifiFor = nil, "", nil, ""
	pmodeScreen.joinURL, pmodeScreen.host = "", ""
}

// pmodeTexture uploads a cover with nearest-pixel scaling, so a tiny crop
// shows as crisp blocks on the handheld just as on the phones (imgvTexture
// asks for smooth scaling).
func pmodeTexture(renderer *sdl.Renderer, pic *image.NRGBA) *sdl.Texture {
	if renderer == nil || pic == nil || len(pic.Pix) == 0 {
		return nil
	}
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "0")
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, int32(pic.Rect.Dx()), int32(pic.Rect.Dy()))
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pic.Pix[0]), pic.Stride); err != nil {
		tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// pmodeRefresh copies the room's view when it changed, plays the sounds for
// what happened since the last frame, and refreshes the address once a
// second. Main thread.
func pmodeRefresh(config *Config, room *pmodeRoom, now time.Time) {
	if room != pmodeScreen.viewFor {
		pmodeScreen.viewFor = room
		pmodeScreen.events = 0
		pmodeScreen.viewAt = time.Time{}
		// The room (and its port) just opened: make the QR code now.
		pmodeScreen.netAt = time.Time{}
	}
	if room != nil {
		ver, _ := room.Changed()
		if ver != pmodeScreen.view.Version || now.Sub(pmodeScreen.viewAt) > time.Second || pmodeScreen.viewAt.IsZero() {
			pmodeScreen.view = room.View(now)
			pmodeScreen.viewAt = now
			ranked := append(pmodeScreen.ranked[:0], pmodeScreen.view.Players...)
			sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Rank < ranked[j].Rank })
			pmodeScreen.ranked = ranked
		}
		var evs []pmodeEvent
		evs, pmodeScreen.events = room.EventsAfter(pmodeScreen.events)
		for _, e := range evs {
			pmodePlayEvent(config, e)
		}
	}
	if now.Sub(pmodeScreen.netAt) >= time.Second {
		pmodeScreen.netAt = now
		pmodeRefreshNet(config, room)
	}
}

// pmodeRefreshNet works out the address phones should open: the hotspot's
// own address while it is on, else the Wi-Fi one.
func pmodeRefreshNet(config *Config, room *pmodeRoom) {
	host := localIPv4()
	wifi := ""
	if hotspotSupported() {
		if hs := hotspotCurrent(); hs.phase == hotspotOn && hs.run.ip != "" {
			if !shotModeOn() {
				host = hs.run.ip
				ssid, pass := hotspotCredentials(config)
				if ssid != "" {
					wifi = hotspotWiFiQR(ssid, pass)
				}
			}
		}
	}
	pmodeScreen.host = host
	pmodeScreen.joinURL = ""
	if _, srv, _ := pmodeCurrent(); srv != nil && room != nil {
		pmodeScreen.joinURL = pmodeJoinURL(host, srv.Port(), room.Code())
	}
	if pmodeScreen.joinURL != pmodeScreen.qrFor {
		pmodeScreen.qrFor = pmodeScreen.joinURL
		pmodeScreen.qr = nil
		if pmodeScreen.joinURL != "" {
			pmodeScreen.qr = tbxQRModules(pmodeScreen.joinURL)
		}
	}
	if wifi != pmodeScreen.wifiFor {
		pmodeScreen.wifiFor = wifi
		pmodeScreen.wifiQR = nil
		if wifi != "" {
			pmodeScreen.wifiQR = tbxQRModules(wifi)
		}
	}
}

// pmodePlayEvent is the handheld's sound (and, at the end, the confetti)
// for one thing that happened in the room.
func pmodePlayEvent(config *Config, e pmodeEvent) {
	switch e.kind {
	case "join":
		PlayToggleSound()
	case "leave", "nobody":
		PlayBackSound()
	case "round", "buzz":
		PlayActivateSound()
	case "stage":
		PlayNavSound()
	case "right":
		PlaySuccessSound()
	case "wrong", "timeout":
		PlayErrorSound()
	case "final":
		PlaySuccessSound()
		pmodeStartConfetti(config)
	case "nocovers":
		ShowToast("None of the chosen covers would open — check the library's box art", ToastKindWarning)
	}
}

// ── Render ───────────────────────────────────────────────────────────────

func pmodeRenderScreen(renderer *sdl.Renderer, config *Config) {
	if pmodeScreen.visit.begin() {
		pmodeScreenEnter()
	}
	now := time.Now()
	room, _, errText := pmodeCurrent()
	pmodeRefresh(config, room, now)
	v := &pmodeScreen.view
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	status, statusColor := "", glassAccent()
	switch {
	case room == nil:
	case v.Phase == pmodeLobby:
		status = "Code " + v.Code
	case v.Phase == pmodeFinal:
		status = "Game over"
	default:
		status = "Round " + strconv.Itoa(v.Round) + " of " + strconv.Itoa(v.Rounds)
	}
	header := tbxHeader("Party Mode", "Guess the Game · friends play from their phones", "sparkles", status, statusColor)
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &pmodeScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	switch {
	case errText != "":
		card := sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: min(body.H, 220)}
		UICard(renderer, card, DefaultUICardStyle())
		tbxDrawEmpty(renderer, config, card, "warning", "Party Mode could not start", errText)
	case room == nil && sceneWorkContext().Err() != nil:
		// Leaving: the room has closed during the fade-out. Draw nothing
		// rather than flash "Opening".
	case room == nil:
		card := sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: min(body.H, 220)}
		UICard(renderer, card, DefaultUICardStyle())
		tbxDrawEmpty(renderer, config, card, "sparkles", "Opening Party Mode…", "Setting up the room")
	case v.Phase == pmodeLobby:
		pmodeRenderLobby(renderer, config, fonts, body, v)
		requestFrameEvery(250)
	case v.Phase == pmodeFinal:
		pmodeRenderFinal(renderer, config, fonts, body, v, now)
		if len(pmodeScreen.confetti) > 0 {
			requestFrameEvery(33)
		} else {
			requestFrameEvery(250)
		}
	default:
		pmodeRenderGame(renderer, config, fonts, body, v, now)
		requestFrameEvery(33)
	}
	tbxUnclip(renderer)
	nav.end()
	tbxEndFrame(renderer, config)
}

// pmodeLeft is the time left in the phase as of now, from the copied view.
func pmodeLeft(v *pmodeView, now time.Time) time.Duration {
	left := v.Left - now.Sub(pmodeScreen.viewAt)
	if left < 0 {
		return 0
	}
	return left
}

func pmodeBigFont(config *Config, size int, fallback *ttf.Font) *ttf.Font {
	if f := getFontSized(config, "large", size); f != nil {
		return f
	}
	return fallback
}

// pmodeCentered draws text centred on cx. raw text (names, titles) skips the
// interface-language catalogue.
func pmodeCentered(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, cx, y, maxW int32, raw bool) {
	if font == nil || text == "" {
		return
	}
	if !raw {
		// Translate first, then fit: the width that matters is the drawn one.
		text = uiT(text)
	}
	text = ellipsize(font, text, maxW)
	w, _ := measureTextRaw(font, text)
	renderTextRaw(renderer, config, font, text, color, cx-w/2, y)
}

// ── Lobby ────────────────────────────────────────────────────────────────

func pmodeRenderLobby(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect, v *pmodeView) {
	nav := &pmodeScreen.nav
	gap := int32(22)
	leftW := body.W * 42 / 100
	left := sdl.Rect{X: body.X, Y: body.Y, W: leftW, H: body.H}
	right := sdl.Rect{X: body.X + leftW + gap, Y: body.Y, W: body.W - leftW - gap, H: body.H}
	row := 0

	// Left: how to get in.
	UICard(renderer, left, DefaultUICardStyle())
	if pmodeScreen.host == "" {
		empty := sdl.Rect{X: left.X, Y: left.Y, W: left.W, H: left.H - 90}
		tbxDrawEmpty(renderer, config, empty, "wifi-off", "No network yet",
			"Connect to Wi-Fi, or start the handheld's hotspot, so phones can reach it.")
		btnH := int32(48)
		wifiW := tbxButtonWidth(fonts.body, "Wi-Fi", 130)
		total := wifiW
		hotW := int32(0)
		if hotspotSupported() {
			hotW = tbxButtonWidth(fonts.body, "Hotspot", 130)
			total += 12 + hotW
		}
		bx := left.X + (left.W-total)/2
		by := left.Y + left.H - btnH - 30
		wifi := sdl.Rect{X: bx, Y: by, W: wifiW, H: btnH}
		tbxDrawButton(renderer, config, fonts.body, wifi, "Wi-Fi", true, nav.focused("wifi"), false)
		nav.add("wifi", pmodeBodyRect(wifi, body), row, false)
		if hotW > 0 {
			hot := sdl.Rect{X: bx + wifiW + 12, Y: by, W: hotW, H: btnH}
			tbxDrawButton(renderer, config, fonts.body, hot, "Hotspot", false, nav.focused("hotspot"), false)
			nav.add("hotspot", pmodeBodyRect(hot, body), row, false)
		}
		row++
	} else {
		pmodeDrawJoinCard(renderer, config, fonts, left, v)
	}

	// Right: the game, the length, who is in, and Start.
	y := right.Y
	titleFont := pmodeBigFont(config, 30, fonts.title)
	renderText(renderer, config, titleFont, "Guess the Game", ColorTextPrimary(), right.X, y)
	y += int32(titleFont.Height()) + 4
	desc := "A tiny piece of a cover from your library zooms out. Buzz on your phone, pick the game — the earlier, the more points."
	for i, line := range pmodeScreen.wrap.get(fonts.small, uiT(desc), right.W) {
		if i == 2 {
			break
		}
		renderTextRaw(renderer, config, fonts.small, line, ColorTextMuted(), right.X, y)
		y += fonts.smallH + 2
	}
	y += 8
	deckText, deckTone := "Reading your library…", tbxToneNeutral
	switch {
	case !v.DeckReady:
	case v.DeckSize < pmodeMinDeck:
		deckText = "Needs 4 games with box art · " + strconv.Itoa(v.DeckSize) + " found"
		deckTone = tbxToneWarn
	default:
		deckText = pluralizeCount(v.DeckSize, "game", "games") + " with box art"
		deckTone = tbxToneGood
	}
	pw, ph := tbxPillSize(fonts.tiny, deckText)
	tbxDrawPill(renderer, config, fonts.tiny, right.X+pw, y+ph/2, deckText, deckTone)
	y += ph + 14

	// Rounds: Left/Right change it.
	rowH := int32(52)
	rr := sdl.Rect{X: right.X, Y: y, W: right.W, H: rowH}
	UIListRow(renderer, rr, glassRowRadius, nav.focused("rounds"), false)
	renderText(renderer, config, fonts.body, "Rounds", ColorTextPrimary(), rr.X+18, rr.Y+(rowH-fonts.bodyH)/2)
	val := "‹  " + strconv.Itoa(v.Target) + "  ›"
	vw, _ := measureText(fonts.body, val)
	renderText(renderer, config, fonts.body, val, glassAccent(), rr.X+rr.W-18-vw, rr.Y+(rowH-fonts.bodyH)/2)
	nav.add("rounds", pmodeBodyRect(rr, body), row, false)
	row++
	y += rowH + 16

	// Players.
	btnH := int32(48)
	listBottom := right.Y + right.H - btnH - 16
	renderText(renderer, config, fonts.tiny, "PLAYERS · "+strconv.Itoa(len(v.Players))+" of "+strconv.Itoa(pmodeMaxPlayers),
		ColorTextTertiary(), right.X, y)
	y += fonts.tinyH + 8
	if len(v.Players) == 0 {
		empty := ellipsize(fonts.body, uiT("Nobody yet — scan the code with a phone camera"), right.W)
		renderTextRaw(renderer, config, fonts.body, empty, ColorTextMuted(), right.X, y)
	} else {
		lineH := fonts.bodyH + 10
		perCol := int((listBottom - y) / lineH)
		if perCol < 1 {
			perCol = 1
		}
		cols := 1
		if len(v.Players) > perCol {
			cols = 2
		}
		colW := right.W / int32(cols)
		slots := perCol * cols
		for i, p := range v.Players {
			col, line := i/perCol, i%perCol
			px := right.X + int32(col)*colW
			py := y + int32(line)*lineH
			if i == slots-1 && len(v.Players) > slots {
				// A large text size leaves less room: say who is not shown.
				more := "+" + strconv.Itoa(len(v.Players)-i) + " more"
				renderText(renderer, config, fonts.body, more, ColorTextMuted(), px+24, py)
				break
			}
			pmodeDrawPlayerLine(renderer, config, fonts, px, py, colW-12, p, false)
		}
	}

	// Start, and the handheld's own seat.
	startW := tbxButtonWidth(fonts.body, "Start game", 170)
	seatLabel := "Play on this handheld"
	if v.HasLocal {
		seatLabel = "Leave the handheld seat"
	}
	seatW := tbxButtonWidth(fonts.body, seatLabel, 170)
	by := right.Y + right.H - btnH
	start := sdl.Rect{X: right.X, Y: by, W: startW, H: btnH}
	seat := sdl.Rect{X: start.X + startW + 12, Y: by, W: seatW, H: btnH}
	canStart := v.DeckReady && v.DeckSize >= pmodeMinDeck && len(v.Players) > 0
	tbxDrawButton(renderer, config, fonts.body, start, "Start game", true, nav.focused("start"), !canStart)
	tbxDrawButton(renderer, config, fonts.body, seat, seatLabel, false, nav.focused("seat"), false)
	nav.add("start", pmodeBodyRect(start, body), row, false)
	nav.add("seat", pmodeBodyRect(seat, body), row, false)
	if nav.focus == "" || (nav.focus == "wifi" || nav.focus == "hotspot") && pmodeScreen.host != "" {
		nav.focus = "start"
	}
}

// pmodeBodyRect converts a screen rect to the nav's body coordinates (the
// screen does not scroll, so this is only the body's top).
func pmodeBodyRect(r, body sdl.Rect) sdl.Rect {
	r.Y -= body.Y
	return r
}

// pmodeDrawJoinCard is the QR code (two while the hotspot is on: the Wi-Fi
// first, then the game), the party code and the address to type.
func pmodeDrawJoinCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, card sdl.Rect, v *pmodeView) {
	codeFont := pmodeBigFont(config, 40, fonts.title)
	codeH := int32(codeFont.Height())
	y := card.Y + 18
	caption := "SCAN TO JOIN"
	if pmodeScreen.wifiQR != nil {
		caption = "SCAN BOTH, LEFT FIRST"
	}
	pmodeCentered(renderer, config, fonts.tiny, caption, ColorTextTertiary(), card.X+card.W/2, y, card.W-24, false)
	y += fonts.tinyH + 10
	room := card.H - (y - card.Y) - (codeH + fonts.smallH*2 + 30)
	if pmodeScreen.wifiQR != nil {
		side := min((card.W-60)/2, room-fonts.smallH-6)
		side = max(side, 90)
		x0 := card.X + (card.W-2*side-20)/2
		pmodeDrawQRTile(renderer, sdl.Rect{X: x0, Y: y, W: side, H: side}, pmodeScreen.wifiQR)
		pmodeDrawQRTile(renderer, sdl.Rect{X: x0 + side + 20, Y: y, W: side, H: side}, pmodeScreen.qr)
		pmodeCentered(renderer, config, fonts.small, "1 · Join Wi-Fi", ColorTextSecondary(), x0+side/2, y+side+4, side, false)
		pmodeCentered(renderer, config, fonts.small, "2 · Open the game", ColorTextSecondary(), x0+side+20+side/2, y+side+4, side, false)
		y += side + fonts.smallH + 12
	} else {
		side := min(card.W-64, room)
		side = max(min(side, 300), 90)
		pmodeDrawQRTile(renderer, sdl.Rect{X: card.X + (card.W-side)/2, Y: y, W: side, H: side}, pmodeScreen.qr)
		y += side + 12
	}
	pmodeCentered(renderer, config, codeFont, v.Code, glassAccent(), card.X+card.W/2, y, card.W-24, true)
	y += codeH + 4
	addr := ""
	if u := pmodeScreen.joinURL; u != "" {
		addr = strings.TrimPrefix(u, "http://")
		if i := strings.Index(addr, "/"); i > 0 {
			addr = addr[:i]
		}
	}
	if addr != "" {
		line := strings.Replace(uiT("or open %s and type the code"), "%s", addr, 1)
		pmodeCentered(renderer, config, fonts.small, line, ColorTextMuted(), card.X+card.W/2, y, card.W-24, true)
	}
}

func pmodeDrawQRTile(renderer *sdl.Renderer, tile sdl.Rect, modules [][]bool) {
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 12, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if len(modules) > 0 {
		drawCompanionQR(renderer, insetRect(tile, 8), modules)
	}
}

// pmodeDrawPlayerLine is one name with its colour dot; score adds the
// points (and this round's change) at the right edge.
func pmodeDrawPlayerLine(renderer *sdl.Renderer, config *Config, fonts tbxFonts, x, y, w int32, p pmodePlayerView, score bool) {
	nameColor := ColorTextPrimary()
	if p.Away || p.Locked {
		nameColor = ColorTextTertiary()
	}
	dot := pmodeColor(p.Color)
	if p.Away {
		dot = WithAlpha(dot, 90)
	}
	fillCircle(renderer, x+8, y+fonts.bodyH/2, 7, dot)
	right := x + w
	if score {
		sc := strconv.Itoa(p.Score)
		sw, _ := measureTextRaw(fonts.body, sc)
		renderTextRaw(renderer, config, fonts.body, sc, ColorTextPrimary(), right-sw, y)
		right -= sw + 12
		if p.Delta != 0 {
			d := strconv.Itoa(p.Delta)
			col := ColorToastSuccess
			if p.Delta > 0 {
				d = "+" + d
			} else {
				col = ColorToastError
			}
			dw, _ := measureTextRaw(fonts.small, d)
			renderTextRaw(renderer, config, fonts.small, d, col, right-dw, y+(fonts.bodyH-fonts.smallH)/2)
			right -= dw + 12
		}
	}
	name := p.Name
	tag := ""
	switch {
	case p.Local:
		tag = "handheld"
	case p.Away:
		tag = "away"
	case p.Locked:
		tag = "out"
	}
	tx := x + 24
	if tag != "" {
		tw, _ := measureText(fonts.tiny, tag)
		if right-tw-8 > tx+60 {
			renderText(renderer, config, fonts.tiny, tag, ColorTextTertiary(), right-tw, y+(fonts.bodyH-fonts.tinyH)/2)
			right -= tw + 10
		}
	}
	renderTextRaw(renderer, config, fonts.body, ellipsize(fonts.body, name, right-tx), nameColor, tx, y)
}

// ── Game ─────────────────────────────────────────────────────────────────

func pmodeRenderGame(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect, v *pmodeView, now time.Time) {
	nav := &pmodeScreen.nav
	side := min(body.H-22, body.W*50/100)
	frame := sdl.Rect{X: body.X, Y: body.Y, W: side, H: side}
	pmodeDrawCover(renderer, config, fonts, frame, v, now)
	if v.HasLocal && !v.LocalOut && v.Phase == pmodeReveal {
		nav.add("buzz", pmodeBodyRect(frame, body), 0, false)
	}

	// The timer under the picture.
	bar := sdl.Rect{X: frame.X, Y: frame.Y + frame.H + 12, W: frame.W, H: 8}
	fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, 4, glassA(40))
	if v.PhaseFor > 0 {
		frac := float64(pmodeLeft(v, now)) / float64(v.PhaseFor)
		if fw := int32(float64(bar.W) * math.Max(0, math.Min(1, frac))); fw > 0 {
			col := glassAccent()
			if v.Phase == pmodeAnswer {
				col = pmodeChoiceColors[0]
			}
			fillRoundedRect(renderer, bar.X, bar.Y, fw, bar.H, 4, col)
		}
	}

	// Right column: what is happening, then the choices or the scoreboard.
	rx := frame.X + frame.W + 26
	rw := body.X + body.W - rx
	y := body.Y
	statusFont := pmodeBigFont(config, 34, fonts.title)
	statusH := int32(statusFont.Height())
	headline, sub := pmodeHeadline(v, now)
	cardH := statusH + 28
	if sub != "" {
		cardH += fonts.bodyH + 4
	}
	if v.Phase == pmodeResult {
		cardH += fonts.titleH + fonts.smallH + 14
	}
	card := sdl.Rect{X: rx, Y: y, W: rw, H: cardH}
	UICard(renderer, card, DefaultUICardStyle())
	ty := card.Y + 14
	// Names make the answer and result lines content; the rest is
	// interface text, translated before it is fitted.
	if v.Phase != pmodeAnswer && v.Phase != pmodeResult {
		headline, sub = uiT(headline), uiT(sub)
	}
	renderTextRaw(renderer, config, statusFont, ellipsize(statusFont, headline, rw-36), ColorTextPrimary(), rx+18, ty)
	ty += statusH + 4
	if sub != "" {
		renderTextRaw(renderer, config, fonts.body, ellipsize(fonts.body, sub, rw-36), glassAccent(), rx+18, ty)
		ty += fonts.bodyH + 4
	}
	if v.Phase == pmodeResult {
		ty += 6
		renderTextRaw(renderer, config, fonts.title, ellipsize(fonts.title, v.Title, rw-36), ColorTextPrimary(), rx+18, ty)
		ty += fonts.titleH + 4
		line := v.System
		if len(v.Wrong) > 0 {
			line += " · missed: " + strings.Join(v.Wrong, ", ")
		}
		renderTextRaw(renderer, config, fonts.small, ellipsize(fonts.small, line, rw-36), ColorTextMuted(), rx+18, ty)
	}
	y += cardH + 16

	if len(v.Choices) > 0 {
		pmodeDrawChoices(renderer, config, fonts, sdl.Rect{X: rx, Y: y, W: rw, H: body.Y + body.H - y}, v, body)
		return
	}
	pmodeDrawBoard(renderer, config, fonts, sdl.Rect{X: rx, Y: y, W: rw, H: body.Y + body.H - y}, v.Phase == pmodeResult)
}

// pmodeHeadline is the status card's two lines.
func pmodeHeadline(v *pmodeView, now time.Time) (string, string) {
	secs := int(math.Ceil(pmodeLeft(v, now).Seconds()))
	switch v.Phase {
	case pmodeIntro:
		if v.Waiting {
			return "Shuffling the next cover…", ""
		}
		return "Get ready!", "Round " + strconv.Itoa(v.Round) + " of " + strconv.Itoa(v.Rounds)
	case pmodeReveal:
		sub := strconv.Itoa(v.Points) + " points now · fewer as the cover opens"
		if v.HasLocal && !v.LocalOut {
			sub = hintSentence("%s to buzz here · ", "A", "Buzz") + strconv.Itoa(v.Points) + " points"
		}
		return "Buzz on your phones!", sub
	case pmodeAnswer:
		if v.BuzzLocal {
			return v.Buzzer + ", pick the game!", strconv.Itoa(secs) + " s"
		}
		return v.Buzzer + " is answering…", strconv.Itoa(secs) + " s"
	case pmodeResult:
		if v.Winner != "" {
			return v.Winner + " got it!", "+" + strconv.Itoa(v.WinPoints) + " points"
		}
		return "Nobody got it", "The answer was"
	}
	return "", ""
}

// pmodeDrawCover draws the picture frame: the stage's crop easing out to the
// whole cover, the points at stake and the stage dots, or the round's intro.
func pmodeDrawCover(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame sdl.Rect, v *pmodeView, now time.Time) {
	fillRoundedRect(renderer, frame.X, frame.Y, frame.W, frame.H, 18, sdl.Color{R: 12, G: 6, B: 32, A: 255})
	inner := insetRect(frame, 6)
	if v.Phase == pmodeIntro || v.Pic == nil {
		pmodeDropTexture()
		big := pmodeBigFont(config, 64, fonts.title)
		cy := frame.Y + frame.H/2 - int32(big.Height())
		pmodeCentered(renderer, config, big, "Round "+strconv.Itoa(v.Round), ColorTextPrimary(), frame.X+frame.W/2, cy, frame.W-24, false)
		pmodeCentered(renderer, config, fonts.title, "of "+strconv.Itoa(v.Rounds), ColorTextMuted(), frame.X+frame.W/2, cy+int32(big.Height())+4, frame.W-24, false)
		strokeRoundedRect(renderer, frame.X, frame.Y, frame.W, frame.H, 18, 2, glassA(50))
		return
	}
	if pmodeScreen.texPic != v.Pic {
		pmodeDropTexture()
		pmodeScreen.tex = pmodeTexture(renderer, v.Pic)
		pmodeScreen.texPic = v.Pic
		pmodeScreen.cropFrom, pmodeScreen.cropTo, pmodeScreen.cropAt = v.Crop, v.Crop, now
	}
	if v.Crop != pmodeScreen.cropTo {
		pmodeScreen.cropFrom = pmodeCropNow(now)
		pmodeScreen.cropTo = v.Crop
		pmodeScreen.cropAt = now
	}
	if pmodeScreen.tex != nil {
		src := pmodeCropNow(now)
		if effectiveReducedMotion(config) {
			src = pmodeScreen.cropTo
		}
		sw, sh := src.Dx(), src.Dy()
		if sw > 0 && sh > 0 {
			scale := math.Min(float64(inner.W)/float64(sw), float64(inner.H)/float64(sh))
			dw, dh := int32(float64(sw)*scale), int32(float64(sh)*scale)
			dst := sdl.Rect{X: inner.X + (inner.W-dw)/2, Y: inner.Y + (inner.H-dh)/2, W: dw, H: dh}
			min0 := v.Pic.Rect.Min
			s := sdl.Rect{X: int32(src.Min.X - min0.X), Y: int32(src.Min.Y - min0.Y), W: int32(sw), H: int32(sh)}
			renderer.Copy(pmodeScreen.tex, &s, &dst)
		}
	}
	strokeRoundedRect(renderer, frame.X, frame.Y, frame.W, frame.H, 18, 2, glassA(50))

	// Points at stake, top right; the stage dots along the bottom.
	if v.Phase == pmodeReveal || v.Phase == pmodeAnswer {
		pts := strconv.Itoa(v.Points) + " pts"
		tbxDrawPill(renderer, config, fonts.small, frame.X+frame.W-14, frame.Y+26, pts, tbxToneAccent)
	}
	lit := v.Stage
	if v.Phase == pmodeResult || v.Phase == pmodeFinal {
		lit = pmodeLastStage
	}
	dotGap := int32(18)
	dx := frame.X + frame.W/2 - dotGap*int32(pmodeStages-1)/2
	for i := 0; i < pmodeStages; i++ {
		col := sdl.Color{R: 255, G: 255, B: 255, A: 70}
		if i <= lit {
			col = glassAccent()
		}
		fillCircle(renderer, dx+int32(i)*dotGap, frame.Y+frame.H-18, 5, col)
	}
	if v.Phase == pmodeAnswer {
		// The reveal is paused while someone answers: say so on the picture.
		band := sdl.Rect{X: frame.X, Y: frame.Y + frame.H/2 - 28, W: frame.W, H: 56}
		fillRoundedRect(renderer, band.X, band.Y, band.W, band.H, 0, sdl.Color{R: 12, G: 6, B: 32, A: 190})
		pmodeCentered(renderer, config, fonts.title, v.Buzzer+" buzzed!", sdl.Color{R: 255, G: 255, B: 255, A: 255},
			band.X+band.W/2, band.Y+(band.H-fonts.titleH)/2, band.W-24, true)
	}
}

// pmodeCropNow is the eased crop between the last two stages.
func pmodeCropNow(now time.Time) image.Rectangle {
	const ease = 450 * time.Millisecond
	t := float64(now.Sub(pmodeScreen.cropAt)) / float64(ease)
	if t >= 1 || pmodeScreen.cropFrom.Empty() {
		return pmodeScreen.cropTo
	}
	if t < 0 {
		t = 0
	}
	t = 1 - (1-t)*(1-t)*(1-t)
	lerp := func(a, b int) int { return a + int(math.Round(float64(b-a)*t)) }
	a, b := pmodeScreen.cropFrom, pmodeScreen.cropTo
	return image.Rect(lerp(a.Min.X, b.Min.X), lerp(a.Min.Y, b.Min.Y), lerp(a.Max.X, b.Max.X), lerp(a.Max.Y, b.Max.Y))
}

// pmodeDrawChoices is the handheld seat's four answers.
func pmodeDrawChoices(renderer *sdl.Renderer, config *Config, fonts tbxFonts, area sdl.Rect, v *pmodeView, body sdl.Rect) {
	nav := &pmodeScreen.nav
	key := strings.Join(v.Choices, "|")
	if key != pmodeScreen.choiceAt {
		pmodeScreen.choiceAt = key
		nav.focus = "c0"
	}
	gap := int32(10)
	h := (area.H - gap*int32(pmodeChoiceCount-1)) / int32(pmodeChoiceCount)
	h = max(min(h, 64), 40)
	for i, title := range v.Choices {
		id := "c" + strconv.Itoa(i)
		r := sdl.Rect{X: area.X, Y: area.Y + int32(i)*(h+gap), W: area.W, H: h}
		col := pmodeChoiceColors[i%pmodeChoiceCount]
		focused := nav.focused(id)
		if !focused {
			col = WithAlpha(col, 170)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, col)
		if focused {
			strokeRoundedRect(renderer, r.X-3, r.Y-3, r.W+6, r.H+6, 16, 3, sdl.Color{R: 255, G: 255, B: 255, A: 255})
		}
		letter := string(rune('A' + i))
		lw, _ := measureTextRaw(fonts.title, letter)
		renderTextRaw(renderer, config, fonts.title, letter, sdl.Color{R: 255, G: 255, B: 255, A: 220}, r.X+18, r.Y+(r.H-fonts.titleH)/2)
		tx := r.X + 18 + lw + 16
		renderTextRaw(renderer, config, fonts.title, ellipsize(fonts.title, title, r.X+r.W-16-tx), sdl.Color{R: 255, G: 255, B: 255, A: 255},
			tx, r.Y+(r.H-fonts.titleH)/2)
		nav.add(id, pmodeBodyRect(r, body), i, false)
	}
}

// pmodeDrawBoard is the scoreboard, best first.
func pmodeDrawBoard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, area sdl.Rect, deltas bool) {
	if area.H < fonts.bodyH+30 {
		return
	}
	UICard(renderer, area, DefaultUICardStyle())
	y := area.Y + 14
	renderText(renderer, config, fonts.tiny, "SCOREBOARD", ColorTextTertiary(), area.X+18, y)
	y += fonts.tinyH + 8
	lineH := fonts.bodyH + 10
	for i, p := range pmodeScreen.ranked {
		if y+fonts.bodyH > area.Y+area.H-8 {
			break
		}
		if y+lineH+fonts.bodyH > area.Y+area.H-8 && i < len(pmodeScreen.ranked)-1 {
			// The last line that fits says who is not shown.
			more := "+" + strconv.Itoa(len(pmodeScreen.ranked)-i) + " more"
			renderText(renderer, config, fonts.body, more, ColorTextMuted(), area.X+48+24, y)
			break
		}
		rk := strconv.Itoa(p.Rank) + "."
		renderTextRaw(renderer, config, fonts.small, rk, ColorTextTertiary(), area.X+18, y+(fonts.bodyH-fonts.smallH)/2)
		line := p
		if !deltas {
			line.Delta = 0
		}
		pmodeDrawPlayerLine(renderer, config, fonts, area.X+48, y, area.W-66, line, true)
		y += lineH
	}
}

// ── Final ────────────────────────────────────────────────────────────────

func pmodeRenderFinal(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect, v *pmodeView, now time.Time) {
	nav := &pmodeScreen.nav
	leftW := body.W * 58 / 100
	stage := sdl.Rect{X: body.X, Y: body.Y, W: leftW, H: body.H}
	UICard(renderer, stage, DefaultUICardStyle())

	// The headline: one winner, a tie for first, or nobody scored at all.
	headline, raw := "Game over", false
	tops := 0
	for _, p := range pmodeScreen.ranked {
		if p.Rank == 1 {
			tops++
		}
	}
	switch {
	case len(pmodeScreen.ranked) == 0 || pmodeScreen.ranked[0].Score <= 0:
	case tops > 1:
		headline = "It's a tie!"
	default:
		headline, raw = pmodeScreen.ranked[0].Name+" wins!", true
	}
	badge := int32(64)
	box := drawGlassBadge(renderer, sdl.Rect{X: stage.X + (stage.W-badge)/2, Y: stage.Y + 18, W: badge, H: badge})
	DrawHomeIcon(renderer, "trophy", box, true)
	big := pmodeBigFont(config, 48, fonts.title)
	y := stage.Y + 18 + badge + 10
	pmodeCentered(renderer, config, big, headline, glassAccent(), stage.X+stage.W/2, y, stage.W-32, raw)
	y += int32(big.Height()) + 18

	// The podium: second, first, third.
	baseY := stage.Y + stage.H - 22
	colW := (stage.W - 80) / 3
	heights := [3]int32{(baseY - y) * 85 / 100, (baseY - y) * 62 / 100, (baseY - y) * 45 / 100}
	slots := [3]int{1, 0, 2}
	for col, place := range slots {
		if place >= len(pmodeScreen.ranked) {
			continue
		}
		p := pmodeScreen.ranked[place]
		// Height follows the rank, so two players tied for first stand
		// equally tall.
		num := pmodeBigFont(config, 40, fonts.title)
		// Tall enough for the place and the score, whatever the text size.
		h := max(heights[min(max(p.Rank-1, 0), 2)]-fonts.bodyH-8, int32(num.Height())+fonts.smallH+14)
		bx := stage.X + 20 + int32(col)*(colW+20)
		block := sdl.Rect{X: bx, Y: baseY - h, W: colW, H: h}
		fillRoundedRect(renderer, block.X, block.Y, block.W, block.H, 12, WithAlpha(pmodeColor(p.Color), 220))
		pmodeCentered(renderer, config, fonts.body, p.Name, ColorTextPrimary(), bx+colW/2, block.Y-fonts.bodyH-6, colW, true)
		pmodeCentered(renderer, config, num, strconv.Itoa(p.Rank), sdl.Color{R: 20, G: 12, B: 40, A: 255}, bx+colW/2, block.Y+10, colW, true)
		pmodeCentered(renderer, config, fonts.small, strconv.Itoa(p.Score)+" pts", sdl.Color{R: 20, G: 12, B: 40, A: 230},
			bx+colW/2, block.Y+10+int32(num.Height()), colW, true)
	}

	// Right: everyone, and what next.
	rx := stage.X + stage.W + 22
	rw := body.X + body.W - rx
	btnH := int32(48)
	board := sdl.Rect{X: rx, Y: body.Y, W: rw, H: body.H - btnH - 16}
	pmodeDrawBoard(renderer, config, fonts, board, false)
	againW := tbxButtonWidth(fonts.body, "Play again", 150)
	lobbyW := tbxButtonWidth(fonts.body, "Lobby", 110)
	again := sdl.Rect{X: rx, Y: body.Y + body.H - btnH, W: againW, H: btnH}
	lobby := sdl.Rect{X: rx + againW + 12, Y: again.Y, W: lobbyW, H: btnH}
	if nav.focus != "again" && nav.focus != "lobby" {
		nav.focus = "again"
	}
	tbxDrawButton(renderer, config, fonts.body, again, "Play again", true, nav.focused("again"), false)
	tbxDrawButton(renderer, config, fonts.body, lobby, "Lobby", false, nav.focused("lobby"), false)
	nav.add("again", pmodeBodyRect(again, body), 0, false)
	nav.add("lobby", pmodeBodyRect(lobby, body), 0, false)

	pmodeDrawConfetti(renderer, body, now)
}

// pmodeStartConfetti throws a burst over the final screen; Reduce motion
// keeps it still.
func pmodeStartConfetti(config *Config) {
	if effectiveReducedMotion(config) {
		pmodeScreen.confetti = nil
		return
	}
	n := 90
	pmodeScreen.confetti = make([]pmodeConfetti, n)
	for i := range pmodeScreen.confetti {
		pmodeScreen.confetti[i] = pmodeConfetti{
			x:     rand.Float64(),
			y:     -rand.Float64() * 0.4,
			vx:    (rand.Float64() - 0.5) * 0.25,
			vy:    0.15 + rand.Float64()*0.35,
			spin:  rand.Float64() * math.Pi * 2,
			color: rand.Intn(pmodeColors),
			size:  int32(6 + rand.Intn(7)),
		}
	}
	pmodeScreen.confAt = time.Now()
}

func pmodeDrawConfetti(renderer *sdl.Renderer, area sdl.Rect, now time.Time) {
	if len(pmodeScreen.confetti) == 0 {
		return
	}
	dt := now.Sub(pmodeScreen.confAt).Seconds()
	pmodeScreen.confAt = now
	if dt > 0.1 {
		dt = 0.1
	}
	alive := 0
	for i := range pmodeScreen.confetti {
		c := &pmodeScreen.confetti[i]
		c.vy += 0.35 * dt
		c.x += c.vx * dt
		c.y += c.vy * dt
		c.spin += dt * 6
		if c.y > 1.05 {
			continue
		}
		alive++
		w := int32(float64(c.size) * (0.35 + 0.65*math.Abs(math.Cos(c.spin))))
		px := area.X + int32(c.x*float64(area.W))
		py := area.Y + int32(c.y*float64(area.H))
		col := pmodeColor(c.color)
		renderer.SetDrawColor(col.R, col.G, col.B, 230)
		renderer.FillRect(&sdl.Rect{X: px, Y: py, W: max(w, 2), H: c.size})
	}
	if alive == 0 {
		pmodeScreen.confetti = nil
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// pmodeSceneAction is one action on the screen (the sheet has had its turn).
func pmodeSceneAction(config *Config, act tbxAction) bool {
	nav := &pmodeScreen.nav
	room, _, errText := pmodeCurrent()
	if errText != "" {
		// The port did not open: there is no party to start or join.
		room = nil
	}
	v := &pmodeScreen.view
	// While the handheld seat picks an answer, B and X do nothing: the tiles
	// are lettered A-D, and B as "answer B" must not leave the party.
	picking := v.BuzzLocal && len(v.Choices) > 0
	if act == tbxActBack {
		if picking {
			return true
		}
		return pmodeAskLeave(config, room)
	}
	if room == nil {
		return tbxNavAction(nav, act)
	}
	now := time.Now()
	switch v.Phase {
	case pmodeLobby, pmodeFinal:
		switch act {
		case tbxActConfirm:
			pmodeActivate(config, room, nav.focus)
			return true
		case tbxActY:
			pmodeToggleSeat(room)
			return true
		case tbxActX:
			if v.Phase == pmodeFinal {
				PlayBackSound()
				room.EndGame()
			}
			return true
		case tbxActLeft, tbxActRight:
			if nav.focus == "rounds" {
				dir := 1
				if act == tbxActLeft {
					dir = -1
				}
				room.CycleTarget(dir)
				PlayNavSound()
				return true
			}
		}
		return tbxNavAction(nav, act)
	}
	// A game is running.
	switch act {
	case tbxActX:
		if !picking {
			pmodeAskEndGame(config, room)
		}
		return true
	case tbxActConfirm:
		if len(v.Choices) > 0 && strings.HasPrefix(nav.focus, "c") {
			pmodeAnswerLocal(room, nav.focus, now)
			return true
		}
		if v.HasLocal && v.Phase == pmodeReveal {
			if err := room.BuzzLocal(now); err != nil {
				ShowToast(pmodeErrText(err), ToastKindInfo)
			}
		}
		return true
	case tbxActUp, tbxActDown, tbxActLeft, tbxActRight:
		if len(v.Choices) > 0 && (act == tbxActUp || act == tbxActDown) {
			tbxNavAction(nav, act)
		}
		// The game owns the D-pad: the top bar is for the lobby.
		return true
	}
	return true
}

func pmodeErrText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func pmodeAnswerLocal(room *pmodeRoom, id string, now time.Time) {
	i, err := strconv.Atoi(strings.TrimPrefix(id, "c"))
	if err != nil {
		return
	}
	if _, err := room.AnswerLocal(i, now); err != nil {
		ShowToast(pmodeErrText(err), ToastKindInfo)
	}
}

func pmodeToggleSeat(room *pmodeRoom) {
	name := partyDeviceName()
	if shotModeOn() {
		// Screenshots never show the owner's name.
		name = "Handheld"
	}
	if err := room.SetLocal(!pmodeScreen.view.HasLocal, name, time.Now()); err != nil {
		ShowToast(pmodeErrText(err), ToastKindWarning)
	}
}

// pmodeActivate runs the focused control.
func pmodeActivate(config *Config, room *pmodeRoom, id string) {
	switch id {
	case "start", "again":
		if err := room.Start(time.Now()); err != nil {
			PlayErrorSound()
			ShowToast(pmodeErrText(err), ToastKindWarning)
		}
	case "lobby":
		PlayBackSound()
		room.EndGame()
		pmodeScreen.nav.focus = "start"
	case "seat":
		pmodeToggleSeat(room)
	case "rounds":
		room.CycleTarget(1)
		PlayNavSound()
	case "wifi":
		PlayActivateSound()
		wifiScrOpen(config)
	case "hotspot":
		PlayActivateSound()
		hotspotOpenScreen(config)
	}
}

// pmodePhonesSeated is how many phones are in the room.
func pmodePhonesSeated() int {
	n := 0
	for _, p := range pmodeScreen.view.Players {
		if !p.Local {
			n++
		}
	}
	return n
}

// pmodeAskLeave is B: leave at once from a lobby or final screen no phone
// is in, else ask, since leaving closes the party (and ends a game).
func pmodeAskLeave(config *Config, room *pmodeRoom) bool {
	n := pmodePhonesSeated()
	if room == nil {
		return false
	}
	ph := pmodeScreen.view.Phase
	idle := ph == pmodeLobby || ph == pmodeFinal || ph == ""
	if n == 0 && idle {
		return false
	}
	body := "Leaving this screen ends the game."
	if n > 0 {
		body = pluralizeCount(n, "phone is", "phones are") + " in the party. Leaving this screen closes it, and every phone shows that the party is over."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Close the party?",
		icon:  "sparkles",
		body:  body,
		buttons: []tbxSheetButton{
			{label: "Close the party", run: func() { goBackScene(config) }},
			{label: "Stay", primary: true},
		},
		focus: 1,
	})
	return true
}

// pmodeAskEndGame is X during a game: back to the lobby, scores cleared.
func pmodeAskEndGame(config *Config, room *pmodeRoom) {
	tbxOpenSheet(config, tbxSheetSpec{
		title: "End this game?",
		icon:  "sparkles",
		body:  "Scores are cleared and everyone goes back to the lobby. Nobody has to join again.",
		buttons: []tbxSheetButton{
			{label: "End game", run: func() {
				PlayBackSound()
				room.EndGame()
				pmodeScreen.nav.focus = "start"
			}},
			{label: "Keep playing", primary: true},
		},
		focus: 1,
	})
}

// pmodeSceneClick handles a click or tap on the screen's controls.
func pmodeSceneClick(mx, my int32, config *Config) bool {
	it, ok := pmodeScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	room, _, _ := pmodeCurrent()
	if room == nil {
		return false
	}
	pmodeScreen.nav.focus = it.id
	switch {
	case it.id == "buzz":
		if err := room.BuzzLocal(time.Now()); err != nil {
			ShowToast(pmodeErrText(err), ToastKindInfo)
		}
	case strings.HasPrefix(it.id, "c"):
		pmodeAnswerLocal(room, it.id, time.Now())
	case it.id == "rounds":
		room.CycleTarget(1)
		PlayNavSound()
	default:
		pmodeActivate(config, room, it.id)
	}
	return true
}

func pmodeSeatHint(v *pmodeView) string {
	if v.HasLocal {
		return "Leave seat"
	}
	return "Play here"
}

// pmodeSceneHints is the footer for the phase on screen.
func pmodeSceneHints() []FooterHint {
	if _, _, errText := pmodeCurrent(); errText != "" {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	v := &pmodeScreen.view
	id := pmodeScreen.nav.focus
	switch v.Phase {
	case pmodeLobby:
		hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
		switch id {
		case "start":
			hints = append(hints, FooterHint{Button: "A", Label: "Start"})
		case "rounds":
			hints[0].Label = "Rounds"
		case "wifi", "hotspot", "seat":
			hints = append(hints, FooterHint{Button: "A", Label: "Select"})
		}
		return append(hints, FooterHint{Button: "Y", Label: pmodeSeatHint(v)}, FooterHint{Button: "B", Label: "Back"})
	case pmodeFinal:
		return []FooterHint{{Button: "A", Label: "Select"}, {Button: "X", Label: "Lobby"},
			{Button: "Y", Label: pmodeSeatHint(v)}, {Button: "B", Label: "Back"}}
	case "":
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	var hints []FooterHint
	switch {
	case len(v.Choices) > 0:
		// B and X are held while the seat answers (pmodeSceneAction).
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Answer"}}
	case v.HasLocal && !v.LocalOut && v.Phase == pmodeReveal:
		hints = append(hints, FooterHint{Button: "A", Label: "Buzz"})
	}
	return append(hints, FooterHint{Button: "X", Label: "End game"}, FooterHint{Button: "B", Label: "Back"})
}
