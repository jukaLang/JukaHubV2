package main

// moonlight_ui_draw.go — drawing the PC streaming panel (moonlight_ui.go):
// the PC list, the PIN page, the games grid with box art, a PC's settings
// and the cards over them. Hit rects are rebuilt every frame so a tap lands
// on what is drawn.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Box art textures ─────────────────────────────────────────────────────

type mlArtEntry struct {
	tex  *sdl.Texture // nil: the file did not load (not tried again)
	used uint64
}

const (
	// mlArtTexMax bounds the box art kept as textures: a 600x800 cover is
	// ~2 MB of texture, and two rows of tiles are on screen at once.
	mlArtTexMax = 24
	// mlArtLoadsPerFrame keeps decoding from stalling a frame.
	mlArtLoadsPerFrame = 1
)

var mlArtTex struct {
	renderer *sdl.Renderer
	m        map[string]*mlArtEntry
	frame    uint64
	loads    int
}

// mlArtTexture is an app's box art, or nil (not fetched yet, none, or over
// this frame's decode budget). Main thread.
func mlArtTexture(renderer *sdl.Renderer, appID int) *sdl.Texture {
	path := mlPanel.artPath[appID]
	if renderer == nil || path == "" {
		return nil
	}
	if mlArtTex.renderer != renderer || mlArtTex.m == nil {
		// A new renderer took the old one's textures with it.
		mlArtTex.renderer = renderer
		mlArtTex.m = make(map[string]*mlArtEntry, mlArtTexMax)
	}
	if mlArtTex.frame != frameCount {
		mlArtTex.frame, mlArtTex.loads = frameCount, 0
	}
	if e, ok := mlArtTex.m[path]; ok {
		e.used = frameCount
		return e.tex
	}
	if mlArtTex.loads >= mlArtLoadsPerFrame {
		return nil
	}
	mlArtTex.loads++
	tex, err := img.LoadTexture(renderer, path)
	if err != nil {
		tex = nil
	}
	mlArtEvict()
	mlArtTex.m[path] = &mlArtEntry{tex: tex, used: frameCount}
	return tex
}

// mlArtEvict makes room for one more texture, never taking one drawn this
// frame.
func mlArtEvict() {
	for len(mlArtTex.m) >= mlArtTexMax {
		oldKey, oldUsed := "", frameCount
		for k, e := range mlArtTex.m {
			if e.used < oldUsed {
				oldKey, oldUsed = k, e.used
			}
		}
		if oldKey == "" {
			return
		}
		if e := mlArtTex.m[oldKey]; e.tex != nil {
			_ = e.tex.Destroy()
		}
		delete(mlArtTex.m, oldKey)
	}
}

// mlArtFree destroys every box art texture (the panel closed, or shows
// another PC).
func mlArtFree() {
	for _, e := range mlArtTex.m {
		if e.tex != nil {
			_ = e.tex.Destroy()
		}
	}
	mlArtTex.m = nil
}

// mlDrawCover draws an app's box art over box, cropped to fill it, or a glass
// tile with the title's initial while the art is on its way (or when the PC
// has none).
func mlDrawCover(renderer *sdl.Renderer, config *Config, box sdl.Rect, app mlApp, medium *ttf.Font) {
	if tex := mlArtTexture(renderer, app.ID); tex != nil {
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			src := sdl.Rect{W: tw, H: th}
			if tw*box.H > th*box.W {
				src.W = th * box.W / box.H
				src.X = (tw - src.W) / 2
			} else {
				src.H = tw * box.H / box.W
				src.Y = (th - src.H) / 2
			}
			_ = renderer.Copy(tex, &src, &box)
			return
		}
	}
	drawGlassBadge(renderer, box)
	if medium == nil {
		return
	}
	glyph := appGridGlyph(app.Title)
	gw, gh, _ := medium.SizeUTF8(glyph)
	renderText(renderer, config, medium, glyph, ColorTextPrimary(), box.X+(box.W-int32(gw))/2, box.Y+(box.H-int32(gh))/2)
}

// ── The panel ────────────────────────────────────────────────────────────

// mlListRow is one drawn row of the PC list or the settings page.
type mlListRow struct {
	title   string
	detail  string
	value   string
	valueOn sdl.Color
	danger  bool
}

func mlPanelTitle() string {
	label := "PC"
	if v, ok := mlCurrentHost(); ok {
		label = v.host.Label()
	}
	switch mlPanel.page {
	case mlPagePair:
		return "Pair " + label
	case mlPageApps:
		return label
	case mlPageSettings:
		return label + " · Settings"
	}
	return "PC streaming"
}

// mlStreamsWith is what Play streams with here, for the subtitles.
func mlStreamsWith() string {
	switch {
	case mlOnHandheld() && !mlPanel.helperReady:
		return "the stream helper installs on first Play"
	case mlOnHandheld():
		return ""
	case mlPanel.desktopName != "":
		return "streams in " + mlPanel.desktopName
	}
	return "needs Moonlight PC to play"
}

func mlPanelSubtitle() string {
	var parts []string
	v, hasHost := mlCurrentHost()
	switch mlPanel.page {
	case mlPageHosts:
		parts = append(parts, "Vibepollo · Apollo · Sunshine")
		if n := len(mlPanel.hosts); n > 0 {
			parts = append(parts, pluralizeCount(n, "PC", "PCs"))
		}
		if mlPanel.discovering || mlPanel.probing > 0 {
			parts = append(parts, "searching…")
		}
		if s := mlStreamsWith(); s != "" {
			parts = append(parts, s)
		}
	case mlPagePair:
		parts = append(parts, "Once per PC")
		if hasHost {
			if a := mlHostAddr(*v); a != "" {
				parts = append(parts, a)
			}
		}
	case mlPageApps, mlPageSettings:
		if hasHost {
			if v.host.ServerKind != "" {
				parts = append(parts, v.host.ServerKind)
			}
			if a := mlHostAddr(*v); a != "" {
				parts = append(parts, a)
			}
			if mlPanel.page == mlPageApps {
				if n := len(mlPanel.apps); n > 0 {
					parts = append(parts, pluralizeCount(n, "game", "games"))
				}
				if v.online && v.info.CurrentGame != 0 {
					parts = append(parts, "playing "+mlRunningName(*v))
				}
				if !mlOnHandheld() {
					if mlPanel.desktopName != "" {
						// Moonlight PC keeps its own pairing.
						parts = append(parts, "Streams in "+mlPanel.desktopName+" (pair it there once too)")
					} else {
						parts = append(parts, "needs Moonlight PC to play")
					}
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}

// renderMoonlightPanel draws the panel's page, the card over it and the
// footer.
func renderMoonlightPanel(renderer *sdl.Renderer, config *Config) {
	if !mlPanel.open || renderer == nil {
		return
	}
	mlPanelTick()
	if _, ok := mlCurrentHost(); !ok && mlPanel.page != mlPageHosts {
		// The PC the page was about is gone (forgotten elsewhere).
		mlPanel.page = mlPageHosts
		mlPanel.confirm = mlConfirmNone
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	const padX = OverlayPadX
	rowH := 8 + medH + 4 + smallH + 8

	header := UIPanelHeader(mlPanelTitle(), mlPanelSubtitle(), "devices")
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := 20 + smallH
	panelW := min32(980, screenWidth-48)
	maxBody := screenHeight - 24 - headerH - footerH
	if maxBody < 3*rowH {
		maxBody = 3 * rowH
	}

	var hostRows, setRows []mlListRow
	var bodyH int32
	switch mlPanel.page {
	case mlPageHosts:
		hostRows = mlHostListRows()
		mlPanel.cursor = min(max(mlPanel.cursor, 0), len(hostRows)-1)
		bodyH = min32(int32(len(hostRows))*rowH, maxBody/rowH*rowH) + 4
	case mlPageSettings:
		setRows = mlSettingsListRows()
		mlPanel.setCursor = min(max(mlPanel.setCursor, 0), len(setRows)-1)
		bodyH = min32(int32(len(setRows))*rowH, maxBody/rowH*rowH) + 4
	case mlPagePair:
		bodyH = min32(maxBody, max32(360, 7*medH))
	default:
		bodyH = maxBody
	}
	panelH := headerH + bodyH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	mlPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, mlPanelRect)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, mlPanelRect)
	UIPageHeaderDraw(renderer, config, header)

	mlHits = mlHits[:0]
	body := sdl.Rect{X: panelX + padX, Y: panelY + headerH, W: panelW - 2*padX, H: bodyH}
	switch mlPanel.page {
	case mlPageHosts:
		mlPanel.scroll = mlDrawList(renderer, config, font, medium, body, hostRows, mlPanel.cursor, mlPanel.scroll, rowH)
	case mlPageSettings:
		mlPanel.setScroll = mlDrawList(renderer, config, font, medium, body, setRows, mlPanel.setCursor, mlPanel.setScroll, rowH)
	case mlPagePair:
		mlDrawPairPage(renderer, config, font, medium, body)
	case mlPageApps:
		mlDrawAppsPage(renderer, config, font, medium, body)
	}

	switch {
	case mlPanel.starting != "":
		mlDrawStartingCard(renderer, config, font, medium)
	case mlPanel.notice != mlNoticeNone:
		mlDrawNoticeCard(renderer, config, font, medium)
	case mlPanel.confirm != mlConfirmNone:
		mlDrawConfirmCard(renderer, config, font, medium)
	}

	hint := mlFooterHint()
	renderText(renderer, config, font, ellipsize(font, hint, panelW-2*padX), ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}

// mlDrawList draws rows into body from scroll (kept so the cursor shows) and
// returns the scroll it used.
func mlDrawList(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, body sdl.Rect, rows []mlListRow, cursor, scroll int, rowH int32) int {
	visible := max(1, int(body.H/rowH))
	mlVisibleRows = visible
	if cursor < scroll {
		scroll = cursor
	}
	if cursor >= scroll+visible {
		scroll = cursor - visible + 1
	}
	scroll = max(0, min(scroll, len(rows)-visible))
	medH := int32(medium.Height())
	smallH := int32(font.Height())
	focusColor := installHighlight()
	modal := mlPanelModal()
	for i := 0; i < visible && scroll+i < len(rows); i++ {
		idx := scroll + i
		row := rows[idx]
		r := sdl.Rect{X: body.X, Y: body.Y + int32(i)*rowH, W: body.W, H: rowH - 8}
		mlHits = append(mlHits, mlHit{kind: mlHitRow, index: idx, rect: r})
		focused := idx == cursor && !modal
		bg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, bg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}
		valueW := int32(0)
		if row.value != "" {
			value := ellipsize(font, row.value, r.W/3)
			vw, _ := measureText(font, value)
			valueW = vw + 12
			renderText(renderer, config, font, value, row.valueOn, r.X+r.W-16-vw, r.Y+8+(medH-smallH)/2)
		}
		titleInk := ColorTextPrimary()
		if row.danger {
			titleInk = DangerInk()
		}
		renderText(renderer, config, medium, ellipsize(medium, row.title, r.W-32-valueW), titleInk, r.X+16, r.Y+8)
		if row.detail != "" {
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
	}
	return scroll
}

// mlStatusInk colours a PC row's status.
func mlStatusInk(status string) sdl.Color {
	switch status {
	case "Paired", "Playing", "Checked":
		return ToastSuccessInk()
	case "Not paired", "Add", "Search", "Check":
		return installHighlight()
	case "Asleep", "Waking…", "Unreachable", "Problem":
		return WarningInk()
	case "Info":
		switch mlPanel.lastKind {
		case ToastKindError:
			return DangerInk()
		case ToastKindWarning:
			return WarningInk()
		case ToastKindSuccess:
			return ToastSuccessInk()
		}
	}
	return ColorTextTertiary()
}

// mlHostListRows is the PC list as drawn.
func mlHostListRows() []mlListRow {
	rows := mlHostRows()
	out := make([]mlListRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, mlListRow{title: r.title, detail: r.detail, value: r.status, valueOn: mlStatusInk(r.status)})
	}
	return out
}

func mlOnOff(on bool) (string, sdl.Color) {
	if on {
		return "On", ToastSuccessInk()
	}
	return "Off", ColorTextTertiary()
}

// mlMbps is a bitrate in kbps as "10 Mbps" or "7.5 Mbps".
func mlMbps(kbps int) string {
	return strconv.FormatFloat(float64(kbps)/1000, 'f', -1, 64) + " Mbps"
}

// mlSettingsListRows is the settings page as drawn.
func mlSettingsListRows() []mlListRow {
	v, ok := mlCurrentHost()
	if !ok {
		return nil
	}
	s := v.host.Settings
	w, h, fps, kbps := mlStreamSize(s)
	step := func(text string) string { return "‹ " + text + " ›" }
	plain := ColorTextSecondary()
	var out []mlListRow
	for _, row := range mlSettingsRows() {
		r := mlListRow{valueOn: plain}
		switch row {
		case mlSetQuality:
			r.title = "Quality"
			r.value = step(mlPresetLabel(mlNormalizePreset(s.Preset)))
			r.detail = strconv.Itoa(w) + "×" + strconv.Itoa(h) + " · " + strconv.Itoa(fps) + " fps · " + mlMbps(kbps) + " · lower it if the picture stutters"
			if strings.TrimSpace(s.Preset) == "" {
				// Never chosen: the handheld check's pick (Balanced until a
				// check has measured this handheld).
				rec := mlRecommendedPreset(int(screenWidth), int(screenHeight))
				why := " · picked by the handheld check (Check this handheld, on the PC list)"
				switch {
				case rec != "":
				case mlOnHandheld():
					rec, why = mlPresetBalanced, " · Balanced until Check this handheld (on the PC list) measures it"
				default:
					rec, why = mlPresetBalanced, " · lower it if the picture stutters"
				}
				r.value = step("Auto: " + mlPresetLabel(rec))
				r.detail = strconv.Itoa(w) + "×" + strconv.Itoa(h) + " · " + strconv.Itoa(fps) + " fps · " + mlMbps(kbps) + why
			}
		case mlSetWidth:
			r.title, r.value, r.detail = "Width", step(strconv.Itoa(w)), "Stream width in pixels"
		case mlSetHeight:
			r.title, r.value, r.detail = "Height", step(strconv.Itoa(h)), "Stream height in pixels"
		case mlSetFPS:
			r.title, r.value, r.detail = "Frame rate", step(strconv.Itoa(fps)+" fps"), "Frames per second the PC sends"
		case mlSetBitrate:
			r.title, r.value, r.detail = "Bitrate", step(mlMbps(kbps)), "More is sharper; Wi-Fi may not keep up"
		case mlSetCodec:
			r.title, r.value = "Video codec", step(mlCodecLabel(s.mlCodec()))
			switch s.mlCodec() {
			case "h265":
				r.detail = "Sharper at the same bitrate · harder for the handheld to decode"
			case "auto":
				r.detail = "The PC picks H.264 or H.265"
			default:
				r.detail = "Easiest for the handheld to decode"
			}
		case mlSetVirtual:
			r.title = "Virtual display for this handheld"
			r.value, r.valueOn = mlOnOff(s.UseVirtualDisplay())
			r.detail = "Apollo / Vibepollo: the PC makes a screen of exactly this handheld's size"
			if v.online && !v.host.VirtualDisplayCapable {
				r.detail = "Apollo / Vibepollo make a screen this size; plain Sunshine ignores it"
			}
		case mlSetPCAudio:
			r.title = "Play sound on the PC too"
			r.value, r.valueOn = mlOnOff(s.PlayAudioOnPC)
			r.detail = "Off: the sound plays only here"
		case mlSetQuitOnExit:
			r.title = "Quit the game when I leave"
			r.value, r.valueOn = mlOnOff(s.QuitOnExit)
			r.detail = "Off: the game keeps running on the PC, ready to resume"
		case mlSetRumble:
			r.title = "Rumble"
			r.value, r.valueOn = mlOnOff(!s.NoRumble)
			r.detail = "The handheld's motor follows the game"
		case mlSetVolume:
			vol := s.Volume
			if vol <= 0 {
				vol = 80
			}
			r.title, r.value, r.detail = "Stream volume", step(strconv.Itoa(vol)+"%"), "How loud the stream plays here"
		case mlSetAddress:
			r.title = "Address"
			if s := strings.TrimSpace(v.host.ManualAddr); s != "" {
				r.value = s
				r.detail = "Tried first · leave it blank to clear it"
			} else {
				r.value = "Automatic"
				r.detail = "Found on the network · type one for a VPN or the internet"
				if v.host.LocalAddr != "" {
					r.detail = "Found on the network at " + v.host.LocalAddr + " · type one for a VPN or the internet"
				}
			}
		case mlSetUnpair:
			r.title, r.danger = "Unpair and forget", true
			r.detail = "This handheld must pair again to play from this PC"
			if !v.host.Paired {
				r.title, r.detail = "Forget this PC", "A search may find it again"
			}
		}
		out = append(out, r)
	}
	return out
}

// ── Pairing page ─────────────────────────────────────────────────────────

func mlDrawPairPage(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, body sdl.Rect) {
	v, ok := mlCurrentHost()
	if !ok {
		return
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	// The phone code on the right, as large as the page allows.
	side := min32(body.H-24, body.W*2/5)
	qrW := int32(0)
	if n := int32(len(mlPanel.pairQR)); n > 0 && side >= 96 {
		tile := sdl.Rect{X: body.X + body.W - side, Y: body.Y + 12, W: side, H: side}
		if module := headerQRModule(side, n); module >= 1 {
			_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, max32(6, side/16), sdl.Color{R: 255, G: 255, B: 255, A: 255})
			code := module * n
			if rects := qrModuleRects(mlPanel.pairQR, tile.X+(side-code)/2, tile.Y+(side-code)/2, module); len(rects) > 0 {
				_ = renderer.SetDrawColor(0, 0, 0, 255)
				_ = renderer.FillRects(rects)
			}
			mlHits = append(mlHits, mlHit{kind: mlHitQR, rect: tile})
			qrW = side + 28
		}
	}

	// The PIN, one box per digit.
	leftW := body.W - qrW
	y := body.Y + 12
	big := getFontSized(config, "big", int(min32(120, max32(48, body.H/4))))
	if big == nil {
		big = medium
	}
	digits := []rune(mlPanel.pairPIN)
	if len(digits) > 0 {
		bigH := int32(big.Height())
		gap := int32(14)
		boxW := min32((leftW-gap*int32(len(digits)-1))/int32(len(digits)), bigH+12)
		boxH := bigH + 16
		x := body.X
		for _, d := range digits {
			fillRoundedRect(renderer, x, y, boxW, boxH, 14, WithAlpha(ColorSurfaceRow, 220))
			strokeRoundedRect(renderer, x, y, boxW, boxH, 14, 1, WithAlpha(installHighlight(), 120))
			ch := string(d)
			cw, ch2 := measureText(big, ch)
			renderText(renderer, config, big, ch, ColorTextPrimary(), x+(boxW-cw)/2, y+(boxH-ch2)/2)
			x += boxW + gap
		}
		y += boxH + 16
	}

	// What to do with it.
	lines := wrapTextLines(medium, "On your PC open the Vibepollo / Sunshine web page (PIN tab), type this PIN and a name for this handheld", leftW, 3)
	for _, line := range lines {
		renderText(renderer, config, medium, line, ColorTextPrimary(), body.X, y)
		y += medH + 4
	}
	y += 6
	if qrW > 0 {
		for _, line := range wrapTextLines(font, "Or scan the code with your phone: it opens that page. A certificate warning there is expected.", leftW, 2) {
			renderText(renderer, config, font, line, ColorTextTertiary(), body.X, y)
			y += smallH + 4
		}
		y += 6
	}
	if y+smallH > body.Y+body.H {
		return
	}
	switch {
	case mlPanel.pairErr != "":
		renderText(renderer, config, font, ellipsize(font, "Pairing failed — "+mlPanel.pairErr, leftW), WarningInk(), body.X, y)
		y += smallH + 6
		if y+smallH <= body.Y+body.H {
			renderText(renderer, config, font, ellipsize(font, hintSentence("Press %s to try again with a new PIN", "A", "Try again"), leftW), ColorTextSecondary(), body.X, y)
		}
	case mlPanel.pairBusy:
		r := smallH / 2
		renderSpinner(renderer, body.X+r, y+smallH/2, r, installHighlight())
		renderText(renderer, config, font, ellipsize(font, "Waiting for the PIN on "+v.host.Label()+"…", leftW-3*r), ColorTextSecondary(), body.X+3*r, y)
	}
}

// ── Games page ───────────────────────────────────────────────────────────

func mlDrawAppsPage(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, body sdl.Rect) {
	v, ok := mlCurrentHost()
	if !ok {
		return
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	apps := mlPanel.apps
	message := func(headline string, lines ...string) {
		y := body.Y + 18
		renderText(renderer, config, medium, ellipsize(medium, headline, body.W), ColorTextPrimary(), body.X, y)
		y += medH + 6
		for _, line := range lines {
			if line == "" {
				continue
			}
			renderText(renderer, config, font, ellipsize(font, line, body.W), ColorTextTertiary(), body.X, y)
			y += smallH + 4
		}
	}
	switch {
	case len(apps) == 0 && mlPanel.appsBusy:
		r := smallH / 2
		renderSpinner(renderer, body.X+r, body.Y+18+medH/2, r, installHighlight())
		renderText(renderer, config, medium, ellipsize(medium, "Reading "+v.host.Label()+"'s games…", body.W-3*r), ColorTextPrimary(), body.X+3*r, body.Y+18)
		return
	case len(apps) == 0 && mlPanel.appsErr != "":
		wake := "" // a PC whose MAC is not known yet cannot be woken
		if v.host.MAC != "" {
			wake = hintSentence("Asleep? On the PC list %s wakes it", "X", "Wake")
		}
		message("Could not read the games", mlPanel.appsErr,
			hintSentence("Press %s to try again", "A", "Try again"), wake)
		return
	case len(apps) == 0:
		message("No games on "+v.host.Label()+" yet",
			"Add them in its web page (Applications), or let Vibepollo bring in your Playnite library.")
		return
	}

	gap := int32(16)
	titleH := smallH + 10
	minTile := max32(120, screenWidth/8)
	cols := max32(1, (body.W+gap)/(minTile+gap))
	tileW := (body.W - (cols-1)*gap) / cols
	artH := tileW * 4 / 3
	// Two rows on screen at least: a tile that would not fit twice shrinks.
	if 2*(artH+titleH+gap) > body.H+gap {
		artH = max32(60, (body.H+gap)/2-titleH-gap)
		tileW = min32(tileW, artH*3/4)
		cols = max32(1, (body.W+gap)/(tileW+gap))
	}
	cellH := max32(1, artH+titleH+gap)
	rowsVisible := max(1, int((body.H+gap)/cellH))
	mlPanel.appCols = int(cols)
	mlVisibleRows = rowsVisible
	gridW := cols*tileW + (cols-1)*gap
	left := body.X + (body.W-gridW)/2

	n := len(apps)
	mlPanel.appCursor = min(max(mlPanel.appCursor, 0), n-1)
	row := mlPanel.appCursor / int(cols)
	if row < mlPanel.appScroll {
		mlPanel.appScroll = row
	}
	if row >= mlPanel.appScroll+rowsVisible {
		mlPanel.appScroll = row - rowsVisible + 1
	}
	totalRows := (n + int(cols) - 1) / int(cols)
	mlPanel.appScroll = max(0, min(mlPanel.appScroll, totalRows-rowsVisible))

	focus := installHighlight()
	modal := mlPanelModal()
	running := 0
	if v.online {
		running = v.info.CurrentGame
	}
	first := mlPanel.appScroll * int(cols)
	last := min(n, first+rowsVisible*int(cols))
	for idx := first; idx < last; idx++ {
		app := apps[idx]
		k := idx - first
		cx := left + int32(k%int(cols))*(tileW+gap)
		cy := body.Y + 4 + int32(k/int(cols))*cellH
		art := sdl.Rect{X: cx, Y: cy, W: tileW, H: artH}
		hit := sdl.Rect{X: cx, Y: cy, W: tileW, H: artH + titleH}
		mlHits = append(mlHits, mlHit{kind: mlHitTile, index: idx, rect: hit})
		focused := idx == mlPanel.appCursor && !modal
		if focused {
			fillRoundedRect(renderer, art.X-6, art.Y-6, art.W+12, art.H+12, 14, WithAlpha(focus, 46))
		}
		mlDrawCover(renderer, config, art, app, medium)
		if focused {
			strokeRoundedRect(renderer, art.X-4, art.Y-4, art.W+8, art.H+8, 12, 3, focus)
		}
		if running != 0 && app.ID == running {
			label := "Running"
			lw, _ := measureText(font, label)
			dot := max32(3, smallH/6)
			badge := sdl.Rect{X: art.X + 6, Y: art.Y + 6, W: lw + 20 + 2*dot, H: smallH + 8}
			fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, WithAlpha(ColorSurfaceRow, 235))
			fillCircle(renderer, badge.X+10+dot, badge.Y+badge.H/2, dot, sdl.Color{R: 52, G: 211, B: 153, A: 255})
			renderText(renderer, config, font, label, ToastSuccessInk(), badge.X+14+2*dot, badge.Y+4)
		}
		title := ellipsize(font, app.Title, tileW)
		tw, _ := measureText(font, title)
		ink := ColorTextSecondary()
		if focused {
			ink = ColorTextPrimary()
		}
		renderText(renderer, config, font, title, ink, cx+(tileW-tw)/2, art.Y+art.H+6)
	}
	if mlPanel.appsErr != "" {
		// A failed reload keeps the games it had; say why below them.
		renderText(renderer, config, font, ellipsize(font, mlPanel.appsErr, body.W), WarningInk(), body.X, body.Y+body.H-smallH)
	}
}

// ── Cards ────────────────────────────────────────────────────────────────

// mlDrawCard draws a card over the panel: a title, wrapped lines, an
// optional spinner line and up to two buttons (A and B).
func mlDrawCard(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, title string, lines []string, busy, warn, primary, secondary string) {
	card := mlPanelRect
	if card.W <= 0 {
		return
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	w := min32(card.W-80, 640)
	textW := w - 48
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, wrapTextLines(font, line, textW, 4)...)
	}
	titleLines := wrapTextLines(medium, title, textW, 2)
	h := int32(24) + int32(len(titleLines))*(medH+4) + 8 + int32(len(wrapped))*(smallH+4)
	if busy != "" {
		h += smallH + 14
	}
	if warn != "" {
		h += smallH + 10
	}
	btnH := smallH + 16
	if primary != "" || secondary != "" {
		h += 16 + btnH
	}
	h += 22
	box := sdl.Rect{X: card.X + (card.W-w)/2, Y: card.Y + (card.H-h)/2, W: w, H: h}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, 18, sdl.Color{R: 0, G: 0, B: 0, A: 120})
	drawOverlayCard(renderer, box)
	y := box.Y + 24
	for _, line := range titleLines {
		renderText(renderer, config, medium, line, ColorTextPrimary(), box.X+24, y)
		y += medH + 4
	}
	y += 8
	for _, line := range wrapped {
		renderText(renderer, config, font, line, ColorTextSecondary(), box.X+24, y)
		y += smallH + 4
	}
	if busy != "" {
		y += 10
		r := smallH / 2
		renderSpinner(renderer, box.X+24+r, y+smallH/2, r, installHighlight())
		renderText(renderer, config, font, ellipsize(font, busy, textW-3*r), ColorTextSecondary(), box.X+24+3*r, y)
		y += smallH + 4
	}
	if warn != "" {
		y += 6
		renderText(renderer, config, font, ellipsize(font, warn, textW), WarningInk(), box.X+24, y)
		y += smallH + 4
	}
	if primary == "" && secondary == "" {
		return
	}
	y += 16
	x := box.X + box.W - 24
	button := func(key, label string, kind mlHitKind, filled bool) {
		if label == "" {
			return
		}
		text := hintKeyFor(key, label) + "  " + label
		tw, _ := measureText(font, text)
		r := sdl.Rect{X: x - tw - 32, Y: y, W: tw + 32, H: btnH}
		bg := WithAlpha(ColorSurfaceRow, 220)
		if filled {
			bg = WithAlpha(installHighlight(), 70)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, bg)
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, WithAlpha(installHighlight(), 140))
		renderText(renderer, config, font, text, ColorTextPrimary(), r.X+16, r.Y+8)
		mlHits = append(mlHits, mlHit{kind: kind, rect: r})
		x = r.X - 12
	}
	// Right to left: A is the far-right button.
	button("A", primary, mlHitPrimary, true)
	button("B", secondary, mlHitSecondary, false)
}

func mlDrawStartingCard(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font) {
	if mlPanel.startCheck {
		lines := []string{
			"JukaHub closes for about 20 s while the streaming helper tests the picture, sound, rumble and decoding speed.",
			hintSentence("Press %s when the check asks; JukaHub then comes back with the result.", "A", "Finish"),
		}
		mlDrawCard(renderer, config, font, medium, mlPanel.starting, lines, "Getting the check ready…", "", "", "Cancel")
		return
	}
	line := "Moonlight PC opens with the stream."
	if mlOnHandheld() {
		line = "JukaHub closes while the stream runs and comes back when it ends."
	}
	mlDrawCard(renderer, config, font, medium, mlPanel.starting, []string{line}, "Asking the PC to start…", "", "", "Cancel")
}

func mlDrawNoticeCard(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font) {
	switch mlPanel.notice {
	case mlNoticeHelper:
		busy, primary := "", "Install"
		if mlPanel.helperBusy {
			busy, primary = "Installing the streaming helper…", ""
		} else if mlPanel.helperMsg != "" {
			primary = "Try again"
		}
		mlDrawCard(renderer, config, font, medium, "The streaming helper is not installed yet",
			[]string{"moonlight-stream plays the PC's picture and sound on this handheld. It comes with this release's Remote Play package: JukaHub downloads it once (Wi-Fi needed)."},
			busy, mlPanel.helperMsg, primary, "Back")
	case mlNoticeDesktop:
		mlDrawCard(renderer, config, font, medium, "Install Moonlight PC",
			[]string{"Moonlight PC plays the stream on this computer — Nonary's VRR build pairs best with Vibepollo.",
				"It installs over the usual Moonlight. Pair it with your PC once in its own window too."},
			"", "", "Open download page", "Back")
	}
}

func mlDrawConfirmCard(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font) {
	v, ok := mlCurrentHost()
	if !ok {
		mlPanel.confirm = mlConfirmNone
		return
	}
	switch mlPanel.confirm {
	case mlConfirmQuit:
		mlDrawCard(renderer, config, font, medium, "Quit "+mlRunningName(*v)+" on "+v.host.Label()+"?",
			[]string{"Progress the game has not saved is lost."}, "", "", "Quit game", "Keep it running")
	case mlConfirmUnpair:
		if !v.host.Paired {
			mlDrawCard(renderer, config, font, medium, "Forget "+v.host.Label()+"?",
				[]string{"JukaHub forgets this PC and its settings. A search may find it again."},
				"", "", "Forget", "Cancel")
			return
		}
		mlDrawCard(renderer, config, font, medium, "Unpair "+v.host.Label()+"?",
			[]string{"JukaHub forgets this PC and its settings, and asks it to forget this handheld. Playing from it again takes a new PIN."},
			"", "", "Unpair", "Cancel")
	}
}

// ── Footer ───────────────────────────────────────────────────────────────

func mlFooterHint() string {
	switch {
	case mlPanel.starting != "":
		return hintLine(FooterHint{"B", "Cancel"})
	case mlPanel.notice == mlNoticeHelper && mlPanel.helperBusy:
		return hintLine(FooterHint{"B", "Back"})
	case mlPanel.notice == mlNoticeHelper:
		label := "Install"
		if mlPanel.helperMsg != "" {
			label = "Try again"
		}
		return hintLine(FooterHint{"A", label}, FooterHint{"B", "Back"})
	case mlPanel.notice == mlNoticeDesktop:
		return hintLine(FooterHint{"A", "Open download page"}, FooterHint{"B", "Back"})
	case mlPanel.confirm == mlConfirmQuit:
		return hintLine(FooterHint{"A", "Quit game"}, FooterHint{"B", "Keep it running"})
	case mlPanel.confirm == mlConfirmUnpair:
		label := "Unpair"
		if v, ok := mlCurrentHost(); ok && !v.host.Paired {
			label = "Forget"
		}
		return hintLine(FooterHint{"A", label}, FooterHint{"B", "Cancel"})
	}
	refresh := FooterHint{"Start / F5", "Search again"}
	switch mlPanel.page {
	case mlPagePair:
		if mlPanel.pairErr != "" || !mlPanel.pairBusy {
			return hintLine(FooterHint{"A", "Try again"}, FooterHint{"B", "Back"})
		}
		if len(mlPanel.pairQRFull) > 0 {
			return hintLine(FooterHint{"A", "Enlarge code"}, FooterHint{"B", "Cancel"})
		}
		return hintLine(FooterHint{"B", "Cancel"})
	case mlPageApps:
		if len(mlPanel.apps) == 0 {
			if mlPanel.appsErr != "" && !mlPanel.appsBusy {
				return hintLine(FooterHint{"A", "Try again"}, FooterHint{"Y", "Settings"}, FooterHint{"B", "PCs"})
			}
			return hintLine(FooterHint{"Y", "Settings"}, FooterHint{"B", "PCs"})
		}
		hints := []FooterHint{{"A", "Play"}}
		v, ok := mlCurrentHost()
		if app, has := mlFocusedApp(); ok && has && v.online && v.info.CurrentGame == app.ID {
			hints[0] = FooterHint{"A", "Resume"}
		}
		if ok && v.online && v.info.CurrentGame != 0 {
			hints = append(hints, FooterHint{"X", "Quit game"})
		}
		hints = append(hints, FooterHint{"Y", "Settings"})
		if len(mlPairedIndexes()) > 1 {
			shoulders := "L1 / R1"
			if hintsUseKeyboard() {
				shoulders = "Tab"
			}
			hints = append(hints, FooterHint{shoulders, "Other PC"})
		}
		hints = append(hints, FooterHint{"Start / F5", "Reload"}, FooterHint{"B", "PCs"})
		return hintLine(hints...)
	case mlPageSettings:
		return hintLine(FooterHint{"Left / Right", "Change"}, FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
	}
	if v, ok := mlFocusedHost(); ok {
		open := "Pair"
		if v.host.Paired {
			open = "Games"
		}
		hints := []FooterHint{{"A", open}}
		if !v.online && v.host.MAC != "" {
			// No MAC learned yet: Wake cannot work, so it is not offered.
			hints = append(hints, FooterHint{"X", "Wake"})
		}
		hints = append(hints, FooterHint{"Y", "Settings"}, refresh, FooterHint{"B", "Close"})
		return hintLine(hints...)
	}
	if _, kind := mlCursorOn(); kind == mlRowCheck {
		return hintLine(FooterHint{"A", "Check"}, refresh, FooterHint{"B", "Close"})
	}
	return hintLine(FooterHint{"A", "Select"}, refresh, FooterHint{"B", "Close"})
}
