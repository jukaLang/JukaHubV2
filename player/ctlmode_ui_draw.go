package main

// ctlmode_ui_draw.go — drawing Controller Mode's page (ctlmode_ui.go): the
// link list, a running link's card, the layout editor, the transport's
// question and the live controller.
//
// Everything is drawn with the Tools pages' frame and rows (tbxui.go) and
// the shared shapes; text goes through the shared texture cache, and the live
// controller draws fixed labels only (no per-frame numbers), so a frame
// creates no textures once each label has been seen lit and unlit. While
// forwarding, a flat fill stands in for Home's backdrop, and while the panel
// is dark for the session the page fills black and draws nothing else.

import (
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ctlUIRender draws the page's current face.
func ctlUIRender(renderer *sdl.Renderer, config *Config) {
	if ctlModeActive() && ctlModeScreenDark() {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	if ctlModeActive() {
		ctlUIDrawLiveFrame(renderer, config, fonts)
		return
	}
	frame := tbxBeginFrame(renderer, config, ctlUIHeader())
	body := frame.bodyRect()
	nav := &ctlUI.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	col := ctlUICol{renderer: renderer, config: config, fonts: fonts, nav: nav,
		x: frame.x, w: frame.w, top: body.Y - nav.scroll}
	switch {
	case ctlUI.editing:
		ctlUIDrawEditor(&col)
	case ctlModeRunning() != nil:
		ctlUIDrawLink(&col, ctlModeRunning())
	default:
		ctlUIDrawLinks(&col)
	}
	nav.extend(col.y)
	tbxUnclip(renderer)
	nav.end()
	if ctlUI.reveal {
		ctlUI.reveal = false
		nav.reveal()
	}
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
	ctlUI.promptYes, ctlUI.promptNo = sdl.Rect{}, sdl.Rect{}
	if p := ctlUIPrompt(); p != nil {
		ctlUIDrawPrompt(renderer, config, fonts, p)
	}
}

// ctlUIHeader is the page bar: the title, what this face is about and, while
// forwarding, the layout in use.
func ctlUIHeader() UIPageHeader {
	sub, status := "", ""
	t := ctlModeRunning()
	switch {
	case ctlModeActive() && t != nil:
		peer := t.Status().Peer
		if peer == "" {
			peer = "the receiver"
		}
		sub = "Connected to " + peer + " · " + t.Title()
		status = ctlModeLayoutNow().Name
	case ctlUI.editing:
		sub = "Layout — how the PC sees this handheld's controls"
	case t != nil:
		sub = t.Title()
	case P().IsHandheld():
		sub = "Use this handheld as a game controller for a PC or phone"
	default:
		sub = "Send this computer's pad to another PC — that PC runs the JukaPad receiver"
	}
	if ctlUI.header.Title == "" || sub != ctlUI.headerSub || status != ctlUI.headerStatus {
		ctlUI.headerSub, ctlUI.headerStatus = sub, status
		ctlUI.header = tbxHeader("Controller Mode", sub, "gamepad", status, sdl.Color{})
	}
	return ctlUI.header
}

// ctlUIIcon is a transport's glyph.
func ctlUIIcon(id string) string {
	switch id {
	case "wifi":
		return "wifi"
	case "dsu":
		return "emulator"
	case "bt":
		return "bluetooth"
	}
	return "gamepad"
}

// ── The column ───────────────────────────────────────────────────────────

// ctlUICol lays the scrolled body out top to bottom: y is in body
// coordinates (0 is the top of the viewport before scrolling), top is where
// that is on screen, row counts the focus list's rows.
type ctlUICol struct {
	renderer *sdl.Renderer
	config   *Config
	fonts    tbxFonts
	nav      *tbxNav
	x, w     int32
	top      int32
	y        int32
	row      int
}

// section draws a group heading.
func (c *ctlUICol) section(title, icon string) {
	if c.y > 0 {
		c.y += 8
	}
	tbxDrawSection(c.renderer, c.config, title, icon, c.x, c.top+c.y, c.w)
	c.y += tbxSectionHeight
}

// item draws one focusable row, indent pixels in from the column's edge.
func (c *ctlUICol) item(id string, spec tbxRowSpec, indent int32) {
	h := tbxRowHeight(c.fonts)
	if indent > 0 {
		// A help row under its link is a step smaller.
		h = c.fonts.titleH + c.fonts.smallH + 22
	}
	rect := sdl.Rect{X: c.x + indent, Y: c.y, W: c.w - indent, H: h}
	screen := rect
	screen.Y += c.top
	tbxDrawRow(c.renderer, c.config, c.fonts, screen, spec, c.nav.focused(id))
	c.nav.add(id, rect, c.row, false)
	c.row++
	c.y += h + 8
}

// toggle draws an on/off row.
func (c *ctlUICol) toggle(id, title, sub string, on bool) {
	h := tbxRowHeight(c.fonts)
	rect := sdl.Rect{X: c.x, Y: c.y, W: c.w, H: h}
	screen := rect
	screen.Y += c.top
	bugrepDrawToggleRow(c.renderer, c.config, c.fonts, screen, title, sub, on, c.nav.focused(id))
	c.nav.add(id, rect, c.row, false)
	c.row++
	c.y += h + 8
}

// ctlUIButton is one button of a row of buttons.
type ctlUIButton struct {
	id, label string
	primary   bool
}

// buttons draws a row of buttons, left to right.
func (c *ctlUICol) buttons(list ...ctlUIButton) {
	if len(list) == 0 {
		return
	}
	bx := c.x
	for _, b := range list {
		bw := tbxButtonWidth(c.fonts.body, b.label, 150)
		rect := sdl.Rect{X: bx, Y: c.y, W: bw, H: 48}
		screen := rect
		screen.Y += c.top
		tbxDrawButton(c.renderer, c.config, c.fonts.body, screen, b.label, b.primary, c.nav.focused(b.id), false)
		c.nav.add(b.id, rect, c.row, false)
		bx += bw + 12
	}
	c.row++
	c.y += 48 + 18
}

// note draws a card that is read, not focused: a glyph, a title and a
// wrapped paragraph.
func (c *ctlUICol) note(icon, title, body string) {
	badge := int32(44)
	textX := c.x + 20 + badge + 16
	textW := c.x + c.w - 20 - textX
	lines := ctlUI.noteWrap.get(c.fonts.small, body, textW)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	block := c.fonts.titleH + 6 + int32(len(lines))*(c.fonts.smallH+2)
	h := block + 32
	if h < 84 {
		h = 84
	}
	card := sdl.Rect{X: c.x, Y: c.top + c.y, W: c.w, H: h}
	UICard(c.renderer, card, DefaultUICardStyle())
	box := drawGlassBadge(c.renderer, sdl.Rect{X: card.X + 20, Y: card.Y + (h-badge)/2, W: badge, H: badge})
	DrawHomeIcon(c.renderer, icon, box, false)
	ty := card.Y + (h-block)/2
	renderText(c.renderer, c.config, c.fonts.title, ellipsize(c.fonts.title, title, textW), ColorTextPrimary(), textX, ty)
	ty += c.fonts.titleH + 6
	for _, line := range lines {
		renderText(c.renderer, c.config, c.fonts.small, line, ColorTextMuted(), textX, ty)
		ty += c.fonts.smallH + 2
	}
	c.y += h + 16
}

// ── Faces ────────────────────────────────────────────────────────────────

// ctlUIDrawLinks is the page with nothing running: the links, the installed
// pad apps and the settings.
func ctlUIDrawLinks(c *ctlUICol) {
	links := ctlModeTransportList()
	if len(links) == 0 {
		body := "No controller links are part of this build. The PC that receives the pad runs the JukaPad receiver."
		if !P().IsHandheld() {
			body = "No controller links are part of this build. On a PC, JukaHub sends the pad plugged into this computer; the PC that receives it runs the JukaPad receiver."
		}
		c.note("gamepad", "Nothing to connect with yet", body)
	} else {
		c.section("Connect to", "gamepad")
		last := ctlLastTransport()
		for _, t := range links {
			id := t.ID()
			spec := tbxRowSpec{icon: ctlUIIcon(id), title: t.Title(), subtitle: t.Subtitle(), pill: "Ready", tone: tbxToneGood}
			if ok, why := t.Available(); !ok {
				spec.pill, spec.tone = "Unavailable", tbxToneWarn
				if why != "" {
					spec.subtitle = why
				}
			}
			if id == last && len(links) > 1 {
				spec.note = "Last used"
			}
			c.item("t:"+id, spec, 0)
			for i, r := range t.Rows() {
				c.item("h:"+id+":"+strconv.Itoa(i), ctlUIRowSpec(r), 28)
			}
		}
		if !P().IsHandheld() {
			c.note("info", "Using a PC as the controller",
				"JukaHub sends the pad plugged into this computer — an Xbox pad works. The PC that receives it runs the JukaPad receiver.")
		}
	}
	pcApp, hasPC := extAppFind(extAppGamepadPC)
	btApp, hasBT := extAppFind(extAppGamepadBT)
	if hasPC || hasBT {
		c.section("Installed apps", "apps")
		if hasPC {
			c.item("app:pc", tbxRowSpec{icon: "devices", title: pcApp.Name,
				subtitle: "Needs its own receiver on the PC · JukaHub comes back when the app closes", pill: "Installed", tone: tbxToneNeutral}, 0)
		}
		if hasBT {
			c.item("app:bt", tbxRowSpec{icon: "bluetooth", title: btApp.Name,
				subtitle: "Pairs as a Bluetooth pad · JukaHub comes back when the app closes", pill: "Installed", tone: tbxToneNeutral}, 0)
		}
	}
	ctlUIDrawSettings(c)
}

// ctlUIRowSpec is a transport's row as a list row.
func ctlUIRowSpec(r ctlRow) tbxRowSpec {
	spec := tbxRowSpec{icon: "link", title: r.Label, subtitle: r.Detail}
	if r.Action == "" {
		spec.icon = "info"
	}
	return spec
}

// ctlUIDrawSettings is the layout and screen rows every list face ends with.
func ctlUIDrawSettings(c *ctlUICol) {
	c.section("Settings", "settings")
	l := ctlUILayout()
	c.item("layout", tbxRowSpec{icon: "gamepad", title: "Layout", subtitle: ctlUILayoutSummary(l), pill: l.Name, tone: tbxToneAccent}, 0)
	if !backlightSupported() {
		// Only the TrimUI's light can be switched (devpower.go): on a PC the
		// row would promise a dark screen that never comes.
		return
	}
	screen := tbxRowSpec{icon: "moon", title: "Screen while playing", pill: "Turns off",
		subtitle: hintSentence("Goes dark 15 s after the link starts, to save battery · %s lights it", "Menu", "Return to JukaHub")}
	if ctlScreenSetting() == "on" {
		screen = tbxRowSpec{icon: "sun", title: "Screen while playing", pill: "Stays on",
			subtitle: "The live controller stays lit (uses more battery)"}
	}
	c.item("screen", screen, 0)
}

// ctlUILayoutSummary is one line saying what a layout does, kept until the
// layout changes.
func ctlUILayoutSummary(l ctlLayout) string {
	if ctlUI.summary != "" && l == ctlUI.summaryOf {
		return ctlUI.summary
	}
	s := "Face buttons by position"
	if l.Face == ctlFaceLabel {
		s = "Face buttons by label"
	}
	if l.Dpad == ctlDpadStick {
		s += " · D-pad as left stick"
	}
	s += " · dead zone " + strconv.Itoa(l.Deadzone) + " %"
	if l.InvertRY {
		s += " · right stick Y inverted"
	}
	if l.MenuGuide {
		s += " · hold Menu for Guide"
	}
	ctlUI.summaryOf, ctlUI.summary = l, s
	return s
}

// ctlUIStateWords are a link's headline, pill and pill tone.
func ctlUIStateWords(st ctlLinkStatus) (string, string, tbxTone) {
	if ctlModePaused() {
		return "Paused — the pad is JukaHub's", "Paused", tbxToneAccent
	}
	switch st.State {
	case ctlLinkWaiting:
		return "Waiting for a PC or phone", "Waiting", tbxToneAccent
	case ctlLinkPairing:
		return "Confirm the pairing code", "Pairing", tbxToneWarn
	case ctlLinkConnected:
		peer := st.Peer
		if peer == "" {
			peer = "the receiver"
		}
		return "Connected to " + peer, "Connected", tbxToneGood
	case ctlLinkError:
		return "The link stopped", "Error", tbxToneBad
	}
	return "Starting…", "Starting", tbxToneNeutral
}

// ctlUIDrawLink is a running link's face: its card, its buttons, its rows
// and the settings.
func ctlUIDrawLink(c *ctlUICol, t ctlTransport) {
	st := t.Status()
	headline, pill, tone := ctlUIStateWords(st)
	detail := st.Detail
	if ctlModePaused() {
		detail = hintSentence2("%s resumes sending the pad · %s stops the link", "A", "Resume", "B", "Stop")
		if st.Peer != "" {
			detail = "Connected to " + st.Peer + ". " + detail
		}
	}
	badge := int32(56)
	pw, _ := tbxPillSize(c.fonts.tiny, pill)
	textX := c.x + 22 + badge + 18
	textW := c.x + c.w - 22 - pw - 16 - textX
	lines := ctlUI.wrap.get(c.fonts.small, detail, textW)
	if len(lines) > 3 {
		lines = lines[:3]
	}
	block := c.fonts.titleH + 6 + int32(len(lines))*(c.fonts.smallH+2)
	heroH := block + 36
	if heroH < 110 {
		heroH = 110
	}
	hero := sdl.Rect{X: c.x, Y: c.top + c.y, W: c.w, H: heroH}
	UICard(c.renderer, hero, DefaultUICardStyle())
	box := drawGlassBadge(c.renderer, sdl.Rect{X: hero.X + 22, Y: hero.Y + (heroH-badge)/2, W: badge, H: badge})
	DrawHomeIcon(c.renderer, ctlUIIcon(t.ID()), box, false)
	tbxDrawPill(c.renderer, c.config, c.fonts.tiny, hero.X+hero.W-22, hero.Y+heroH/2, pill, tone)
	ty := hero.Y + (heroH-block)/2
	renderText(c.renderer, c.config, c.fonts.title, ellipsize(c.fonts.title, headline, textW), ColorTextPrimary(), textX, ty)
	ty += c.fonts.titleH + 6
	for _, line := range lines {
		renderText(c.renderer, c.config, c.fonts.small, line, ColorTextMuted(), textX, ty)
		ty += c.fonts.smallH + 2
	}
	c.nav.extend(c.y + heroH)
	c.y += heroH + 16

	switch {
	case st.State == ctlLinkConnected:
		c.buttons(ctlUIButton{id: "resume", label: "Resume", primary: true}, ctlUIButton{id: "stop", label: "Stop"})
	case st.State == ctlLinkError:
		c.buttons(ctlUIButton{id: "retry", label: "Try again", primary: true}, ctlUIButton{id: "stop", label: "Stop"})
	default:
		c.buttons(ctlUIButton{id: "stop", label: "Stop"})
	}

	if rows := t.Rows(); len(rows) > 0 {
		c.section(t.Title(), ctlUIIcon(t.ID()))
		for i, r := range rows {
			c.item("r:"+strconv.Itoa(i), ctlUIRowSpec(r), 0)
		}
	}
	ctlUIDrawSettings(c)
}

// ctlUIDrawEditor is the layout editor over the draft.
func ctlUIDrawEditor(c *ctlUICol) {
	l := ctlUI.draft
	c.section("Layout", "gamepad")
	list := ctlModeLayoutList()
	at := 0
	for i, x := range list {
		if x.ID == l.ID {
			at = i + 1
			break
		}
	}
	c.item("ed:layout", tbxRowSpec{icon: "gamepad", title: "Layout in use",
		subtitle: "Left or Right picks another · " + strconv.Itoa(at) + " of " + strconv.Itoa(len(list)),
		pill:     l.Name, tone: tbxToneAccent}, 0)
	nameSub := hintSentence("%s renames it", "A", "Rename")
	if l.Builtin {
		nameSub = "Built-in · a change here is saved as a layout of your own"
	}
	c.item("ed:name", tbxRowSpec{icon: "edit", title: "Name", subtitle: nameSub, pill: l.Name, tone: tbxToneNeutral}, 0)

	c.section("Buttons", "gamepad")
	face := tbxRowSpec{icon: "gamepad", title: "Face buttons", pill: "By position", tone: tbxToneNeutral,
		subtitle: "The bottom button is A, as PC games expect"}
	if l.Face == ctlFaceLabel {
		face.pill, face.subtitle = "By label", "The button labelled A is A (Nintendo-style)"
	}
	c.item("ed:face", face, 0)
	dpad := tbxRowSpec{icon: "gamepad", title: "D-pad", pill: "D-pad", tone: tbxToneNeutral,
		subtitle: "The D-pad is the D-pad"}
	if l.Dpad == ctlDpadStick {
		dpad.pill, dpad.subtitle = "Left stick", "Moves the left stick, for games that ignore the D-pad"
	}
	c.item("ed:dpad", dpad, 0)
	c.toggle("ed:guide", "Hold Menu for Guide", "Off: Menu only ever returns to JukaHub", l.MenuGuide)

	c.section("Sticks", "gamepad")
	c.item("ed:dz", tbxRowSpec{icon: "gamepad", title: "Dead zone", pill: strconv.Itoa(l.Deadzone) + " %", tone: tbxToneNeutral,
		subtitle: "How far a stick moves before it counts (0–30 %)"}, 0)
	c.toggle("ed:invert", "Invert right stick Y", "For games that look the other way", l.InvertRY)

	c.y += 8
	if l.Builtin {
		c.buttons(ctlUIButton{id: "ed:new", label: "New layout from this one"}, ctlUIButton{id: "ed:done", label: "Done", primary: true})
	} else {
		c.buttons(ctlUIButton{id: "ed:new", label: "New layout from this one"}, ctlUIButton{id: "ed:delete", label: "Delete"},
			ctlUIButton{id: "ed:done", label: "Done", primary: true})
	}
}

// ── The question ─────────────────────────────────────────────────────────

// ctlUIDrawPrompt draws the transport's question over the page: its title,
// the code drawn big, the body and the two answers.
func ctlUIDrawPrompt(renderer *sdl.Renderer, config *Config, fonts tbxFonts, p *ctlPrompt) {
	cardW := int32(720)
	if cardW > screenWidth-64 {
		cardW = screenWidth - 64
	}
	if ctlUI.promptHeader.Title == "" || ctlUI.promptTitle != p.Title {
		ctlUI.promptTitle = p.Title
		title := p.Title
		if title == "" {
			title = "Controller Mode"
		}
		ctlUI.promptHeader = UIPanelHeader(title, "", "gamepad")
	}
	header := ctlUI.promptHeader
	headerH := UIPanelHeaderBand(config, header, 8)
	textW := cardW - 2*OverlayPadX
	lines := ctlUI.promptWrap.get(fonts.body, p.Body, textW)
	if len(lines) > 6 {
		lines = lines[:6]
	}
	codeFont := getFontSized(config, "big", 52)
	if codeFont == nil {
		codeFont = fonts.title
	}
	codeH := int32(0)
	if p.Code != "" {
		_, ch := measureText(codeFont, p.Code)
		codeH = ch + 20
	}
	lineH := fonts.bodyH + 5
	cardH := headerH + codeH + int32(len(lines))*lineH + 46 + 24 + 24
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	y := card.Y + headerH
	if p.Code != "" {
		cw, _ := measureText(codeFont, p.Code)
		renderText(renderer, config, codeFont, p.Code, ColorTextPrimary(), card.X+(card.W-cw)/2, y+4)
		y += codeH
	}
	for _, line := range lines {
		renderText(renderer, config, fonts.body, line, ColorTextSecondary(), card.X+OverlayPadX, y)
		y += lineH
	}
	yes, no := ctlUIPromptWords(p)
	bh := int32(46)
	by := card.Y + card.H - bh - 20
	yw := tbxButtonWidth(fonts.body, yes, 130)
	nw := tbxButtonWidth(fonts.body, no, 130)
	ctlUI.promptYes = sdl.Rect{X: card.X + card.W - OverlayPadX - yw, Y: by, W: yw, H: bh}
	ctlUI.promptNo = sdl.Rect{X: ctlUI.promptYes.X - 12 - nw, Y: by, W: nw, H: bh}
	tbxDrawButton(renderer, config, fonts.body, ctlUI.promptNo, no, false, false, false)
	tbxDrawButton(renderer, config, fonts.body, ctlUI.promptYes, yes, true, true, false)
	// The keys, beside the buttons: the footer is under the scrim.
	keys := hintSentence2("%s "+yes+"   %s "+no, "A", yes, "B", no)
	_, kh := measureText(fonts.small, keys)
	kw := ctlUI.promptNo.X - card.X - OverlayPadX - 12
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, keys, kw), ColorTextTertiary(), card.X+OverlayPadX, by+(bh-kh)/2)
}

// ── The live controller ──────────────────────────────────────────────────

// ctlUIDrawLiveFrame is the forwarding face's frame: the page bar and the
// shared bars on a flat fill. Home's backdrop (the water, a wallpaper) would
// be redrawn at the input cadence the session keeps (ctlModeTick) behind a
// screen nobody looks at while playing.
func ctlUIDrawLiveFrame(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	base := ColorBackground
	base.A = 255
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	setDrawColor(renderer, base)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	header := ctlUIHeader()
	frame := tbxMeasureFrame(config, header)
	header.X, header.Y, header.W = frame.x, frame.headerY, frame.w
	UIPageHeaderDraw(renderer, config, header)
	body := frame.bodyRect()
	ctlUI.nav.begin(body.Y, body.H)
	ctlUI.nav.end()
	ctlUIDrawLive(renderer, config, fonts, body)
	tbxEndFrame(renderer, config)
}

// ctlUIDrawLive is the forwarding face: the handheld's controls lit as the
// PC receives them, and the way back.
func ctlUIDrawLive(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	st := ctlModeLiveState()
	l := ctlModeLayoutNow()
	infoH := fonts.bodyH + fonts.smallH + 36
	padW := body.W
	if padW > 980 {
		padW = 980
	}
	padH := body.H - infoH
	if padH > 440 {
		padH = 440
	}
	if padH < 200 {
		padH = 200
	}
	pad := sdl.Rect{X: body.X + (body.W-padW)/2, Y: body.Y, W: padW, H: padH}
	if spare := body.H - infoH - padH; spare > 0 {
		pad.Y += spare / 2
	}
	UICard(renderer, pad, DefaultUICardStyle())
	u := padH / 10
	b := st.Buttons

	// Shoulders and triggers along the top edge.
	sy := pad.Y + u/2
	sw := u * 3
	ctlUIDrawKey(renderer, config, fonts.small, sdl.Rect{X: pad.X + u, Y: sy, W: sw, H: u}, "LB", b&ctlBtnLB != 0)
	ctlUIDrawTrigger(renderer, config, fonts.small, sdl.Rect{X: pad.X + u + sw + u/2, Y: sy, W: sw, H: u}, "LT", st.LT)
	ctlUIDrawKey(renderer, config, fonts.small, sdl.Rect{X: pad.X + pad.W - u - sw, Y: sy, W: sw, H: u}, "RB", b&ctlBtnRB != 0)
	ctlUIDrawTrigger(renderer, config, fonts.small, sdl.Rect{X: pad.X + pad.W - u - 2*sw - u/2, Y: sy, W: sw, H: u}, "RT", st.RT)

	// The D-pad and the face buttons above, the sticks below, the middle
	// buttons between them: the handheld's own arrangement.
	upY := pad.Y + u*4
	lowY := pad.Y + u*15/2
	cx := pad.X + pad.W/2
	ctlUIDrawDpad(renderer, pad.X+pad.W/5, upY, u*9/10, b)
	ctlUIDrawFace(renderer, config, fonts.small, pad.X+pad.W-pad.W/5, upY, u, b)
	ctlUIDrawStick(renderer, config, fonts.tiny, pad.X+pad.W*3/10, lowY, u*7/5, st.LX, st.LY, b&ctlBtnLS != 0, "Left stick")
	ctlUIDrawStick(renderer, config, fonts.tiny, pad.X+pad.W*7/10, lowY, u*7/5, st.RX, st.RY, b&ctlBtnRS != 0, "Right stick")
	mw := u * 5 / 2
	ctlUIDrawKey(renderer, config, fonts.tiny, sdl.Rect{X: cx - u/2 - mw, Y: upY - u, W: mw, H: u * 4 / 5}, "Back", b&ctlBtnBack != 0)
	ctlUIDrawKey(renderer, config, fonts.tiny, sdl.Rect{X: cx + u/2, Y: upY - u, W: mw, H: u * 4 / 5}, "Start", b&ctlBtnStart != 0)
	ctlUIDrawRound(renderer, config, fonts.tiny, cx, upY+u*3/4, u*3/4, "Guide", b&ctlBtnGuide != 0)

	// The way back, then what is in use.
	y := pad.Y + pad.H + 14
	line := ctlUIReturnLine(l)
	lw, _ := measureText(fonts.body, line)
	if lw > body.W {
		lw = body.W
	}
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, line, body.W), ColorTextPrimary(), body.X+(body.W-lw)/2, y)
	y += fonts.bodyH + 8
	if ctlUI.liveMeta == "" || time.Since(ctlUI.liveMetaAt) >= time.Second {
		ctlUI.liveMetaAt = time.Now()
		ctlUI.liveMeta = "Layout: " + l.Name + " · " + ctlUIPressesText(ctlModePresses()) + " · buttons light as the PC sees them"
		if ctlScreenSetting() == "off" && backlightSupported() {
			ctlUI.liveMeta += " · the screen sleeps after 15 s"
		}
	}
	meta := ctlUI.liveMeta
	mw2, _ := measureText(fonts.small, meta)
	if mw2 > body.W {
		mw2 = body.W
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, meta, body.W), ColorTextMuted(), body.X+(body.W-mw2)/2, y)
}

// ctlUIReturnLine says how to get back, in the words of the device in hand.
func ctlUIReturnLine(l ctlLayout) string {
	switch {
	case hintsUseKeyboard():
		return "Press Esc to return to JukaHub"
	case !P().IsHandheld() && l.MenuGuide:
		return "Tap the pad's Guide button to return to JukaHub · hold it to send Guide"
	case !P().IsHandheld():
		return "Tap the pad's Guide button to return to JukaHub"
	case l.MenuGuide:
		return "Tap Menu to return to JukaHub · hold Menu for Guide"
	}
	return "Tap Menu to return to JukaHub"
}

// ctlUIPressesText is "1 press", "12 presses".
func ctlUIPressesText(n int) string {
	if n == 1 {
		return "1 press"
	}
	return strconv.Itoa(n) + " presses"
}

// ctlUILitInk is the label ink on a lit control.
func ctlUILitInk() sdl.Color { return TextInk(glassAccent()) }

// ctlUIDrawCap draws a control's body, lit or not.
func ctlUIDrawCap(renderer *sdl.Renderer, r sdl.Rect, radius int32, lit bool) {
	if lit {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassAccent())
		return
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassA(30))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassA(70))
}

// ctlUIDrawKey draws a labelled capsule (a shoulder, Back, Start).
func ctlUIDrawKey(renderer *sdl.Renderer, config *Config, font *ttf.Font, r sdl.Rect, label string, lit bool) {
	ctlUIDrawCap(renderer, r, r.H/2, lit)
	lw, lh := measureText(font, label)
	ink := ColorTextSecondary()
	if lit {
		ink = ctlUILitInk()
	}
	renderText(renderer, config, font, label, ink, r.X+(r.W-lw)/2, r.Y+(r.H-lh)/2)
}

// ctlUIDrawRound draws a labelled round button.
func ctlUIDrawRound(renderer *sdl.Renderer, config *Config, font *ttf.Font, cx, cy, radius int32, label string, lit bool) {
	ink := ColorTextSecondary()
	if lit {
		fillCircle(renderer, cx, cy, radius, glassAccent())
		ink = ctlUILitInk()
	} else {
		fillCircle(renderer, cx, cy, radius, glassA(30))
		strokeCircle(renderer, cx, cy, radius, glassA(70))
	}
	lw, lh := measureText(font, label)
	renderText(renderer, config, font, label, ink, cx-lw/2, cy-lh/2)
}

// ctlUIDrawTrigger draws an analogue trigger as a capsule that fills.
func ctlUIDrawTrigger(renderer *sdl.Renderer, config *Config, font *ttf.Font, r sdl.Rect, label string, v uint8) {
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, glassA(30))
	if v > 0 {
		fw := r.W * int32(v) / 255
		if fw < r.H {
			// A capsule needs both its round ends.
			fw = r.H
		}
		fillRoundedRect(renderer, r.X, r.Y, fw, r.H, r.H/2, glassAccent())
	}
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassA(70))
	ink := ColorTextSecondary()
	if v >= ctlTriggerPressed {
		ink = ctlUILitInk()
	}
	lw, lh := measureText(font, label)
	renderText(renderer, config, font, label, ink, r.X+(r.W-lw)/2, r.Y+(r.H-lh)/2)
}

// ctlUIDrawDpad draws the D-pad's cross around (cx, cy), arms s wide.
func ctlUIDrawDpad(renderer *sdl.Renderer, cx, cy, s int32, b uint32) {
	h := s / 2
	ctlUIDrawCap(renderer, sdl.Rect{X: cx - h, Y: cy - h, W: s, H: s}, 4, false)
	ctlUIDrawCap(renderer, sdl.Rect{X: cx - h, Y: cy - h - s, W: s, H: s}, 6, b&ctlBtnUp != 0)
	ctlUIDrawCap(renderer, sdl.Rect{X: cx - h, Y: cy + h, W: s, H: s}, 6, b&ctlBtnDown != 0)
	ctlUIDrawCap(renderer, sdl.Rect{X: cx - h - s, Y: cy - h, W: s, H: s}, 6, b&ctlBtnLeft != 0)
	ctlUIDrawCap(renderer, sdl.Rect{X: cx + h, Y: cy - h, W: s, H: s}, 6, b&ctlBtnRight != 0)
}

// ctlUIDrawFace draws the four face buttons around (cx, cy) with the PC's
// names in the PC's places: A at the bottom, Y at the top.
func ctlUIDrawFace(renderer *sdl.Renderer, config *Config, font *ttf.Font, cx, cy, u int32, b uint32) {
	d := u * 23 / 20
	r := u * 11 / 20
	ctlUIDrawRound(renderer, config, font, cx, cy-d, r, "Y", b&ctlBtnY != 0)
	ctlUIDrawRound(renderer, config, font, cx-d, cy, r, "X", b&ctlBtnX != 0)
	ctlUIDrawRound(renderer, config, font, cx+d, cy, r, "B", b&ctlBtnB != 0)
	ctlUIDrawRound(renderer, config, font, cx, cy+d, r, "A", b&ctlBtnA != 0)
}

// ctlUIDrawStick draws a stick's well and where the stick is in it; a
// pushed-in stick lights its well.
func ctlUIDrawStick(renderer *sdl.Renderer, config *Config, font *ttf.Font, cx, cy, radius int32, x, y int16, pressed bool, label string) {
	well := glassA(24)
	if pressed {
		well = WithAlpha(glassAccent(), 90)
	}
	fillCircle(renderer, cx, cy, radius, well)
	strokeCircle(renderer, cx, cy, radius, glassA(70))
	dot := radius * 2 / 5
	travel := int64(radius - dot - 2)
	dx := int32(int64(x) * travel / 32767)
	dy := int32(int64(y) * travel / 32767)
	ink := WithAlpha(ColorTextPrimary(), 200)
	if x != 0 || y != 0 || pressed {
		ink = glassAccent()
	}
	fillCircle(renderer, cx+dx, cy+dy, dot, ink)
	lw, _ := measureText(font, label)
	renderText(renderer, config, font, label, ColorTextTertiary(), cx-lw/2, cy+radius+4)
}
