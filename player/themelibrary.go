package main

// themelibrary.go — the Library theme: the Tabs Home as the front room of a
// media library. Its pages open in the Library view (hometabs_library.go) —
// a featured banner over a row of tall posters — in a grey studio: a soft
// light above the banner and a boardwalk of dark planks running from under it
// toward the viewer. The top bar trades the wordmark for a large clock over
// the date, and the tabs sit together in one glass capsule.
//
// The theme is the "library" palette (Colour tab) and the Library style
// (Styles tab, convert-library): the palette plus the Tabs arrangement and
// the look, which the preset asks for with carouselStyle "library"
// (themeconsole.go reads it into homeConsoleLook.library; the Carousel
// arrangement's row is not reshaped by it). While the look is on, the Tabs
// Home keeps page views of its own (homeTabViewsLibrary, hometabs.go): every
// page starts in the Library view, A on the strip still changes a page's
// view, and the views picked under any other theme are left as they were.
//
// The big clock is the clock control while it is drawn: the status cluster
// leaves its own time out, the D-pad reaches the clock Left of the first tab
// (and the strip Right of it), and A on it opens the calendar. When the bar
// is too crowded for the clock beside the tabs, the wordmark's usual rule
// hides it and the cluster's time comes back.
//
// The clock, the banner's title and the row's heading are set in the Library
// serif: Lora (SIL Open Font License 1.1, Reserved Font Name "Lora")
// instanced at its SemiBold weight and renamed "JukaHub Library Serif", as the
// licence asks of a modified copy. It is embedded (librarytheme_serif.ttf; the
// licence is librarytheme_serif_OFL.txt). Text the serif cannot draw — a
// Japanese game name — is set in Inter instead, and so is everything while
// Legible reading font or Bold text is on.
//
// No artwork ships with the theme: the planks, the banner and the posters'
// rounded covers are SDL_RenderGeometry fans and quads, built from numbers.
// Where SDL cannot draw geometry the look falls back to square covers and
// plain fills, and the boardwalk is left out.

import (
	_ "embed"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// homeCarouselLibrary is the carousel style that asks for the Library
	// look. normalizeCarouselStyle maps it onto the centred carousel; the
	// look reads the raw value (applyThemeConsoleLook).
	homeCarouselLibrary = "library"
	// homeTabsViewsLibraryKey keeps the Tabs Home's page views while the
	// Library look is on (homeTabsViewsKeyNow).
	homeTabsViewsLibraryKey = "homeTabViewsLibrary"
	// libThemeFloorBoards is how many planks the boardwalk shows.
	libThemeFloorBoards = 26
)

// homeIsLibraryStyle reports whether a carousel style asks for the Library
// look.
func homeIsLibraryStyle(style string) bool {
	return strings.EqualFold(strings.TrimSpace(style), homeCarouselLibrary)
}

// homeLibraryLookOn reports whether the active theme is the Library look.
func homeLibraryLookOn() bool { return homeConsoleLook.library }

// libraryChrome reports whether the Library look dresses the live Home: the
// theme is on and Home is the Tabs Home (Edit Home, which borrows the old
// cards, shows the usual bar).
func (h *HomeLayout) libraryChrome() bool {
	return homeConsoleLook.library && h.tabsActive() && homeTabs.on
}

// homeLibTheme is the Library look's and view's state between frames. Render
// thread only.
var homeLibTheme struct {
	// clock is the big clock as drawn and clockFrame the frame it was drawn
	// in: the clock control's rect (libraryClockFocus).
	clock      sdl.Rect
	clockFrame uint64
	// parts is the status cluster without its time, reused every frame.
	parts []string
	// options is the banner's Options button as last drawn, for the pointer
	// (homeLibThemeOptionsClick); tabsRender clears it every frame.
	options sdl.Rect
	// The kicker of the page on screen, rebuilt when the page or the
	// interface language changes (libThemeKicker).
	kickTab, kickTitle, kickLang, kicker string
	// The banner's name as last laid out (libraryTitle), and the faces it
	// was laid out with.
	titleText  string
	titleW     int32
	titleSerif *ttf.Font
	titleInter *ttf.Font
	titleFont  *ttf.Font
	titleLines []string
	// The heading's "3 / 12" (libThemeCount).
	countI, countN int
	count          string
	// The accent as it reads on the banner (libThemeAccentOnDark).
	accentSrc, accent sdl.Color
	accentOK          bool
	// The banner picture's own fade-in (libThemeHeroPicture).
	picTex  *sdl.Texture
	picFade uiEnter
}

// ── The serif ────────────────────────────────────────────────────────────

//go:embed librarytheme_serif.ttf
var libThemeSerifTTF []byte

// libThemeSerifName names the embedded face for fonts.go's character lookup
// and keys it in fontCache. No file of this name exists: the coverage is
// registered from the embedded bytes before any open.
const libThemeSerifName = "embedded:librarytheme_serif.ttf"

// libThemeSerif records the face's state. Render thread only.
var libThemeSerif struct {
	covered bool // its character map is registered (coverageCache)
	failed  bool // it would not open: Inter from now on
	// faces are the sizes opened so far. Each is in fontCache as well, so the
	// shutdown closes it; a text-size change drops fontCache without closing
	// anything, and the new sizes are opened here under their own keys.
	faces map[int]*ttf.Font
}

// libThemeSerifFont is the Library serif at size (an already scaled size,
// uiLegibleFontSize), or nil when the interface face must be used instead:
// the face would not open, or Legible reading font or Bold text is on.
func libThemeSerifFont(size int) *ttf.Font {
	if libThemeSerif.failed || len(libThemeSerifTTF) == 0 || size <= 0 {
		return nil
	}
	if a11y.boldText || legibleFontFile() != "" {
		return nil
	}
	if font := libThemeSerif.faces[size]; font != nil {
		return font
	}
	if !libThemeSerif.covered {
		ranges, err := parseCMAPData(libThemeSerifTTF)
		if err != nil {
			libThemeSerif.failed = true
			logWarn("home", "library serif: unreadable character map: %v", err)
			return nil
		}
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo < ranges[j].lo })
		ranges = mergeRanges(ranges)
		coverageMu.Lock()
		coverageCache[libThemeSerifName] = ranges
		coverageMu.Unlock()
		libThemeSerif.covered = true
	}
	// The bytes are the binary's own, never moved or freed, so SDL_ttf may
	// keep reading them for as long as the face is open.
	rw, err := sdl.RWFromMem(libThemeSerifTTF)
	if err != nil {
		libThemeSerif.failed = true
		logWarn("home", "library serif: %v", err)
		return nil
	}
	font, err := ttf.OpenFontRW(rw, 1, size)
	if err != nil || font == nil {
		libThemeSerif.failed = true
		logWarn("home", "library serif at %d px: %v", size, err)
		return nil
	}
	registerFontPath(font, libThemeSerifName)
	if libThemeSerif.faces == nil {
		libThemeSerif.faces = make(map[int]*ttf.Font, 4)
	}
	libThemeSerif.faces[size] = font
	fontCache[libThemeSerifName+"\n"+strconv.Itoa(size)] = font
	return font
}

// libThemeSerifCovers reports whether the serif draws every letter of s.
func libThemeSerifCovers(s string) bool {
	if !libThemeSerif.covered {
		return false
	}
	for _, r := range s {
		if !fontCoversRune(libThemeSerifName, r) {
			return false
		}
	}
	return true
}

// libThemeSerifOr is the serif at size for s, or fallback when the serif
// cannot set it.
func libThemeSerifOr(size int, s string, fallback *ttf.Font) *ttf.Font {
	if f := libThemeSerifFont(uiLegibleFontSize(size)); f != nil && libThemeSerifCovers(s) {
		return f
	}
	return fallback
}

// ── Geometry ─────────────────────────────────────────────────────────────

// libThemeGeomFailed is set the first time SDL_RenderGeometry refuses (an SDL
// older than 2.0.18, or a renderer without it).
var libThemeGeomFailed bool

// libThemeGeom is the scratch the fans are built in, reused frame to frame.
var libThemeGeom struct {
	v   []sdl.Vertex
	idx []int32
	in  []sdl.FPoint
	out []sdl.FPoint
}

// libThemeGeomFail notes that SDL cannot draw geometry, once.
func libThemeGeomFail(err error) {
	if !libThemeGeomFailed {
		logWarn("home", "RenderGeometry is unavailable (%v): the Library look draws square covers and no boardwalk", err)
	}
	libThemeGeomFailed = true
}

// libThemeInk is a fan's colour: a everywhere, or — ramp — a straight ramp
// from a at `from` to b at `to`, across (x) or down (y). A ramp is affine in
// the position, so the fan's triangles draw it exactly as long as the shape
// lies between from and to.
type libThemeInk struct {
	a, b     sdl.Color
	from, to float32
	across   bool
	ramp     bool
}

// libThemeFlat is one colour.
func libThemeFlat(c sdl.Color) libThemeInk { return libThemeInk{a: c} }

// libThemeRamp is a ramp over rect: a at its left (across) or top, b at the
// other side.
func libThemeRamp(rect sdl.Rect, a, b sdl.Color, across bool) libThemeInk {
	k := libThemeInk{a: a, b: b, across: across, ramp: true}
	if across {
		k.from, k.to = float32(rect.X), float32(rect.X+rect.W)
	} else {
		k.from, k.to = float32(rect.Y), float32(rect.Y+rect.H)
	}
	return k
}

// at is the colour at (x, y).
func (k libThemeInk) at(x, y float32) sdl.Color {
	if !k.ramp || k.to == k.from {
		return k.a
	}
	p := y
	if k.across {
		p = x
	}
	return lerpColorRGBA(k.a, k.b, (p-k.from)/(k.to-k.from))
}

// libThemeCornerSteps is how many segments a corner's arc of radius r gets.
func libThemeCornerSteps(r float32) int {
	if r < 1 {
		return 0
	}
	return min(int(r/3)+3, 12)
}

// libThemeOutline appends rect's rounded outline to out, clockwise from the
// top-left corner's arc: radii are the top-left, top-right, bottom-right and
// bottom-left corners' (below 1 is square), each corner steps[i] segments.
func libThemeOutline(out []sdl.FPoint, x0, y0, w, h float32, radii [4]float32, steps [4]int) []sdl.FPoint {
	limit := float32(math.Min(float64(w), float64(h))) / 2
	centres := [4][3]float32{
		{x0, y0, math.Pi},             // top left, from the left edge
		{x0 + w, y0, 3 * math.Pi / 2}, // top right, from the top edge
		{x0 + w, y0 + h, 0},           // bottom right, from the right edge
		{x0, y0 + h, math.Pi / 2},     // bottom left, from the bottom edge
	}
	signs := [4][2]float32{{1, 1}, {-1, 1}, {-1, -1}, {1, -1}}
	for i, c := range centres {
		r := min(radii[i], limit)
		if r < 0 {
			r = 0
		}
		cx, cy := c[0]+signs[i][0]*r, c[1]+signs[i][1]*r
		n := steps[i]
		if n <= 0 || r < 0.5 {
			for s := 0; s <= max(n, 0); s++ {
				out = append(out, sdl.FPoint{X: cx, Y: cy})
			}
			continue
		}
		for s := 0; s <= n; s++ {
			t := float64(c[2]) + math.Pi/2*float64(s)/float64(n)
			out = append(out, sdl.FPoint{X: cx + r*float32(math.Cos(t)), Y: cy + r*float32(math.Sin(t))})
		}
	}
	return out
}

// libThemeFan draws rect as one rounded fan (libThemeOutline's radii), each
// vertex coloured by ink at its position. With tex, uv (u0, v0, u1, v1) is
// the part of the picture spread over rect, and the colour modulates it. It
// reports false when SDL cannot draw geometry: the caller draws its square
// fallback.
func libThemeFan(renderer *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect, radii [4]int32, ink libThemeInk, uv [4]float32) bool {
	if libThemeGeomFailed || renderer == nil {
		return false
	}
	if rect.W <= 0 || rect.H <= 0 {
		return true
	}
	g := &libThemeGeom
	var rf [4]float32
	var steps [4]int
	for i, r := range radii {
		rf[i] = float32(r)
		steps[i] = libThemeCornerSteps(rf[i])
	}
	x0, y0, w, h := float32(rect.X), float32(rect.Y), float32(rect.W), float32(rect.H)
	g.out = libThemeOutline(g.out[:0], x0, y0, w, h, rf, steps)
	vertex := func(p sdl.FPoint) sdl.Vertex {
		v := sdl.Vertex{Position: p, Color: ink.at(p.X, p.Y)}
		if tex != nil {
			v.TexCoord = sdl.FPoint{X: uv[0] + (p.X-x0)/w*(uv[2]-uv[0]), Y: uv[1] + (p.Y-y0)/h*(uv[3]-uv[1])}
		}
		return v
	}
	g.v = append(g.v[:0], vertex(sdl.FPoint{X: x0 + w/2, Y: y0 + h/2}))
	for _, p := range g.out {
		g.v = append(g.v, vertex(p))
	}
	n := int32(len(g.out))
	g.idx = g.idx[:0]
	for i := int32(1); i <= n; i++ {
		next := i + 1
		if next > n {
			next = 1
		}
		g.idx = append(g.idx, 0, i, next)
	}
	if tex != nil {
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	} else {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	}
	if err := renderer.RenderGeometry(tex, g.v, g.idx); err != nil {
		libThemeGeomFail(err)
		return false
	}
	return true
}

// libThemeFillRounded fills rect with c, rounded by radii; the square-cornered
// shape cache (radius radii[0]) stands in where SDL cannot draw geometry.
func libThemeFillRounded(renderer *sdl.Renderer, rect sdl.Rect, radii [4]int32, c sdl.Color) {
	if c.A == 0 || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if !libThemeFan(renderer, nil, rect, radii, libThemeFlat(c), [4]float32{}) {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radii[0], c)
	}
}

// libThemeFillRamp fills rect with a ramp from a to b (across or down),
// rounded by radii.
func libThemeFillRamp(renderer *sdl.Renderer, rect sdl.Rect, radii [4]int32, a, b sdl.Color, across bool) {
	if (a.A == 0 && b.A == 0) || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if !libThemeFan(renderer, nil, rect, radii, libThemeRamp(rect, a, b, across), [4]float32{}) {
		fillRoundedGradient(renderer, rect, radii[2], a, b, across)
	}
}

// libThemeStrokeRounded draws rect's rounded outline, width pixels inward.
func libThemeStrokeRounded(renderer *sdl.Renderer, rect sdl.Rect, radius, width int32, c sdl.Color) {
	if renderer == nil || c.A == 0 || rect.W <= 2*width || rect.H <= 2*width || width < 1 {
		return
	}
	if libThemeGeomFailed {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, width, c)
		return
	}
	g := &libThemeGeom
	r := float32(radius)
	steps := libThemeCornerSteps(r)
	st := [4]int{steps, steps, steps, steps}
	wf := float32(width)
	inner := max(r-wf, 0)
	g.out = libThemeOutline(g.out[:0], float32(rect.X), float32(rect.Y), float32(rect.W), float32(rect.H), [4]float32{r, r, r, r}, st)
	g.in = libThemeOutline(g.in[:0], float32(rect.X)+wf, float32(rect.Y)+wf, float32(rect.W)-2*wf, float32(rect.H)-2*wf,
		[4]float32{inner, inner, inner, inner}, st)
	if len(g.in) != len(g.out) || len(g.out) == 0 {
		return
	}
	g.v, g.idx = g.v[:0], g.idx[:0]
	for i := range g.out {
		g.v = append(g.v, sdl.Vertex{Position: g.out[i], Color: c}, sdl.Vertex{Position: g.in[i], Color: c})
	}
	n := int32(len(g.out))
	for i := int32(0); i < n; i++ {
		j := (i + 1) % n
		o0, i0, o1, i1 := 2*i, 2*i+1, 2*j, 2*j+1
		g.idx = append(g.idx, o0, o1, i0, i0, o1, i1)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if err := renderer.RenderGeometry(nil, g.v, g.idx); err != nil {
		libThemeGeomFail(err)
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, width, c)
	}
}

// libThemeCopyCover draws tex into dst cropped to dst's shape (the way a cover
// fills a card), rounded by radii and modulated by mod (its alpha is the
// picture's). Without geometry the picture is drawn square.
func libThemeCopyCover(renderer *sdl.Renderer, tex *sdl.Texture, dst sdl.Rect, radii [4]int32, mod sdl.Color) {
	if renderer == nil || tex == nil || mod.A == 0 || dst.W <= 0 || dst.H <= 0 {
		return
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return
	}
	uv := [4]float32{0, 0, 1, 1}
	if int64(dst.W)*int64(th) > int64(dst.H)*int64(tw) {
		// Wider than the picture: its top and bottom are cut.
		span := float32(float64(tw) * float64(dst.H) / (float64(th) * float64(dst.W)))
		uv[1] = (1 - span) / 2
		uv[3] = uv[1] + span
	} else {
		span := float32(float64(th) * float64(dst.W) / (float64(tw) * float64(dst.H)))
		uv[0] = (1 - span) / 2
		uv[2] = uv[0] + span
	}
	if libThemeFan(renderer, tex, dst, radii, libThemeFlat(mod), uv) {
		return
	}
	_ = tex.SetColorMod(mod.R, mod.G, mod.B)
	homeTabsCopyCover(renderer, tex, dst, mod.A)
	_ = tex.SetColorMod(255, 255, 255)
}

// ── The top bar ──────────────────────────────────────────────────────────

// libraryDrawClock draws the Library look's top-left corner in the
// wordmark's place — the time large in the serif with its AM/PM small beside
// it, the date under it — and returns the x just past it. ok is false when
// the look is not up, or the user took the clock off the top bar (Edit Home →
// Top bar): the wordmark is drawn as ever.
func (h *HomeLayout) libraryDrawClock(renderer *sdl.Renderer, config *Config, fonts HomeFonts, barH int32) (int32, bool) {
	if renderer == nil || !h.libraryChrome() || headerSlotHidden(config, hsClock) {
		return 0, false
	}
	c := hwpClockUpdate(config, time.Now())
	big := libThemeSerifOr(30, c.digits, fonts.Greeting)
	small := fonts.Meta
	if big == nil || small == nil {
		return 0, false
	}
	x := headerBrandX()
	bigA, smallA, smallH := int32(big.Ascent()), int32(small.Ascent()), int32(small.Height())
	// The figures' capitals and the date's line make one block, centred on
	// the bar: the date hangs a few pixels under the figures' baseline.
	capH := bigA * 68 / 100
	dateCap := smallA * 72 / 100
	under := scaleY(7, h.Height)
	vis := capH + under + smallH - (smallA - dateCap)
	visTop := (barH - vis) / 2
	ty := visTop - (bigA - capH)
	dy := ty + bigA + under - (smallA - dateCap)
	tw, _ := measureText(big, c.digits)
	drawTextRaw(renderer, big, c.digits, x, ty, ColorTextPrimary(), textAlignLeft)
	w := tw
	if c.suffix != "" {
		sx := x + tw + scaleX(5, h.Width)
		drawTextRaw(renderer, small, c.suffix, sx, ty+bigA-smallA, ColorTextMuted(), textAlignLeft)
		sw, _ := measureText(small, c.suffix)
		w = sx + sw - x
	}
	if c.long != "" {
		drawTextRaw(renderer, small, c.long, x, dy, ColorTextMuted(), textAlignLeft)
		dw, _ := measureText(small, c.long)
		w = max32(w, dw)
	}
	padX, padY := scaleX(8, h.Width), scaleY(4, h.Height)
	homeLibTheme.clock = sdl.Rect{X: x - padX, Y: max32(visTop-padY, 0), W: w + 2*padX, H: vis + 2*padY}
	homeLibTheme.clockFrame = frameCount
	return x + w, true
}

// libraryClockFocus makes the big clock the clock control for this frame
// (drawHeader, once the bar is planned, before it enables the clock): its
// rect is the clock's focus stop and hit area, so the D-pad, a tap and the
// focus ring find it. It reports whether the big clock is drawn; a clock not
// drawn this frame is forgotten.
func (h *HomeLayout) libraryClockFocus() bool {
	l := &homeLibTheme
	if l.clockFrame != frameCount || l.clock.W <= 0 {
		l.clock = sdl.Rect{}
		return false
	}
	item := h.Items[HomeFocusClock]
	item.Rect = l.clock
	h.Items[HomeFocusClock] = item
	return true
}

// libraryStatusParts is the status cluster's parts for this frame: without
// the time while the Library look's big clock stands in for it. It returns
// parts itself otherwise.
func (h *HomeLayout) libraryStatusParts(parts []string) []string {
	if !h.libraryChrome() || h.tabsBrandYields() {
		return parts
	}
	out := homeLibTheme.parts[:0]
	for _, p := range parts {
		if !isClockStatusPart(p) {
			out = append(out, p)
		}
	}
	homeLibTheme.parts = out
	return out
}

// libraryClockOn reports whether the big clock was drawn in the last frame
// and is a stop in the top bar.
func (h *HomeLayout) libraryClockOn() bool {
	return homeLibTheme.clock.W > 0 && h.libraryChrome() && h.Focus.IsEnabled(HomeFocusClock)
}

// libraryClockStep is a press in the top bar while the big clock stands left
// of the tab strip (tabsMove): Right from the clock reaches the strip, Left
// and Up stay on it, and Left from a control right of the strip stops at the
// strip instead of jumping over it to the clock. It reports whether it took
// the press.
func (h *HomeLayout) libraryClockStep(cur HomeFocusID, direction FocusDirection) bool {
	if !h.libraryClockOn() || homeTabsStripBottom() {
		return false
	}
	if cur == HomeFocusClock {
		if direction == FocusRight {
			h.Focus.Set(homeTabsStripID)
			homeTabs.autoStrip = false
		}
		return true
	}
	if direction != FocusLeft {
		return false
	}
	if next := h.Focus.Move(direction); next == HomeFocusClock || next == cur {
		h.Focus.Set(homeTabsStripID)
		homeTabs.autoStrip = false
	}
	return true
}

// libraryDrawCapsule draws the glass capsule the Library look holds the tabs
// in (tabsDrawStrip): a little larger than the strip, with a hairline edge.
func (h *HomeLayout) libraryDrawCapsule(renderer *sdl.Renderer, strip sdl.Rect) {
	padX, padY := scaleX(6, h.Width), scaleY(4, h.Height)
	r := sdl.Rect{X: strip.X - padX, Y: strip.Y - padY, W: strip.W + 2*padX, H: strip.H + 2*padY}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, homeGlass(glassOpacityA(18)))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, homeGlass(72))
}

// ── The room ─────────────────────────────────────────────────────────────

// libThemeStageKey is what the boardwalk was built for: the canvas, the
// banner's foot and centre, and the walk's half-width there.
type libThemeStageKey struct{ w, h, top, cx, half int32 }

// homeLibThemeStage is the boardwalk's triangles, rebuilt only when the
// banner moves or the canvas changes size. Render thread only.
var homeLibThemeStage struct {
	key libThemeStageKey
	v   []sdl.Vertex
	idx []int32
}

// libraryDrawStage draws the Library look's room behind a page in the
// Library view (tabsBackdropBehind, before the top bar): a soft light above
// the banner, the boardwalk running from under it to the screen's foot, and
// the banner's two feet standing on it.
func (h *HomeLayout) libraryDrawStage(renderer *sdl.Renderer, lvl *homeTabsLevel) {
	if renderer == nil || lvl == nil || len(lvl.items) == 0 || !homeLibraryLookOn() {
		return
	}
	body := h.tabsBody(lvl)
	f := homeLibViewFrameFor(body.W, body.H, homeTabsScale(h.Width, h.Height))
	hero := sdl.Rect{X: body.X + f.hero.X, Y: body.Y + f.hero.Y, W: f.hero.W, H: f.hero.H}
	homeGlowBlob(renderer, sdl.Rect{X: hero.X - hero.W/4, Y: hero.Y - hero.H*3/4, W: hero.W * 3 / 2, H: hero.H * 5 / 2},
		sdl.Color{R: 255, G: 255, B: 255, A: 255}, 40)
	if !libThemeGeomFailed {
		k := libThemeStageKey{w: h.Width, h: h.Height, top: hero.Y + hero.H, cx: hero.X + hero.W/2, half: hero.W * 35 / 100}
		s := &homeLibThemeStage
		if s.key != k || len(s.v) == 0 {
			libThemeBuildFloor(k)
		}
		if len(s.idx) > 0 {
			_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			if err := renderer.RenderGeometry(nil, s.v, s.idx); err != nil {
				libThemeGeomFail(err)
			}
		}
	}
	// The banner's feet, from just inside its foot down to the heading's line.
	fw := max32(scaleX(22, h.Width), 4)
	fh := max32(f.headY-f.hero.H, 2) + scaleY(4, h.Height)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(20, 21, 24, 255)
	for _, at := range [...]int32{17, 83} {
		cx := hero.X + hero.W*at/100
		_ = renderer.FillRect(&sdl.Rect{X: cx - fw/2, Y: hero.Y + hero.H - scaleY(4, h.Height), W: fw, H: fh})
	}
}

// libThemeBuildFloor builds the boardwalk for k into homeLibThemeStage: planks
// across the view, spaced as a floor recedes (each one a uniform step away,
// so they crowd toward the banner), lighter far off and down the middle where
// the light falls, a dark seam before each one and an end joint somewhere
// along it; the planks' cut ends along both edges; and the banner's shadow on
// the nearest of them.
func libThemeBuildFloor(k libThemeStageKey) {
	s := &homeLibThemeStage
	s.key = k
	s.v, s.idx = s.v[:0], s.idx[:0]
	top, bottom := float64(k.top), float64(k.h)
	if bottom-top < 8 || k.half <= 0 {
		return
	}
	cx, half := float64(k.cx), float64(k.half)
	// The horizon sits high — a fifth of the way down to the banner's foot —
	// so the walk widens gently toward the viewer, past both screen edges.
	yh := top / 5
	reach := bottom - yh // y = yh + reach/z, z = 1 at the screen's foot
	z0 := reach / (top - yh)
	halfAt := func(y float64) float64 { return half * (y - yh) / (top - yh) }
	yAt := func(z float64) float64 { return yh + reach/z }
	pt := func(x, y float64) sdl.FPoint { return sdl.FPoint{X: float32(x), Y: float32(y)} }
	grey := func(v float64, a uint8) sdl.Color {
		g := uint8(math.Max(0, math.Min(255, math.Round(v))))
		return sdl.Color{R: g, G: g, B: uint8(math.Min(255, float64(g)+3)), A: a}
	}
	quad := func(p0, p1, p2, p3 sdl.FPoint, c0, c1, c2, c3 sdl.Color) {
		n := int32(len(s.v))
		s.v = append(s.v,
			sdl.Vertex{Position: p0, Color: c0}, sdl.Vertex{Position: p1, Color: c1},
			sdl.Vertex{Position: p2, Color: c2}, sdl.Vertex{Position: p3, Color: c3})
		s.idx = append(s.idx, n, n+1, n+2, n, n+2, n+3)
	}
	seamInk := grey(24, 255)
	dz := (z0 - 1) / libThemeFloorBoards
	for i := 0; i < libThemeFloorBoards; i++ {
		ya, yb := yAt(z0-float64(i)*dz), yAt(z0-float64(i+1)*dz)
		depth := (ya - top) / (bottom - top)
		tone := 92 - 40*depth + float64((i*37)%13-6)
		seam := math.Max(1, (yb-ya)*0.09)
		ys := ya + seam
		hA, hS, hB := halfAt(ya), halfAt(ys), halfAt(yb)
		quad(pt(cx-hA, ya), pt(cx+hA, ya), pt(cx+hS, ys), pt(cx-hS, ys), seamInk, seamInk, seamInk, seamInk)
		// The plank in two halves, so its middle can be lighter than its
		// ends; its far edge catches a little more light.
		farMid, farEnd := grey(tone+9, 255), grey((tone+9)*0.72, 255)
		nearMid, nearEnd := grey(tone, 255), grey(tone*0.72, 255)
		quad(pt(cx-hS, ys), pt(cx, ys), pt(cx, yb), pt(cx-hB, yb), farEnd, farMid, nearMid, nearEnd)
		quad(pt(cx, ys), pt(cx+hS, ys), pt(cx+hB, yb), pt(cx, yb), farMid, farEnd, nearEnd, nearMid)
		// An end joint where two planks meet, somewhere along it.
		u := 2*(0.15+0.7*math.Mod(float64(i)*0.618034, 1)) - 1
		jw := math.Max(1, hB*0.004)
		xa, xb := cx+u*hS, cx+u*hB
		joint := grey(tone*0.45, 255)
		quad(pt(xa-jw/2, ys), pt(xa+jw/2, ys), pt(xb+jw/2, yb), pt(xb-jw/2, yb), joint, joint, joint, joint)
	}
	// The planks' cut ends along both edges: a band that widens toward the
	// viewer.
	t0, t1 := 2.0, math.Max(4, (bottom-top)*0.03)
	edgeFar, edgeNear := grey(66, 255), grey(44, 255)
	hT, hBot := halfAt(top), halfAt(bottom)
	quad(pt(cx-hT-t0, top), pt(cx-hT, top), pt(cx-hBot, bottom), pt(cx-hBot-t1, bottom), edgeFar, edgeFar, edgeNear, edgeNear)
	quad(pt(cx+hT, top), pt(cx+hT+t0, top), pt(cx+hBot+t1, bottom), pt(cx+hBot, bottom), edgeFar, edgeFar, edgeNear, edgeNear)
	// The banner's shadow on the nearest planks.
	sh := math.Min((bottom-top)*0.18, 40)
	dark, gone := sdl.Color{A: 110}, sdl.Color{}
	quad(pt(cx-hT, top), pt(cx+hT, top), pt(cx+halfAt(top+sh), top+sh), pt(cx-halfAt(top+sh), top+sh), dark, dark, gone, gone)
}

// ── The gallery card ─────────────────────────────────────────────────────

// drawLibraryHomeSketch is the Library style's card in the theme gallery: the
// tab capsule, the dark banner with its kicker, name and button, and the row
// of tall posters with one ringed. It reports false for any other preset.
func drawLibraryHomeSketch(renderer *sdl.Renderer, p ThemePreset, x, y, w, h int32, ink, accent sdl.Color) bool {
	if !homeIsLibraryStyle(p.CarouselStyle) || renderer == nil || w <= 8 || h <= 8 {
		return false
	}
	soft := WithAlpha(ink, 80)
	ring := accent
	if c, ok := parseHexColor(p.FocusRing); ok {
		ring = c
	}
	capW, capH := w*6/10, max32(h/12, 3)
	fillRoundedRect(renderer, x+(w-capW)/2, y, capW, capH, capH/2, soft)
	by := y + capH + max32(h/20, 2)
	bh := h * 42 / 100
	fillRoundedRect(renderer, x, by, w, bh, 4, sdl.Color{R: 10, G: 11, B: 14, A: 235})
	fillRoundedRect(renderer, x+w*45/100, by, w-w*45/100, bh, 4, WithAlpha(ink, 40))
	pad := max32(w/16, 3)
	line := max32(bh/12, 2)
	fillRoundedRect(renderer, x+pad, by+bh/4, w/6, line, 1, ring)
	fillRoundedRect(renderer, x+pad, by+bh/4+line*2, w*3/10, line*2, 1, WithAlpha(ink, 230))
	fillRoundedRect(renderer, x+pad, by+bh*68/100, w/8, max32(bh/7, 3), 2, ring)
	py := by + bh + max32(h/14, 3)
	ph := y + h - py
	if ph <= 4 {
		return true
	}
	pw := max32(ph*2/3, 3)
	gap := max32(w/40, 2)
	for i := int32(0); x+i*(pw+gap) < x+w; i++ {
		px := x + i*(pw+gap)
		ww := min32(pw, x+w-px)
		fillRoundedRect(renderer, px, py, ww, ph, 3, soft)
		if i == 1 && ww == pw {
			strokeRoundedRect(renderer, px-2, py-2, ww+4, ph+4, 4, 2, ring)
		}
	}
	return true
}
