package main

// vkui.go — the on-screen keyboard, drawn in Home's glass.
//
// The keyboard is a bottom sheet flush with the screen edge: the glass sheet
// fill with a grabber and a light edge along its top, the header (the field's
// name and the tool row), a recessed text field with a real caret, the
// suggestion strip and the key grid, with the controller hints as Home's glass
// badges at the bottom. Keys are opaque glass caps (opaque so the cached layer
// composites exactly like a live frame), function keys a step darker, the
// action key and an active Shift in the accent, and the focus is Home's 3px
// accent ring on a cap lifted by 2px.
//
// Everything that does not follow typing — the sheet, the header, the resting
// field, the keys and the hints — is drawn once into a screen-sized texture
// (vkEnsureStaticLayer) and copied each frame; only the field's text, the strip,
// the focus, hover, the press animation and the popups are drawn live. Fonts,
// the title and the hint plan are memoised per layout, so a steady frame does
// no measuring and allocates nothing.

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Palette and type ─────────────────────────────────────────────────────

// vkPalette is the keyboard's colours for the current theme. Fills are opaque
// mixes of the glass sheet and the glass ink, so a cap reads the same on the
// cached layer as in a live frame and on light presets too.
type vkPalette struct {
	sheet      sdl.Color
	field      sdl.Color
	fieldEdge  sdl.Color
	cap        sdl.Color
	capUtil    sdl.Color
	capFocus   sdl.Color
	capHover   sdl.Color
	chip       sdl.Color
	cell       sdl.Color
	cellBest   sdl.Color
	edge       sdl.Color
	accent     sdl.Color
	accentInk  sdl.Color
	accentText sdl.Color
	ink        sdl.Color
	ink2       sdl.Color
	ink3       sdl.Color
}

func vkPaletteNow() vkPalette {
	sheet := glassSheetFill()
	ink := glassInk()
	accent := vkFocusColor()
	field := glassMix(sheet, ColorBackground, 0.55)
	return vkPalette{
		sheet:      sheet,
		field:      field,
		fieldEdge:  glassMix(field, ink, 0.14),
		cap:        glassMix(sheet, ink, 0.11),
		capUtil:    glassMix(sheet, ink, 0.05),
		capFocus:   glassMix(sheet, ink, 0.21),
		capHover:   glassMix(sheet, ink, 0.16),
		chip:       glassMix(sheet, ink, 0.07),
		cell:       glassMix(sheet, ink, 0.04),
		cellBest:   glassMix(sheet, ink, 0.10),
		edge:       glassMix(sheet, ink, 0.17),
		accent:     accent,
		accentInk:  contrastText(accent),
		accentText: ColorTextAccent(),
		ink:        ColorTextPrimary(),
		ink2:       ColorTextSecondary(),
		ink3:       ColorTextTertiary(),
	}
}

// vkFontSet is the keyboard's type.
type vkFontSet struct {
	key   *ttf.Font // letters on caps
	label *ttf.Font // words on caps: space, Done, 123
	field *ttf.Font // the text being typed
	strip *ttf.Font // suggestions
	chip  *ttf.Font // tool chips and the title
	small *ttf.Font // counters and the strip's tip line
}

var vkFontMemo struct {
	valid  bool
	config *Config
	small  bool
	scale  float64
	fonts  vkFontSet
}

// vkFonts resolves the keyboard's fonts once per config, screen class and
// text size: every getFontSized builds a cache key string, which is fine once
// and wasteful per frame.
func vkFonts(config *Config) vkFontSet {
	small := vkScreenSmall()
	if vkFontMemo.valid && vkFontMemo.config == config && vkFontMemo.small == small && vkFontMemo.scale == a11y.fontScale {
		return vkFontMemo.fonts
	}
	pick := func(role string, size, smallSize int) *ttf.Font {
		if config == nil {
			return nil
		}
		if small {
			size = smallSize
		}
		if f := getFontSized(config, role, size); f != nil {
			return f
		}
		f, _ := getCachedFont(config, role)
		return f
	}
	fonts := vkFontSet{
		key:   pick("medium", 26, 21),
		label: pick("small", 17, 14),
		field: pick("medium", 22, 18),
		strip: pick("medium", 19, 16),
		chip:  pick("small", 15, 13),
		small: pick("small", 13, 12),
	}
	vkFontMemo.valid, vkFontMemo.config, vkFontMemo.small, vkFontMemo.scale = true, config, small, a11y.fontScale
	vkFontMemo.fonts = fonts
	return fonts
}

// vkMemo holds the per-layout strings: the field's name and placeholder, the
// hint plan's input and the strip's tip. They are rebuilt when the layout or
// session changes, never per frame.
var vkMemo struct {
	key         vkLayoutKey
	anim        uint64
	valid       bool
	keys        hintStamp // the hints' vocabulary and remap (inputhints.go)
	voice       bool      // the footer offers voice typing (vkVoiceHintShown)
	title       string
	placeholder string
	hints       []FooterHint
	tip         string
}

func vkMemoRefresh() {
	keys := hintStampNow()
	// The microphone probe answers after the keyboard opens, so the voice
	// badge can come and go within a session.
	voice := vkVoiceHintShown()
	if vkMemo.valid && vkMemo.key == vkLayoutKeyBuilt && vkMemo.anim == vkAnimStart && vkMemo.keys == keys && vkMemo.voice == voice {
		return
	}
	vkMemo.key, vkMemo.anim, vkMemo.valid, vkMemo.keys, vkMemo.voice = vkLayoutKeyBuilt, vkAnimStart, true, keys, voice
	vkMemo.title = vkTitle()
	vkMemo.placeholder = vkPlaceholder()
	vkMemo.hints = vkHintList()
	vkMemo.tip = vkStripTip()
}

// vkHintList is the footer: the pad's buttons, or the keyboard's keys while
// the hints name keys (a PC without a pad, a keyboard on the handheld:
// inputhints.go). Hints are dropped from the end when the sheet is narrow, so
// they are in order of importance.
func vkHintList() []FooterHint {
	// Start and Enter send (vkSubmit without Ask AI); in chat Y is the second
	// way out, to Juka AI, and elsewhere Y does what Start does.
	send := vkActionLabel()
	chat := vkTarget == vkTargetChat
	if chat {
		send = "Send"
	}
	layer := "123"
	if vkLayer == vkLayerSymbols {
		layer = "ABC"
	}
	if hintsUseKeyboard() {
		// A real keyboard types straight into the field (handleTextInputEvent);
		// Backspace deletes and Shift is the keyboard's own.
		keys := []FooterHint{
			{Button: "Enter", Label: send},
			{Button: "Esc", Label: "Close"},
			{Button: "Backspace", Label: "Delete"},
			{Button: "Ctrl+V", Label: "Paste"},
			{Button: "Ctrl+C", Label: "Copy"},
		}
		if vkVoiceHintShown() {
			keys = append(keys, vkVoiceHint()) // F4 (vkvoice_button.go)
		}
		return keys
	}
	hints := []FooterHint{
		{Button: "A", Label: "Type"},
		{Button: "Start", Label: send},
		{Button: "B", Label: "Close"},
		{Button: "X", Label: "Delete"},
	}
	if chat {
		hints = append(hints, FooterHint{Button: "Y", Label: "Ask AI"})
	}
	hints = append(hints,
		FooterHint{Button: "L1", Label: "Shift"},
		FooterHint{Button: "R1", Label: layer},
	)
	// R2 toggles voice typing (vkvoice_button.go).
	if vkVoiceHintShown() {
		hints = append(hints, vkVoiceHint())
	}
	if len(vkEnabledLayouts()) > 1 {
		hints = append(hints, FooterHint{Button: "L1+R1", Label: "Language"})
	}
	return hints
}

// vkStripTip fills an empty suggestion strip with what the keyboard can do.
func vkStripTip() string {
	if !hintsUseKeyboard() {
		return "Hold A for accents  ·  L1+X copy  ·  L1+Y paste"
	}
	return "Hold a letter for accents  ·  Ctrl+C copy  ·  Ctrl+V paste"
}

// vkLiveFilterTarget is a search whose results update behind the keyboard,
// so its dim stays light enough to read them.
func vkLiveFilterTarget() bool {
	switch vkTarget {
	case vkTargetTV, vkTargetGameSearch, vkTargetMusic, vkTargetIPStream, homeSearchVKTarget, tzPickerVKTarget, steamSearchVKTarget, fdroidSearchVKTarget,
		setSearchVKTarget:
		return true
	}
	return false
}

// ── Static layer ─────────────────────────────────────────────────────────

// vkSheetState is everything the cached layer shows; a change rebuilds it.
type vkSheetState struct {
	layout vkLayoutKey
	sheet  sdl.Rect
	pal    vkPalette
	fonts  vkFontSet
	title  string
	action string
	tools  uint16
	space  string
}

func vkSheetStateNow(config *Config) vkSheetState {
	var tools uint16
	for t := vkTool(0); t < vkToolClose; t++ {
		if vkToolEnabled(t) {
			tools |= 1 << uint(t)
		}
	}
	return vkSheetState{
		layout: vkLayoutKeyBuilt,
		sheet:  vkPanelRect,
		pal:    vkPaletteNow(),
		fonts:  vkFonts(config),
		title:  vkMemo.title,
		action: vkActionLabel(),
		tools:  tools,
		space:  vkKeyLabel("SPACE"),
	}
}

// vkEnsureStaticLayer returns the cached sheet, header, resting field, keys
// and hints, rebuilding it when anything they show changed. The font
// arguments are kept for callers that pass their own; the keyboard resolves
// its own set (vkFonts).
func vkEnsureStaticLayer(renderer *sdl.Renderer, config *Config, font, smallFont *ttf.Font) *sdl.Texture {
	vkForgetForeignTexture(renderer, &vkStaticTex, &vkStaticOwner, &vkStaticValid)
	state := vkSheetStateNow(config)
	if vkStaticValid && vkStaticTex != nil && vkStaticState == state {
		return vkStaticTex
	}
	// Checked before the destroy below (which clears the flag): a renderer
	// without render-target support must not retry and log every frame.
	if vkStaticBroken || screenWidth <= 0 || screenHeight <= 0 {
		return nil
	}
	DestroyVKStaticCache()

	tex, err := renderer.CreateTexture(0, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
	if err != nil || tex == nil {
		vkStaticBroken = true
		logWarn("vk", "keyboard cache disabled: %v", err)
		return nil
	}
	if err := tex.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		tex.Destroy()
		vkStaticBroken = true
		logWarn("vk", "keyboard cache blend mode: %v", err)
		return nil
	}
	prevTarget := renderer.GetRenderTarget()
	var prevBlend sdl.BlendMode
	_ = renderer.GetDrawBlendMode(&prevBlend)
	if err := renderer.SetRenderTarget(tex); err != nil {
		tex.Destroy()
		vkStaticBroken = true
		logWarn("vk", "keyboard cache target: %v", err)
		return nil
	}
	// The keyboard is drawn last, so no clip should be active; make sure the
	// cached layer is not clipped by a leftover scene rect.
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 0)
	renderer.Clear()
	vkDrawStatic(renderer, config, state.fonts, state.pal, 0)
	_ = renderer.SetRenderTarget(prevTarget)
	_ = renderer.SetDrawBlendMode(prevBlend)
	vkStaticTex = tex
	vkStaticOwner = renderer
	vkStaticState = state
	vkStaticValid = true
	vkStaticBuilds++
	return tex
}

// vkDrawStatic paints the parts of the sheet that do not follow typing, moved
// down by offset (the slide-in, on the live path).
func vkDrawStatic(renderer *sdl.Renderer, config *Config, fonts vkFontSet, pal vkPalette, offset int32) {
	sheet := vkPanelRect
	sheet.Y += offset
	vkDrawSheet(renderer, sheet, pal)
	header := vkGeom.title
	header.Y += offset
	vkDrawTitle(renderer, fonts, pal, header)
	for t := vkTool(0); t < vkToolCount; t++ {
		r := vkGeom.tools[t]
		if r.W <= 0 {
			continue
		}
		r.Y += offset
		vkDrawToolChip(renderer, fonts, pal, t, r, vkToolEnabled(t), false, false)
	}
	field := vkFieldRect
	if field.W > 0 {
		field.Y += offset
		fillRoundedRect(renderer, field.X, field.Y, field.W, field.H, glassControlRadius, pal.field)
		strokeRoundedRect(renderer, field.X, field.Y, field.W, field.H, glassControlRadius, 1, pal.fieldEdge)
	}
	for i, rect := range keyboardRects {
		if i >= len(keyboardKeys) {
			break
		}
		r := rect
		r.Y += offset
		vkDrawKeyCap(renderer, config, fonts, pal, r, keyboardKeys[i], false)
	}
	footer := vkGeom.footer
	footer.Y += offset
	vkDrawFooterHints(renderer, config, footer)
}

// vkDrawSheet is the glass sheet: opaque, rounded at the top and running off
// the bottom edge, with a grabber and the glass edge highlight.
func vkDrawSheet(renderer *sdl.Renderer, sheet sdl.Rect, pal vkPalette) {
	if sheet.W <= 0 || sheet.H <= 0 {
		return
	}
	radius := OverlayCardRadius
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, sheet.X, sheet.Y, sheet.W, sheet.H+radius, radius, pal.sheet)
	strokeRoundedRect(renderer, sheet.X, sheet.Y, sheet.W, sheet.H+radius, radius, 1, glassSheetEdge())
	drawOverlayCardAccent(renderer, sheet.X, sheet.Y, sheet.W)
	grab := int32(40)
	fillRoundedRect(renderer, sheet.X+(sheet.W-grab)/2, sheet.Y+6, grab, 4, 2, glassMix(pal.sheet, glassInk(), 0.22))
}

// vkDrawTitle is the field's name: Home's section-label style, an accent bar
// and the tertiary ink, ellipsized to the room left of the tools.
func vkDrawTitle(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette, rect sdl.Rect) {
	font := fonts.chip
	if font == nil || rect.W <= 20 {
		return
	}
	fillRoundedRect(renderer, rect.X, rect.Y+rect.H/2-8, 3, 16, 2, pal.accent)
	title := ellipsize(font, vkMemo.title, rect.W-14)
	_, th := measureText(font, title)
	drawText(renderer, font, title, rect.X+12, rect.Y+(rect.H-th)/2, pal.ink3, textAlignLeft)
}

// vkDrawToolChip draws one chip of the tool row: a glass pill with its label
// (Copy, Paste...) or glyph (globe with the layout code, mic, close).
func vkDrawToolChip(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette, t vkTool, r sdl.Rect, enabled, focused, hovered bool) {
	radius := r.H / 2
	fill := pal.chip
	switch {
	case focused:
		fill = pal.capFocus
	case hovered && enabled:
		fill = pal.capHover
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, fill)
	if focused {
		ring := sdl.Rect{X: r.X - 2, Y: r.Y - 2, W: r.W + 4, H: r.H + 4}
		strokeRoundedRectAt(renderer, ring, homeFocusGlide.apply(ring), radius+2, glassFocusRingWidth, pal.accent)
	} else {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, pal.edge)
	}
	ink := pal.ink
	if !enabled {
		ink = WithAlpha(pal.ink3, 110)
	}
	icon := r.H * 11 / 20
	switch t {
	case vkToolGlobe:
		code := vkToolLabel(t, vkGeom.compact)
		font := fonts.chip
		cw, ch := measureText(font, code)
		total := icon + 6 + cw
		x := r.X + (r.W-total)/2
		drawHomeGlyphTinted(renderer, "globe", sdl.Rect{X: x, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, ink)
		if font != nil {
			drawText(renderer, font, code, x+icon+6, r.Y+(r.H-ch)/2, ink, textAlignLeft)
		}
	case vkToolMic:
		drawHomeGlyphTinted(renderer, "mic", sdl.Rect{X: r.X + (r.W-icon)/2, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, ink)
	case vkToolClose:
		drawHomeGlyphTinted(renderer, "close", sdl.Rect{X: r.X + (r.W-icon)/2, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, ink)
	default:
		font := fonts.chip
		if font == nil {
			return
		}
		label := ellipsize(font, vkToolLabel(t, vkGeom.compact), r.W-12)
		lw, lh := measureText(font, label)
		drawText(renderer, font, label, r.X+(r.W-lw)/2, r.Y+(r.H-lh)/2, ink, textAlignLeft)
	}
}

// vkKeyLook is how one cap is painted.
type vkKeyLook struct {
	fill, edge, ink sdl.Color
	accent          bool // a solid accent cap (the action key, Shift on)
}

// vkKeyLookFor picks a cap's colours from its role: letters on the glass cap,
// function keys a step darker, Shift and delete in the accent ink, the action
// key and an active Shift as solid accent caps.
func vkKeyLookFor(pal vkPalette, key string) vkKeyLook {
	look := vkKeyLook{fill: pal.cap, edge: pal.edge, ink: pal.ink}
	switch key {
	case "ENTER", "SEND":
		look = vkKeyLook{fill: pal.accent, edge: pal.accent, ink: pal.accentInk, accent: true}
	case "⇧":
		look = vkKeyLook{fill: pal.capUtil, edge: pal.edge, ink: pal.accentText}
		if keyboardUpper || vkCapsLock {
			look = vkKeyLook{fill: pal.accent, edge: pal.accent, ink: pal.accentInk, accent: true}
		}
	case "BACK":
		look = vkKeyLook{fill: pal.capUtil, edge: pal.edge, ink: pal.accentText}
	case "ASK_AI":
		look = vkKeyLook{fill: pal.capUtil, edge: WithAlpha(pal.accent, 170), ink: pal.accentText}
	case "123", "ABC", "GLOBE":
		look = vkKeyLook{fill: pal.capUtil, edge: pal.edge, ink: pal.ink2}
	case "SPACE":
		look.ink = pal.ink3
	}
	return look
}

func vkCapRadius(r sdl.Rect) int32 {
	if r.H < 44 {
		return 8
	}
	return 10
}

// vkDrawKeyCap draws one key. A focused cap lifts 2px on every side with
// Home's 3px ring on its edge; on an accent cap the ring is the primary ink so
// it does not vanish into the fill.
func vkDrawKeyCap(renderer *sdl.Renderer, config *Config, fonts vkFontSet, pal vkPalette, r sdl.Rect, key string, focused bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	look := vkKeyLookFor(pal, key)
	radius := vkCapRadius(r)
	draw := r
	if focused {
		draw = sdl.Rect{X: r.X - 2, Y: r.Y - 2, W: r.W + 4, H: r.H + 4}
		radius += 2
		if look.accent {
			look.fill = lighten(look.fill, 12)
		} else {
			look.fill = pal.capFocus
		}
	}
	fillRoundedRect(renderer, draw.X, draw.Y, draw.W, draw.H, radius, look.fill)
	if focused {
		ring := pal.accent
		if look.accent {
			ring = pal.ink
		}
		strokeRoundedRectAt(renderer, draw, homeFocusGlide.apply(draw), radius, glassFocusRingWidth, ring)
	} else {
		strokeRoundedRect(renderer, draw.X, draw.Y, draw.W, draw.H, radius, 1, look.edge)
	}
	vkDrawKeyLabel(renderer, config, fonts, draw, key, look.ink)
}

// vkDrawKeyLabel centres a cap's label, choosing the size that fits.
func vkDrawKeyLabel(renderer *sdl.Renderer, config *Config, fonts vkFontSet, r sdl.Rect, key string, ink sdl.Color) {
	label := vkKeyLabel(key)
	if key == "GLOBE" {
		icon := r.H * 2 / 5
		font := fonts.label
		lw, lh := measureText(font, label)
		total := icon
		if font != nil && r.W > icon+lw+20 {
			total += 5 + lw
		}
		x := r.X + (r.W-total)/2
		drawHomeGlyphTinted(renderer, "globe", sdl.Rect{X: x, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, ink)
		if total > icon {
			drawText(renderer, font, label, x+icon+5, r.Y+(r.H-lh)/2, ink, textAlignLeft)
		}
		return
	}
	font, fitted := vkLabelFont(config, label, r.W-14)
	if font == nil || fitted == "" {
		return
	}
	w, h := measureText(font, fitted)
	tx := r.X + (r.W-w)/2
	ty := r.Y + (r.H-h)/2
	drawText(renderer, font, fitted, tx, ty, ink, textAlignLeft)
	if key == "⇧" && vkCapsLock {
		// Caps lock: a bar under the arrow, as phone keyboards mark it.
		fillRoundedRect(renderer, r.X+(r.W-14)/2, ty+h+1, 14, 2, 1, ink)
	}
}

// vkLabelFont picks a font whose label fits the key cap and ellipsizes only as
// a last resort, so keys stay legible at every supported screen size. Single
// characters use the large key face.
func vkLabelFont(config *Config, label string, maxW int32) (*ttf.Font, string) {
	if maxW < 8 {
		maxW = 8
	}
	fonts := vkFonts(config)
	candidates := [3]*ttf.Font{fonts.label, fonts.small, nil}
	if utf8.RuneCountInString(label) <= 2 {
		candidates = [3]*ttf.Font{fonts.key, fonts.label, fonts.small}
	}
	for _, f := range candidates {
		if f == nil {
			continue
		}
		if w, _ := measureText(f, label); w <= maxW {
			return f, label
		}
	}
	f := fonts.small
	if f == nil {
		f = fonts.label
	}
	if f == nil {
		return nil, ""
	}
	return f, ellipsize(f, label, maxW)
}

// vkDrawFooterHints lays the hint badges out like Home's footer, centred.
func vkDrawFooterHints(renderer *sdl.Renderer, config *Config, rect sdl.Rect) {
	if rect.W <= 0 || config == nil || len(vkMemo.hints) == 0 {
		return
	}
	vkDrawHintPlan(renderer, config, planFooterHints(config, vkMemo.hints, rect.W), rect)
}

// ── Chord hints ──────────────────────────────────────────────────────────

// vkChordDelayMs is how long L1 must be held before the footer shows the L1
// combinations, so a quick tap on Shift does not flash them.
const vkChordDelayMs = 250

// vkL1DownAt is when L1 last went down on the keyboard (0: not seen).
var vkL1DownAt uint64

// vkChordHints are the L1 combinations. The language switch is listed only
// when there is a second language to switch to.
var vkChordHints = [...]FooterHint{
	{Button: "L1+X", Label: "Copy"},
	{Button: "L1+Y", Label: "Paste"},
	{Button: "L1+R1", Label: "Language"},
}

// vkChordPlan is the measured chord row, kept until the config, the width,
// the type size, the list or the hints' words (a remap) change.
var vkChordPlan struct {
	ok     bool
	config *Config
	w      int32
	n      int
	scale  float64
	words  hintStamp
	plan   footerPlan
}

// vkChordHintList is the chord hints for the enabled languages.
func vkChordHintList() []FooterHint {
	if len(vkEnabledLayouts()) > 1 {
		return vkChordHints[:]
	}
	return vkChordHints[:2]
}

// vkChordHintsWanted reports whether the footer shows the L1 combinations: L1
// is held on a pad (for a moment, so tapping Shift does not flash them) and no
// popup or recording has the buttons.
func vkChordHintsWanted(now uint64) bool {
	if vkPopup.open || vkVoice.phase == vkVoiceRecording || !vkPadHeld(sdl.CONTROLLER_BUTTON_LEFTSHOULDER) {
		return false
	}
	return vkL1DownAt == 0 || now < vkL1DownAt || now-vkL1DownAt >= vkChordDelayMs
}

// vkDrawChordHints covers the footer's hints with the L1 combinations, so
// copy and paste are discoverable on the pad that performs them.
func vkDrawChordHints(renderer *sdl.Renderer, config *Config, pal vkPalette, offset int32) {
	rect := vkGeom.footer
	if rect.W <= 0 || config == nil {
		return
	}
	hints := vkChordHintList()
	cp := &vkChordPlan
	words := hintStampNow()
	if !cp.ok || cp.config != config || cp.w != rect.W || cp.n != len(hints) || cp.scale != a11y.fontScale || cp.words != words {
		cp.plan = planFooterHints(config, hints, rect.W)
		cp.ok, cp.config, cp.w, cp.n, cp.scale, cp.words = true, config, rect.W, len(hints), a11y.fontScale, words
	}
	rect.Y += offset
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 0, pal.sheet)
	vkDrawHintPlan(renderer, config, cp.plan, rect)
}

// vkDrawHintPlan draws a planned hint row centred in rect, like Home's footer.
func vkDrawHintPlan(renderer *sdl.Renderer, config *Config, plan footerPlan, rect sdl.Rect) {
	if plan.font == nil {
		return
	}
	x := rect.X + (rect.W-plan.width)/2
	textY := rect.Y + (rect.H-plan.fontHeight)/2
	badgeY := rect.Y + (rect.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
}

// ── Frame ────────────────────────────────────────────────────────────────

// renderKeyboard draws the keyboard when it is open: the dim behind it, the
// cached sheet, then the live parts.
func renderKeyboard(renderer *sdl.Renderer, config *Config) {
	if !virtualKeyboardActive {
		return
	}
	vkSyncSession()
	vkEnsureLayout()
	vkMemoRefresh()
	vkSyncSuggestions()

	// Short rise-in animation (120ms) so the sheet slides up from the edge;
	// the dim fades in with it, lighter over a live search's results.
	offset := vkAnimOffset()
	progress := vkOpenProgress()
	dim := 170.0
	if vkLiveFilterTarget() {
		dim = 70
	}
	fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, WithAlpha(ColorBackground, uint8(dim*progress)))

	fonts := vkFonts(config)
	pal := vkPaletteNow()
	if cache := vkEnsureStaticLayer(renderer, config, fonts.key, fonts.label); cache != nil {
		// The layer is copied as a whole, so the slide-in moves the sheet,
		// header, keys and hints together.
		src := vkPanelRect
		dst := vkPanelRect
		dst.Y += offset
		_ = renderer.Copy(cache, &src, &dst)
	} else {
		vkDrawStatic(renderer, config, fonts, pal, offset)
	}
	vkDrawLive(renderer, config, fonts, pal, offset)
}

// vkDrawLive draws what follows typing and focus on top of the static layer.
func vkDrawLive(renderer *sdl.Renderer, config *Config, fonts vkFontSet, pal vkPalette, offset int32) {
	now := sdl.GetTicks64()
	field := vkFieldRect
	field.Y += offset
	vkDrawFieldLive(renderer, fonts, pal, field, now)

	if vkVoiceActive() {
		strip := vkGeom.strip
		strip.Y += offset
		vkDrawVoiceBar(renderer, fonts, pal, strip, now)
	} else {
		vkDrawStrip(renderer, fonts, pal, offset)
	}

	hoverKey := vkHoverFromPointer()
	hoverTool, hoverCell := -1, -1
	if px, py, ok := vkPointerLogical(); ok {
		hoverTool = vkToolHit(px, py)
		hoverCell = vkStripHit(px, py)
	}

	// The mic shows it is listening; the focused or hovered chip gets its ring.
	if vkVoiceActive() {
		r := vkGeom.tools[vkToolMic]
		r.Y += offset
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, pal.accent)
		icon := r.H * 11 / 20
		drawHomeGlyphTinted(renderer, "mic", sdl.Rect{X: r.X + (r.W-icon)/2, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, pal.accentInk)
	}
	for t := vkTool(0); t < vkToolCount; t++ {
		focused := vkFocusZone == vkZoneTools && vkZoneIndex == int(t)
		hovered := hoverTool == int(t)
		if !focused && !hovered {
			continue
		}
		if t == vkToolMic && vkVoiceActive() && !focused {
			continue
		}
		r := vkGeom.tools[t]
		r.Y += offset
		vkDrawToolChip(renderer, fonts, pal, t, r, vkToolEnabled(t), focused, hovered)
	}
	if hoverCell >= 0 && hoverCell < vkGeom.nCells && !vkVoiceActive() {
		if vkStripDisplay(hoverCell) != "" && !(vkFocusZone == vkZoneStrip && vkZoneIndex == hoverCell) {
			r := vkGeom.cells[hoverCell]
			r.Y += offset
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, WithAlpha(pal.accent, 140))
		}
	}

	// Mouse affordance: the cap a click would press. The focused cap keeps its
	// own look, so hover never competes with it.
	if hoverKey >= 0 && hoverKey < len(keyboardRects) && hoverKey < len(keyboardKeys) &&
		!(vkFocusZone == vkZoneKeys && hoverKey == vkFocusedRectIndex()) {
		r := keyboardRects[hoverKey]
		r.Y += offset
		look := vkKeyLookFor(pal, keyboardKeys[hoverKey])
		if !look.accent {
			radius := vkCapRadius(r)
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, pal.capHover)
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, WithAlpha(pal.accent, 120))
			vkDrawKeyLabel(renderer, config, fonts, r, keyboardKeys[hoverKey], look.ink)
		}
	}
	if vkFocusZone == vkZoneKeys && !vkPopup.open {
		if i := vkFocusedRectIndex(); i >= 0 && i < len(keyboardRects) && i < len(keyboardKeys) {
			r := keyboardRects[i]
			r.Y += offset
			vkDrawKeyCap(renderer, config, fonts, pal, r, keyboardKeys[i], true)
		}
	}

	// Press feedback on top of everything: the cap the finger touched sinks in
	// and comes back, even though the highlight may already have moved on.
	if pressed, ok := vkPressedRectAt(now); ok {
		pressed.Y += offset
		vkDrawPressedKey(renderer, pressed, vkPressProgress(now))
	}
	// Holding L1 turns the footer into the L1 combinations (copy, paste,
	// language), like a modifier key revealing its layer.
	if vkChordHintsWanted(now) {
		vkDrawChordHints(renderer, config, pal, offset)
	}
	vkDrawLangFlash(renderer, fonts, pal, offset, now)
	vkDrawPopup(renderer, fonts, pal)
}

// vkPressProgress is how far the current press animation has run, 0..1.
func vkPressProgress(now uint64) float64 {
	if vkPressedUntil == 0 || now >= vkPressedUntil {
		return 1
	}
	left := float64(vkPressedUntil-now) / float64(vkPressFlashMs)
	if left > 1 {
		left = 1
	}
	return 1 - left
}

// vkDrawPressedKey makes a pressed cap sink: it shrinks by up to 2px and dims,
// then eases back over vkPressFlashMs.
func vkDrawPressedKey(renderer *sdl.Renderer, r sdl.Rect, progress float64) {
	if r.W <= 6 || r.H <= 6 {
		return
	}
	depth := 1 - vkEaseOut(progress)
	inset := int32(1 + 2*depth)
	// Black, not the theme's background: a pressed cap reads darker on the
	// light presets too.
	alpha := uint8(24 + 56*depth)
	radius := vkCapRadius(r) - 1
	fillRoundedRect(renderer, r.X+inset, r.Y+inset, r.W-2*inset, r.H-2*inset, radius, sdl.Color{R: 0, G: 0, B: 0, A: alpha})
}

// vkPointerLogical is the mouse position in logical coordinates while the
// pointer is live (moved recently); vkHoverFromPointer refreshes it.
func vkPointerLogical() (int32, int32, bool) {
	if !vkHoverFresh(sdl.GetTicks64()) {
		return 0, 0, false
	}
	x, y := physicalToLogical(vkPointerX, vkPointerY)
	return x, y, true
}

// ── Field ────────────────────────────────────────────────────────────────

// vkFieldLayout is the field's text as laid out for its width: the visible
// slice around the caret and where the caret sits. It is recomputed only when
// the text, caret or width changes.
type vkFieldLayout struct {
	valid     bool
	text      string
	caret     int
	masked    bool
	font      *ttf.Font
	maxW      int32
	display   string
	startRune int
	endRune   int
	visible   string
	caretX    int32
	visW      int32
	remaining int
	counter   string
}

var vkFieldCache vkFieldLayout

// vkFieldLayoutFor lays text out for a maxW-wide field. A long value scrolls
// so the caret stays in view; only the visible slice is ever rendered, which
// keeps the text texture under the GPU's size limit.
func vkFieldLayoutFor(font *ttf.Font, text string, caret int, maxW int32) *vkFieldLayout {
	masked := vkSecretTarget()
	fc := &vkFieldCache
	if fc.valid && fc.text == text && fc.caret == caret && fc.masked == masked && fc.font == font && fc.maxW == maxW {
		return fc
	}
	prevStart := 0
	if fc.valid && fc.font == font && fc.maxW == maxW {
		prevStart = fc.startRune
	}
	n := utf8.RuneCountInString(text)
	display := text
	if masked {
		display = strings.Repeat("•", n)
	}
	width := func(a, b int) int32 {
		if b <= a {
			return 0
		}
		// Raw: typed text is never the interface language (i18n.go).
		w, _ := measureTextRaw(font, display[vkRuneOffset(display, a):vkRuneOffset(display, b)])
		return w
	}
	start, end := 0, n
	if width(0, n) > maxW {
		// Keep the window's start unless the caret left it, then slide just
		// far enough that the caret is visible again.
		start = prevStart
		if start > caret {
			start = caret
		}
		lo, hi := start, caret
		for lo < hi {
			mid := (lo + hi) / 2
			if width(mid, caret) > maxW-4 {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		start = lo
		lo, hi = caret, n
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if width(start, mid) <= maxW {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		end = lo
	}
	*fc = vkFieldLayout{
		valid: true, text: text, caret: caret, masked: masked, font: font, maxW: maxW,
		display: display, startRune: start, endRune: end,
	}
	fc.visible = display[vkRuneOffset(display, start):vkRuneOffset(display, end)]
	fc.visW, _ = measureTextRaw(font, fc.visible)
	fc.caretX = width(start, caret)
	fc.remaining = vkMaxTextLen - len(text)
	if !masked && fc.remaining < 200 && fc.remaining >= 0 {
		fc.counter = strconv.Itoa(fc.remaining)
	}
	return fc
}

// vkFieldIndexAt is the caret position a click at x lands on, from the field
// as last drawn.
func vkFieldIndexAt(mx int32) int {
	fc := &vkFieldCache
	text := vkCurrentText()
	if _, _, preview := vkVoiceDisplayText(); preview {
		return vkCaretIndex(text)
	}
	if !fc.valid || fc.font == nil || fc.text != text {
		return utf8.RuneCountInString(text)
	}
	x := mx - (vkFieldRect.X + vkFieldPadX)
	best, bestD := fc.startRune, int32(1<<30)
	for i := fc.startRune; i <= fc.endRune; i++ {
		w, _ := measureTextRaw(fc.font, fc.display[vkRuneOffset(fc.display, fc.startRune):vkRuneOffset(fc.display, i)])
		d := w - x
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// vkFieldPadX is the field's inner left margin.
const vkFieldPadX = int32(16)

// vkDrawFieldLive draws the field's text, selection, caret, placeholder and
// counter, and the focus ring when the highlight is on the field.
func vkDrawFieldLive(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette, field sdl.Rect, now uint64) {
	if field.W <= 0 {
		return
	}
	if vkFocusZone == vkZoneField {
		strokeRoundedRect(renderer, field.X-2, field.Y-2, field.W+4, field.H+4, glassControlRadius+2, glassFocusRingWidth, pal.accent)
	}
	font := fonts.field
	if font == nil {
		return
	}
	textH := int32(font.Height())
	textY := field.Y + (field.H-textH)/2
	x0 := field.X + vkFieldPadX
	text := vkCurrentText()
	caret := vkCaretIndex(text)
	preview := false
	if live, liveCaret, ok := vkVoiceDisplayText(); ok {
		text, caret, preview = live, liveCaret, true
	}
	if text == "" {
		// A steady caret marks the field before the first character; the
		// placeholder moves aside so the two never overlap.
		fillRoundedRect(renderer, x0, field.Y+field.H/2-textH/2+2, 2, textH-4, 1, pal.accent)
		if ph := vkMemo.placeholder; ph != "" {
			drawText(renderer, font, ellipsize(font, ph, field.W-2*vkFieldPadX-12), x0+10, textY, pal.ink3, textAlignLeft)
		}
		return
	}
	counterW := int32(0)
	if fc := &vkFieldCache; fc.valid && fc.counter != "" && fonts.small != nil {
		cw, _ := measureText(fonts.small, fc.counter)
		counterW = cw + 10
	}
	lay := vkFieldLayoutFor(font, text, caret, field.W-2*vkFieldPadX-counterW)
	if vkTextSelected && !preview {
		// The whole value is selected: the first key typed replaces it.
		fillRoundedRect(renderer, x0-3, textY-2, lay.visW+6, textH+4, 4, WithAlpha(pal.accent, 90))
	}
	ink := pal.ink
	if preview {
		ink = pal.ink2
	}
	drawTextRaw(renderer, font, lay.visible, x0, textY, ink, textAlignLeft)
	if !vkTextSelected && !preview && (now/530)%2 == 0 {
		fillRoundedRect(renderer, x0+lay.caretX, field.Y+field.H/2-textH/2+2, 2, textH-4, 1, pal.accent)
	}
	if lay.counter != "" && fonts.small != nil {
		cw, ch := measureText(fonts.small, lay.counter)
		drawText(renderer, fonts.small, lay.counter, field.X+field.W-vkFieldPadX-cw, field.Y+(field.H-ch)/2, pal.ink3, textAlignLeft)
	}
}

// ── Strip ────────────────────────────────────────────────────────────────

// vkDrawStrip draws the suggestion cells (the best guess in the middle,
// brighter) or the quick inserts, or the tip line when there is nothing to
// suggest yet.
func vkDrawStrip(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette, offset int32) {
	if vkGeom.nCells == 0 || fonts.strip == nil {
		return
	}
	mode := vkStripModeNow()
	if mode == vkStripWords && !vkStripHasContent() {
		strip := vkGeom.strip
		strip.Y += offset
		if font := fonts.small; font != nil && vkMemo.tip != "" {
			tip := ellipsize(font, vkMemo.tip, strip.W-24)
			tw, th := measureText(font, tip)
			drawText(renderer, font, tip, strip.X+(strip.W-tw)/2, strip.Y+(strip.H-th)/2, pal.ink3, textAlignLeft)
		}
		return
	}
	for i := 0; i < vkGeom.nCells; i++ {
		text := vkStripDisplay(i)
		if text == "" {
			continue
		}
		cell := vkGeom.cells[i]
		cell.Y += offset
		best := mode == vkStripWords && i == 1
		fill, ink := pal.cell, pal.ink2
		if best {
			fill, ink = pal.cellBest, pal.ink
		}
		fillRoundedRect(renderer, cell.X, cell.Y, cell.W, cell.H, 10, fill)
		if vkFocusZone == vkZoneStrip && vkZoneIndex == i {
			strokeRoundedRectAt(renderer, cell, homeFocusGlide.apply(cell), 10, glassFocusRingWidth, pal.accent)
		}
		fitted := ellipsize(fonts.strip, text, cell.W-16)
		w, h := measureText(fonts.strip, fitted)
		drawText(renderer, fonts.strip, fitted, cell.X+(cell.W-w)/2, cell.Y+(cell.H-h)/2, ink, textAlignLeft)
	}
}

// vkDrawLangFlash names the new layout over the keys for a moment after the
// globe switched it.
func vkDrawLangFlash(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette, offset int32, now uint64) {
	if vkLangFlashUntil == 0 || now >= vkLangFlashUntil || fonts.label == nil || len(keyboardRects) == 0 {
		return
	}
	left := float64(vkLangFlashUntil-now) / float64(vkLangFlashMs)
	alpha := 1.0
	if left < 0.35 {
		alpha = left / 0.35
	}
	name := vkCurrentLayout().Native
	tw, th := measureText(fonts.label, name)
	w, h := tw+36, th+18
	top := keyboardRects[0]
	x := vkGeom.content.X + (vkGeom.content.W-w)/2
	y := top.Y + offset + top.H/2 - h/2
	fillRoundedRect(renderer, x, y, w, h, h/2, WithAlpha(pal.accent, uint8(235*alpha)))
	drawText(renderer, fonts.label, name, x+(w-tw)/2, y+(h-th)/2, WithAlpha(pal.accentInk, uint8(255*alpha)), textAlignLeft)
}

// vkDrawPopup draws the accent row or the language list above its key.
func vkDrawPopup(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette) {
	if !vkPopup.open || len(vkPopup.rects) == 0 {
		return
	}
	drawGlassSheet(renderer, vkPopup.card, glassRaisedFill())
	cur := vkCurrentLayout()
	for i, r := range vkPopup.rects {
		if i >= len(vkPopup.items) {
			break
		}
		item := vkPopup.items[i]
		focused := i == vkPopup.focus
		fill, ink := pal.cap, pal.ink
		if focused {
			fill, ink = pal.accent, pal.accentInk
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, fill)
		if vkPopup.kind == vkPopupAccents {
			font, fitted := vkLabelFont(vkFontMemo.config, item, r.W-8)
			if font == nil {
				continue
			}
			w, h := measureText(font, fitted)
			drawText(renderer, font, fitted, r.X+(r.W-w)/2, r.Y+(r.H-h)/2, ink, textAlignLeft)
			continue
		}
		font := fonts.label
		if font == nil {
			continue
		}
		mark := ""
		if cur != nil && item == cur.Native {
			mark = "✓"
		}
		markW, _ := measureText(font, mark)
		fitted := ellipsize(font, item, r.W-28-markW)
		_, h := measureText(font, fitted)
		drawText(renderer, font, fitted, r.X+14, r.Y+(r.H-h)/2, ink, textAlignLeft)
		if mark != "" {
			drawText(renderer, font, mark, r.X+r.W-14-markW, r.Y+(r.H-h)/2, ink, textAlignLeft)
		}
	}
}
