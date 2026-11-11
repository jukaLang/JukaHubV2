package main

// home_widgetdraw.go — what the dynamic Home layouts paint inside a tile.
//
// The carousel and the widget grid place the tiles; this file paints them:
// the info widgets (Clock, Weather, Battery, Storage, Now playing, Calendar,
// Playtime), the app tile a Start-menu entry — or a Home card away from its
// own design — is drawn as, and the compact Continue card the carousel shows
// off its centre.
//
// Every painter takes two rects. item.Rect is the tile's settled rect: the
// base the shape cache keys the card's texture by, so one texture serves a
// card while it animates, and the size the Continue preview is made at. draw
// is where the tile is this frame — lifted, pressed, scrolled or scaled by the
// carousel — and it picks the composition, in 1280x720 design units: wide from
// 300 across, tall from 250 down (a 1x1 grid tile is 189x152, a 2x1 395x152,
// a 2x2 395x320; the carousel's centre card 520x330, its side cards 260x165).
//
// A frame that shows what the last one showed formats nothing: each widget
// keeps its strings with the values they were made from (the minute, the
// level, the reading), and the shapes inside a tile that follows the animation
// are cached at stepped sizes rather than one per pixel it grows.

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Entry points ─────────────────────────────────────────────────────────

// drawHomeWidget paints the info widget name (its tile ref without "w:"): the
// focus glow, the glass card with its ring, then the widget. An unknown name —
// a widget a newer build saved — draws as a plain tile with the item's own
// icon and title.
func (h *HomeLayout) drawHomeWidget(renderer *sdl.Renderer, fonts HomeFonts, name string, item HomeItem, draw sdl.Rect, focused, pressed bool) {
	if h == nil || renderer == nil || draw.W <= 0 || draw.H <= 0 {
		return
	}
	if !hwpKnown(name) {
		h.drawHomeAppTile(renderer, fonts, item, draw, focused, pressed, false)
		return
	}
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	drawGlassTile(renderer, item.Rect, draw, focused, pressed)
	f, ok := hwpResolveFonts(fonts)
	if !ok {
		return
	}
	lay := h.hwpSettled(item.Rect, draw)
	b := h.hwpFrame(lay)
	if lay.W != item.Rect.W || lay.H != item.Rect.H {
		// A card the carousel is scaling changes size every frame of a
		// slide: its text column steps in 8 px (centred, so the margins stay
		// even), or every width would be a new entry in the text-fit caches.
		w := b.inner.W &^ 7
		b.inner.X += (b.inner.W - w) / 2
		b.inner.W = w
	}
	if b.inner.W < scaleX(64, h.Width) || b.inner.H < scaleY(56, h.Height) {
		return
	}
	now := time.Now()
	switch name {
	case "clock":
		h.hwpDrawClock(renderer, f, b, now, focused)
	case "weather":
		h.hwpDrawWeather(renderer, f, b, now, focused)
	case "battery":
		s := hwpBatteryStat()
		h.hwpDrawStat(renderer, f, b, &s, focused)
	case "storage":
		s := hwpStorageStat(now)
		h.hwpDrawStat(renderer, f, b, &s, focused)
	case "nowplaying":
		h.hwpDrawNowPlaying(renderer, f, b, focused)
	case "calendar":
		h.hwpDrawCalendar(renderer, f, b, now)
	case "playtime":
		s := hwpPlaytimeStat(now)
		h.hwpDrawStat(renderer, f, b, &s, focused)
	}
}

// drawHomeAppTile paints a tile that opens something: a Start-menu entry, or
// a Home card drawn away from its own design (the carousel's side cards). The
// small tile is drawActionCard's composition exactly — the 48 px badge
// top-left, the name over its description bottom-left — and the larger sizes
// keep its parts: a wide tile sets the badge beside the text, a tall one a
// larger badge over a larger name. dim marks an entry this device no longer
// has: muted ink, and the description says so.
func (h *HomeLayout) drawHomeAppTile(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, draw sdl.Rect, focused, pressed, dim bool) {
	if h == nil || renderer == nil || fonts.Card == nil || draw.W <= 0 || draw.H <= 0 {
		return
	}
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	drawGlassTile(renderer, item.Rect, draw, focused, pressed)

	titleInk, subInk := ColorTextPrimary(), ColorTextMuted()
	subtitle := item.Subtitle
	if dim {
		titleInk, subInk = ColorTextMuted(), ColorTextTertiary()
		subtitle = "Not available here"
	}
	switch {
	case draw.H >= scaleY(250, h.Height):
		h.hwpAppTall(renderer, fonts, item.Icon, item.Title, subtitle, item.Rect, draw, focused, dim, titleInk, subInk)
	case draw.W >= scaleX(300, h.Width):
		h.hwpAppWide(renderer, fonts, item.Icon, item.Title, subtitle, draw, focused, dim, titleInk, subInk)
	default:
		h.hwpAppSmall(renderer, fonts, item.Icon, item.Title, subtitle, draw, focused, dim, titleInk, subInk)
	}
}

// drawHomeContinueCompact is the Continue card where the full one does not
// fit its moment — the carousel's cards away from the settled centre: the
// same artwork, chip and title, the progress line only on a tall card, and
// never the Resume pill.
func (h *HomeLayout) drawHomeContinueCompact(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, draw sdl.Rect, state HomeContinueState, focused, pressed bool) {
	if h == nil || renderer == nil || fonts.Small == nil || fonts.Card == nil || draw.W <= 0 || draw.H <= 0 {
		return
	}
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	// The artwork is requested and looked up at the settled size only: the
	// carousel scales this card every frame, and home_continue.go keeps one
	// preview, which a request per animated size would keep replacing.
	homeRequestPreview(state.Preview, item.Rect.W, item.Rect.H)
	if art, _, _ := homePreviewTexture(renderer, state.Preview, item.Rect.W, item.Rect.H); art != nil {
		_ = renderer.Copy(art, nil, &draw)
		fillVerticalScrim(renderer, draw, draw.Y+draw.H/3,
			sdl.Color{R: 0, G: 0, B: 0, A: 0}, sdl.Color{R: 0, G: 0, B: 0, A: 215})
	} else {
		hwpContinueThumb(renderer, draw)
	}
	if pressed {
		fillRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, homeGlass(24))
	}
	if focused {
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 1, homeCardEdgeFocus())
		drawHomeFocusRing(renderer, item.Rect, draw, homeTileRadius)
	} else {
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 1, homeCardEdge())
	}

	pad := scaleX(20, h.Width)
	textW := draw.W - pad*2
	if textW < scaleX(48, h.Width) {
		return
	}
	// The chip top-left, and the play button top-right when both fit.
	top := draw.Y + scaleY(20, h.Height)
	label := hwpContinueChip(state)
	playSize := scaleY(40, h.Height)
	gap := scaleX(12, h.Width)
	labelW, _ := measureText(fonts.Small, label)
	chipMax := textW
	showPlay := labelW+scaleX(20, h.Width)+gap+playSize <= textW
	if showPlay {
		chipMax = textW - playSize - gap
		drawPlayBadge(renderer, sdl.Rect{X: draw.X + draw.W - pad - playSize, Y: top, W: playSize, H: playSize})
	}
	chip := h.hwpChip(renderer, fonts.Small, label, draw.X+pad, top, chipMax, ColorAccent)
	minTop := chip.Y + chip.H
	if showPlay && top+playSize > minTop {
		minTop = top + playSize
	}
	minTop += scaleY(8, h.Height)

	// Bottom up: the progress line (a tall card only), the subtitle, the title.
	bottom := draw.Y + draw.H - scaleY(20, h.Height)
	if draw.H >= scaleY(250, h.Height) && state.HasRecent && state.Progress > 0 {
		progressH := scaleY(8, h.Height)
		remW := int32(0)
		remaining := strings.TrimSpace(state.Remaining)
		if remaining != "" {
			remW, _ = measureText(fonts.Small, remaining)
			remW += gap
		}
		// The track steps in 8 px while the card scales, so the bar's shapes
		// stay a handful of cached sizes (hwpPill).
		track := sdl.Rect{X: draw.X + pad, Y: bottom - progressH, W: (textW - remW) &^ 7, H: progressH}
		if track.W > 0 {
			drawProgressBar(renderer, track, state.Progress, ColorAccent)
			if remaining != "" {
				drawText(renderer, fonts.Small, remaining, track.X+track.W+gap,
					track.Y+(track.H-int32(fonts.Small.Height()))/2, ColorTextMuted(), textAlignLeft)
			}
		}
		bottom = track.Y - scaleY(14, h.Height)
	}
	title, sub := hwpContinueLines(state)
	titleH := int32(fonts.Card.Height())
	if fonts.Helper != nil {
		subH := int32(fonts.Helper.Height())
		if bottom-subH-scaleY(6, h.Height)-titleH >= minTop {
			bottom -= subH
			drawText(renderer, fonts.Helper, ellipsize(fonts.Helper, sub, textW), draw.X+pad, bottom, ColorTextMuted(), textAlignLeft)
			bottom -= scaleY(6, h.Height)
		}
	}
	titleY := bottom - titleH
	if titleY < minTop {
		titleY = minTop
	}
	drawText(renderer, fonts.Card, ellipsize(fonts.Card, title, textW), draw.X+pad, titleY, ColorTextPrimary(), textAlignLeft)
}

// homeWidgetActivate is A (or a click) on a widget: each opens the screen its
// numbers come from. The caller has already played the activation sound.
func homeWidgetActivate(config *Config, name string) {
	if config == nil {
		return
	}
	switch name {
	case "clock":
		openAlarmScene(config)
	case "weather":
		if weatherEnabledFor(config) {
			toggleWeatherPopover()
			return
		}
		// Weather is off: A goes to the switch that turns it on.
		setOpenSettingRow(config, "general", "weather", "Weather")
	case "battery":
		qsToggleOpen()
	case "storage":
		// The Storage screen when this config has it, else the file manager.
		for _, scene := range [...]string{"DiskSpace", "FileExplorer"} {
			if idx := sceneIndexExact(config, scene); idx >= 0 {
				changeSceneTo(config, idx)
				return
			}
		}
		ShowToast("Storage is not part of this setup", ToastKindInfo)
	case "nowplaying":
		if !openNowPlaying(config) {
			openMusicPlayerScene(config)
		}
	case "calendar":
		toggleCalendarPopover()
	case "playtime":
		if idx := sceneIndexExact(config, playtimeSceneName); idx >= 0 {
			changeSceneTo(config, idx)
			return
		}
		ShowToast("Trophies are not part of this setup", ToastKindInfo)
	}
}

// homeWidgetAvailable reports whether a widget can be offered on this device;
// the add sheet leaves the others out (Battery where there is no battery). An
// unknown name is never offered.
func homeWidgetAvailable(name string) bool {
	if name == "battery" {
		return getBatteryPercent() >= 0
	}
	return hwpKnown(name)
}

// hwpKnown reports whether name is a widget this file paints.
func hwpKnown(name string) bool {
	switch name {
	case "clock", "weather", "battery", "storage", "nowplaying", "calendar", "playtime":
		return true
	}
	return false
}

// hwpConfig is the config Home is drawn for this frame (renderHomeModern sets
// it before the layout draws), the global one before the first frame.
func hwpConfig() *Config {
	if homeBackdropConfig != nil {
		return homeBackdropConfig
	}
	return appConfig
}

// ── Type ─────────────────────────────────────────────────────────────────

// homeWidgetFontSet is the display type the widgets set their figures in:
// Home's own scale (loadHomeFonts) stops at the 32 px greeting, and a clock or
// a battery level reads from across the room only larger than that.
type homeWidgetFontSet struct {
	Huge   *ttf.Font // 96 px: tall tiles only
	Big    *ttf.Font // 64 px
	Large  *ttf.Font // 48 px
	Medium *ttf.Font // 40 px
	Number *ttf.Font // 32 px
}

var (
	homeWidgetFontsOnce  sync.Once
	homeWidgetFontsCache homeWidgetFontSet
)

// homeWidgetFonts opens the display sizes once, on first use, through the same
// Inter cache and legibility floor as loadHomeFonts, so the compact canvas
// scales them with the rest of Home's type.
func homeWidgetFonts() homeWidgetFontSet {
	homeWidgetFontsOnce.Do(func() {
		homeWidgetFontsCache = homeWidgetFontSet{
			Huge:   openHomeFont(uiLegibleFontSize(96)),
			Big:    openHomeFont(uiLegibleFontSize(64)),
			Large:  openHomeFont(uiLegibleFontSize(48)),
			Medium: openHomeFont(uiLegibleFontSize(40)),
			Number: openHomeFont(uiLegibleFontSize(32)),
		}
	})
	return homeWidgetFontsCache
}

// hwpFonts is what a widget draws with this frame: Home's scale with its
// fallbacks resolved, so a face that failed to open costs a size step rather
// than a blank tile.
type hwpFonts struct {
	tile     *ttf.Font
	card     *ttf.Font
	meta     *ttf.Font
	small    *ttf.Font
	helper   *ttf.Font
	greeting *ttf.Font
	big      homeWidgetFontSet
}

// hwpResolveFonts fills the gaps in Home's fonts; false when there is nothing
// to write with at all.
func hwpResolveFonts(fonts HomeFonts) (hwpFonts, bool) {
	f := hwpFonts{
		tile:     fonts.Tile,
		card:     fonts.Card,
		meta:     fonts.Meta,
		small:    fonts.Small,
		helper:   fonts.Helper,
		greeting: fonts.Greeting,
	}
	if f.card == nil {
		f.card = f.tile
	}
	if f.tile == nil {
		f.tile = f.card
	}
	if f.small == nil {
		f.small = f.helper
	}
	if f.small == nil {
		f.small = f.meta
	}
	if f.meta == nil {
		f.meta = f.small
	}
	if f.helper == nil {
		f.helper = f.small
	}
	if f.greeting == nil {
		f.greeting = f.card
	}
	if f.tile == nil || f.meta == nil {
		return f, false
	}
	f.big = homeWidgetFonts()
	return f, true
}

// bigCands lists the faces a value may take, largest first, ending with the
// card face; only a tall tile is offered the 96 px one.
func (f hwpFonts) bigCands(tall bool) [6]*ttf.Font {
	if tall {
		return [6]*ttf.Font{f.big.Huge, f.big.Big, f.big.Large, f.big.Medium, f.big.Number, f.card}
	}
	return [6]*ttf.Font{nil, f.big.Big, f.big.Large, f.big.Medium, f.big.Number, f.card}
}

// unitFor is the face a unit or an AM/PM suffix is set in beside figures in
// font: the title face, or the greeting's beside the 96 px figures.
func (f hwpFonts) unitFor(font *ttf.Font) *ttf.Font {
	if font != nil && font == f.big.Huge {
		return f.greeting
	}
	return f.tile
}

// ── Frame and text ───────────────────────────────────────────────────────

// hwpBox is one tile's content box for this draw — inside the card's padding —
// and the composition its drawn size calls for.
type hwpBox struct {
	inner sdl.Rect
	wide  bool
	tall  bool
}

// hwpSettled is the rect a tile lays its content out in. A card lifted by
// focus (or held in edit mode) is drawn a few pixels larger than its settled
// rect; it keeps the settled size, centred in the grown card, so a value does
// not change face, wrap or ellipsis while the lift eases. A card the carousel
// scales is laid out at the size it is drawn.
func (h *HomeLayout) hwpSettled(base, draw sdl.Rect) sdl.Rect {
	dw, dh := draw.W-base.W, draw.H-base.H
	// 16, not the focus lift's 3: a tile Edit Home is carrying stands 7 px
	// proud on each side (home_dynbody.go dynPaintTile).
	if dw < 0 || dh < 0 || dw > scaleX(16, h.Width) || dh > scaleY(16, h.Height) {
		return draw
	}
	return sdl.Rect{X: draw.X + dw/2, Y: draw.Y + dh/2, W: base.W, H: base.H}
}

// hwpFrame measures a tile's layout rect: the padding is the cards'
// (homeTilePad), the size buckets are taken in design units.
func (h *HomeLayout) hwpFrame(draw sdl.Rect) hwpBox {
	pad := scaleX(homeTilePad, h.Width)
	return hwpBox{
		inner: sdl.Rect{X: draw.X + pad, Y: draw.Y + pad, W: draw.W - 2*pad, H: draw.H - 2*pad},
		wide:  draw.W >= scaleX(300, h.Width),
		tall:  draw.H >= scaleY(250, h.Height),
	}
}

// hwpDigitH is the height of a font's figures: Inter's cap height, about 0.6
// of its line box.
func hwpDigitH(font *ttf.Font) int32 {
	if font == nil {
		return 0
	}
	return int32(float64(font.Height())*0.6 + 0.5)
}

// hwpDigitBand is the blank band at the top of a font's line box above its
// figures. drawText places a label by its line box, so a display numeral set
// at a box's top edge would sit lower than the padding says by this much.
func hwpDigitBand(font *ttf.Font) int32 {
	if font == nil {
		return 0
	}
	if band := int32(font.Ascent()) - hwpDigitH(font); band > 0 {
		return band
	}
	return 0
}

// hwpUnitGap is the space between a figure and its unit, hwpPairGap the space
// before the next figure ("1 h  12 m"); both follow the figures' size.
func hwpUnitGap(font *ttf.Font) int32 { return int32(font.Height())/14 + 2 }
func hwpPairGap(font *ttf.Font) int32 { return int32(font.Height()) / 5 }

// hwpPickPairs returns the largest face in cands (nil entries skipped) whose
// figures fit maxH and in which the value fits maxW, or the smallest face
// offered when none does.
func hwpPickPairs(f hwpFonts, cands []*ttf.Font, s *hwpStat, maxW, maxH int32) *ttf.Font {
	var last *ttf.Font
	for _, font := range cands {
		if font == nil {
			continue
		}
		last = font
		if hwpDigitH(font) <= maxH && hwpPairsW(f, font, s) <= maxW {
			return font
		}
	}
	return last
}

// hwpPairsW is the value's drawn width with its figures in font: each figure,
// at least as wide as s.ref for the first, and its unit after it.
func hwpPairsW(f hwpFonts, font *ttf.Font, s *hwpStat) int32 {
	if font == nil {
		return 0
	}
	unit := f.unitFor(font)
	w := int32(0)
	for i := 0; i < s.n && i < len(s.nums); i++ {
		if i > 0 {
			w += hwpPairGap(font)
		}
		nw, _ := measureText(font, s.nums[i])
		if i == 0 && s.ref != "" {
			if rw, _ := measureText(font, s.ref); rw > nw {
				nw = rw
			}
		}
		w += nw
		if s.units[i] != "" && unit != nil {
			uw, _ := measureText(unit, s.units[i])
			w += hwpUnitGap(font) + uw
		}
	}
	return w
}

// hwpDrawPairs draws the value from x with its figures' top at top — figures
// in font, each unit after its figure on the same baseline in the unit face —
// and returns the baseline.
func hwpDrawPairs(renderer *sdl.Renderer, f hwpFonts, font *ttf.Font, s *hwpStat, x, top int32, ink, unitInk sdl.Color) int32 {
	if font == nil {
		return top
	}
	unit := f.unitFor(font)
	y := top - hwpDigitBand(font)
	base := y + int32(font.Ascent())
	for i := 0; i < s.n && i < len(s.nums); i++ {
		if i > 0 {
			x += hwpPairGap(font)
		}
		drawText(renderer, font, s.nums[i], x, y, ink, textAlignLeft)
		nw, _ := measureText(font, s.nums[i])
		x += nw
		if s.units[i] != "" && unit != nil {
			x += hwpUnitGap(font)
			drawText(renderer, unit, s.units[i], x, base-int32(unit.Ascent()), unitInk, textAlignLeft)
			uw, _ := measureText(unit, s.units[i])
			x += uw
		}
	}
	return base
}

// hwpLabels draws a title over a muted line with the line's bottom at bottom —
// the cards' label position. Either may be empty; both are ellipsized to maxW.
func (h *HomeLayout) hwpLabels(renderer *sdl.Renderer, titleFont, subFont *ttf.Font, title, sub string, x, bottom, maxW int32, titleInk, subInk sdl.Color) {
	y := bottom
	if sub != "" && subFont != nil {
		y -= int32(subFont.Height())
		drawText(renderer, subFont, ellipsize(subFont, sub, maxW), x, y, subInk, textAlignLeft)
		y -= scaleY(2, h.Height)
	}
	if title != "" && titleFont != nil {
		y -= int32(titleFont.Height())
		drawText(renderer, titleFont, ellipsize(titleFont, title, maxW), x, y, titleInk, textAlignLeft)
	}
}

// hwpGlyph paints the named glyph into a size×size square at (x, y).
func hwpGlyph(renderer *sdl.Renderer, name string, x, y, size int32, ink sdl.Color) {
	if size <= 0 {
		return
	}
	drawHomeGlyphTinted(renderer, name, sdl.Rect{X: x, Y: y, W: size, H: size}, ink)
}

// hwpBadgeGlyph paints an icon badge and its glyph: lit in the accent on a
// tinted badge while focused (drawActionCard's focus), quiet when dim. big
// sets the glyph at half the badge instead of the 24 px a 48 px badge holds.
func hwpBadgeGlyph(renderer *sdl.Renderer, badge sdl.Rect, icon string, focused, dim, big bool) {
	glyph := drawIconBadge(renderer, badge, focused && !dim)
	if big {
		g := badge.W / 2
		glyph = sdl.Rect{X: badge.X + (badge.W-g)/2, Y: badge.Y + (badge.H-g)/2, W: g, H: g}
	}
	switch {
	case dim:
		drawHomeGlyphTinted(renderer, icon, glyph, ColorTextTertiary())
	case focused:
		drawHomeGlyphTinted(renderer, icon, glyph, ColorAccent)
	default:
		DrawHomeIcon(renderer, icon, glyph, false)
	}
}

// hwpChip draws a label chip — the Continue card's: an accent wash with a
// hairline — at (x, y), at most maxW wide with its label ellipsized to fit,
// and returns its rect.
func (h *HomeLayout) hwpChip(renderer *sdl.Renderer, font *ttf.Font, label string, x, y, maxW int32, ink sdl.Color) sdl.Rect {
	padX := scaleX(10, h.Width)
	w, _ := measureText(font, label)
	w += 2 * padX
	if w > maxW {
		label = ellipsize(font, label, maxW-2*padX)
		w = maxW
	}
	chip := sdl.Rect{X: x, Y: y, W: w, H: int32(font.Height()) + scaleY(10, h.Height)}
	if chip.W <= 2*padX {
		return chip
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 8, WithAlpha(ColorAccent, 38))
	strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 8, 1, WithAlpha(ColorAccent, 90))
	drawText(renderer, font, label, chip.X+padX, chip.Y+(chip.H-int32(font.Height()))/2, ink, textAlignLeft)
	return chip
}

// hwpBar paints a level bar: the progress line's glass track and a fill of
// frac of its width.
func hwpBar(renderer *sdl.Renderer, track sdl.Rect, frac float32, fill sdl.Color) {
	if track.W <= 0 || track.H <= 0 {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	hwpPill(renderer, track, homeGlass(51))
	w := int32(float32(track.W)*frac + 0.5)
	if w <= 0 {
		return
	}
	if w < track.H {
		w = track.H
	}
	hwpPill(renderer, sdl.Rect{X: track.X, Y: track.Y, W: w, H: track.H}, fill)
}

// hwpPill fills a pill whose width follows the tile. Its shape is cached at
// the width rounded down to 8 px and stretched by the rest, so a tile growing
// with the focus lift or the carousel's scale reuses a few textures instead of
// minting one per pixel.
func hwpPill(renderer *sdl.Renderer, r sdl.Rect, c sdl.Color) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	base := r
	if base.W > 16 {
		base.W &^= 7
	}
	fillRoundedRectAt(renderer, base, r, r.H/2, c)
}

// hwpWrapEntry is one remembered two-line wrap.
type hwpWrapEntry struct {
	font *ttf.Font
	text string
	maxW int32
	l1   string
	l2   string
}

var (
	hwpWraps    [6]hwpWrapEntry
	hwpWrapNext int
)

// hwpWrap2 breaks text into at most two lines of maxW, the second ellipsized.
// The answer is remembered: a steady frame asks the same question again.
func hwpWrap2(font *ttf.Font, text string, maxW int32) (string, string) {
	for i := range hwpWraps {
		if e := &hwpWraps[i]; e.font == font && e.maxW == maxW && e.text == text {
			return e.l1, e.l2
		}
	}
	l1, l2 := hwpWrapLines(font, text, maxW)
	hwpWraps[hwpWrapNext] = hwpWrapEntry{font: font, text: text, maxW: maxW, l1: l1, l2: l2}
	hwpWrapNext = (hwpWrapNext + 1) % len(hwpWraps)
	return l1, l2
}

// hwpWrapLines is hwpWrap2's measurement: the first line ends at the last
// space that fits, or — for one word longer than the line — where the word
// stops fitting.
func hwpWrapLines(font *ttf.Font, text string, maxW int32) (string, string) {
	text = strings.TrimSpace(text)
	if font == nil || maxW <= 0 || text == "" {
		return text, ""
	}
	if w, _ := measureText(font, text); w <= maxW {
		return text, ""
	}
	cut := -1
	for i := 1; i < len(text); i++ {
		if text[i] != ' ' {
			continue
		}
		if w, _ := measureText(font, text[:i]); w > maxW {
			break
		}
		cut = i
	}
	if cut < 0 {
		cut = hwpFitPrefix(font, text, maxW)
		if cut <= 0 {
			return ellipsize(font, text, maxW), ""
		}
		return ellipsize(font, text[:cut], maxW), ellipsize(font, text[cut:], maxW)
	}
	// The first line goes through ellipsize as well: it is measured raw
	// here, but drawn with missing glyphs replaced, which can be wider.
	return ellipsize(font, strings.TrimSpace(text[:cut]), maxW), ellipsize(font, strings.TrimSpace(text[cut+1:]), maxW)
}

// hwpFitPrefix is the longest whole-rune prefix of text that fits maxW, in
// bytes.
func hwpFitPrefix(font *ttf.Font, text string, maxW int32) int {
	best := 0
	lo, hi := 1, len(text)
	for lo <= hi {
		mid := (lo + hi) / 2
		cut := mid
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
		if w, _ := measureText(font, text[:cut]); w <= maxW {
			best = cut
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// hwpHints holds the lines that name the A button, made for one button word.
var hwpHints struct {
	key    string
	turnOn string // "A turns it on"
	music  string // "A opens Music"
}

// hwpHintText is the two lines that name the A button, in this frame's button
// words ("Enter" while the hints speak keyboard, inputhints.go).
func hwpHintText() (string, string) {
	key := hintKeyFor("A", "Open")
	if key != hwpHints.key || hwpHints.turnOn == "" {
		hwpHints.key = key
		hwpHints.turnOn = key + " turns it on"
		hwpHints.music = key + " opens Music"
	}
	return hwpHints.turnOn, hwpHints.music
}

// ── App tile ─────────────────────────────────────────────────────────────

// hwpAppSmall is drawActionCard's composition: the badge top-left, the name
// over its description bottom-left, the label kept below the badge on a short
// tile.
func (h *HomeLayout) hwpAppSmall(renderer *sdl.Renderer, fonts HomeFonts, icon, title, subtitle string, draw sdl.Rect, focused, dim bool, titleInk, subInk sdl.Color) {
	pad := scaleX(homeTilePad, h.Width)
	iconSize := min32(scaleX(homeIconSize, h.Width), scaleY(homeIconSize, h.Height))
	if maxIcon := min32(draw.W-pad*2, draw.H-pad*2-scaleY(homeGlyphSize, h.Height)); iconSize > maxIcon {
		iconSize = maxIcon
	}
	if iconSize < scaleY(22, h.Height) {
		return
	}
	badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + pad, W: iconSize, H: iconSize}
	hwpBadgeGlyph(renderer, badge, icon, focused, dim, false)

	textW := draw.W - pad*2
	if textW < 24 {
		textW = 24
	}
	titleFont := fonts.Tile
	if titleFont == nil {
		titleFont = fonts.Card
	}
	subFont := fonts.Meta
	if subFont == nil {
		subFont = fonts.Helper
	}
	titleText := ellipsize(titleFont, title, textW)
	subText := ""
	if subFont != nil {
		subText = ellipsize(subFont, subtitle, textW)
	}
	titleH := int32(titleFont.Height())
	subH := int32(0)
	if subFont != nil {
		subH = int32(subFont.Height())
	}
	textY := draw.Y + draw.H - pad - subH - titleH
	if subText != "" {
		textY -= scaleY(2, h.Height)
	}
	if textY < badge.Y+badge.H+scaleY(4, h.Height) {
		textY = badge.Y + badge.H + scaleY(4, h.Height)
	}
	drawText(renderer, titleFont, titleText, draw.X+pad, textY, titleInk, textAlignLeft)
	if subText != "" {
		drawText(renderer, subFont, subText, draw.X+pad, textY+titleH+scaleY(2, h.Height), subInk, textAlignLeft)
	}
}

// hwpAppWide sets the badge at the left, centred top to bottom, with the name
// and its description beside it in the card face (the Media card's layout).
func (h *HomeLayout) hwpAppWide(renderer *sdl.Renderer, fonts HomeFonts, icon, title, subtitle string, draw sdl.Rect, focused, dim bool, titleInk, subInk sdl.Color) {
	pad := scaleX(homeTilePad, h.Width)
	iconSize := min32(scaleX(homeIconSize, h.Width), scaleY(homeIconSize, h.Height))
	if maxIcon := draw.H - pad*2; iconSize > maxIcon {
		iconSize = maxIcon
	}
	if iconSize < scaleY(22, h.Height) {
		return
	}
	badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + (draw.H-iconSize)/2, W: iconSize, H: iconSize}
	hwpBadgeGlyph(renderer, badge, icon, focused, dim, false)

	textX := badge.X + badge.W + scaleX(16, h.Width)
	textW := draw.X + draw.W - pad - textX
	if textW < 24 {
		return
	}
	subFont := fonts.Helper
	if subFont == nil {
		subFont = fonts.Meta
	}
	titleText := ellipsize(fonts.Card, title, textW)
	subText := ""
	subH := int32(0)
	if subFont != nil {
		if subText = ellipsize(subFont, subtitle, textW); subText != "" {
			subH = int32(subFont.Height())
		}
	}
	titleH := int32(fonts.Card.Height())
	textY := draw.Y + (draw.H-titleH-subH)/2
	drawText(renderer, fonts.Card, titleText, textX, textY, titleInk, textAlignLeft)
	if subText != "" {
		drawText(renderer, subFont, subText, textX, textY+titleH, subInk, textAlignLeft)
	}
}

// hwpAppTall is the small composition at a tall tile's scale: a larger badge
// top-left, the name in the card face over its description bottom-left. The
// badge is sized from the settled rect, so the focus lift does not step it.
func (h *HomeLayout) hwpAppTall(renderer *sdl.Renderer, fonts HomeFonts, icon, title, subtitle string, base, draw sdl.Rect, focused, dim bool, titleInk, subInk sdl.Color) {
	pad := scaleX(homeTilePad, h.Width)
	size := h.hwpTallBadge(h.hwpSettled(base, draw))
	badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + pad, W: size, H: size}
	hwpBadgeGlyph(renderer, badge, icon, focused, dim, true)

	textW := draw.W - pad*2
	if textW < 24 {
		return
	}
	subFont := fonts.Helper
	if subFont == nil {
		subFont = fonts.Meta
	}
	h.hwpLabels(renderer, fonts.Card, subFont, title, subtitle, draw.X+pad, draw.Y+draw.H-pad, textW, titleInk, subInk)
}

// hwpTallBadge is a tall tile's badge: 28% of the tile's shorter side, 72 to
// 96 design px in 8 px steps, so the carousel's scaling redraws the glyph at a
// few sizes rather than one per frame.
func (h *HomeLayout) hwpTallBadge(draw sdl.Rect) int32 {
	if h.Width <= 0 || h.Height <= 0 {
		return draw.W / 4
	}
	w := draw.W * HomeBaseWidth / h.Width
	ht := draw.H * HomeBaseHeight / h.Height
	d := min32(w, ht) * 28 / 100 / 8 * 8
	if d < 72 {
		d = 72
	}
	if d > 96 {
		d = 96
	}
	return min32(scaleX(d, h.Width), scaleY(d, h.Height))
}

// ── Clock ────────────────────────────────────────────────────────────────

// hwpClockText is the clock widget's strings, made once a minute.
type hwpClockText struct {
	made    bool
	minute  int64
	clock12 bool
	order   string
	alarmOn bool
	alarmH  int
	alarmM  int
	digits  string // "09:41", or "9:41" with the 12-hour clock
	suffix  string // "AM" or "PM"; empty with the 24-hour clock
	ref     string // the widest time this clock shows, for a steady size
	weekday string // "Tuesday"
	short   string // "10 Nov"
	long    string // "Tuesday, 10 November"
	alarm   string // "Alarm 07:30"; empty with no alarm armed
	alarmS  string // "07:30": the alarm when its label does not fit
}

var hwpClock hwpClockText

// hwpClockUpdate remakes the clock's strings when the minute, the clock or
// date setting or the next alarm changed since they were made. In screenshot
// mode the face reads 9:41 on Saturday 14 November and an armed alarm 10:45,
// like the dock's (shotClock, saverDrawAlarmChip).
func hwpClockUpdate(config *Config, now time.Time) *hwpClockText {
	c := &hwpClock
	shown := shotClock(now)
	minute := shown.Unix() / 60
	clock12 := clockHour12(config)
	order := dateOrder(config)
	alarmOn, alarmH, alarmM := false, 0, 0
	if next := alarmNextUpcoming(); next != nil {
		alarmOn, alarmH, alarmM = true, next.Hour, next.Minute
		if shotModeOn() {
			alarmH, alarmM = 10, 45
		}
	}
	if c.made && c.minute == minute && c.clock12 == clock12 && c.order == order &&
		c.alarmOn == alarmOn && c.alarmH == alarmH && c.alarmM == alarmM {
		return c
	}
	c.made, c.minute, c.clock12, c.order = true, minute, clock12, order
	c.alarmOn, c.alarmH, c.alarmM = alarmOn, alarmH, alarmM
	c.digits, c.suffix = hwpSplitClock(clockDigits(config, now))
	// The widest figures Inter sets (4 is its widest digit), so the face
	// picked for the time never steps down as the minutes change.
	c.ref = "04:44"
	if clock12 {
		c.ref = "10:44"
	}
	c.weekday = shown.Weekday().String()
	c.short = formatDateShort(config, shown)
	c.long = formatDayLong(config, now)
	c.alarm, c.alarmS = "", ""
	if alarmOn {
		c.alarmS = clockDigitsHM(config, alarmH, alarmM)
		c.alarm = "Alarm " + c.alarmS
	}
	return c
}

// hwpSplitClock splits "3:04 PM" into its figures and the AM/PM suffix the
// widget sets smaller on the same baseline; a 24-hour time has none.
func hwpSplitClock(s string) (string, string) {
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// hwpClockValue is the time as a value: the figures, the suffix as their
// unit, and the widest time as the size reference.
func hwpClockValue(c *hwpClockText) hwpStat {
	v := hwpStat{n: 1, ref: c.ref}
	v.nums[0], v.units[0] = c.digits, c.suffix
	return v
}

// hwpDrawClock paints the clock. Small: the time at the top, the weekday and
// the date bottom-left. Wide: the full day bottom-left and the next alarm in
// the top-right corner. Tall: the time large, everything centred.
func (h *HomeLayout) hwpDrawClock(renderer *sdl.Renderer, f hwpFonts, b hwpBox, now time.Time, focused bool) {
	c := hwpClockUpdate(hwpConfig(), now)
	in := b.inner
	v := hwpClockValue(c)
	switch {
	case b.tall:
		dayGap := scaleY(12, h.Height)
		alarmGap := scaleY(16, h.Height)
		below := dayGap + int32(f.card.Height())
		if c.alarm != "" {
			below += alarmGap + int32(f.small.Height())
		}
		cands := f.bigCands(true)
		font := hwpPickPairs(f, cands[:], &v, in.W, in.H-below)
		figH := hwpDigitH(font)
		top := in.Y + (in.H-figH-below)/2
		cx := in.X + in.W/2
		v.ref = ""
		hwpDrawPairs(renderer, f, font, &v, cx-hwpPairsW(f, font, &v)/2, top, ColorTextPrimary(), ColorTextMuted())
		y := top + figH + dayGap
		drawText(renderer, f.card, ellipsize(f.card, c.long, in.W), cx, y, ColorTextMuted(), textAlignCenter)
		if c.alarm != "" {
			h.hwpAlarmLine(renderer, f.small, c, cx, y+int32(f.card.Height())+alarmGap, in.W, focused, true)
		}
	case b.wide:
		cands := f.bigCands(false)
		font := hwpPickPairs(f, cands[:], &v, in.W*2/3, in.H-int32(f.tile.Height())-scaleY(10, h.Height))
		hwpDrawPairs(renderer, f, font, &v, in.X, in.Y, ColorTextPrimary(), ColorTextMuted())
		drawText(renderer, f.tile, ellipsize(f.tile, c.long, in.W), in.X, in.Y+in.H-int32(f.tile.Height()), ColorTextPrimary(), textAlignLeft)
		if c.alarm != "" {
			room := in.W - hwpPairsW(f, font, &v) - scaleX(16, h.Width)
			h.hwpAlarmLine(renderer, f.small, c, in.X+in.W, in.Y, room, focused, false)
		}
	default:
		labels := int32(f.tile.Height()) + scaleY(2, h.Height) + int32(f.meta.Height())
		cands := f.bigCands(false)
		font := hwpPickPairs(f, cands[:], &v, in.W, in.H-labels-scaleY(8, h.Height))
		hwpDrawPairs(renderer, f, font, &v, in.X, in.Y, ColorTextPrimary(), ColorTextMuted())
		h.hwpLabels(renderer, f.tile, f.meta, c.weekday, c.short, in.X, in.Y+in.H, in.W, ColorTextPrimary(), ColorTextMuted())
	}
}

// hwpAlarmLine draws the next alarm — the alarm glyph, then its time — ending
// at x, or centred on x when centred is set, in at most maxW. It drops the
// word "Alarm" before it drops the line.
func (h *HomeLayout) hwpAlarmLine(renderer *sdl.Renderer, font *ttf.Font, c *hwpClockText, x, y, maxW int32, focused, centred bool) {
	gs := int32(font.Height())
	gap := scaleX(6, h.Width)
	label := c.alarm
	w, _ := measureText(font, label)
	if gs+gap+w > maxW {
		label = c.alarmS
		if w, _ = measureText(font, label); gs+gap+w > maxW {
			return
		}
	}
	total := gs + gap + w
	left := x - total
	if centred {
		left = x - total/2
	}
	ink := ColorTextMuted()
	if focused {
		ink = ColorAccent
	}
	hwpGlyph(renderer, "alarm", left, y, gs, ink)
	drawText(renderer, font, label, left+gs+gap, y, ColorTextMuted(), textAlignLeft)
}

// ── Calendar ─────────────────────────────────────────────────────────────

// hwpCalendarText is the calendar's strings and today's month, made once a
// day.
type hwpCalendarText struct {
	made    bool
	year    int
	month   time.Month
	day     int
	weekday string // "TUESDAY"
	name    string // "November"
	title   string // "November 2026"
	first   int    // the column of the 1st, Monday = 0
	days    int
	rows    int
}

var hwpCalendar hwpCalendarText

// hwpDayLabels are the day numbers, so the month grid formats nothing.
var hwpDayLabels = [...]string{
	"", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10",
	"11", "12", "13", "14", "15", "16", "17", "18", "19", "20",
	"21", "22", "23", "24", "25", "26", "27", "28", "29", "30", "31",
}

// hwpWeekInitials head the month grid's columns, Monday first.
var hwpWeekInitials = [7]string{"M", "T", "W", "T", "F", "S", "S"}

// hwpDayLabel is a day's number as text.
func hwpDayLabel(d int) string {
	if d < 1 || d >= len(hwpDayLabels) {
		return strconv.Itoa(d)
	}
	return hwpDayLabels[d]
}

// hwpMonthGrid lays a month out in Monday-first weeks: the column of its first
// day (Monday 0 … Sunday 6), its length and the weeks it spans.
func hwpMonthGrid(year int, month time.Month) (first, days, rows int) {
	start := time.Date(year, month, 1, 12, 0, 0, 0, time.UTC)
	first = (int(start.Weekday()) + 6) % 7
	days = time.Date(year, month+1, 0, 12, 0, 0, 0, time.UTC).Day()
	rows = (first + days + 6) / 7
	return first, days, rows
}

// hwpCalendarUpdate remakes the calendar's strings when the day changed. In
// screenshot mode today is Saturday 14 November (shotClock).
func hwpCalendarUpdate(now time.Time) *hwpCalendarText {
	c := &hwpCalendar
	shown := shotClock(now)
	y, m, d := shown.Date()
	if c.made && c.year == y && c.month == m && c.day == d {
		return c
	}
	c.made, c.year, c.month, c.day = true, y, m, d
	c.weekday = strings.ToUpper(shown.Weekday().String())
	c.name = m.String()
	c.title = c.name + " " + strconv.Itoa(y)
	c.first, c.days, c.rows = hwpMonthGrid(y, m)
	return c
}

// hwpDrawCalendar paints the calendar: on a small tile the weekday in the
// accent over the day's number with the month under it, on a tall one the
// month.
func (h *HomeLayout) hwpDrawCalendar(renderer *sdl.Renderer, f hwpFonts, b hwpBox, now time.Time) {
	c := hwpCalendarUpdate(now)
	in := b.inner
	if b.tall {
		h.hwpDrawMonth(renderer, f, in, c)
		return
	}
	drawText(renderer, f.small, ellipsize(f.small, c.weekday, in.W), in.X, in.Y, ColorTextAccent(), textAlignLeft)
	top := in.Y + int32(f.small.Height()) + scaleY(6, h.Height)
	monthY := in.Y + in.H - int32(f.tile.Height())
	v := hwpStat{n: 1, ref: "24"}
	v.nums[0] = hwpDayLabel(c.day)
	cands := f.bigCands(false)
	font := hwpPickPairs(f, cands[:], &v, in.W, monthY-scaleY(8, h.Height)-top)
	hwpDrawPairs(renderer, f, font, &v, in.X, top, ColorTextPrimary(), ColorTextMuted())
	drawText(renderer, f.tile, ellipsize(f.tile, c.name, in.W), in.X, monthY, ColorTextMuted(), textAlignLeft)
}

// hwpDrawMonth is the tall calendar: the month's name, the weekday initials
// (Monday first) and the month's weeks with today ringed in the accent. The
// neighbouring months' days are left out.
func (h *HomeLayout) hwpDrawMonth(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect, c *hwpCalendarText) {
	gridW := min32(in.W, 7*scaleX(58, h.Width))
	x0 := in.X + (in.W-gridW)/2
	titleH := int32(f.tile.Height())
	drawText(renderer, f.tile, ellipsize(f.tile, c.title, gridW), x0, in.Y, ColorTextPrimary(), textAlignLeft)
	tw, _ := measureText(f.tile, c.title)
	if ww, _ := measureText(f.meta, c.weekday); tw+scaleX(16, h.Width)+ww <= gridW {
		drawText(renderer, f.meta, c.weekday, x0+gridW, in.Y+(titleH-int32(f.meta.Height()))/2, ColorTextAccent(), textAlignRight)
	}

	headY := in.Y + titleH + scaleY(8, h.Height)
	for col := int32(0); col < 7; col++ {
		ink := ColorTextMuted()
		if col >= 5 {
			ink = ColorTextTertiary()
		}
		drawText(renderer, f.meta, hwpWeekInitials[col], x0+(2*col+1)*gridW/14, headY, ink, textAlignCenter)
	}

	gridY := headY + int32(f.meta.Height()) + scaleY(6, h.Height)
	rows := int32(c.rows)
	if rows < 1 {
		rows = 1
	}
	rowH := (in.Y + in.H - gridY) / rows
	if most := scaleY(44, h.Height); rowH > most {
		rowH = most
	}
	r := min32(gridW/7, rowH)/2 - scaleY(2, h.Height)
	dayH := int32(f.small.Height())
	for d := 1; d <= c.days; d++ {
		cell := int32(c.first + d - 1)
		col, row := cell%7, cell/7
		cx := x0 + (2*col+1)*gridW/14
		cy := gridY + row*rowH + rowH/2
		ink := ColorTextMuted()
		if col >= 5 {
			ink = ColorTextTertiary()
		}
		if d == c.day {
			if r > 4 {
				fillCircle(renderer, cx, cy, r, WithAlpha(ColorAccent, 46))
				strokeCircle(renderer, cx, cy, r, ColorAccent)
				strokeCircle(renderer, cx, cy, r-1, ColorAccent)
			}
			ink = ColorTextPrimary()
		}
		drawText(renderer, f.small, hwpDayLabel(d), cx, cy-dayH/2, ink, textAlignCenter)
	}
}

// ── Weather ──────────────────────────────────────────────────────────────

// hwpWeatherDay is one coming day of the forecast, as drawn.
type hwpWeatherDay struct {
	day   string // "Wed"
	hi    string // "9°"
	lo    string // "3°"
	glyph string // sun, moon or cloud
}

// hwpWeatherText is the weather widget's strings and the inputs they were made
// from: the switch, the forecast (by identity: a fetch replaces the slice), the
// unit and the minute, which carries the change of day.
type hwpWeatherText struct {
	made   bool
	on     bool
	ready  bool
	data   *WeatherDay
	n      int
	unit   string
	minute int64
	has    bool   // today is in the forecast
	hi     string // "12°"
	lo     string // "/ 5°"
	desc   string // "Partly cloudy"
	glyph  string // sun, moon or cloud
	next   [3]hwpWeatherDay
	nNext  int
}

var hwpWeather hwpWeatherText

// hwpWeatherUpdate remakes the weather strings when an input changed. Today's
// entry comes from weatherDayFor, which has none in screenshot mode: the
// widget then says there is no forecast, and no weather reaches a picture.
func hwpWeatherUpdate(config *Config, now time.Time) *hwpWeatherText {
	c := &hwpWeather
	on := weatherEnabledFor(config)
	unit := weatherUnit(config)
	minute := now.Unix() / 60
	wxMutex.Lock()
	ready := weatherReady
	n := len(weatherDaily)
	var data *WeatherDay
	if n > 0 {
		data = &weatherDaily[0]
	}
	wxMutex.Unlock()
	if c.made && c.on == on && c.ready == ready && c.data == data && c.n == n &&
		c.unit == unit && c.minute == minute {
		return c
	}
	c.made, c.on, c.ready, c.data, c.n, c.unit, c.minute = true, on, ready, data, n, unit, minute
	c.has, c.nNext = false, 0
	if !on || !ready {
		return c
	}
	var next [3]WeatherDay
	count := 0
	wxMutex.Lock()
	today, ok := weatherDayFor(weatherDaily, now)
	if ok {
		for i := range weatherDaily {
			if weatherDaily[i].Date != today.Date {
				continue
			}
			for j := i + 1; j < len(weatherDaily) && count < len(next); j++ {
				next[count] = weatherDaily[j]
				count++
			}
			break
		}
	}
	wxMutex.Unlock()
	if !ok {
		return c
	}
	c.has = true
	c.hi = hwpTemp(today.TMax, unit)
	c.lo = "/ " + hwpTemp(today.TMin, unit)
	c.desc = weatherCodeDesc(today.Code)
	c.glyph = hwpWeatherGlyph(today.Code, now.Hour())
	for i := 0; i < count; i++ {
		c.next[i] = hwpWeatherDay{
			day:   hwpShortWeekday(next[i].Date),
			hi:    hwpTemp(next[i].TMax, unit),
			lo:    hwpTemp(next[i].TMin, unit),
			glyph: hwpWeatherGlyph(next[i].Code, 12),
		}
	}
	c.nNext = count
	return c
}

// hwpTemp is a forecast temperature in the display unit, whole degrees.
func hwpTemp(celsius float64, unit string) string {
	return strconv.Itoa(int(math.Round(weatherTemp(celsius, unit)))) + "°"
}

// hwpWeatherGlyph is the sky's glyph: the sun (the moon after dark) for a
// clear sky, a cloud for anything else — the dock's rule
// (saverDockWeatherIcon).
func hwpWeatherGlyph(code, hour int) string {
	if code <= 1 {
		if hour < 6 || hour >= 20 {
			return "moon"
		}
		return "sun"
	}
	return "cloud"
}

// hwpShortWeekday is a forecast date's weekday ("2026-11-11" is "Wed"); empty
// for a date it cannot read.
func hwpShortWeekday(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return t.Weekday().String()[:3]
}

// hwpSkyInk is the sky glyph's colour: the warm accent for the sun, the
// quieter ink for the moon, the text ink for clouds.
func hwpSkyInk(glyph string) sdl.Color {
	switch glyph {
	case "sun":
		return ColorWarning
	case "moon":
		return ColorTextSecondary()
	}
	return ColorTextPrimary()
}

// hwpDrawWeather paints the forecast: today's sky, high and low and sky on
// every size, the coming days beside it on a wide tile and under it on a tall
// one. With weather off or no forecast for today it says so instead.
func (h *HomeLayout) hwpDrawWeather(renderer *sdl.Renderer, f hwpFonts, b hwpBox, now time.Time, focused bool) {
	c := hwpWeatherUpdate(hwpConfig(), now)
	in := b.inner
	if !c.on || !c.has {
		ink := ColorTextMuted()
		if focused {
			ink = ColorAccent
		}
		hwpGlyph(renderer, "cloud", in.X, in.Y, min32(scaleY(40, h.Height), in.H/3), ink)
		title, sub := "No forecast yet", "Weather"
		if !c.on {
			turnOn, _ := hwpHintText()
			title, sub = "Weather is off", turnOn
		}
		h.hwpLabels(renderer, f.tile, f.meta, title, sub, in.X, in.Y+in.H, in.W, ColorTextPrimary(), ColorTextMuted())
		return
	}
	if b.tall {
		h.hwpWeatherTall(renderer, f, in, c)
		return
	}
	today := in
	if b.wide && c.nNext > 0 {
		today.W = h.hwpWeatherDays(renderer, f, in, c)
	}
	h.hwpWeatherToday(renderer, f, today, c, min32(scaleY(40, h.Height), in.H/3), false)
}

// hwpWeatherToday is today in box: the sky's glyph top-left, the high (large)
// and the low over the sky's description bottom-left.
func (h *HomeLayout) hwpWeatherToday(renderer *sdl.Renderer, f hwpFonts, box sdl.Rect, c *hwpWeatherText, glyphSize int32, tall bool) {
	hwpGlyph(renderer, c.glyph, box.X, box.Y, glyphSize, hwpSkyInk(c.glyph))
	descY := box.Y + box.H - int32(f.meta.Height())
	drawText(renderer, f.meta, ellipsize(f.meta, c.desc, box.W), box.X, descY, ColorTextMuted(), textAlignLeft)
	v := hwpStat{n: 1}
	v.nums[0], v.units[0] = c.hi, c.lo
	gap := scaleY(6, h.Height)
	top := box.Y + glyphSize + gap
	cands := f.bigCands(tall)
	font := hwpPickPairs(f, cands[:], &v, box.W, descY-gap-top)
	hwpDrawPairs(renderer, f, font, &v, box.X, descY-gap-hwpDigitH(font), ColorTextPrimary(), ColorTextMuted())
}

// hwpWeatherDays draws the coming days as columns along the right of box —
// weekday, sky, high over low — and returns the width it leaves for today.
func (h *HomeLayout) hwpWeatherDays(renderer *sdl.Renderer, f hwpFonts, box sdl.Rect, c *hwpWeatherText) int32 {
	colW := scaleX(64, h.Width)
	if colW <= 0 {
		return box.W
	}
	n := int32(c.nNext)
	if fit := box.W * 55 / 100 / colW; n > fit {
		n = fit
	}
	if n <= 0 {
		return box.W
	}
	x0 := box.X + box.W - n*colW
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, homeGlass(26))
	_ = renderer.FillRect(&sdl.Rect{X: x0 - scaleX(8, h.Width), Y: box.Y, W: 1, H: box.H})
	lineH := int32(f.meta.Height())
	gs := min32(scaleY(24, h.Height), box.H-3*lineH-scaleY(8, h.Height))
	for i := int32(0); i < n; i++ {
		d := &c.next[i]
		cx := x0 + i*colW + colW/2
		drawText(renderer, f.meta, d.day, cx, box.Y, ColorTextMuted(), textAlignCenter)
		if gs > 0 {
			hwpGlyph(renderer, d.glyph, cx-gs/2, box.Y+lineH+(box.H-3*lineH-gs)/2, gs, hwpSkyInk(d.glyph))
		}
		drawText(renderer, f.meta, d.hi, cx, box.Y+box.H-2*lineH, ColorTextPrimary(), textAlignCenter)
		drawText(renderer, f.meta, d.lo, cx, box.Y+box.H-lineH, ColorTextMuted(), textAlignCenter)
	}
	return x0 - scaleX(16, h.Width) - box.X
}

// hwpWeatherTall is the tall forecast: today large at the top, the coming days
// as rows along the bottom — weekday, sky, high and low.
func (h *HomeLayout) hwpWeatherTall(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect, c *hwpWeatherText) {
	rowH := scaleY(30, h.Height)
	n := int32(c.nNext)
	today := in
	if n > 0 {
		top := in.Y + in.H - n*rowH
		today.H = top - scaleY(16, h.Height) - in.Y
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		setDrawColor(renderer, homeGlass(26))
		_ = renderer.FillRect(&sdl.Rect{X: in.X, Y: top - scaleY(8, h.Height), W: in.W, H: 1})
		lineH := int32(f.small.Height())
		gs := min32(scaleY(22, h.Height), rowH)
		gap := scaleX(10, h.Width)
		for i := int32(0); i < n; i++ {
			d := &c.next[i]
			y := top + i*rowH
			ty := y + (rowH-lineH)/2
			drawText(renderer, f.small, d.day, in.X, ty, ColorTextMuted(), textAlignLeft)
			hwpGlyph(renderer, d.glyph, in.X+in.W*2/5, y+(rowH-gs)/2, gs, hwpSkyInk(d.glyph))
			loW, _ := measureText(f.small, d.lo)
			drawText(renderer, f.small, d.lo, in.X+in.W, ty, ColorTextMuted(), textAlignRight)
			drawText(renderer, f.small, d.hi, in.X+in.W-loW-gap, ty, ColorTextPrimary(), textAlignRight)
		}
	}
	h.hwpWeatherToday(renderer, f, today, c, min32(scaleY(48, h.Height), today.H/3), true)
}

// ── Stats: Battery, Storage, Playtime ────────────────────────────────────

// hwpStat is a stat widget's content — and the value the clock and calendar
// set their figures as: a header (glyph and name), the value as figures with
// their units, a line under it, an optional level bar and a caption.
type hwpStat struct {
	glyph    string
	glyphInk sdl.Color
	lit      bool // the glyph takes the accent while the tile has focus
	name     string
	nums     [2]string
	units    [2]string
	n        int    // figure/unit pairs in use; 0 draws plain instead
	ref      string // the widest first figure shown, for a steady size
	plain    string // a sentence in place of the figures
	note     string // a line under the figures ("played today")
	bar      bool
	frac     float32
	barInk   sdl.Color
	capGlyph string
	capInk   sdl.Color
	caption  string
}

// hwpDrawStat paints a stat widget: the header top-left, the value under it in
// the largest face that fits, and from the bottom up the caption and the bar.
func (h *HomeLayout) hwpDrawStat(renderer *sdl.Renderer, f hwpFonts, b hwpBox, s *hwpStat, focused bool) {
	in := b.inner
	hdrH := int32(f.meta.Height())
	gap := scaleX(8, h.Width)
	ink := s.glyphInk
	if focused && s.lit {
		ink = ColorAccent
	}
	hwpGlyph(renderer, s.glyph, in.X, in.Y, hdrH, ink)
	drawText(renderer, f.meta, ellipsize(f.meta, s.name, in.W-hdrH-gap), in.X+hdrH+gap, in.Y, ColorTextMuted(), textAlignLeft)

	bottom := in.Y + in.H
	if s.caption != "" {
		capH := int32(f.meta.Height())
		bottom -= capH
		x := in.X
		if s.capGlyph != "" {
			hwpGlyph(renderer, s.capGlyph, x, bottom, capH, s.capInk)
			x += capH + scaleX(4, h.Width)
		}
		drawText(renderer, f.meta, ellipsize(f.meta, s.caption, in.X+in.W-x), x, bottom, ColorTextMuted(), textAlignLeft)
		bottom -= scaleY(8, h.Height)
	}
	if s.bar {
		barH := scaleY(8, h.Height)
		bottom -= barH
		hwpBar(renderer, sdl.Rect{X: in.X, Y: bottom, W: in.W, H: barH}, s.frac, s.barInk)
		bottom -= scaleY(8, h.Height)
	}

	top := in.Y + hdrH + scaleY(6, h.Height)
	if s.n == 0 {
		// A sentence rather than a reading: the title face, on two lines when
		// it needs them and there is room.
		lineH := int32(f.tile.Height())
		l1, l2 := hwpWrap2(f.tile, s.plain, in.W)
		if l2 != "" && top+2*lineH > bottom {
			l1, l2 = ellipsize(f.tile, s.plain, in.W), ""
		}
		drawText(renderer, f.tile, l1, in.X, top, ColorTextPrimary(), textAlignLeft)
		if l2 != "" {
			drawText(renderer, f.tile, l2, in.X, top+lineH, ColorTextPrimary(), textAlignLeft)
		}
		return
	}
	noteH := int32(0)
	if s.note != "" {
		noteH = scaleY(4, h.Height) + int32(f.meta.Height())
	}
	cands := f.bigCands(b.tall)
	font := hwpPickPairs(f, cands[:], s, in.W, bottom-top-noteH)
	base := hwpDrawPairs(renderer, f, font, s, in.X, top, ColorTextPrimary(), ColorTextMuted())
	if s.note != "" {
		drawText(renderer, f.meta, ellipsize(f.meta, s.note, in.W), in.X, base+scaleY(4, h.Height), ColorTextMuted(), textAlignLeft)
	}
}

// hwpBattery is the Battery widget's figure, made per level.
var hwpBattery struct {
	made  bool
	pct   int
	value string
}

// hwpBatteryStat is the Battery widget: the level with a bar in the status
// bar's colours, and whether it is charging.
func hwpBatteryStat() hwpStat {
	s := hwpStat{glyph: "battery", glyphInk: ColorTextMuted(), lit: true, name: "Battery"}
	pct := getBatteryPercent()
	if pct < 0 {
		s.plain = "No battery"
		return s
	}
	if pct > 100 {
		pct = 100
	}
	c := &hwpBattery
	if !c.made || c.pct != pct {
		c.made, c.pct, c.value = true, pct, strconv.Itoa(pct)
	}
	s.nums[0], s.units[0], s.n, s.ref = c.value, "%", 1, "100"
	s.bar, s.frac, s.barInk = true, float32(pct)/100, hwpLevelInk(pct)
	s.glyphInk, s.lit = s.barInk, false
	if getBatteryCharging() {
		s.capGlyph, s.capInk, s.caption = "bolt", ColorSuccess, "Charging"
	} else {
		s.caption = "On battery"
	}
	return s
}

// hwpLevelInk is the battery level's colour: the status bar's thresholds
// (statusBatteryColor) in the theme's semantic colours.
func hwpLevelInk(pct int) sdl.Color {
	switch {
	case pct >= 50:
		return ColorSuccess
	case pct >= 20:
		return ColorWarning
	}
	return ColorDanger
}

// hwpStorageStat is the Storage widget: the free space of the volume JukaHub
// runs from, with the used share as the bar.
func hwpStorageStat(now time.Time) hwpStat {
	s := hwpStat{glyph: "disk", glyphInk: ColorTextMuted(), lit: true, name: "Storage"}
	r := hwpStorageNow(now)
	switch {
	case r == nil:
		s.plain = "Reading storage…"
	case !r.ok:
		s.plain = "Storage not readable"
	default:
		s.nums[0], s.units[0], s.n = r.value, r.unit, 1
		s.bar, s.frac, s.barInk = true, r.used, hwpStorageInk(r.used)
		s.caption = r.of
	}
	return s
}

// hwpStorageInk is the used share's colour: the accent, then the warning
// colours as the volume runs out of room.
func hwpStorageInk(used float32) sdl.Color {
	switch {
	case used >= 0.95:
		return ColorDanger
	case used >= 0.9:
		return ColorWarning
	}
	return ColorAccent
}

// hwpStorageReading is one reading of the volume, with the strings the widget
// draws, made on the reader's goroutine.
type hwpStorageReading struct {
	at    time.Time
	ok    bool
	value string  // "23.4"
	unit  string  // "GB"
	of    string  // "free of 59.6 GB"
	used  float32 // the used share of the volume, 0..1
}

// hwpStorageRefresh is how old a reading may get while the widget is drawn.
// Free space moves slowly, and on Windows a read can stall on a sleeping
// drive, which is why it never runs on the render thread.
const hwpStorageRefresh = 60 * time.Second

// hwpStorage is the reader: started on the first frame that draws the widget,
// kicked by a frame that finds the reading stale.
var hwpStorage struct {
	once    sync.Once
	kick    chan struct{}
	seen    atomic.Int64 // when a frame last drew the widget, unix ns
	reading atomic.Pointer[hwpStorageReading]
}

// hwpStorageNow is the latest reading (nil until the first one lands). It
// starts the reader on first use and asks for a new reading when this one is
// older than hwpStorageRefresh; it never waits for one.
func hwpStorageNow(now time.Time) *hwpStorageReading {
	hwpStorage.once.Do(func() {
		hwpStorage.kick = make(chan struct{}, 1)
		go hwpStorageLoop(hwpStorage.kick)
	})
	hwpStorage.seen.Store(now.UnixNano())
	r := hwpStorage.reading.Load()
	if r != nil && now.Sub(r.at) > hwpStorageRefresh {
		select {
		case hwpStorage.kick <- struct{}{}:
		default:
		}
	}
	return r
}

// hwpStorageLoop reads the volume, then again whenever a frame finds the
// reading stale, or a minute on while the widget is still being drawn.
func hwpStorageLoop(kick <-chan struct{}) {
	ticker := time.NewTicker(hwpStorageRefresh)
	defer ticker.Stop()
	for {
		hwpStorageRead()
		// A frame that found the reading stale while this read was still
		// running (a sleeping drive answers slowly) has queued a request the
		// reading just taken already answers.
		select {
		case <-kick:
		default:
		}
		hwpStorageWait(kick, ticker.C)
	}
}

// hwpStorageWait blocks until the next reading is due. A tick while no frame
// has drawn the widget for two minutes — another layout, a game on screen — is
// skipped: the next frame that draws it asks again.
func hwpStorageWait(kick <-chan struct{}, tick <-chan time.Time) {
	for {
		select {
		case <-kick:
			return
		case now := <-tick:
			if !backgroundPaused() && now.Sub(time.Unix(0, hwpStorage.seen.Load())) < 2*hwpStorageRefresh {
				return
			}
		}
	}
}

// hwpStorageRead takes one reading and publishes it. A failed read keeps the
// last good reading on screen (only its age moves on), so one slow answer
// does not blank the widget.
func hwpStorageRead() {
	r := &hwpStorageReading{at: time.Now()}
	func() {
		// The readers report their errors, but one that panics must cost a
		// reading, never the app.
		defer func() { _ = recover() }()
		total, free, err := sysToolVolumeUsage(sysToolAppVolumePath())
		if err != nil || total == 0 {
			return
		}
		if free > total {
			free = total
		}
		value, unit := hwpSplitSize(formatBytes(free))
		r.value, r.unit = value, unit
		r.of = "free of " + formatBytes(total)
		r.used = float32(float64(total-free) / float64(total))
		r.ok = true
	}()
	if !r.ok {
		if prev := hwpStorage.reading.Load(); prev != nil && prev.ok {
			keep := *prev
			keep.at = r.at
			r = &keep
		}
	}
	hwpStorage.reading.Store(r)
}

// hwpSplitSize splits "23.4 GB" (formatBytes, the Storage screen's own
// wording) into the figure and its unit.
func hwpSplitSize(s string) (string, string) {
	if num, unit, ok := strings.Cut(s, " "); ok {
		return num, unit
	}
	return s, ""
}

// hwpPlay is the Playtime widget's strings: today's date key, and the minutes
// and trophy count they were made for.
var hwpPlay struct {
	made     bool
	year     int
	yday     int
	dayKey   string
	shown    int64 // minutes the strings show, -1 for nothing yet today
	trophies int
	nums     [2]string
	units    [2]string
	n        int
	plain    string
	trophy   string
}

// hwpPlaytimeStat is the Playtime widget: the time played today (the trophy
// room's own count, playtime.go) and the trophies earned so far.
func hwpPlaytimeStat(now time.Time) hwpStat {
	c := &hwpPlay
	if y, d := now.Year(), now.YearDay(); c.dayKey == "" || y != c.year || d != c.yday {
		c.year, c.yday, c.dayKey = y, d, playtimeTodayKey()
		c.made = false
	}
	secs := playtime.data.Days[c.dayKey]
	shown := int64(-1)
	if secs > 0 {
		shown = secs / 60
	}
	trophies := len(playtime.data.Unlocked)
	if !c.made || c.shown != shown || c.trophies != trophies {
		c.made, c.shown, c.trophies = true, shown, trophies
		c.nums, c.units, c.n, c.plain = hwpPlayedParts(secs)
		c.trophy = hwpTrophyText(trophies)
	}
	s := hwpStat{glyph: "games", glyphInk: ColorTextMuted(), lit: true, name: "Playtime"}
	s.nums, s.units, s.n, s.plain = c.nums, c.units, c.n, c.plain
	if s.n > 0 {
		s.note = "played today"
	}
	s.capGlyph, s.capInk, s.caption = "trophy", ColorWarning, c.trophy
	return s
}

// hwpPlayedParts splits a day's played seconds into figures and units: "1 h
// 12 m" is (1, h) (12, m), a whole hour "2 h", under an hour "12 m" and under
// a minute "<1 m"; nothing yet is the sentence "Not yet today".
func hwpPlayedParts(secs int64) (nums, units [2]string, n int, plain string) {
	if secs <= 0 {
		return nums, units, 0, "Not yet today"
	}
	m := secs / 60
	if m < 1 {
		nums[0], units[0] = "<1", "m"
		return nums, units, 1, ""
	}
	if m < 60 {
		nums[0], units[0] = strconv.FormatInt(m, 10), "m"
		return nums, units, 1, ""
	}
	nums[0], units[0] = strconv.FormatInt(m/60, 10), "h"
	if m%60 == 0 {
		return nums, units, 1, ""
	}
	nums[1], units[1] = strconv.FormatInt(m%60, 10), "m"
	return nums, units, 2, ""
}

// hwpTrophyText is the trophy line: "No trophies yet", "1 trophy", "12
// trophies".
func hwpTrophyText(n int) string {
	switch {
	case n <= 0:
		return "No trophies yet"
	case n == 1:
		return "1 trophy"
	}
	return strconv.Itoa(n) + " trophies"
}

// ── Now playing ──────────────────────────────────────────────────────────

// hwpDrawNowPlaying paints what the status bar's media chip names: a "Now
// playing" chip over the title (two lines on a wide or tall tile), or, with
// nothing playing, the music badge and where A goes.
func (h *HomeLayout) hwpDrawNowPlaying(renderer *sdl.Renderer, f hwpFonts, b hwpBox, focused bool) {
	in := b.inner
	title, ok := currentMediaTitle()
	if !ok {
		size := min32(scaleX(homeIconSize, h.Width), scaleY(homeIconSize, h.Height))
		if room := in.H - int32(f.tile.Height()) - int32(f.meta.Height()) - scaleY(8, h.Height); size > room {
			size = room
		}
		if size >= scaleY(22, h.Height) {
			hwpBadgeGlyph(renderer, sdl.Rect{X: in.X, Y: in.Y, W: size, H: size}, "music", focused, false, false)
		}
		_, music := hwpHintText()
		h.hwpLabels(renderer, f.tile, f.meta, "Nothing playing", music, in.X, in.Y+in.H, in.W, ColorTextPrimary(), ColorTextMuted())
		return
	}
	label := "NOW PLAYING"
	if mediaPaused() {
		label = "PAUSED"
	}
	chipFont, titleFont, lines := f.meta, f.tile, 1
	if b.wide || b.tall {
		chipFont, titleFont, lines = f.small, f.card, 2
	}
	// The music glyph takes the far corner only when the chip leaves room for
	// it: on the Brick's 1x1 tile the chip needs the whole line.
	gs := int32(chipFont.Height())
	glyphRoom := gs + scaleX(8, h.Width)
	labelW, _ := measureText(chipFont, label)
	showGlyph := labelW+2*scaleX(10, h.Width)+glyphRoom <= in.W
	chipMax := in.W
	if showGlyph {
		chipMax -= glyphRoom
	}
	chip := h.hwpChip(renderer, chipFont, label, in.X, in.Y, chipMax, ColorTextAccent())
	if showGlyph {
		hwpGlyph(renderer, "music", in.X+in.W-gs, chip.Y+(chip.H-gs)/2, gs, ColorAccent)
	}
	lineH := int32(titleFont.Height())
	bottom := in.Y + in.H
	if lines == 2 {
		if l1, l2 := hwpWrap2(titleFont, title, in.W); l2 != "" && bottom-2*lineH >= chip.Y+chip.H+scaleY(6, h.Height) {
			drawText(renderer, titleFont, l1, in.X, bottom-2*lineH, ColorTextPrimary(), textAlignLeft)
			drawText(renderer, titleFont, l2, in.X, bottom-lineH, ColorTextPrimary(), textAlignLeft)
			return
		}
	}
	drawText(renderer, titleFont, ellipsize(titleFont, title, in.W), in.X, bottom-lineH, ColorTextPrimary(), textAlignLeft)
}

// ── Continue (compact) ───────────────────────────────────────────────────

// hwpContinue is the compact card's chip label, made per chip text.
var hwpContinue struct {
	made   bool
	recent bool
	src    string
	chip   string
}

// hwpContinueChip is the chip's label, upper-cased: the card's own words
// (state.Chip), "Continue watching", or "Get started" on the first-run card —
// drawContinueCard's rule.
func hwpContinueChip(state HomeContinueState) string {
	c := &hwpContinue
	if c.made && c.recent == state.HasRecent && c.src == state.Chip {
		return c.chip
	}
	c.made, c.recent, c.src = true, state.HasRecent, state.Chip
	text := "Continue watching"
	if s := strings.TrimSpace(state.Chip); s != "" {
		text = s
	} else if !state.HasRecent {
		text = "Get started"
	}
	c.chip = strings.ToUpper(text)
	return c.chip
}

// hwpContinueLines is the card's title and subtitle with drawContinueCard's
// fallbacks for a missing one.
func hwpContinueLines(state HomeContinueState) (string, string) {
	title := strings.TrimSpace(state.Title)
	sub := strings.TrimSpace(state.Subtitle)
	if state.HasRecent {
		if title == "" {
			title = "Continue playing"
		}
		if sub == "" {
			sub = "Pick up where you left off."
		}
		return title, sub
	}
	if title == "" {
		title = "Your next video starts here"
	}
	if sub == "" {
		sub = "What you watch shows up here, ready to resume."
	}
	return title, sub
}

// hwpContinueThumb is drawContinueThumb with the caller's clip put back: the
// thumb clips its blooms to the card and then switches clipping off, which in
// a clipped viewport would let every tile drawn after this one spill past it.
func hwpContinueThumb(renderer *sdl.Renderer, rect sdl.Rect) {
	hadClip := renderer.IsClipEnabled()
	prev := renderer.GetClipRect()
	drawContinueThumb(renderer, rect)
	if hadClip {
		_ = renderer.SetClipRect(&prev)
	}
}
