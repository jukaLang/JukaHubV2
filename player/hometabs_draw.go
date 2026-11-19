package main

// hometabs_draw.go — drawing the Tabs Home (hometabs.go): the strip of tabs
// in the middle of the top bar and the page in its view.
//
// The strip sits in the free span between the wordmark and the system
// controls (drawHeader measures it, tabsNoteSpan), centred on the screen
// where it can be, at most 55% of the width. The active tab wears a glass pill
// in the accent that glides from tab to tab; the others are muted. A soft
// band behind the tabs fades to nothing at both ends (one cached 1-pixel-tall
// ramp texture, stretched), and when the tabs do not all fit the strip
// scrolls to keep the active one centred, the labels fading out towards the
// ends. With Tabs position Bottom (homelook.go) the strip has a low bar of
// its own over the footer instead (tabsBottomBar) and the page ends above it.
//
// The look (homelook.go): every cover but the selected one is dimmed, and the
// page fades at an end of its scroll axis with more past it (listfade.go).
//
// Every view is a list of rects in the page's own space (tabsGeom), cached by
// view, count and size: the D-pad steps by them, the page scrolls along one
// axis to keep the focused one in view, and the painters draw them.

import (
	"math"
	"strings"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// homeTabsArtSide is the size covers are baked at for the tiles: one bake
// per game whatever the view (homeSwArtTexture).
const homeTabsArtSide int32 = 200

// homeTabsArt is a game's cover for a tile, from the cache (nil while it is
// being baked).
func homeTabsArt(job homePreviewJob) *sdl.Texture {
	if homeTabs.renderer == nil {
		return nil
	}
	return homeSwArtTexture(homeTabs.renderer, job, homeTabsArtSide, homeTabsArtSide)
}

// ── Geometry ─────────────────────────────────────────────────────────────

// homeTabsGeom is one view laid out for n items in a body: each item's rect
// in the page's space (the body's top-left at 0,0, nothing scrolled), the
// axis it scrolls along and how long it is along it.
type homeTabsGeom struct {
	ok     bool
	view   string
	n      int
	hero   bool
	body   sdl.Rect
	cw, ch int32
	rects  []sdl.Rect
	horiz  bool
	extent int32
}

// Two layouts are kept: a slide draws the page leaving and the one coming.
var (
	homeTabsGeoms    [2]homeTabsGeom
	homeTabsGeomNext int
)

// tabsBody is the page's box: under the top bar, over the footer, inside the
// side margins; a sub-page gives its first line to the title and Back.
func (h *HomeLayout) tabsBody(lvl *homeTabsLevel) sdl.Rect {
	top := headerBarHeight() + scaleY(24, h.Height)
	if lvl != nil && lvl.folder.Folder {
		top += scaleY(44, h.Height)
	}
	bottom := scaleY(664, h.Height) - scaleY(16, h.Height)
	if homeTabsStripBottom() {
		// The strip's bar over the footer takes the page's last band.
		bottom = h.tabsBottomBar().Y - scaleY(10, h.Height)
	}
	x := scaleX(40, h.Width)
	return sdl.Rect{X: x, Y: top, W: max32(h.Width-2*x, 1), H: max32(bottom-top, 1)}
}

// tabsBottomBar is the bar the strip sits in when Tabs position is Bottom
// (homelook.go): across the page, just over the footer (drawFooter's band
// starts at 664 of the 720-high design canvas). It is kept low — one line of
// tabs — so the 4:3 Brick canvas loses little of the page to it.
func (h *HomeLayout) tabsBottomBar() sdl.Rect {
	x := scaleX(40, h.Width)
	bh := scaleY(44, h.Height)
	return sdl.Rect{X: x, Y: scaleY(664, h.Height) - scaleY(6, h.Height) - bh, W: max32(h.Width-2*x, 1), H: bh}
}

// homeTabsScale is the canvas's scale for sizes that stay square: the
// smaller of across and down, so a 4:3 canvas does not stretch the tiles.
func homeTabsScale(cw, ch int32) float64 {
	return math.Min(float64(cw)/float64(HomeBaseWidth), float64(ch)/float64(HomeBaseHeight))
}

// tabsGeom is the layout of view for n items in body, from the cache.
func (h *HomeLayout) tabsGeom(view string, n int, hero bool, body sdl.Rect) *homeTabsGeom {
	for i := range homeTabsGeoms {
		g := &homeTabsGeoms[i]
		if g.ok && g.view == view && g.n == n && g.hero == hero && g.body == body && g.cw == h.Width && g.ch == h.Height {
			return g
		}
	}
	g := &homeTabsGeoms[homeTabsGeomNext]
	homeTabsGeomNext = (homeTabsGeomNext + 1) % len(homeTabsGeoms)
	g.ok, g.view, g.n, g.hero, g.body, g.cw, g.ch = true, view, n, hero, body, h.Width, h.Height
	g.rects, g.horiz, g.extent = homeTabsLayout(view, n, hero, body.W, body.H, homeTabsScale(h.Width, h.Height), g.rects[:0])
	return g
}

// homeTabsLayout lays out n items of view in a bw×bh body; k is the canvas
// scale. Pure.
func homeTabsLayout(view string, n int, hero bool, bw, bh int32, k float64, out []sdl.Rect) ([]sdl.Rect, bool, int32) {
	sz := func(v float64) int32 { return int32(math.Round(v * k)) }
	gap := max32(6, sz(16))
	if n <= 0 {
		return out, view == homeTabViewRow || view == homeTabViewCarousel, 0
	}
	switch view {
	case homeTabViewRow:
		// One or two rows of large tiles, filled column by column, that
		// scroll left and right; a hero stands two rows tall at the front.
		side := min32((bh-gap)/2, sz(236))
		rows := int32(1)
		if visible := (bw + gap) / max32(side+gap, 1); int32(n) > visible {
			rows = 2
		}
		if rows == 1 {
			side = min32(bh, sz(280))
		}
		block := rows*side + (rows-1)*gap
		y0 := (bh - block) / 2
		x, start := int32(0), 0
		if hero {
			w := side * 3 / 2
			if rows == 1 {
				w = side * 16 / 10
			}
			out = append(out, sdl.Rect{X: 0, Y: y0, W: w, H: block})
			x, start = w+gap, 1
		}
		for i := start; i < n; i++ {
			j := int32(i - start)
			out = append(out, sdl.Rect{X: x + (j/rows)*(side+gap), Y: y0 + (j%rows)*(side+gap), W: side, H: side})
		}
		last := out[len(out)-1]
		return out, true, last.X + last.W
	case homeTabViewList:
		rowH := max32(40, sz(64))
		step := rowH + max32(4, sz(6))
		w := bw * 42 / 100
		for i := 0; i < n; i++ {
			out = append(out, sdl.Rect{X: 0, Y: int32(i) * step, W: w, H: rowH})
		}
		return out, false, int32(n)*step - (step - rowH)
	case homeTabViewCarousel:
		// One big tile in the middle; the page scrolls to centre the focus
		// and the painter shrinks the neighbours (tabsDrawLevel).
		side := min32(bh-sz(70), sz(340))
		pitch := side*88/100 + gap
		y := (bh - side) / 2
		for i := 0; i < n; i++ {
			out = append(out, sdl.Rect{X: int32(i) * pitch, Y: y, W: side, H: side})
		}
		return out, true, int32(n-1)*pitch + side
	}
	// The default grid gives covers and card metadata room to read.
	// Compact keeps the old dense layout for people who want more at once.
	target, capSide := sz(210), sz(260)
	if view == homeTabViewCompact {
		target, capSide = sz(104), 0
	}
	cols := (bw + gap) / max32(target+gap, 1)
	if cols < 1 {
		cols = 1
	}
	if view == homeTabViewGrid {
		// Six cards work better as two complete rows of three. Seven and
		// eight get four columns; fuller pages use the normal five.
		wanted := int32(n)
		switch {
		case n == 6:
			wanted = 3
		case n == 7 || n == 8:
			wanted = 4
		case n > 8:
			wanted = 5
		}
		cols = min32(cols, wanted)
	}
	side := max32(1, (bw-(cols-1)*gap)/cols)
	if capSide > 0 {
		side = min32(side, capSide)
	}
	rows := (int32(n) + cols - 1) / cols
	if view == homeTabViewGrid && rows <= 2 {
		// Leave room for the focus ring, including with the tabs at the
		// bottom where the page is shorter.
		side = min32(side, max32(1, (bh-(rows-1)*gap-2*sz(14))/rows))
	}
	gridW := cols*side + (cols-1)*gap
	gridH := rows*side + (rows-1)*gap
	x0, y0 := int32(0), int32(0)
	if view == homeTabViewGrid {
		x0 = max32(0, (bw-gridW)/2)
		if gridH <= bh {
			y0 = (bh - gridH) / 2
		}
	}
	for i := 0; i < n; i++ {
		r, c := int32(i)/cols, int32(i)%cols
		rowX := x0
		if view == homeTabViewGrid && n <= 10 && r == rows-1 {
			rowCols := int32(n) - r*cols
			rowW := rowCols*side + (rowCols-1)*gap
			rowX = max32(0, (bw-rowW)/2)
		}
		out = append(out, sdl.Rect{X: rowX + c*(side+gap), Y: y0 + r*(side+gap), W: side, H: side})
	}
	return out, false, y0 + gridH
}

// homeTabsScrollTarget is where the level's page should be scrolled to: the
// focused item in view with room for its ring, never past the ends; the
// carousel centres it. A focus the pointer set does not scroll.
func homeTabsScrollTarget(lvl *homeTabsLevel, view string, g *homeTabsGeom, pad float64) float64 {
	if lvl.focus < 0 || lvl.focus >= len(g.rects) {
		return 0
	}
	r := g.rects[lvl.focus]
	if view == homeTabViewCarousel {
		return float64(r.X + r.W/2 - g.body.W/2)
	}
	if lvl.hover && lvl.placed {
		return lvl.scrollTo
	}
	viewLen, pos, size := float64(g.body.H), float64(r.Y), float64(r.H)
	if g.horiz {
		viewLen, pos, size = float64(g.body.W), float64(r.X), float64(r.W)
	}
	s := lvl.scrollTo
	if pos-pad < s {
		s = pos - pad
	}
	if pos+size+pad > s+viewLen {
		s = pos + size + pad - viewLen
	}
	maxS := math.Max(float64(g.extent)-viewLen, 0)
	return math.Max(0, math.Min(s, maxS))
}

// ── The top bar ──────────────────────────────────────────────────────────

// tabsNoteSpan records the top bar's free span for the strip: from the end
// of the wordmark to the first control drawn on the right. drawHeader calls
// it once the bar is planned. Where the system controls leave the strip too
// little room beside the wordmark (a narrow canvas, a long forecast), the
// wordmark yields from the next frame on (tabsBrandYields), and comes back
// once there is room for both again.
func (h *HomeLayout) tabsNoteSpan(brandEnd int32, plan []headerPlanItem) {
	if !h.tabsActive() || homeTabsStripBottom() {
		// With the strip at the bottom (homelook.go) the bar is the
		// wordmark's and the controls' as on any other Home.
		homeTabs.brandYield, homeTabs.spanOK = false, false
		return
	}
	right := h.Width - StatusBarMargin
	for _, it := range plan {
		if it.w > 0 && it.x < right {
			right = it.x
		}
	}
	left := headerBrandX()
	need := scaleX(300, h.Width)
	if homeTabs.brandYield {
		if right-(left+homeTabs.brandW) >= need+scaleX(40, h.Width) {
			homeTabs.brandYield = false
		}
	} else {
		homeTabs.brandW = brandEnd - left
		if right-brandEnd < need {
			homeTabs.brandYield = true
		}
	}
	if homeTabs.brandYield {
		brandEnd = left
	}
	homeTabs.spanL, homeTabs.spanR, homeTabs.spanOK = brandEnd, right, true
}

// tabsBrandYields reports whether the top bar leaves out the wordmark this
// frame, to give the Tabs Home's strip its room.
func (h *HomeLayout) tabsBrandYields() bool {
	return homeTabs.brandYield && h.tabsActive()
}

// homeTabsFade is the cached ramp texture behind the strip: white, opaque in
// the middle, fading to nothing over fade pixels at both ends.
var homeTabsFade struct {
	tex     *sdl.Texture
	owner   *sdl.Renderer
	w, fade int32
}

// homeTabsFadeTexture is the ramp for a strip w pixels wide, made once per
// width.
func homeTabsFadeTexture(renderer *sdl.Renderer, w, fade int32) *sdl.Texture {
	c := &homeTabsFade
	if c.tex != nil && c.owner == renderer && c.w == w && c.fade == fade {
		return c.tex
	}
	if c.tex != nil && c.owner == renderer {
		_ = c.tex.Destroy()
	}
	c.tex, c.owner = nil, renderer
	if renderer == nil || w <= 0 {
		return nil
	}
	pix := make([]byte, int(w)*4)
	for x := int32(0); x < w; x++ {
		a := 1.0
		if d := min32(x, w-1-x); fade > 0 && d < fade {
			t := float64(d) / float64(fade)
			a = t * t * (3 - 2*t)
		}
		i := int(x) * 4
		pix[i], pix[i+1], pix[i+2], pix[i+3] = 255, 255, 255, uint8(math.Round(a*255))
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, 1)
	if err != nil || tex == nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pix[0]), int(w)*4); err != nil {
		_ = tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	c.tex, c.w, c.fade = tex, w, fade
	return tex
}

// homeTabsLabelKey and homeTabsLabelTex are the strip's rasterised labels:
// drawn with an alpha of their own (the fade), which drawText's cache —
// keyed by colour — would make a texture per step of.
type homeTabsLabelKey struct {
	font *ttf.Font
	text string
	rgb  uint32
}

type homeTabsLabelTex struct {
	tex  *sdl.Texture
	w, h int32
}

var homeTabsLabels struct {
	m     map[homeTabsLabelKey]homeTabsLabelTex
	owner *sdl.Renderer
}

// homeTabsLabel is text rasterised in font and colour, from the cache.
func homeTabsLabel(renderer *sdl.Renderer, font *ttf.Font, text string, color sdl.Color) homeTabsLabelTex {
	c := &homeTabsLabels
	if c.owner != renderer {
		// Textures die with their renderer: forget them, never destroy them.
		c.m, c.owner = nil, renderer
	}
	if c.m == nil {
		c.m = make(map[homeTabsLabelKey]homeTabsLabelTex, 16)
	}
	// Keyed by the translated text, so a language change gets new labels.
	text = uiT(text)
	key := homeTabsLabelKey{font: font, text: text, rgb: uint32(color.R)<<16 | uint32(color.G)<<8 | uint32(color.B)}
	if t, ok := c.m[key]; ok {
		return t
	}
	if renderer == nil || font == nil {
		return homeTabsLabelTex{}
	}
	if len(c.m) >= 48 {
		for k, t := range c.m {
			if t.tex != nil {
				_ = t.tex.Destroy()
			}
			delete(c.m, k)
		}
	}
	s := sanitizeCached(font, uiUnmarkTranslated(text))
	if strings.TrimSpace(s) == "" {
		return homeTabsLabelTex{}
	}
	// Before the first frame possibly the last start's (startup_framecache.go).
	tex, tw, th, err := renderLabelTexture(renderer, font, s, sdl.Color{R: color.R, G: color.G, B: color.B, A: 255})
	if err != nil || tex == nil {
		return homeTabsLabelTex{}
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	t := homeTabsLabelTex{tex: tex, w: tw, h: th}
	c.m[key] = t
	return t
}

// homeTabsEdgeAlpha is how visible something centred at cx with half-width
// half is at the strip's ends: 1 clear of the fade, 0 at the edge.
func homeTabsEdgeAlpha(cx, half int32, strip sdl.Rect, fade int32) float64 {
	d := min32(cx-strip.X, strip.X+strip.W-cx)
	return uiClamp01(float64(d) / float64(max32(fade+half, 1)))
}

// tabsDrawStrip draws the strip of tabs and records where each tab is.
func (h *HomeLayout) tabsDrawStrip(renderer *sdl.Renderer, fonts HomeFonts, focused bool, dt float64) {
	homeTabs.strip = sdl.Rect{}
	homeTabs.tabHits = homeTabs.tabHits[:0]
	f := fonts.Helper
	if f == nil {
		f = fonts.Small
	}
	if renderer == nil || f == nil {
		return
	}
	vis := homeTabsVisible(appConfig)
	active := max(homeTabIndexIn(vis, homeTabs.cur), 0)
	barH := headerBarHeight()
	padX, gap := scaleX(16, h.Width), scaleX(4, h.Width)
	var widths [len(homeTabOrder)]int32
	var xs [len(homeTabOrder)]int32
	total := int32(0)
	for i, id := range vis {
		if i >= len(widths) {
			break
		}
		lw, _ := measureText(f, homeTabLabel(id))
		widths[i] = lw + 2*padX
		xs[i] = total
		total += widths[i] + gap
	}
	total -= gap
	left, right := h.Width*22/100, h.Width*78/100
	if homeTabs.spanOK {
		left, right = homeTabs.spanL, homeTabs.spanR
	}
	// Tabs position Bottom (homelook.go): the strip has a bar of its own over
	// the footer, as wide as the page.
	barY := int32(0)
	bottom := homeTabsStripBottom()
	if bottom {
		bar := h.tabsBottomBar()
		left, right, barY, barH = bar.X, bar.X+bar.W, bar.Y, bar.H
	}
	margin := scaleX(16, h.Width)
	left, right = left+margin, right-margin
	maxW := min32(right-left, h.Width*55/100)
	if bottom {
		maxW = right - left
	}
	if maxW < scaleX(120, h.Width) || total <= 0 {
		return
	}
	sw := min32(total, maxW)
	x0 := h.Width/2 - sw/2
	if x0 < left {
		x0 = left
	}
	if x0+sw > right {
		x0 = right - sw
	}
	fh := int32(f.Height())
	ph := min32(barH-8, fh+scaleY(14, h.Height))
	strip := sdl.Rect{X: x0, Y: barY + (barH-ph)/2, W: sw, H: ph}
	homeTabs.strip = strip
	overflow := total > sw
	fade := min32(scaleX(48, h.Width), sw/4)

	// The pill and the strip's scroll ease toward the active tab.
	targetX, targetW := float64(xs[active]), float64(widths[active])
	scrollTo := 0.0
	if overflow {
		scrollTo = math.Max(0, math.Min(targetX+targetW/2-float64(sw)/2, float64(total-sw)))
	}
	if !homeTabs.pillPlaced || !h.Motion {
		homeTabs.pillX, homeTabs.pillW, homeTabs.stripScroll = targetX, targetW, scrollTo
		homeTabs.pillPlaced = true
	} else {
		homeTabs.pillX = homeDynEase(homeTabs.pillX, targetX, dt)
		homeTabs.pillW = homeDynEase(homeTabs.pillW, targetW, dt)
		homeTabs.stripScroll = homeDynEase(homeTabs.stripScroll, scrollTo, dt)
		if math.Abs(homeTabs.pillX-targetX) < 0.5 && math.Abs(homeTabs.pillW-targetW) < 0.5 && math.Abs(homeTabs.stripScroll-scrollTo) < 0.5 {
			homeTabs.pillX, homeTabs.pillW, homeTabs.stripScroll = targetX, targetW, scrollTo
		} else {
			homeTabs.anim = true
		}
	}
	off := strip.X - int32(math.Round(homeTabs.stripScroll))

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// The band behind the tabs, fading out at both ends.
	if tex := homeTabsFadeTexture(renderer, strip.W, fade); tex != nil {
		band := homeGlass(18)
		_ = tex.SetColorMod(band.R, band.G, band.B)
		_ = tex.SetAlphaMod(band.A)
		_ = renderer.Copy(tex, nil, &strip)
	}

	hadClip := renderer.IsClipEnabled()
	prevClip := renderer.GetClipRect()
	clip := strip
	if hadClip {
		clip = glassIntersect(prevClip, strip)
	}
	_ = renderer.SetClipRect(&clip)

	pill := sdl.Rect{X: off + int32(math.Round(homeTabs.pillX)), Y: strip.Y, W: int32(math.Round(homeTabs.pillW)), H: strip.H}
	base := sdl.Rect{X: off + xs[active], Y: strip.Y, W: widths[active], H: strip.H}
	pillAlpha := 1.0
	if overflow {
		pillAlpha = homeTabsEdgeAlpha(pill.X+pill.W/2, pill.W/2, strip, fade)
	}
	fill := ColorAccent
	fill.A = uint8(math.Round(70 * pillAlpha))
	edge := ColorAccent
	edge.A = uint8(math.Round(150 * pillAlpha))
	fillRoundedRectAt(renderer, base, pill, strip.H/2, fill)
	strokeRoundedRectAt(renderer, base, pill, strip.H/2, 1, edge)

	for i, id := range vis {
		if i >= len(widths) {
			break
		}
		r := sdl.Rect{X: off + xs[i], Y: strip.Y, W: widths[i], H: strip.H}
		if r.X+r.W > strip.X && r.X < strip.X+strip.W {
			homeTabs.tabHits = append(homeTabs.tabHits, glassIntersect(r, strip))
		} else {
			homeTabs.tabHits = append(homeTabs.tabHits, sdl.Rect{})
			continue
		}
		ink := ColorTextMuted()
		if i == active {
			ink = ColorTextPrimary()
		}
		t := homeTabsLabel(renderer, f, homeTabLabel(id), ink)
		if t.tex == nil {
			continue
		}
		a := 1.0
		if overflow {
			a = homeTabsEdgeAlpha(r.X+r.W/2, r.W/2, strip, fade)
		}
		if a <= 0.01 {
			continue
		}
		_ = t.tex.SetAlphaMod(uint8(math.Round(a * 255)))
		dst := sdl.Rect{X: r.X + (r.W-t.w)/2, Y: strip.Y + (strip.H-t.h)/2, W: t.w, H: t.h}
		_ = renderer.Copy(t.tex, nil, &dst)
		_ = t.tex.SetAlphaMod(255)
	}
	if hadClip {
		_ = renderer.SetClipRect(&prevClip)
	} else {
		_ = renderer.SetClipRect(nil)
	}
	if focused {
		strokeRoundedRectAt(renderer, base, homeFocusGlide.apply(pill), strip.H/2, 2, homeFocusRingColor())
	}
	item := h.Items[homeTabsStripID]
	item.Rect = strip
	h.Items[homeTabsStripID] = item
}

// ── The page ─────────────────────────────────────────────────────────────

// tabsRender draws the Tabs Home's strip and page. Render calls it in the
// dynamic body's place; focused is the focused stop and lift its bounce.
func (h *HomeLayout) tabsRender(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID, lift float64) {
	if renderer == nil || !homeTabs.on {
		return
	}
	homeTabs.renderer = renderer
	now := sdl.GetTicks64()
	dt := 0.0
	if homeTabs.tick != 0 && now > homeTabs.tick {
		dt = math.Min(float64(now-homeTabs.tick), 100)
	}
	homeTabs.tick, homeTabs.dt = now, dt
	homeTabs.anim = false
	// Render's switchChromeOff (themeswitch.go) switches the search pill back
	// on every frame; on this Home it is not drawn, so it must not take the
	// D-pad or the pointer before the next tabsKeepFocus.
	if h.Focus.IsEnabled(HomeFocusSearch) {
		h.Focus.SetEnabled(HomeFocusSearch, false)
	}
	if homeEditBarRingOnly() {
		focused = ""
	}
	h.tabsDrawStrip(renderer, fonts, focused == homeTabsStripID && !homeTabsEdit.open, dt)

	homeTabs.hits = homeTabs.hits[:0]
	homeTabs.back = sdl.Rect{}
	lvl := homeTabsLevelNow()
	if lvl == nil {
		return
	}
	body := h.tabsBody(lvl)
	homeTabs.body = body
	item := h.Items[homeTabsPageID]
	item.Rect = body
	h.Items[homeTabsPageID] = item

	pressed := data.Pressed == homeTabsPageID
	depth := 0.0
	if pressed {
		depth = data.PressDepth
	}
	pageFocus := focused == homeTabsPageID && !homeTabsEdit.open && !homeTabsItemSheetActive()

	// The slide: the level being left goes out to one side as the new one
	// comes in from the other.
	p := 1.0
	if homeTabs.slideOn {
		if h.Motion {
			p = uiEaseOutCubic(homeTabs.slide.progress(uiClockMS()))
		} else {
			homeTabs.slide.cancel()
		}
		if !homeTabs.slide.active {
			homeTabs.slideOn = false
			homeTabs.slideOld = homeTabsLevel{}
			p = 1
		}
	}
	shift := int32(0)
	if homeTabs.slideOn {
		old := &homeTabs.slideOld
		travel := float64(body.W) * 0.35
		oldDX := int32(math.Round(-float64(homeTabs.slideDir) * p * travel))
		oldBody := h.tabsBody(old)
		h.tabsDrawLevel(renderer, fonts, old, oldBody, oldDX, false, 0, false, 0, homeTabsFadeStep(1-p), false)
		shift = int32(math.Round(float64(homeTabs.slideDir) * (1 - p) * travel))
	}
	if lvl.folder.Folder {
		h.tabsDrawSubHeader(renderer, fonts, lvl, shift)
	}
	h.tabsDrawLevel(renderer, fonts, lvl, body, shift, pageFocus, lift, pressed, depth, homeTabsFadeStep(p), true)
	if homeTabs.slideOn {
		homeTabs.anim = true
	}
}

// homeTabsFadeStep is a slide's opacity in five steps: labels are cached per
// colour, alpha included (drawText), so a smooth fade would rasterise every
// label anew each frame of it.
func homeTabsFadeStep(p float64) uint8 {
	return uint8(math.Round(uiClamp01(p)*5) * 51)
}

// homeEditBarRingOnly reports whether Edit Home's top bar row is ringing a
// control in the live bar, which then holds the only ring.
func homeEditBarRingOnly() bool {
	return homeEdit.open && homeEditBar.open
}

// tabsDrawSubHeader is a sub-page's first line: the way back and its title.
func (h *HomeLayout) tabsDrawSubHeader(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, dx int32) {
	f := fonts.Small
	if f == nil || fonts.Card == nil {
		return
	}
	x := scaleX(40, h.Width) + dx
	y := headerBarHeight() + scaleY(20, h.Height)
	lineH := scaleY(36, h.Height)
	badgeD := scaleY(24, h.Height)
	key := homeHintKey("B", "Back")
	bw, _ := measureText(f, key)
	badge := sdl.Rect{X: x, Y: y + (lineH-badgeD)/2, W: max32(badgeD, bw+scaleX(12, h.Width)), H: badgeD}
	fh := int32(f.Height())
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, homeGlass(16))
	strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, homeGlass(45))
	drawText(renderer, f, key, badge.X+badge.W/2, badge.Y+(badge.H-fh)/2, homeGlass(210), textAlignCenter)
	lx := badge.X + badge.W + scaleX(7, h.Width)
	drawText(renderer, f, "Back", lx, y+(lineH-fh)/2, ColorTextMuted(), textAlignLeft)
	lw, _ := measureText(f, "Back")
	homeTabs.back = sdl.Rect{X: badge.X - scaleX(6, h.Width), Y: y, W: lx + lw - badge.X + scaleX(12, h.Width), H: lineH}
	tx := lx + lw + scaleX(24, h.Width)
	ch := int32(fonts.Card.Height())
	if room := h.Width - scaleX(40, h.Width) - tx; room > 0 {
		drawText(renderer, fonts.Card, ellipsize(fonts.Card, lvl.title, room), tx, y+(lineH-ch)/2, ColorTextPrimary(), textAlignLeft)
	}
}

// tabsDrawLevel draws one level's page in body, moved across by dx: its
// tiles in its view, or its empty state. live records the tiles for the
// pointer and eases the scroll; alpha fades the whole page (the slide).
func (h *HomeLayout) tabsDrawLevel(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, body sdl.Rect, dx int32,
	focused bool, lift float64, pressed bool, depth float64, alpha uint8, live bool) {
	if lvl == nil || alpha == 0 {
		return
	}
	view := homeTabsViewFor(appConfig, lvl.key)
	if len(lvl.items) == 0 {
		if live || alpha > 40 {
			h.tabsDrawEmpty(renderer, fonts, lvl, sdl.Rect{X: body.X + dx, Y: body.Y, W: body.W, H: body.H}, alpha)
		}
		return
	}
	// Dim unselected art (homelook.go): the selected cover in full colour,
	// easing in as the selection moves; a page sliding out keeps its own.
	dimOn := homeDimArtOn(appConfig)
	if dimOn && live {
		// The depth as well as the key: a folder without a view of its own
		// shares its parent's key.
		lvlDepth := 0
		if p := homeTabsPageOf(homeTabs.cur); p != nil {
			lvlDepth = len(p.levels) - 1
		}
		if homeTabsDimEase.note(lvl.key, lvlDepth, lvl.focus, h.Motion) {
			homeTabs.anim = true
		}
	}
	dimOf := func(i int) float64 {
		switch {
		case !dimOn:
			return 0
		case live:
			return homeTabsDimEase.dim(i)
		case i == lvl.focus:
			return 0
		}
		return 1
	}
	g := h.tabsGeom(view, len(lvl.items), lvl.hero, body)
	pad := float64(scaleY(14, h.Height))
	if live {
		target := homeTabsScrollTarget(lvl, view, g, pad)
		lvl.scrollTo = target
		switch {
		case !lvl.placed || !h.Motion:
			lvl.scroll, lvl.placed = target, true
		case math.Abs(target-lvl.scroll) < 0.5:
			lvl.scroll = target
		default:
			lvl.scroll = homeDynEase(lvl.scroll, target, homeTabs.dt)
			homeTabs.anim = true
		}
	}
	scroll := int32(math.Round(lvl.scroll))
	ring := homeFocusRingOutset() + scaleY(4, h.Height)
	clip := sdl.Rect{X: body.X - ring, Y: body.Y - ring, W: body.W + 2*ring, H: body.H + 2*ring}
	hadClip := renderer.IsClipEnabled()
	prevClip := renderer.GetClipRect()
	if hadClip {
		clip = glassIntersect(prevClip, clip)
	}
	_ = renderer.SetClipRect(&clip)
	if lvl.key == homeTabCollections && !lvl.folder.Folder && view == homeTabViewGrid && len(g.rects) > 0 {
		h.tabsDrawCollectionsIntro(renderer, fonts, g, body, dx, alpha)
	}

	place := func(i int) sdl.Rect {
		r := g.rects[i]
		r.X += body.X + dx
		r.Y += body.Y
		if g.horiz {
			r.X -= scroll
		} else {
			r.Y -= scroll
		}
		return r
	}
	if view == homeTabViewList {
		h.tabsDrawList(renderer, fonts, lvl, g, body, dx, scroll, clip, focused, pressed, alpha, live, dimOf)
	} else {
		center := float64(body.X+dx) + float64(body.W)/2
		pitch := float64(1)
		if len(g.rects) > 1 {
			pitch = float64(g.rects[1].X - g.rects[0].X)
		}
		drawOne := func(i int) {
			r := place(i)
			if view == homeTabViewCarousel {
				// The neighbours shrink and dim with their distance from the
				// middle.
				mid := float64(r.X) + float64(r.W)/2
				d := math.Abs(mid-center) / pitch
				s := 1 - 0.28*math.Min(d, 1)
				w := int32(math.Round(float64(r.W) * s))
				cx, cy := r.X+r.W/2, r.Y+r.H/2
				r = sdl.Rect{X: cx - w/2, Y: cy - w/2, W: w, H: w}
			}
			if !homeRectsOverlap(r, clip) {
				return
			}
			isFocus := focused && i == lvl.focus
			tileLift, tilePress, tileDepth := 0.0, false, 0.0
			if isFocus {
				tileLift, tilePress, tileDepth = lift, pressed, depth
			}
			fav := i < len(lvl.favs) && lvl.favs[i]
			h.tabsPaintTile(renderer, fonts, lvl.items[i], r, isFocus, tilePress, tileLift, tileDepth, fav, view == homeTabViewCompact, alpha, dimOf(i))
			if live {
				homeTabs.hits = append(homeTabs.hits, homeTabsHit{i: i, r: glassIntersect(r, clip)})
			}
			_ = renderer.SetClipRect(&clip)
		}
		for i := range lvl.items {
			if !(focused && i == lvl.focus) {
				drawOne(i)
			}
		}
		if focused && lvl.focus >= 0 && lvl.focus < len(lvl.items) {
			drawOne(lvl.focus)
		}
	}
	// Not while the page slides in: a full-strength band over a half-faded
	// page would show as a stripe.
	if live && alpha == 255 {
		h.tabsDrawEdgeFade(renderer, lvl, g, view, body, dx, clip)
	}
	if hadClip {
		_ = renderer.SetClipRect(&prevClip)
	} else {
		_ = renderer.SetClipRect(nil)
	}
}

// tabsDrawCollectionsIntro uses spare room above the cards to explain the
// automatic lists. It disappears when the grid needs that room.
func (h *HomeLayout) tabsDrawCollectionsIntro(renderer *sdl.Renderer, fonts HomeFonts, g *homeTabsGeom, body sdl.Rect, dx int32, alpha uint8) {
	if fonts.Card == nil || fonts.Helper == nil {
		return
	}
	titleH, lineH := int32(fonts.Card.Height()), int32(fonts.Helper.Height())
	gap := scaleY(6, h.Height)
	blockH := titleH + gap + lineH
	if g.rects[0].Y < blockH+scaleY(36, h.Height) {
		return
	}
	x := body.X + dx + g.rects[0].X
	y := body.Y + (g.rects[0].Y-blockH)/2
	room := body.W - g.rects[0].X
	drawText(renderer, fonts.Card, ellipsize(fonts.Card, "Your collections", room), x, y,
		homeTabsInk(ColorTextPrimary(), alpha), textAlignLeft)
	drawText(renderer, fonts.Helper, ellipsize(fonts.Helper, "Your lists, plus ones updated automatically", room), x, y+titleH+gap,
		homeTabsInk(ColorTextMuted(), alpha), textAlignLeft)
}

// tabsDrawEdgeFade fades the ends of the page's scroll axis that have more
// past them (listfade.go), each band kept short of the selected tile so its
// ring stays crisp. The list view fades its column only, not the preview.
func (h *HomeLayout) tabsDrawEdgeFade(renderer *sdl.Renderer, lvl *homeTabsLevel, g *homeTabsGeom, view string, body sdl.Rect, dx int32, clip sdl.Rect) {
	if len(g.rects) == 0 || !listEdgeFadeOn(appConfig) {
		return
	}
	// ro is the selected tile's ring and glow past its rect.
	ro := homeFocusRingOutset() + scaleY(4, h.Height)
	area := clip
	if view == homeTabViewList {
		area = glassIntersect(clip, sdl.Rect{X: body.X + dx - ro, Y: clip.Y, W: g.rects[0].W + 2*ro, H: clip.H})
	}
	viewLen := float64(body.H)
	if g.horiz {
		viewLen = float64(body.W)
	}
	before := lvl.scroll > 0.5
	after := lvl.scroll+viewLen < float64(g.extent)-0.5
	if !before && !after {
		return
	}
	span := area.H
	if g.horiz {
		span = area.W
	}
	size := listFadeSize(span)
	near, far := int32(0), int32(0)
	if before {
		near = size
	}
	if after {
		far = size
	}
	if lvl.focus >= 0 && lvl.focus < len(g.rects) {
		r := g.rects[lvl.focus]
		scroll := int32(math.Round(lvl.scroll))
		if g.horiz {
			x := r.X + body.X + dx - scroll
			near = min32(near, max32(x-ro-area.X, 0))
			far = min32(far, max32(area.X+area.W-(x+r.W+ro), 0))
		} else {
			y := r.Y + body.Y - scroll
			near = min32(near, max32(y-ro-area.Y, 0))
			far = min32(far, max32(area.Y+area.H-(y+r.H+ro), 0))
		}
	}
	if g.horiz {
		drawListEdgeFadeSides(renderer, area, near, far)
	} else {
		drawListEdgeFadeSized(renderer, area, near, far)
	}
}

// homeTabsCopyCover copies tex into dst cropped to dst's shape, the way a
// cover fills a card.
func homeTabsCopyCover(renderer *sdl.Renderer, tex *sdl.Texture, dst sdl.Rect, alpha uint8) {
	homeTabsCopyCoverDim(renderer, tex, dst, alpha, 0)
}

// homeTabsCopyCoverDim is homeTabsCopyCover dimmed by dim (0 full colour, 1
// fully dimmed: Dim unselected art, homelook.go).
func homeTabsCopyCoverDim(renderer *sdl.Renderer, tex *sdl.Texture, dst sdl.Rect, alpha uint8, dim float64) {
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 || dst.W <= 0 || dst.H <= 0 {
		return
	}
	src := sdl.Rect{W: tw, H: th}
	if int64(dst.W)*int64(th) > int64(dst.H)*int64(tw) {
		src.H = max32(1, int32(int64(tw)*int64(dst.H)/int64(dst.W)))
		src.Y = (th - src.H) / 2
	} else {
		src.W = max32(1, int32(int64(th)*int64(dst.W)/int64(dst.H)))
		src.X = (tw - src.W) / 2
	}
	_ = tex.SetAlphaMod(alpha)
	homeLookCopyDim(renderer, tex, &src, dst, dim, alpha)
	_ = tex.SetAlphaMod(255)
}

// homeTabsInk is c at the page's alpha.
func homeTabsInk(c sdl.Color, alpha uint8) sdl.Color {
	c.A = uint8(uint32(c.A) * uint32(alpha) / 255)
	return c
}

// tabsPaintTile paints one tile: the glass, the cover or the glyph, the
// name (and its line where the tile has room), the badge and the heart. dim
// greys the cover (Dim unselected art, homelook.go).
func (h *HomeLayout) tabsPaintTile(renderer *sdl.Renderer, fonts HomeFonts, it homeTabItem, base sdl.Rect, focused, pressed bool,
	lift, depth float64, fav, compact bool, alpha uint8, dim float64) {
	draw := scaleHomeCard(base, lift)
	draw.Y += homePressOffset(depth, h.Height)
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	if alpha == 255 {
		drawGlassTile(renderer, base, draw, focused, pressed)
	} else {
		fillRoundedRectAt(renderer, base, draw, homeTileRadius, homeTabsInk(homeCardFill(), alpha))
		strokeRoundedRectAt(renderer, base, draw, homeTileRadius, 1, homeTabsInk(homeCardEdge(), alpha))
	}
	title, sub := fonts.Tile, fonts.Meta
	if compact || draw.W < scaleX(130, h.Width) {
		title, sub = fonts.Small, nil
	}
	if title == nil {
		return
	}
	pad := max32(scaleX(10, h.Width), draw.W/14)
	textW := draw.W - 2*pad
	var art *sdl.Texture
	if it.Art != nil && !compact {
		art = it.Art()
	}
	titleInk, subInk, glyphInk := ColorTextPrimary(), ColorTextMuted(), ColorTextPrimary()
	if it.Dim {
		titleInk, subInk, glyphInk = ColorTextMuted(), ColorTextTertiary(), ColorTextTertiary()
	}
	th := int32(title.Height())
	if art != nil {
		homeTabsCopyCoverDim(renderer, art, draw, alpha, dim)
		from, to := continueScrim(255)
		fillVerticalScrimUntil(renderer, draw, draw.Y+draw.H*42/100, draw.Y+draw.H*72/100,
			homeTabsInk(from, alpha), homeTabsInk(to, alpha))
		y := draw.Y + draw.H - pad - th
		if sub != nil && it.Sub != "" && draw.H >= scaleY(180, h.Height) {
			sh := int32(sub.Height())
			drawText(renderer, sub, ellipsize(sub, it.Sub, textW), draw.X+pad, draw.Y+draw.H-pad-sh,
				homeTabsInk(subInk, alpha), textAlignLeft)
			y -= sh + scaleY(2, h.Height)
		}
		if textW > 0 {
			drawText(renderer, title, ellipsize(title, it.Title, textW), draw.X+pad, y, homeTabsInk(titleInk, alpha), textAlignLeft)
		}
	} else {
		// The glyph over the name, the pair centred in the tile.
		showSub := sub != nil && it.Sub != "" && draw.H >= scaleY(150, h.Height)
		textH := th
		if showSub {
			textH += int32(sub.Height()) + scaleY(2, h.Height)
		}
		gs := draw.W * 36 / 100
		if compact {
			gs = draw.W * 42 / 100
		}
		gs = min32(gs, draw.H-textH-3*pad)
		if gs < scaleY(16, h.Height) {
			gs = 0
		}
		gap := scaleY(10, h.Height)
		top := draw.Y + (draw.H-gs-gap-textH)/2
		glyph := it.Glyph
		if glyph == "" {
			glyph = "apps"
		}
		if gs > 0 {
			drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: draw.X + (draw.W-gs)/2, Y: top, W: gs, H: gs}, homeTabsInk(glyphInk, alpha))
		}
		y := top + gs + gap
		if gs == 0 {
			y = draw.Y + (draw.H-textH)/2
		}
		cx := draw.X + draw.W/2
		if textW > 0 {
			drawText(renderer, title, ellipsize(title, it.Title, textW), cx, y, homeTabsInk(titleInk, alpha), textAlignCenter)
			if showSub {
				drawText(renderer, sub, ellipsize(sub, it.Sub, textW), cx, y+th+scaleY(2, h.Height), homeTabsInk(subInk, alpha), textAlignCenter)
			}
		}
	}
	h.tabsPaintCorner(renderer, fonts, it, draw, fav, alpha)
}

// tabsPaintCorner hangs the badge on a tile's top-right corner and the heart
// on its top-left.
func (h *HomeLayout) tabsPaintCorner(renderer *sdl.Renderer, fonts HomeFonts, it homeTabItem, draw sdl.Rect, fav bool, alpha uint8) {
	inset := scaleX(8, h.Width)
	if fav {
		hs := max32(scaleY(16, h.Height), draw.W/9)
		drawHomeGlyphTinted(renderer, "heart", sdl.Rect{X: draw.X + inset, Y: draw.Y + inset, W: hs, H: hs}, homeTabsInk(ColorDanger, alpha))
	}
	f := fonts.Meta
	if it.Badge == "" || f == nil {
		return
	}
	label := ellipsize(f, it.Badge, draw.W/2)
	lw, _ := measureText(f, label)
	fh := int32(f.Height())
	chip := sdl.Rect{W: lw + scaleX(12, h.Width), H: fh + scaleY(4, h.Height)}
	chip.X = draw.X + draw.W - inset - chip.W
	chip.Y = draw.Y + inset
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, homeTabsInk(WithAlpha(ColorAccent, 225), alpha))
	drawText(renderer, f, label, chip.X+chip.W/2, chip.Y+(chip.H-fh)/2, homeTabsInk(TextInk(ColorAccent), alpha), textAlignCenter)
}

// tabsDrawList draws the list view: a line per item on the left, the
// focused one large in a preview pane on the right. dimOf is each line's
// cover dim (homelook.go); the preview is always in full colour.
func (h *HomeLayout) tabsDrawList(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, g *homeTabsGeom, body sdl.Rect, dx, scroll int32,
	clip sdl.Rect, focused, pressed bool, alpha uint8, live bool, dimOf func(int) float64) {
	title, sub := fonts.Tile, fonts.Meta
	if title == nil {
		return
	}
	radius := scaleY(14, h.Height)
	for i, it := range lvl.items {
		r := g.rects[i]
		r.X += body.X + dx
		r.Y += body.Y - scroll
		if !homeRectsOverlap(r, clip) {
			continue
		}
		isFocus := focused && i == lvl.focus
		switch {
		case isFocus && pressed:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, homeTabsInk(homeGlass(32), alpha))
		case isFocus:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, homeTabsInk(homeGlass(24), alpha))
		case i == lvl.focus:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, homeTabsInk(homeGlass(12), alpha))
		}
		if isFocus {
			strokeRoundedRectAt(renderer, r, homeFocusGlide.apply(r), radius, 2, homeFocusRingColor())
		}
		icon := min32(r.H-scaleY(12, h.Height), scaleY(44, h.Height))
		ib := sdl.Rect{X: r.X + scaleX(10, h.Width), Y: r.Y + (r.H-icon)/2, W: icon, H: icon}
		var art *sdl.Texture
		if it.Art != nil {
			art = it.Art()
		}
		if art != nil {
			homeTabsCopyCoverDim(renderer, art, ib, alpha, dimOf(i))
		} else {
			glyphBox := drawIconBadge(renderer, ib, isFocus)
			ink := ColorTextPrimary()
			if isFocus {
				ink = ColorAccent
			}
			if it.Dim {
				ink = ColorTextTertiary()
			}
			glyph := it.Glyph
			if glyph == "" {
				glyph = "apps"
			}
			drawHomeGlyphTinted(renderer, glyph, glyphBox, homeTabsInk(ink, alpha))
		}
		tx := ib.X + ib.W + scaleX(12, h.Width)
		right := r.X + r.W - scaleX(12, h.Width)
		if i < len(lvl.favs) && lvl.favs[i] {
			hs := scaleY(16, h.Height)
			right -= hs
			drawHomeGlyphTinted(renderer, "heart", sdl.Rect{X: right, Y: r.Y + (r.H-hs)/2, W: hs, H: hs}, homeTabsInk(ColorDanger, alpha))
			right -= scaleX(8, h.Width)
		}
		if room := right - tx; room > 0 {
			titleInk := ColorTextPrimary()
			if it.Dim {
				titleInk = ColorTextMuted()
			}
			th := int32(title.Height())
			sh := int32(0)
			if sub != nil && it.Sub != "" {
				sh = int32(sub.Height())
			}
			ty := r.Y + (r.H-th-sh)/2
			drawText(renderer, title, ellipsize(title, it.Title, room), tx, ty, homeTabsInk(titleInk, alpha), textAlignLeft)
			if sh > 0 {
				drawText(renderer, sub, ellipsize(sub, it.Sub, room), tx, ty+th, homeTabsInk(ColorTextMuted(), alpha), textAlignLeft)
			}
		}
		if live {
			homeTabs.hits = append(homeTabs.hits, homeTabsHit{i: i, r: glassIntersect(r, clip)})
		}
	}
	// The preview: the focused item, large.
	if lvl.focus < 0 || lvl.focus >= len(lvl.items) {
		return
	}
	gap := scaleX(24, h.Width)
	pane := sdl.Rect{X: body.X + dx + g.rects[0].W + gap, Y: body.Y, W: body.W - g.rects[0].W - gap, H: body.H}
	if pane.W < scaleX(160, h.Width) {
		return
	}
	it := lvl.items[lvl.focus]
	fillRoundedRect(renderer, pane.X, pane.Y, pane.W, pane.H, homeTileRadius, homeTabsInk(homeCardFill(), alpha))
	strokeRoundedRect(renderer, pane.X, pane.Y, pane.W, pane.H, homeTileRadius, 1, homeTabsInk(homeCardEdge(), alpha))
	big, line := fonts.Card, fonts.Helper
	if big == nil || line == nil {
		return
	}
	pad := scaleX(28, h.Width)
	textH := int32(big.Height()) + int32(line.Height()) + scaleY(6, h.Height)
	side := min32(pane.W-2*pad, pane.H-2*pad-textH-scaleY(16, h.Height))
	if side > 0 {
		box := sdl.Rect{X: pane.X + (pane.W-side)/2, Y: pane.Y + pad, W: side, H: side}
		var art *sdl.Texture
		if it.Art != nil {
			art = it.Art()
		}
		if art != nil {
			homeTabsCopyCover(renderer, art, box, alpha)
		} else {
			gs := side * 55 / 100
			glyph := it.Glyph
			if glyph == "" {
				glyph = "apps"
			}
			drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: box.X + (side-gs)/2, Y: box.Y + (side-gs)/2, W: gs, H: gs}, homeTabsInk(ColorTextPrimary(), alpha))
		}
	}
	ty := pane.Y + pane.H - pad - textH
	room := pane.W - 2*pad
	drawText(renderer, big, ellipsize(big, it.Title, room), pane.X+pane.W/2, ty, homeTabsInk(ColorTextPrimary(), alpha), textAlignCenter)
	if it.Sub != "" {
		drawText(renderer, line, ellipsize(line, it.Sub, room), pane.X+pane.W/2, ty+int32(big.Height())+scaleY(6, h.Height),
			homeTabsInk(ColorTextMuted(), alpha), textAlignCenter)
	}
}

// homeTabsEmptyText is what an empty page says, and how to fill it.
func homeTabsEmptyText(lvl *homeTabsLevel) (title, line, glyph string) {
	if lvl.folder.Folder {
		return "Nothing here yet", "Press X on any game or app to add it to a collection.", "apps"
	}
	switch lvl.key {
	case homeTabRecent:
		return "Nothing played yet", "The games and apps you open show up here.", "history"
	case homeTabFavourites:
		return "No favourites yet", "Press X on any game or app to add it here.", "heart"
	case homeTabCollections:
		return "No collections yet", "Press X on a game or app and add it to a new collection.", "apps"
	case homeTabGames:
		return "No games yet", "Add games and emulators from the Store, then come back here.", "emulator"
	}
	return "Nothing here yet", "Apps show up here once they are available.", "apps"
}

// tabsDrawEmpty draws an empty page's message in its middle.
func (h *HomeLayout) tabsDrawEmpty(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, body sdl.Rect, alpha uint8) {
	title, line := fonts.Card, fonts.Helper
	if title == nil || line == nil {
		return
	}
	t, l, glyph := homeTabsEmptyText(lvl)
	gs := scaleY(56, h.Height)
	total := gs + scaleY(16, h.Height) + int32(title.Height()) + scaleY(6, h.Height) + int32(line.Height())
	y := body.Y + (body.H-total)/2
	cx := body.X + body.W/2
	drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: cx - gs/2, Y: y, W: gs, H: gs}, homeTabsInk(ColorTextTertiary(), alpha))
	y += gs + scaleY(16, h.Height)
	room := body.W - scaleX(80, h.Width)
	drawText(renderer, title, ellipsize(title, t, room), cx, y, homeTabsInk(ColorTextPrimary(), alpha), textAlignCenter)
	y += int32(title.Height()) + scaleY(6, h.Height)
	drawText(renderer, line, ellipsize(line, l, room), cx, y, homeTabsInk(ColorTextMuted(), alpha), textAlignCenter)
}
