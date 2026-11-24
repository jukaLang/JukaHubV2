package main

// hometabs_library.go — the Tabs Home's Library view (a media library's front
// page, the Library theme's own, themelibrary.go): the focused item large in
// a featured banner across the top — its picture on the right, and on the
// left a letter-spaced kicker in the accent ("FEATURED IN GAMES"), its name
// in the Library serif, its line and two buttons — then a heading and the
// page's items as one row of tall 2:3 posters. It is one of the views every
// tab and folder can be drawn in (homeTabViews: A on the strip).
//
// The posters are the view's rects (homeTabsLayout), so the D-pad walks them
// Left and Right as on the Backdrop view's row, Up and Down leave the page,
// the row scrolls to keep the focus in view, and the pointer, Dim unselected
// art and the item sheet work as on every view. The banner is drawn from the
// focused item: A plays or opens it (its first button names A), X opens its
// sheet (the second names X), and a click on the banner opens it too.
//
// The banner's picture comes from the Backdrop view's loader
// (hometabs_backdrop.go): a game's screenshot, newest save-state picture or
// box art, looked up and decoded off the render thread once the selection
// rests, fading in over the item's own cover, which shows at once. A wide
// picture fills the banner's right side; a tall one (box art) stands upright
// at its end over a dim, cropped copy of itself. The banner is a dark screen
// on every theme, so its type is white whatever the palette, and the accent
// is lifted until it reads there.

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// homeTabViewLibrary is the Library view's name (homeTabViews).
const homeTabViewLibrary = "library"

// libThemeDotsMax is the most items the banner marks with a dot each; a longer
// row leaves it to the heading's counter.
const libThemeDotsMax = 12

// The banner is a dark screen on every theme, and so are the posters' faces
// (see the file comment).
var (
	libThemeHeroFill   = sdl.Color{R: 10, G: 11, B: 14, A: 250}
	libThemePosterFace = sdl.Color{R: 24, G: 26, B: 31, A: 255}
	libThemeScrim      = sdl.Color{R: 8, G: 9, B: 12, A: 255}
	libThemeWhite      = sdl.Color{R: 255, G: 255, B: 255, A: 255}
	libThemeMuted      = sdl.Color{R: 204, G: 207, B: 213, A: 255}
)

// ── Geometry ─────────────────────────────────────────────────────────────

// homeLibViewFrame is the Library view laid out in a body, in the page's
// space (the body's top-left at 0,0): the banner across the top, the heading
// line under it, the row of posters along the bottom.
type homeLibViewFrame struct {
	hero             sdl.Rect
	headY, headH     int32
	rowY             int32
	posterW, posterH int32
	gap              int32
}

// homeLibViewFrameFor lays the Library view out in a bw×bh body; k is the
// canvas scale (homeTabsScale). The posters take about two fifths of the
// height, shaped 2:3 like a film's; the banner gets the rest, and never less
// than a third of it. Pure.
func homeLibViewFrameFor(bw, bh int32, k float64) homeLibViewFrame {
	sz := func(v float64) int32 { return int32(math.Round(v * k)) }
	var f homeLibViewFrame
	f.gap = max32(8, sz(18))
	sp := max32(8, sz(14))
	f.headH = max32(22, sz(34))
	f.posterH = min32(max32(bh*38/100, 1), max32(sz(250), 96))
	if least := bh / 3; bh-f.posterH-f.headH-2*sp < least {
		f.posterH = max32(bh-least-f.headH-2*sp, 1)
	}
	f.posterW = max32(f.posterH*2/3, 1)
	f.hero = sdl.Rect{W: max32(bw, 1), H: max32(bh-f.posterH-f.headH-2*sp, 1)}
	f.headY = f.hero.H + sp
	f.rowY = f.headY + f.headH + sp
	return f
}

// homeTabsLibraryLayout lays n posters out as one row along the bottom of a
// bw×bh body (homeTabsLayout); the room above is the banner's and the
// heading's. Pure.
func homeTabsLibraryLayout(n int, bw, bh int32, k float64, out []sdl.Rect) ([]sdl.Rect, bool, int32) {
	if n <= 0 {
		return out, true, 0
	}
	f := homeLibViewFrameFor(bw, bh, k)
	for i := 0; i < n; i++ {
		out = append(out, sdl.Rect{X: int32(i) * (f.posterW + f.gap), Y: f.rowY, W: f.posterW, H: f.posterH})
	}
	last := out[len(out)-1]
	return out, true, last.X + last.W
}

// ── The page ─────────────────────────────────────────────────────────────

// tabsDrawLibrary draws the Library view's page: the banner for the focused
// item, the heading and the row of posters (tabsDrawLevel's arguments).
func (h *HomeLayout) tabsDrawLibrary(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, g *homeTabsGeom, body sdl.Rect,
	dx, scroll int32, clip sdl.Rect, focused, pressed bool, lift, depth float64, alpha uint8, live bool, dimOf func(int) float64) {
	if len(g.rects) == 0 {
		return
	}
	if live && homeTabsBackdropBusy {
		homeTabs.anim = true
	}
	homeTabsBackdropBusy = false
	f := homeLibViewFrameFor(body.W, body.H, homeTabsScale(h.Width, h.Height))
	hero := sdl.Rect{X: body.X + dx + f.hero.X, Y: body.Y + f.hero.Y, W: f.hero.W, H: f.hero.H}
	h.libraryDrawHero(renderer, fonts, lvl, hero, alpha, live)
	h.libraryDrawHeading(renderer, fonts, lvl, sdl.Rect{X: body.X + dx, Y: body.Y + f.headY, W: body.W, H: f.headH}, alpha)

	place := func(i int) sdl.Rect {
		r := g.rects[i]
		r.X += body.X + dx - scroll
		r.Y += body.Y
		return r
	}
	drawOne := func(i int) {
		r := place(i)
		if !homeRectsOverlap(r, clip) {
			return
		}
		isFocus := focused && i == lvl.focus
		tileLift, tilePress, tileDepth := 0.0, false, 0.0
		if isFocus {
			tileLift, tilePress, tileDepth = lift, pressed, depth
		}
		fav := i < len(lvl.favs) && lvl.favs[i]
		h.libraryPaintPoster(renderer, fonts, lvl.items[i], r, isFocus, tilePress, tileLift, tileDepth, fav, alpha, dimOf(i))
		if live {
			homeTabs.hits = append(homeTabs.hits, homeTabsHit{i: i, r: glassIntersect(r, clip)})
		}
		_ = renderer.SetClipRect(&clip)
	}
	for i := range lvl.items {
		if i < len(g.rects) && !(focused && i == lvl.focus) {
			drawOne(i)
		}
	}
	if focused && lvl.focus >= 0 && lvl.focus < len(lvl.items) && lvl.focus < len(g.rects) {
		drawOne(lvl.focus)
	}
	if live && lvl.focus >= 0 && lvl.focus < len(lvl.items) {
		// The banner is the focused item's: a click on it opens that item.
		// After the posters, so a poster under the pointer wins.
		homeTabs.hits = append(homeTabs.hits, homeTabsHit{i: lvl.focus, r: glassIntersect(hero, clip)})
	}
}

// libraryDrawHeading writes the row's heading in the serif — "Next up" on
// Recent, the page's name elsewhere — and, at its right end, where the
// focused item is in the row ("3 / 12").
func (h *HomeLayout) libraryDrawHeading(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, area sdl.Rect, alpha uint8) {
	title := lvl.title
	if lvl.key == homeTabRecent && !lvl.folder.Folder {
		title = "Next up"
	}
	title = uiT(title)
	font := libThemeSerifOr(26, title, fonts.Card)
	if font == nil {
		return
	}
	countW := int32(0)
	if n := len(lvl.items); n > 1 && fonts.Small != nil {
		c := libThemeCount(lvl.focus, n)
		countW, _ = measureText(fonts.Small, c)
		drawTextRaw(renderer, fonts.Small, c, area.X+area.W, area.Y+(area.H-int32(fonts.Small.Height()))/2,
			homeTabsInk(ColorTextMuted(), alpha), textAlignRight)
	}
	if room := area.W - countW - scaleX(24, h.Width); room > 0 {
		drawTextRaw(renderer, font, ellipsize(font, title, room), area.X, area.Y+(area.H-int32(font.Height()))/2,
			homeTabsInk(ColorTextPrimary(), alpha), textAlignLeft)
	}
}

// libThemeCount is "i+1 / n", remade only when either changes.
func libThemeCount(i, n int) string {
	l := &homeLibTheme
	if l.count == "" || l.countI != i || l.countN != n {
		l.countI, l.countN = i, n
		l.count = strconv.Itoa(i+1) + " / " + strconv.Itoa(n)
	}
	return l.count
}

// ── The banner ───────────────────────────────────────────────────────────

// libraryDrawHero draws the banner for the page's focused item in hero.
func (h *HomeLayout) libraryDrawHero(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, hero sdl.Rect, alpha uint8, live bool) {
	if hero.W < scaleX(240, h.Width) || hero.H < scaleY(100, h.Height) {
		return
	}
	radius := min32(scaleY(18, h.Height), hero.H/4)
	round := [4]int32{radius, radius, radius, radius}
	// Its shadow, then the dark screen.
	libThemeFillRounded(renderer, sdl.Rect{X: hero.X, Y: hero.Y + scaleY(8, h.Height), W: hero.W, H: hero.H}, round,
		homeTabsInk(sdl.Color{A: 80}, alpha))
	libThemeFillRounded(renderer, hero, round, homeTabsInk(libThemeHeroFill, alpha))
	if lvl.focus >= 0 && lvl.focus < len(lvl.items) {
		it := lvl.items[lvl.focus]
		accent := libThemeAccentOnDark()
		h.libraryDrawHeroArt(renderer, it, hero, radius, alpha)
		h.libraryDrawHeroText(renderer, fonts, lvl, it, hero, accent, alpha, live)
		h.libraryDrawHeroDots(renderer, lvl, hero, accent, alpha)
	}
	libThemeStrokeRounded(renderer, hero, radius, 1, homeTabsInk(sdl.Color{R: 255, G: 255, B: 255, A: 28}, alpha))
}

// libraryDrawHeroArt fills the banner's right side with the focused item's
// picture (libraryDrawPicture): its cover at once, the game's own picture
// fading in over it once it is loaded; an item with neither gets its glyph,
// large and quiet. The banner's dark left side then fades over the picture's
// edge, and a soft dark floor under the dots.
func (h *HomeLayout) libraryDrawHeroArt(renderer *sdl.Renderer, it homeTabItem, hero sdl.Rect, radius int32, alpha uint8) {
	left := hero.W * 36 / 100
	area := sdl.Rect{X: hero.X + left, Y: hero.Y, W: hero.W - left, H: hero.H}
	var cover *sdl.Texture
	if it.Art != nil {
		cover = it.Art()
	}
	wide, p := libThemeHeroPicture(it)
	switch {
	case cover != nil:
		h.libraryDrawPicture(renderer, cover, area, radius, alpha)
	case wide == nil:
		glyph := it.Glyph
		if glyph == "" {
			glyph = "apps"
		}
		gs := min32(area.H*62/100, area.W/2)
		drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: area.X + area.W - gs - area.H*20/100, Y: area.Y + (area.H-gs)/2, W: gs, H: gs},
			homeTabsInk(sdl.Color{R: 255, G: 255, B: 255, A: 46}, alpha))
	}
	if wide != nil && p > 0 {
		h.libraryDrawPicture(renderer, wide, area, radius, uint8(math.Round(float64(alpha)*p)))
	}
	edge := libThemeHeroFill
	edge.A = alpha
	gone := edge
	gone.A = 0
	libThemeFillRamp(renderer, sdl.Rect{X: area.X, Y: area.Y, W: area.W * 46 / 100, H: area.H}, [4]int32{}, edge, gone, true)
	floorH := area.H / 3
	floor := sdl.Color{A: uint8(uint32(110) * uint32(alpha) / 255)}
	libThemeFillRamp(renderer, sdl.Rect{X: area.X, Y: area.Y + area.H - floorH, W: area.W, H: floorH}, [4]int32{0, 0, radius, 0},
		sdl.Color{}, floor, false)
}

// libraryDrawPicture draws tex in the banner's picture area: a wide picture
// (a screen capture) cropped to fill it, a tall or square one (box art)
// standing upright at its right end over a dim, cropped copy of itself.
func (h *HomeLayout) libraryDrawPicture(renderer *sdl.Renderer, tex *sdl.Texture, area sdl.Rect, radius int32, alpha uint8) {
	if tex == nil || alpha == 0 || area.W <= 0 || area.H <= 0 {
		return
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return
	}
	right := [4]int32{0, radius, radius, 0}
	if int64(tw)*4 >= int64(th)*5 {
		// Wider than 5:4: across the whole area.
		libThemeCopyCover(renderer, tex, area, right, sdl.Color{R: 255, G: 255, B: 255, A: alpha})
		return
	}
	libThemeCopyCover(renderer, tex, area, right, sdl.Color{R: 92, G: 92, B: 98, A: alpha})
	pad := area.H * 9 / 100
	box := sdl.Rect{X: area.X, Y: area.Y + pad, W: area.W - pad, H: area.H - 2*pad}
	if box.W <= 0 || box.H <= 0 {
		return
	}
	fit := homeTabsBackdropFit(tex, box)
	fit.X = box.X + box.W - fit.W
	r := min32(scaleY(10, h.Height), fit.W/6)
	all := [4]int32{r, r, r, r}
	libThemeFillRounded(renderer, sdl.Rect{X: fit.X + scaleX(6, h.Width), Y: fit.Y + scaleY(10, h.Height), W: fit.W, H: fit.H}, all,
		sdl.Color{A: uint8(uint32(110) * uint32(alpha) / 255)})
	libThemeCopyCover(renderer, tex, fit, all, sdl.Color{R: 255, G: 255, B: 255, A: alpha})
}

// libThemeHeroPicture is the focused game's picture from the Backdrop view's
// loader and how far it has faded in (0..1), or nil while there is none for
// this item: not a game, not looked up yet, or still being decoded.
func libThemeHeroPicture(it homeTabItem) (*sdl.Texture, float64) {
	b := &homeTabsBackdrop
	path := homeTabsBackdropGame(it)
	if path == "" || b.tex == nil || b.texPath != path || b.want != path {
		return nil, 0
	}
	l := &homeLibTheme
	if l.picTex != b.tex {
		l.picTex = b.tex
		if homeLayout != nil && homeLayout.Motion {
			l.picFade.begin(uiClockMS(), homeTabsBackdropFadeMS)
		} else {
			l.picFade.cancel()
		}
	}
	if !l.picFade.active {
		return b.tex, 1
	}
	p := uiEaseOutCubic(l.picFade.progress(uiClockMS()))
	if l.picFade.active {
		homeTabs.anim = true
	}
	return b.tex, p
}

// libraryDrawHeroText sets the banner's left side as one block centred down
// it: the kicker, the name (one or two lines of the serif), its line and the
// two buttons. A short banner drops the line, then the name's second line.
func (h *HomeLayout) libraryDrawHeroText(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, it homeTabItem, hero sdl.Rect,
	accent sdl.Color, alpha uint8, live bool) {
	kf, sf, bf := fonts.Meta, fonts.Helper, fonts.Small
	if kf == nil || sf == nil || bf == nil {
		return
	}
	pad := scaleX(48, h.Width)
	x := hero.X + pad
	colW := hero.W*50/100 - pad
	if colW < scaleX(120, h.Width) {
		return
	}
	kicker := libThemeKicker(lvl)
	titleFont, lines := h.libraryTitle(fonts, uiT(it.Title), colW)
	if titleFont == nil {
		return
	}
	sub := uiT(it.Sub)
	kh, th, sh := int32(kf.Height()), int32(titleFont.Height()), int32(sf.Height())
	btnH := max32(scaleY(40, h.Height), int32(bf.Height())+scaleY(14, h.Height))
	gapK, gapS, gapB := scaleY(8, h.Height), scaleY(6, h.Height), scaleY(18, h.Height)
	room := hero.H - 2*scaleY(16, h.Height)
	block := func() int32 {
		b := kh + gapK + int32(len(lines))*th + gapB + btnH
		if sub != "" {
			b += gapS + sh
		}
		return b
	}
	if block() > room {
		sub = ""
	}
	if block() > room && len(lines) > 1 {
		lines = lines[:1]
	}
	y := hero.Y + (hero.H-block())/2
	drawTextRaw(renderer, kf, kicker, x, y, homeTabsInk(accent, alpha), textAlignLeft)
	y += kh + gapK
	ink := libThemeWhite
	if it.Dim {
		ink = libThemeMuted
	}
	for _, line := range lines {
		drawTextRaw(renderer, titleFont, line, x, y, homeTabsInk(ink, alpha), textAlignLeft)
		y += th
	}
	if sub != "" {
		y += gapS
		drawTextRaw(renderer, sf, ellipsize(sf, sub, colW), x, y, homeTabsInk(libThemeMuted, alpha), textAlignLeft)
		y += sh
	}
	y += gapB
	primary := "Open"
	switch {
	case it.Key == "x:continue":
		primary = "Resume"
	case libThemePlayable(it):
		primary = "Play"
	}
	first := h.libraryButton(renderer, bf, homeHintKey("A", primary), uiT(primary), x, y, btnH,
		accent, contrastText(accent), sdl.Color{}, alpha)
	options := h.libraryButton(renderer, bf, homeHintKey("X", "Options"), uiT("Options"), first.X+first.W+scaleX(12, h.Width), y, btnH,
		sdl.Color{R: 30, G: 33, B: 40, A: 235}, libThemeWhite, sdl.Color{R: 255, G: 255, B: 255, A: 46}, alpha)
	if live {
		homeLibTheme.options = options
	}
}

// libraryButton draws one of the banner's buttons at (x, y), bh tall: fill
// (edge outlines it when it has alpha), the controller button's key in a
// small badge, then the label in ink. It returns the button's rect.
func (h *HomeLayout) libraryButton(renderer *sdl.Renderer, font *ttf.Font, key, label string, x, y, bh int32,
	fill, ink, edge sdl.Color, alpha uint8) sdl.Rect {
	if font == nil {
		return sdl.Rect{}
	}
	fh := int32(font.Height())
	kw, _ := measureText(font, key)
	lw, _ := measureText(font, label)
	d := bh * 60 / 100
	badgeW := max32(d, kw+scaleX(12, h.Width))
	padL, gap, padR := scaleX(9, h.Width), scaleX(8, h.Width), scaleX(18, h.Width)
	r := sdl.Rect{X: x, Y: y, W: padL + badgeW + gap + lw + padR, H: bh}
	radius := bh / 4
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, homeTabsInk(fill, alpha))
	if edge.A > 0 {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, homeTabsInk(edge, alpha))
	}
	badge := sdl.Rect{X: r.X + padL, Y: r.Y + (bh-d)/2, W: badgeW, H: d}
	disc := ink
	disc.A = 46
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, d/2, homeTabsInk(disc, alpha))
	drawTextRaw(renderer, font, key, badge.X+badge.W/2, badge.Y+(d-fh)/2, homeTabsInk(ink, alpha), textAlignCenter)
	drawTextRaw(renderer, font, label, badge.X+badge.W+gap, r.Y+(bh-fh)/2, homeTabsInk(ink, alpha), textAlignLeft)
	return r
}

// libraryDrawHeroDots marks the focused item's place in the row at the
// banner's bottom-right: a dot per item and an accent bar for the focused
// one, on a dark capsule. Past libThemeDotsMax the heading's counter says it.
func (h *HomeLayout) libraryDrawHeroDots(renderer *sdl.Renderer, lvl *homeTabsLevel, hero sdl.Rect, accent sdl.Color, alpha uint8) {
	n := len(lvl.items)
	if n < 2 || n > libThemeDotsMax {
		return
	}
	d := max32(4, scaleY(8, h.Height))
	gap := max32(4, scaleX(8, h.Width))
	bar := max32(d*2, scaleX(22, h.Width))
	total := int32(n-1)*(d+gap) + bar
	pad := scaleX(10, h.Width)
	right := hero.X + hero.W - scaleX(28, h.Width)
	cy := hero.Y + hero.H - scaleY(26, h.Height)
	capH := d + pad
	fillRoundedRect(renderer, right-total-pad, cy-capH/2, total+2*pad, capH, capH/2, homeTabsInk(sdl.Color{A: 110}, alpha))
	x := right - total
	for i := 0; i < n; i++ {
		if i == lvl.focus {
			fillRoundedRect(renderer, x, cy-d/2, bar, d, d/2, homeTabsInk(accent, alpha))
			x += bar + gap
			continue
		}
		fillRoundedRect(renderer, x, cy-d/2, d, d, d/2, homeTabsInk(sdl.Color{R: 255, G: 255, B: 255, A: 150}, alpha))
		x += d + gap
	}
}

// libThemeKicker is the line over the banner's name: letter-spaced capitals,
// "FEATURED IN GAMES" ("JUMP BACK IN" on Recent), in the interface language.
// Remade only when the page or the language changes.
func libThemeKicker(lvl *homeTabsLevel) string {
	l := &homeLibTheme
	lang := uiLanguageCode()
	if l.kicker != "" && l.kickTab == lvl.key && l.kickTitle == lvl.title && l.kickLang == lang {
		return l.kicker
	}
	src := "Jump back in"
	if lvl.key != homeTabRecent || lvl.folder.Folder {
		src = "Featured in " + lvl.title
	}
	l.kickTab, l.kickTitle, l.kickLang = lvl.key, lvl.title, lang
	l.kicker = libThemeTrack(strings.ToUpper(uiT(src)))
	return l.kicker
}

// libThemeTrack letter-spaces s with thin spaces (a fifth of an em in Inter),
// the way the kicker is set.
func libThemeTrack(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 4)
	for i, r := range s {
		if i > 0 {
			b.WriteRune('\u2009')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// libraryTitle is the banner's name laid out for a column w pixels wide: one
// line of the large serif when it fits, else two, else two lines of the
// smaller cut ending in "…". A name the serif cannot draw is set in Inter.
// The answer is kept for the name on screen.
func (h *HomeLayout) libraryTitle(fonts HomeFonts, text string, w int32) (*ttf.Font, []string) {
	// The faces in force key the answer too: a text-size, Bold text or
	// language change opens new ones.
	serif := libThemeSerifFont(uiLegibleFontSize(46))
	l := &homeLibTheme
	if l.titleText == text && l.titleW == w && l.titleSerif == serif && l.titleInter == fonts.Greeting && l.titleFont != nil {
		return l.titleFont, l.titleLines
	}
	big := libThemeSerifOr(46, text, fonts.Greeting)
	small := libThemeSerifOr(36, text, fonts.Card)
	if small == nil {
		small = big
	}
	font, lines := big, []string(nil)
	if big != nil {
		if tw, _ := measureText(big, text); tw <= w {
			lines = []string{text}
		} else if ls := wrapTextLinesRaw(big, text, w, 8); len(ls) <= 2 {
			lines = ls
		} else {
			font = small
			lines = wrapTextLinesRaw(small, text, w, 2)
		}
	}
	l.titleText, l.titleW, l.titleSerif, l.titleInter, l.titleFont, l.titleLines = text, w, serif, fonts.Greeting, font, lines
	return font, lines
}

// libThemeAccentOnDark is the theme's accent, lifted until it reads as text on
// the banner (4.5:1): the kicker, the A button and the dots.
func libThemeAccentOnDark() sdl.Color {
	l := &homeLibTheme
	src := ColorAccent
	src.A = 255
	if l.accentOK && l.accentSrc == src {
		return l.accent
	}
	dark := libThemeHeroFill
	dark.A = 255
	c := src
	if wcagContrast(c, dark) < 4.5 {
		hue, sat, light := rgbToHSL(c)
		for i := 0; i < 100 && wcagContrast(c, dark) < 4.5; i++ {
			light = math.Min(1, light+0.01)
			c = hslToRGB(hue, sat, light)
			c.A = 255
		}
	}
	l.accentSrc, l.accent, l.accentOK = src, c, true
	return c
}

// homeLibThemeOptionsClick is a click on the banner's Options button: the
// focused item's sheet, as X opens it. It reports whether the click was the
// button's.
func homeLibThemeOptionsClick(c *Config, mx, my int32) bool {
	if homeLayout == nil || !pointInRect(mx, my, homeLibTheme.options) {
		return false
	}
	if homeLayout.Focus.Set(homeTabsPageID) {
		homeLayout.Focus.Remember()
	}
	homeTabsX(c)
	return true
}

// ── The posters ──────────────────────────────────────────────────────────

// libraryPaintPoster paints one poster: a tall card with the item's cover
// filling it (or its glyph on the dark face), its name on a dark ramp along
// the bottom, a play mark over a cover that plays, and the corner's badge and
// heart. The focused one is lifted and ringed. dim greys the cover (Dim
// unselected art, homelook.go).
func (h *HomeLayout) libraryPaintPoster(renderer *sdl.Renderer, fonts HomeFonts, it homeTabItem, base sdl.Rect, focused, pressed bool,
	lift, depth float64, fav bool, alpha uint8, dim float64) {
	draw := scaleHomeCard(base, lift)
	draw.Y += homePressOffset(depth, h.Height)
	radius := max32(min32(homeTileRadius, draw.W/5), 2)
	all := [4]int32{radius, radius, radius, radius}
	ring := homeTabsTileRing(focused)
	if ring {
		drawHomeFocusGlow(renderer, draw)
	}
	libThemeFillRounded(renderer, sdl.Rect{X: draw.X, Y: draw.Y + scaleY(6, h.Height), W: draw.W, H: draw.H}, all,
		homeTabsInk(sdl.Color{A: 70}, alpha))
	libThemeFillRounded(renderer, draw, all, homeTabsInk(libThemePosterFace, alpha))
	var art *sdl.Texture
	if it.Art != nil {
		art = it.Art()
	}
	if art != nil {
		dim = uiClamp01(dim)
		m := uint8(255 - dim*float64(255-homeLookDimMod) + 0.5)
		libThemeCopyCover(renderer, art, draw, all, sdl.Color{R: m, G: m, B: m, A: alpha})
		if dim > 0.01 {
			wash := homeLookWashColor()
			wash.A = uint8(dim*homeLookDimWash*float64(alpha)/255 + 0.5)
			libThemeFillRounded(renderer, draw, all, wash)
		}
	} else {
		glyph := it.Glyph
		if glyph == "" {
			glyph = "apps"
		}
		gs := draw.W * 40 / 100
		ink := sdl.Color{R: 255, G: 255, B: 255, A: 215}
		if it.Dim {
			ink.A = 110
		}
		drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: draw.X + (draw.W-gs)/2, Y: draw.Y + draw.H*38/100 - gs/2, W: gs, H: gs}, homeTabsInk(ink, alpha))
	}
	// The name stands on a dark ramp that settles into a flat band, so
	// lettering in the cover cannot show through it.
	rampTop, flatTop := draw.Y+draw.H*48/100, draw.Y+draw.H*78/100
	shade := libThemeScrim
	shade.A = uint8(uint32(225) * uint32(alpha) / 255)
	gone := shade
	gone.A = 0
	libThemeFillRamp(renderer, sdl.Rect{X: draw.X, Y: rampTop, W: draw.W, H: flatTop - rampTop}, [4]int32{}, gone, shade, false)
	libThemeFillRounded(renderer, sdl.Rect{X: draw.X, Y: flatTop, W: draw.W, H: draw.Y + draw.H - flatTop}, [4]int32{0, 0, radius, radius}, shade)
	if font := fonts.Helper; font != nil {
		pad := max32(scaleX(10, h.Width), draw.W/14)
		if textW := draw.W - 2*pad; textW > 0 {
			ink := libThemeWhite
			if it.Dim {
				ink = libThemeMuted
			}
			drawTextRaw(renderer, font, ellipsize(font, uiT(it.Title), textW), draw.X+pad, draw.Y+draw.H-pad-int32(font.Height()),
				homeTabsInk(ink, alpha), textAlignLeft)
		}
	}
	if art != nil && libThemePlayable(it) {
		h.libraryPlayMark(renderer, draw, focused, alpha)
	}
	libThemeStrokeRounded(renderer, draw, radius, 1, homeTabsInk(sdl.Color{R: 255, G: 255, B: 255, A: 30}, alpha))
	h.tabsPaintCorner(renderer, fonts, it, draw, fav, alpha)
	if ring {
		drawHomeFocusRing(renderer, base, draw, radius)
	}
}

// libThemePlayable reports whether A plays the item: a game (by its ref), or
// today's challenge, which Recent lists without one. A console opens its
// games, so it is not.
func libThemePlayable(it homeTabItem) bool {
	if it.Folder {
		return false
	}
	return strings.HasPrefix(it.Ref, homeTabRefGame) || (it.Ref == "" && it.Glyph == "emulator")
}

// libraryPlayMark is the round play button over a poster's cover, a little
// larger and brighter on the focused one.
func (h *HomeLayout) libraryPlayMark(renderer *sdl.Renderer, draw sdl.Rect, focused bool, alpha uint8) {
	r := draw.W * 15 / 100
	ringA := uint8(205)
	if focused {
		r, ringA = draw.W*17/100, 255
	}
	if r < 6 {
		return
	}
	cx, cy := draw.X+draw.W/2, draw.Y+draw.H*42/100
	fillCircle(renderer, cx, cy, r, homeTabsInk(sdl.Color{A: 96}, alpha))
	edge := homeTabsInk(sdl.Color{R: 255, G: 255, B: 255, A: ringA}, alpha)
	strokeCircle(renderer, cx, cy, r, edge)
	strokeCircle(renderer, cx, cy, r-1, edge)
	tri := r * 8 / 10
	drawHomeGlyphTinted(renderer, "play", sdl.Rect{X: cx - tri/2 + tri/10, Y: cy - tri/2, W: tri, H: tri}, edge)
}
