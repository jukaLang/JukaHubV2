package main

// pkgui.go — the Packages store, drawn.
//
// One element fills the scene body and draws three views of the same model:
//
//   - Rows (the default): a hero banner — the updates waiting, or a featured
//     package — over shelves of poster cards that scroll sideways, the page
//     scrolling down shelf by shelf.
//   - Grid: every package as a poster card, under filter chips and a sort.
//   - List: one row per package with its icon, version, size and status.
//
// A opens the details sheet over whichever view is up. Everything follows
// Home's glass recipe (ui_glass.go): glass surfaces, the 3px accent ring on
// the focused control with the lift and bloom behind it, theme inks for every
// word that sits on the backdrop. Poster cards carry their own colour, so the
// type on them is white on every theme.
//
// Frame cost on the Smart Pro: fonts are looked up once a second, every label
// goes through the shared text and measure caches, shapes through the shape
// cache (animated cards reuse their resting shape), and only cards that
// intersect the viewport are drawn. Nothing here touches the disk or the
// network; the one texture that is not text or a shape — a package's own icon
// — comes from the bounded app-icon cache.

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Metrics ──────────────────────────────────────────────────────────────

const (
	pkgPad         = int32(8)
	pkgToolbarH    = int32(44)
	pkgBannerH     = int32(38)
	pkgGap         = int32(16)
	pkgCardW       = int32(176)
	pkgCardR       = int32(16)
	pkgLift        = int32(10)
	pkgHeroH       = int32(168)
	pkgShelfTitleH = int32(30)
	pkgChipsH      = int32(36)
	pkgListRowH    = int32(68)
	pkgListGap     = int32(8)
	pkgLiftMS      = 140.0
)

// pkgWhite is the ink on poster tiles, which are dark on every theme.
var pkgWhite = sdl.Color{R: 255, G: 255, B: 255, A: 255}

// pkgType is the screen's type set.
type pkgType struct {
	at       uint64
	key      int
	title    *ttf.Font
	hero     *ttf.Font
	shelf    *ttf.Font
	name     *ttf.Font
	body     *ttf.Font
	meta     *ttf.Font
	overline *ttf.Font
	mono     *ttf.Font
	monoBig  *ttf.Font
	monoList *ttf.Font
}

var pkgTypeCache pkgType

// pkgFonts returns the type set, looked up again once a second or when the
// text scale changes (a size picked in Accessibility shows within a frame).
func pkgFonts(config *Config) *pkgType {
	t := &pkgTypeCache
	now := sdl.GetTicks64()
	key := a11yScaleFontSize(100)*1000 + uiScaleFontSize(100)
	if t.name != nil && key == t.key && now-t.at < 1000 {
		return t
	}
	t.at, t.key = now, key
	t.title = getFontSized(config, "small", 24)
	t.hero = getFontSized(config, "small", 30)
	t.shelf = getFontSized(config, "small", 19)
	t.name = getFontSized(config, "small", 16)
	t.body = getFontSized(config, "small", 15)
	t.meta = getFontSized(config, "small", 13)
	t.overline = getFontSized(config, "small", 12)
	t.mono = getFontSized(config, "small", 40)
	t.monoBig = getFontSized(config, "small", 56)
	t.monoList = getFontSized(config, "small", 20)
	return t
}

// pkgFontH is a font's line height, 0 for a missing font.
func pkgFontH(f *ttf.Font) int32 {
	if f == nil {
		return 0
	}
	return int32(f.Height())
}

// pkgClip runs draw with the clip set to r inside the current clip, and puts
// the old clip back. A nested clip that ignored its parent let a shelf half
// scrolled under the toolbar paint over it.
func pkgClip(renderer *sdl.Renderer, r sdl.Rect, draw func()) {
	prevOn := renderer.IsClipEnabled()
	prev := renderer.GetClipRect()
	clip := r
	if prevOn {
		clip = glassIntersect(prev, r)
	}
	if clip.W <= 0 || clip.H <= 0 {
		return
	}
	_ = renderer.SetClipRect(&clip)
	draw()
	if prevOn {
		_ = renderer.SetClipRect(&prev)
	} else {
		_ = renderer.SetClipRect(nil)
	}
}

// pkgAddHit records a click target for the input side.
func pkgAddHit(h pkgHit) {
	if h.Rect.W <= 0 || h.Rect.H <= 0 {
		return
	}
	pkgUI.hits = append(pkgUI.hits, h)
}

// pkgBodyFocused reports whether the pad is on the store rather than on the
// header's buttons.
func pkgBodyFocused() bool {
	return chromeTier(focusEngine.ElementIndex()) == 0
}

// ── Live install state ───────────────────────────────────────────────────

// pkgLive is the install card's state for this frame.
type pkgLive struct {
	tool   string
	active bool
	frac   float64
}

func pkgLiveNow() pkgLive {
	visible, tool, phase, _, done, total, _, _, ending := installBarSnapshot()
	if !visible || ending {
		return pkgLive{}
	}
	l := pkgLive{tool: tool, active: true, frac: -1}
	if total > 0 && phase == installPhaseDownload {
		l.frac = float64(done) / float64(total)
		if l.frac > 1 {
			l.frac = 1
		}
	}
	return l
}

// pkgStatus is what a card's badge says.
type pkgStatus int

const (
	pkgStatusGet pkgStatus = iota
	pkgStatusInstalled
	pkgStatusUpdate
	pkgStatusQueued
	pkgStatusInstalling
	pkgStatusRemoving
)

// pkgStatusOf is an item's status this frame, with the install progress
// (-1 when unknown).
func pkgStatusOf(it *pkgItem, live pkgLive) (pkgStatus, float64) {
	if live.active && live.tool == it.installTool() {
		return pkgStatusInstalling, live.frac
	}
	if it.Kind == pkgKindPackage {
		queued, current := pkgQueueState(it.Pkg.Name)
		if current {
			return pkgStatusInstalling, -1
		}
		if queued {
			return pkgStatusQueued, -1
		}
	}
	if pkgUI.removing[it.Key] {
		return pkgStatusRemoving, -1
	}
	if it.HasUpdate {
		return pkgStatusUpdate, -1
	}
	if it.Installed {
		return pkgStatusInstalled, -1
	}
	return pkgStatusGet, -1
}

// pkgStatusText is the badge label.
func pkgStatusText(s pkgStatus, frac float64) string {
	switch s {
	case pkgStatusInstalled:
		return "Installed"
	case pkgStatusUpdate:
		return "Update"
	case pkgStatusQueued:
		return "Queued"
	case pkgStatusInstalling:
		if frac >= 0 {
			return strconv.Itoa(int(frac*100+0.5)) + "%"
		}
		return "Installing…"
	case pkgStatusRemoving:
		return "Removing…"
	}
	return "Get"
}

// pkgDrawBadge paints a status pill with its right edge at right and its top
// at top, and returns its rect. onTile picks the variant for a poster.
func pkgDrawBadge(renderer *sdl.Renderer, config *Config, font *ttf.Font, s pkgStatus, frac float64, right, top int32, onTile, dim bool) sdl.Rect {
	label := pkgStatusText(s, frac)
	tw, th := measureText(font, label)
	h := th + 6
	if h < 20 {
		h = 20
	}
	w := tw + 18
	r := sdl.Rect{X: right - w, Y: top, W: w, H: h}
	fill := glassA(26)
	ink := ColorTextPrimary()
	if onTile {
		fill = sdl.Color{R: 0, G: 0, B: 0, A: 120}
		ink = pkgWhite
	}
	switch s {
	case pkgStatusInstalled:
		fill = ColorSuccess
		ink = contrastText(ColorSuccess)
	case pkgStatusUpdate:
		fill = glassAccent()
		ink = contrastText(fill)
	case pkgStatusQueued, pkgStatusInstalling, pkgStatusRemoving:
		if onTile {
			fill = sdl.Color{R: 0, G: 0, B: 0, A: 150}
		}
		ink = glassAccent()
	}
	if dim && s == pkgStatusGet {
		ink = WithAlpha(ink, 140)
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, fill)
	if s == pkgStatusGet && !onTile {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, 1, glassA(40))
	}
	renderText(renderer, config, font, label, ink, r.X+(r.W-tw)/2, r.Y+(r.H-th)/2)
	return r
}

// ── Tiles and cards ──────────────────────────────────────────────────────

// pkgTileSheens are the poster's light layers: alpha and the share of the
// height (in thirds) each covers, top down.
var pkgTileSheens = [...]struct {
	alpha  uint8
	thirds int32
}{{18, 1}, {9, 2}}

// pkgTileBands paints a poster's light: two sheens down from the top and a
// darker band at the bottom. Each layer is the poster's own rounded shape
// clipped to a band, so no layer pokes past the rounded corners and every
// layer is one cached shape copy — three copies a card, which keeps a screen
// of twenty cards inside the handheld's frame budget.
func pkgTileBands(renderer *sdl.Renderer, base, r sdl.Rect, radius int32, bandH int32) {
	for _, sh := range pkgTileSheens {
		color := sdl.Color{R: 255, G: 255, B: 255, A: sh.alpha}
		pkgClip(renderer, sdl.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H * sh.thirds / 3}, func() {
			fillRoundedRectAt(renderer, base, r, radius, color)
		})
	}
	if bandH > 0 {
		pkgClip(renderer, sdl.Rect{X: r.X, Y: r.Y + r.H - bandH, W: r.W, H: bandH}, func() {
			fillRoundedRectAt(renderer, base, r, radius, sdl.Color{R: 0, G: 0, B: 0, A: 120})
		})
	}
}

// pkgDrawArt paints the picture part of a tile: the package's own icon when
// the folder ships one, otherwise its monogram.
func pkgDrawArt(renderer *sdl.Renderer, config *Config, it *pkgItem, area sdl.Rect, mono *ttf.Font) {
	if it.Icon != "" {
		if tex := appIconTexture(renderer, it.Icon); tex != nil {
			dims, ok := pkgUI.iconDims[it.Icon]
			if !ok {
				_, _, w, h, err := tex.Query()
				if err != nil || w <= 0 || h <= 0 {
					w, h = 1, 1
				}
				dims = [2]int32{w, h}
				if len(pkgUI.iconDims) > 256 {
					pkgUI.iconDims = map[string][2]int32{}
				}
				pkgUI.iconDims[it.Icon] = dims
			}
			box := area.H * 3 / 5
			if area.W*3/5 < box {
				box = area.W * 3 / 5
			}
			w, h := box, box
			if dims[0] > dims[1] {
				h = box * dims[1] / dims[0]
			} else if dims[1] > dims[0] {
				w = box * dims[0] / dims[1]
			}
			dst := sdl.Rect{X: area.X + (area.W-w)/2, Y: area.Y + (area.H-h)/2, W: w, H: h}
			_ = renderer.Copy(tex, nil, &dst)
			return
		}
	}
	if mono == nil {
		return
	}
	mw, mh := measureText(mono, it.Initials)
	renderText(renderer, config, mono, it.Initials, sdl.Color{R: 255, G: 255, B: 255, A: 235},
		area.X+(area.W-mw)/2, area.Y+(area.H-mh)/2)
}

// pkgLiftRect is a card grown by its focus lift: up to 6% larger and 4px
// higher, around its own centre.
func pkgLiftRect(rest sdl.Rect, lift float64) sdl.Rect {
	if lift <= 0 {
		return rest
	}
	gw := int32(math.Round(float64(rest.W) * 0.06 * lift))
	gh := int32(math.Round(float64(rest.H) * 0.06 * lift))
	up := int32(math.Round(4 * lift))
	return sdl.Rect{X: rest.X - gw/2, Y: rest.Y - gh/2 - up, W: rest.W + gw, H: rest.H + gh}
}

// pkgDrawProgress paints install progress along a strip; frac < 0 is the
// travelling shimmer of a step with no byte count.
func pkgDrawProgress(renderer *sdl.Renderer, r sdl.Rect, frac float64, now uint64) {
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, sdl.Color{R: 255, G: 255, B: 255, A: 50})
	if frac >= 0 {
		UIProgressBar(renderer, r, frac, sdl.Color{}, glassAccent())
		return
	}
	span := r.W / 3
	period := uint64(1200)
	shift := int32(float64(r.W+span) * float64(now%period) / float64(period))
	pkgClip(renderer, r, func() {
		fillRoundedRect(renderer, r.X+shift-span, r.Y, span, r.H, r.H/2, glassAccent())
	})
}

// pkgDrawCard paints one poster card at its resting rect, lifted by lift
// (0..1) and ringed when focused.
func pkgDrawCard(renderer *sdl.Renderer, config *Config, ft *pkgType, it *pkgItem, rest sdl.Rect, focused bool, lift float64, live pkgLive, now uint64) {
	r := pkgLiftRect(rest, lift)
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	fillRoundedRectAt(renderer, rest, r, pkgCardR, it.Tile)
	bandH := pkgFontH(ft.name) + pkgFontH(ft.meta) + 18
	if bandH > r.H/2 {
		bandH = r.H / 2
	}
	pkgTileBands(renderer, rest, r, pkgCardR, bandH)
	art := sdl.Rect{X: r.X, Y: r.Y + 4, W: r.W, H: r.H - bandH - 4}
	pkgDrawArt(renderer, config, it, art, ft.mono)

	status, frac := pkgStatusOf(it, live)
	offline := !pkgUI.online
	pkgDrawBadge(renderer, config, ft.meta, status, frac, r.X+r.W-8, r.Y+8, true, offline)

	textX := r.X + 12
	textW := r.W - 24
	nameY := r.Y + r.H - bandH + 7
	renderText(renderer, config, ft.name, ellipsize(ft.name, it.Pkg.Name, textW), pkgWhite, textX, nameY)
	if it.Meta != "" {
		renderText(renderer, config, ft.meta, ellipsize(ft.meta, it.Meta, textW),
			sdl.Color{R: 255, G: 255, B: 255, A: 185}, textX, nameY+pkgFontH(ft.name)+1)
	}
	if status == pkgStatusInstalling || status == pkgStatusRemoving {
		pkgDrawProgress(renderer, sdl.Rect{X: r.X + 12, Y: r.Y + r.H - 7, W: r.W - 24, H: 3}, frac, now)
	}
	if offline && status == pkgStatusGet {
		// Not on the card and no way to fetch it: the poster steps back.
		fillRoundedRectAt(renderer, rest, r, pkgCardR, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	}
	if focused {
		strokeRoundedRectAt(renderer, rest, r, pkgCardR, glassFocusRingWidth, glassAccent())
	} else {
		strokeRoundedRectAt(renderer, rest, r, pkgCardR, 1, sdl.Color{R: 255, G: 255, B: 255, A: 28})
	}
}

// pkgLiftFor is a card's lift progress this frame: rising for the focused
// card, falling for the one that just lost the focus.
func pkgLiftFor(id pkgFocusKey, focused bool, now uint64, motion bool) float64 {
	st := &pkgUI
	if focused {
		if !motion || now < st.liftSince {
			return 1
		}
		return uiEaseOutCubic(float64(now-st.liftSince) / pkgLiftMS)
	}
	if motion && id.kind != 0 && id == st.dropKey && now >= st.dropSince {
		t := float64(now-st.dropSince) / pkgLiftMS
		if t < 1 {
			return 1 - uiEaseOutCubic(t)
		}
	}
	return 0
}

// pkgTrackFocus notices a focus change and starts the lift animations.
func pkgTrackFocus(id pkgFocusKey, now uint64) {
	st := &pkgUI
	if id == st.liftKey {
		return
	}
	st.dropKey, st.dropSince = st.liftKey, now
	st.liftKey, st.liftSince = id, now
}

// pkgDrawSpinner is eight dots round a circle with a bright one walking.
func pkgDrawSpinner(renderer *sdl.Renderer, cx, cy, radius int32, now uint64) {
	step := int(now/90) % 8
	accent := glassAccent()
	for i := 0; i < 8; i++ {
		a := float64(i) * math.Pi / 4
		x := cx + int32(math.Round(math.Cos(a)*float64(radius)))
		y := cy + int32(math.Round(math.Sin(a)*float64(radius)))
		age := (step - i + 8) % 8
		fillCircle(renderer, x, y, 2, WithAlpha(accent, uint8(235-age*26)))
	}
}

// pkgDrawKeyBadge paints a controller button name in Home's glass pill.
func pkgDrawKeyBadge(renderer *sdl.Renderer, config *Config, font *ttf.Font, label string, x, cy int32) sdl.Rect {
	tw, th := measureText(font, label)
	h := th + 4
	if h < 20 {
		h = 20
	}
	w := tw + 12
	if w < h {
		w = h
	}
	r := sdl.Rect{X: x, Y: cy - h/2, W: w, H: h}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, glassChipFill())
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, 1, glassChipEdge())
	renderText(renderer, config, font, label, glassChipInk(), r.X+(r.W-tw)/2, r.Y+(r.H-th)/2)
	return r
}

// ── The screen ───────────────────────────────────────────────────────────

// renderPackageList draws the store into the packagelist element.
func renderPackageList(renderer *sdl.Renderer, config *Config, element Element) {
	if renderer == nil || config == nil {
		return
	}
	pkgSync(config)
	st := &pkgUI
	now := sdl.GetTicks64()
	dt := 0.0
	if st.lastTick != 0 && now > st.lastTick {
		dt = float64(now - st.lastTick)
		if dt > 100 {
			dt = 100
		}
	}
	st.lastTick = now
	motion := uiMotionEnabled(config)
	ft := pkgFonts(config)
	st.hits = st.hits[:0]
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	body := sdl.Rect{X: element.X, Y: element.Y, W: getElementWidth(element, 1232), H: getElementHeight(element, 576)}
	inner := sdl.Rect{X: body.X + pkgPad, Y: body.Y, W: body.W - 2*pkgPad, H: body.H}
	pkgAddHit(pkgHit{Kind: pkgHitBody, Rect: body})
	toolbar := sdl.Rect{X: inner.X, Y: inner.Y, W: inner.W, H: pkgToolbarH}
	pkgDrawToolbar(renderer, config, ft, toolbar, now)
	y := toolbar.Y + toolbar.H + 8
	if !st.online {
		banner := sdl.Rect{X: inner.X, Y: y, W: inner.W, H: pkgBannerH}
		pkgDrawOfflineBanner(renderer, config, ft, banner)
		y += pkgBannerH + 10
	}
	content := sdl.Rect{X: inner.X, Y: y, W: inner.W, H: body.Y + body.H - y}
	live := pkgLiveNow()
	m := st.model
	switch {
	case m == nil || (len(m.Items) == 0 && m.App == nil):
		pkgDrawEmpty(renderer, config, ft, body, content, now)
	case st.view == pkgViewRows:
		pkgDrawRows(renderer, config, ft, body, content, live, dt, motion, now)
	default:
		pkgDrawGridOrList(renderer, config, ft, body, content, live, dt, motion, now)
	}
	if st.sheet.open {
		pkgDrawSheet(renderer, config, ft, body, live, dt, motion, now)
	}
}

// pkgStatusLine is the toolbar's one line of state next to the title.
func pkgStatusLine(now time.Time) (string, sdl.Color) {
	st := &pkgUI
	m := st.model
	switch {
	case !st.haveCatalog && st.loading:
		return "Loading the package list…", ColorTextTertiary()
	case !st.haveCatalog && !st.online:
		return "Offline · showing what is on this device", WarningInk()
	case st.loading:
		return "Refreshing…", ColorTextSecondary()
	case st.fetchErr != "" && st.haveCatalog:
		return "Couldn't refresh — showing the saved list", WarningInk()
	case st.fetchErr != "":
		return st.fetchErr, WarningInk()
	}
	if m == nil {
		return "", ColorTextTertiary()
	}
	parts := make([]string, 0, 4)
	parts = append(parts, strconv.Itoa(m.Available)+" apps")
	if m.Installed > 0 {
		parts = append(parts, strconv.Itoa(m.Installed)+" installed")
	}
	if m.Updates == 1 {
		parts = append(parts, "1 update")
	} else if m.Updates > 1 {
		parts = append(parts, strconv.Itoa(m.Updates)+" updates")
	}
	if ago := pkgAgo(st.fetchedAt, now); ago != "" {
		parts = append(parts, "updated "+ago)
	} else if st.haveCatalog && !st.fromNetwork {
		parts = append(parts, "saved list")
	}
	return strings.Join(parts, " · "), ColorTextTertiary()
}

// pkgStatusCache keeps the status line from being rebuilt every frame.
var pkgStatusCache struct {
	gen     uint64
	loading bool
	online  bool
	err     string
	minute  int64
	text    string
	color   sdl.Color
}

func pkgStatusLineCached() (string, sdl.Color) {
	st := &pkgUI
	c := &pkgStatusCache
	now := time.Now()
	minute := now.Unix() / 30
	if c.text != "" && c.gen == st.modelGen && c.loading == st.loading && c.online == st.online && c.err == st.fetchErr && c.minute == minute {
		return c.text, c.color
	}
	c.text, c.color = pkgStatusLine(now)
	c.gen, c.loading, c.online, c.err, c.minute = st.modelGen, st.loading, st.online, st.fetchErr, minute
	return c.text, c.color
}

// pkgDrawToolbar paints the title, the state line, the view switch and the
// refresh chip.
func pkgDrawToolbar(renderer *sdl.Renderer, config *Config, ft *pkgType, r sdl.Rect, now uint64) {
	st := &pkgUI
	segH := int32(34)
	segW := int32(46)
	midY := r.Y + r.H/2
	segY := midY - segH/2

	// Refresh chip at the right edge: the X badge (a spinner while working).
	label := "Refresh"
	if st.loading {
		label = "Refreshing"
	}
	lw, lh := measureText(ft.meta, label)
	lead := int32(24)
	chipW := 12 + lead + 8 + lw + 14
	chip := sdl.Rect{X: r.X + r.W - chipW, Y: segY, W: chipW, H: segH}
	drawGlassSurface(renderer, chip, segH/2, glassState{Hovered: pointInRect(mouseX, mouseY, chip), NoLift: true})
	if st.loading {
		pkgDrawSpinner(renderer, chip.X+12+lead/2, midY, 7, now)
	} else {
		pkgDrawKeyBadge(renderer, config, ft.meta, "X", chip.X+12, midY)
	}
	renderText(renderer, config, ft.meta, label, ColorTextSecondary(), chip.X+12+lead+8, midY-lh/2)
	pkgAddHit(pkgHit{Kind: pkgHitRefresh, Rect: chip})

	// View switch: rows, grid, list.
	segTotal := segW * int32(pkgViewModes)
	seg := sdl.Rect{X: chip.X - 12 - segTotal, Y: segY, W: segTotal, H: segH}
	fillRoundedRect(renderer, seg.X, seg.Y, seg.W, seg.H, segH/2, glassA(10))
	strokeRoundedRect(renderer, seg.X, seg.Y, seg.W, seg.H, segH/2, 1, glassA(26))
	icons := [...]string{"rows", "grid", "list"}
	for i := 0; i < int(pkgViewModes); i++ {
		cell := sdl.Rect{X: seg.X + int32(i)*segW, Y: segY, W: segW, H: segH}
		in := insetRect(cell, 3)
		selected := int(st.view) == i
		if selected {
			fillRoundedRect(renderer, in.X, in.Y, in.W, in.H, in.H/2, glassAccent())
		} else if pointInRect(mouseX, mouseY, cell) {
			fillRoundedRect(renderer, in.X, in.Y, in.W, in.H, in.H/2, glassA(22))
		}
		g := int32(18)
		DrawHomeIcon(renderer, icons[i], sdl.Rect{X: cell.X + (segW-g)/2, Y: cell.Y + (segH-g)/2, W: g, H: g}, selected)
		pkgAddHit(pkgHit{Kind: pkgHitView, Rect: cell, Index: i})
	}
	yBadge := pkgDrawKeyBadge(renderer, config, ft.meta, "Y", seg.X-8-24, midY)

	// Title and state on the left, in whatever room is left.
	title := "Packages"
	tw, th := measureText(ft.title, title)
	renderText(renderer, config, ft.title, title, ColorTextPrimary(), r.X, midY-th/2)
	sx := r.X + tw + 14
	if avail := yBadge.X - 16 - sx; avail > 60 {
		text, tone := pkgStatusLineCached()
		text = ellipsize(ft.body, text, avail)
		_, sh := measureText(ft.body, text)
		renderText(renderer, config, ft.body, text, tone, sx, midY-sh/2+2)
	}
}

// pkgDrawOfflineBanner is the compact offline notice.
func pkgDrawOfflineBanner(renderer *sdl.Renderer, config *Config, ft *pkgType, r sdl.Rect) {
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(ColorWarning, 34))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, WithAlpha(ColorWarning, 130))
	g := int32(20)
	DrawHomeIcon(renderer, "wifi-off", sdl.Rect{X: r.X + 14, Y: r.Y + (r.H-g)/2, W: g, H: g}, false)
	tx := r.X + 14 + g + 10
	text := ellipsize(ft.body, pkgOfflineNotice, r.X+r.W-16-tx)
	_, th := measureText(ft.body, text)
	renderText(renderer, config, ft.body, text, ColorTextPrimary(), tx, r.Y+(r.H-th)/2)
}

// pkgOfflineNotice is the banner's sentence.
const pkgOfflineNotice = "Offline — showing installed and cached packages. Connect Wi-Fi to download new ones."

// pkgDrawEmpty is the screen with nothing to list: skeleton shelves while the
// first list loads, or a plain state saying why there is nothing.
func pkgDrawEmpty(renderer *sdl.Renderer, config *Config, ft *pkgType, body, c sdl.Rect, now uint64) {
	st := &pkgUI
	if (!st.cacheTried || st.loading) && !st.haveCatalog {
		phase := float64(now%100000) / 1000
		UILoadingSkeleton(renderer, sdl.Rect{X: c.X, Y: c.Y, W: c.W, H: pkgHeroH}, phase)
		y := c.Y + pkgHeroH + 24
		cardH := pkgCardW * 3 / 4
		for row := 0; row < 2 && y+cardH < c.Y+c.H; row++ {
			UILoadingSkeleton(renderer, sdl.Rect{X: c.X, Y: y, W: 180, H: 18}, phase)
			y += 30
			for x := c.X; x+pkgCardW <= c.X+c.W; x += pkgCardW + pkgGap {
				UILoadingSkeleton(renderer, sdl.Rect{X: x, Y: y, W: pkgCardW, H: cardH}, phase+float64(x-c.X)/900)
			}
			y += cardH + 30
		}
		return
	}
	switch {
	case !st.online:
		UIEmptyState(renderer, config, c, "@wifi-off", "You're offline",
			"Connect Wi-Fi to browse packages. Installed packages appear here.")
	case st.fetchErr != "":
		msg := st.fetchErr
		if msg != offlinePlainNotice {
			msg += " Press X to try again."
		}
		if f := FontForRole(config, TypeCaption); f != nil {
			msg = ellipsize(f, msg, c.W-48)
		}
		UIErrorState(renderer, config, c, "@packages", "Couldn't load packages", msg)
	default:
		UIEmptyState(renderer, config, c, "@packages", "No packages yet", "Press X to load the package list.")
	}
}

// ── Rows view ────────────────────────────────────────────────────────────

// pkgHeroMode is what the hero banner shows: the updates waiting, a featured
// package, or nothing.
const (
	pkgHeroNone = iota
	pkgHeroUpdates
	pkgHeroFeatured
)

// pkgHeroFor picks the hero and its item.
func pkgHeroFor(m *pkgModel) (int, *pkgItem) {
	if m == nil {
		return pkgHeroNone, nil
	}
	if m.Updates > 0 {
		for _, s := range m.Shelves {
			if s.ID == "updates" && len(s.Items) > 0 {
				return pkgHeroUpdates, s.Items[0]
			}
		}
	}
	if len(m.Featured) > 0 {
		i := pkgUI.heroIndex % len(m.Featured)
		if i < 0 {
			i += len(m.Featured)
		}
		return pkgHeroFeatured, m.Featured[i]
	}
	return pkgHeroNone, nil
}

// pkgRowsGeometry is the rows view's page layout for a card size.
func pkgRowsGeometry(m *pkgModel) (heroH, rowsTop, shelfH, cardH int32) {
	cardH = pkgCardW * 3 / 4
	shelfH = pkgShelfTitleH + 6 + cardH + 2*pkgLift + 10
	if mode, _ := pkgHeroFor(m); mode != pkgHeroNone {
		heroH = pkgHeroH
		rowsTop = pkgHeroH + 18
	}
	return heroH, rowsTop, shelfH, cardH
}

// pkgRevealRows sets the scroll targets that bring the focused row and card
// into view: the hero at the top, the first shelf just below the hero, every
// other shelf aligned to the top like a TV shelf.
func pkgRevealRows(m *pkgModel, c sdl.Rect) {
	st := &pkgUI
	_, rowsTop, shelfH, _ := pkgRowsGeometry(m)
	pageH := rowsTop + int32(len(m.Shelves))*shelfH
	maxPage := float64(pageH - c.H)
	shelf, si := pkgShelfAtRow(m, st.rowFocus)
	switch {
	case shelf == nil:
		st.pageScroll.target = 0
	case si == 0 && rowsTop > 0:
		st.pageScroll.target = math.Max(0, float64(rowsTop+shelfH-c.H))
	default:
		st.pageScroll.target = float64(rowsTop + int32(si)*shelfH)
	}
	st.pageScroll.clamp(maxPage)
	if shelf == nil {
		return
	}
	pitch := pkgCardW + pkgGap
	n := len(shelf.Items)
	col := clampIndex(st.rowCols[shelf.ID], n)
	scroll := pkgShelfScroll(shelf.ID)
	viewW := c.W
	contentW := int32(n)*pitch - pkgGap
	colX := int32(col) * pitch
	peek := pitch / 2
	if col == n-1 {
		peek = 0
	}
	target := scroll.target
	if float64(colX+pkgCardW+peek) > target+float64(viewW) {
		target = float64(colX + pkgCardW + peek - viewW)
	}
	lead := pitch / 3
	if col == 0 {
		lead = 0
	}
	if float64(colX-lead) < target {
		target = float64(colX - lead)
	}
	scroll.target = target
	scroll.clamp(float64(contentW - viewW))
}

// pkgShelfScroll is a shelf's horizontal scroll, created on first use.
func pkgShelfScroll(id string) *pkgScroll {
	pkgEnsureMaps()
	s := pkgUI.rowScroll[id]
	if s == nil {
		s = &pkgScroll{}
		pkgUI.rowScroll[id] = s
	}
	return s
}

// pkgDrawRows paints the hero and the shelves.
func pkgDrawRows(renderer *sdl.Renderer, config *Config, ft *pkgType, body, c sdl.Rect, live pkgLive, dt float64, motion bool, now uint64) {
	st := &pkgUI
	m := st.model
	heroH, rowsTop, shelfH, cardH := pkgRowsGeometry(m)
	rows := pkgRowCount(m)
	st.rowFocus = clampIndex(st.rowFocus, rows)
	if st.reveal {
		st.reveal = false
		pkgRevealRows(m, c)
	}
	pageH := rowsTop + int32(len(m.Shelves))*shelfH
	st.pageScroll.clamp(float64(pageH - c.H))
	st.pageScroll.step(dt, motion)
	scrollY := int32(math.Round(st.pageScroll.cur))
	bodyFocused := pkgBodyFocused()
	clip := sdl.Rect{X: body.X, Y: c.Y, W: body.W, H: c.H}
	st.viewW = c.W
	pkgTrackFocus(pkgCurrentFocusID(), now)

	pkgClip(renderer, clip, func() {
		top := c.Y - scrollY
		if heroH > 0 {
			hero := sdl.Rect{X: c.X, Y: top, W: c.W, H: heroH}
			if hero.Y+hero.H > c.Y {
				heroFocused := bodyFocused && st.rowFocus == 0
				pkgDrawHero(renderer, config, ft, hero, heroFocused, live, motion, now)
			}
		}
		for si := range m.Shelves {
			shelf := &m.Shelves[si]
			row := si
			if heroH > 0 {
				row++
			}
			area := sdl.Rect{X: c.X, Y: top + rowsTop + int32(si)*shelfH, W: c.W, H: shelfH}
			focusedRow := bodyFocused && st.rowFocus == row
			scroll := pkgShelfScroll(shelf.ID)
			scroll.clamp(float64(int32(len(shelf.Items))*(pkgCardW+pkgGap) - pkgGap - c.W))
			scroll.step(dt, motion)
			if area.Y+area.H < c.Y || area.Y > c.Y+c.H {
				continue
			}
			pkgDrawShelf(renderer, config, ft, shelf, row, area, c, cardH, focusedRow, live, motion, now)
		}
	})
	if rows > 0 && pageH > c.H {
		pkgDrawScrollbar(renderer, sdl.Rect{X: body.X + body.W - 4, Y: c.Y + 4, W: 3, H: c.H - 8}, float64(scrollY), float64(pageH), float64(c.H), false)
	}
}

// pkgDrawScrollbar is a thin glass track with its thumb.
func pkgDrawScrollbar(renderer *sdl.Renderer, track sdl.Rect, offset, content, view float64, active bool) {
	if content <= view || track.H <= 8 {
		return
	}
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, track.W/2, glassA(14))
	th := int32(float64(track.H) * view / content)
	if th < 24 {
		th = 24
	}
	ty := track.Y + int32(float64(track.H-th)*offset/(content-view))
	color := glassA(70)
	if active {
		color = glassAccent()
	}
	fillRoundedRect(renderer, track.X, ty, track.W, th, track.W/2, color)
}

// pkgDrawShelf paints one shelf: its heading and the cards that are in view.
func pkgDrawShelf(renderer *sdl.Renderer, config *Config, ft *pkgType, shelf *pkgShelf, row int, area, c sdl.Rect, cardH int32, focusedRow bool, live pkgLive, motion bool, now uint64) {
	st := &pkgUI
	// Heading: glyph badge, title, count.
	g := int32(28)
	badge := sdl.Rect{X: area.X, Y: area.Y + (pkgShelfTitleH-g)/2, W: g, H: g}
	glyph := drawGlassBadge(renderer, badge)
	DrawHomeIcon(renderer, shelf.Icon, glyph, false)
	tx := badge.X + g + 10
	tw, th := measureText(ft.shelf, shelf.Title)
	titleColor := ColorTextSecondary()
	if focusedRow {
		titleColor = ColorTextPrimary()
	}
	renderText(renderer, config, ft.shelf, shelf.Title, titleColor, tx, area.Y+(pkgShelfTitleH-th)/2)
	count := shelf.CountText
	cw, ch := measureText(ft.meta, count)
	pill := sdl.Rect{X: tx + tw + 10, Y: area.Y + (pkgShelfTitleH-ch-6)/2, W: cw + 14, H: ch + 6}
	fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, glassA(16))
	renderText(renderer, config, ft.meta, count, ColorTextTertiary(), pill.X+7, pill.Y+3)

	// Cards: the focused one last, so its lift and bloom sit over its
	// neighbours.
	n := len(shelf.Items)
	pitch := pkgCardW + pkgGap
	scroll := pkgShelfScroll(shelf.ID)
	sx := int32(math.Round(scroll.cur))
	col := clampIndex(st.rowCols[shelf.ID], n)
	cardY := area.Y + pkgShelfTitleH + 6 + pkgLift
	var focusID pkgFocusKey
	var focusedItem *pkgItem
	var focusedRest sdl.Rect
	for i := 0; i < n; i++ {
		x := area.X + int32(i)*pitch - sx
		if x+pkgCardW < c.X-pkgLift-24 || x > c.X+c.W+24 {
			continue
		}
		it := shelf.Items[i]
		rest := sdl.Rect{X: x, Y: cardY, W: pkgCardW, H: cardH}
		id := pkgFocusKey{kind: 'r', shelf: shelf.ID, key: it.Key}
		if focusedRow && i == col {
			focusedItem, focusedRest, focusID = it, rest, id
		} else {
			pkgDrawCard(renderer, config, ft, it, rest, false, pkgLiftFor(id, false, now, motion), live, now)
		}
		pkgAddHit(pkgHit{Kind: pkgHitCard, Rect: glassIntersect(rest, c), Row: row, Index: i, Key: it.Key})
	}
	if focusedItem != nil {
		pkgDrawCard(renderer, config, ft, focusedItem, focusedRest, true, pkgLiftFor(focusID, true, now, motion), live, now)
	}

	// Arrows for a mouse: a shelf that runs past either edge offers a page
	// while the pointer is over it (the handheld has no pointer, so its
	// shelves never wear them).
	maxX := int32(n)*pitch - pkgGap - c.W
	if maxX > 0 && pointInRect(mouseX, mouseY, glassIntersect(area, c)) {
		arrowY := cardY + cardH/2
		if sx > 0 {
			pkgDrawShelfArrow(renderer, config, ft, sdl.Rect{X: c.X + 4, Y: arrowY - 18, W: 36, H: 36}, "‹", row, -1)
		}
		if sx < maxX {
			pkgDrawShelfArrow(renderer, config, ft, sdl.Rect{X: c.X + c.W - 40, Y: arrowY - 18, W: 36, H: 36}, "›", row, 1)
		}
	}
}

// pkgDrawShelfArrow is a round glass button that pages a shelf sideways.
func pkgDrawShelfArrow(renderer *sdl.Renderer, config *Config, ft *pkgType, r sdl.Rect, glyph string, row, dir int) {
	hovered := pointInRect(mouseX, mouseY, r)
	fill := glassSheetFill()
	fill.A = 215
	if hovered {
		fill = glassMix(glassSheetFill(), glassInk(), 0.12)
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, fill)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassA(40))
	gw, gh := measureText(ft.shelf, glyph)
	renderText(renderer, config, ft.shelf, glyph, ColorTextPrimary(), r.X+(r.W-gw)/2, r.Y+(r.H-gh)/2-1)
	pkgAddHit(pkgHit{Kind: pkgHitShelfArrow, Rect: r, Row: row, Index: dir})
}

// pkgWrapKey is what a wrapped text block was laid out for. It is compared
// field by field, so asking "is it still valid" allocates nothing.
type pkgWrapKey struct {
	key   string
	width int32
	gen   uint64
	font  *ttf.Font
}

// pkgHeroWrap remembers the hero description's two lines.
var pkgHeroWrap struct {
	at    pkgWrapKey
	lines []string
}

// pkgHeroLines wraps the hero description to two lines, remembered per item,
// width and font so it is not re-wrapped every frame.
func pkgHeroLines(font *ttf.Font, it *pkgItem, width int32) []string {
	at := pkgWrapKey{key: it.Key, width: width, gen: pkgUI.modelGen, font: font}
	if pkgHeroWrap.at == at && pkgHeroWrap.lines != nil {
		return pkgHeroWrap.lines
	}
	lines := wrapPackageText(strings.TrimSpace(it.Pkg.Description), font, width)
	if len(lines) > 2 {
		lines = append(lines[:1:1], ellipsize(font, lines[1]+" "+lines[2], width))
	}
	pkgHeroWrap.at, pkgHeroWrap.lines = at, lines
	return lines
}

// pkgHeroButtons is what the hero's two buttons say.
func pkgHeroButtons(mode int, it *pkgItem, live pkgLive) (primary, secondary string, primaryOff bool) {
	m := pkgUI.model
	if mode == pkgHeroUpdates {
		n := 0
		for _, x := range m.Items {
			if x.HasUpdate {
				n++
			}
		}
		if n == 0 && m.App != nil {
			return "Update JukaHub", "See updates", !pkgUI.online
		}
		return "Update all", "See updates", !pkgUI.online
	}
	status, frac := pkgStatusOf(it, live)
	switch status {
	case pkgStatusInstalled:
		return "Open folder", "Details", false
	case pkgStatusUpdate:
		return "Update", "Details", !pkgUI.online
	case pkgStatusQueued:
		return "Queued", "Details", true
	case pkgStatusInstalling:
		return pkgStatusText(status, frac), "Details", true
	case pkgStatusRemoving:
		return "Removing…", "Details", true
	}
	return "Get", "Details", !pkgUI.online
}

// pkgDrawHero paints the banner over the shelves.
func pkgDrawHero(renderer *sdl.Renderer, config *Config, ft *pkgType, r sdl.Rect, focused bool, live pkgLive, motion bool, now uint64) {
	st := &pkgUI
	mode, it := pkgHeroFor(st.model)
	if it == nil {
		return
	}
	// Featured packages take turns while the hero is not being used.
	if mode == pkgHeroFeatured && !focused && len(st.model.Featured) > 1 && now-st.heroSince > 9000 {
		st.heroIndex = (st.heroIndex + 1) % len(st.model.Featured)
		st.heroSince = now
		mode, it = pkgHeroFor(st.model)
	}
	slide := int32(0)
	if motion {
		if t := float64(now-st.heroSince) / 260; t < 1 {
			slide = int32(math.Round(36 * (1 - uiEaseOutCubic(t))))
		}
	}

	radius := int32(22)
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassA(12))
	pkgClip(renderer, r, func() {
		homeGlowBlob(renderer, sdl.Rect{X: r.X - r.H, Y: r.Y - r.H, W: r.H * 4, H: r.H * 3}, it.Tile, 120)
		homeGlowBlob(renderer, sdl.Rect{X: r.X + r.W - r.H*2, Y: r.Y - r.H/2, W: r.H * 3, H: r.H * 2}, glassAccent(), 40)
	})
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassA(26))

	pad := int32(18)
	tileSide := r.H - 2*pad
	tile := sdl.Rect{X: r.X + pad + slide, Y: r.Y + pad, W: tileSide, H: tileSide}
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 18, it.Tile)
	pkgTileBands(renderer, tile, tile, 18, 0)
	pkgDrawArt(renderer, config, it, tile, ft.monoBig)
	strokeRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 18, 1, sdl.Color{R: 255, G: 255, B: 255, A: 30})

	// Buttons on the right.
	primary, secondary, primaryOff := pkgHeroButtons(mode, it, live)
	btnW := int32(176)
	btnH := int32(40)
	bx := r.X + r.W - pad - btnW
	by := r.Y + (r.H-2*btnH-12)/2
	pr := sdl.Rect{X: bx, Y: by, W: btnW, H: btnH}
	sr := sdl.Rect{X: bx, Y: by + btnH + 12, W: btnW, H: btnH}
	pkgDrawActionButton(renderer, config, ft, pr, primary, pkgBtnPrimary, primaryOff, focused && st.heroBtn == 0, -1)
	pkgDrawActionButton(renderer, config, ft, sr, secondary, pkgBtnGlass, false, focused && st.heroBtn == 1, -1)
	pkgAddHit(pkgHit{Kind: pkgHitHeroPrimary, Rect: pr, Key: it.Key})
	pkgAddHit(pkgHit{Kind: pkgHitHeroDetails, Rect: sr, Key: it.Key})

	// Text between the tile and the buttons.
	tx := tile.X + tile.W + 24
	tw := bx - 24 - tx
	if tw < 80 {
		return
	}
	overline := "FEATURED"
	title := it.Pkg.Name
	meta := pkgJoinDot(it.CategoryTitle, pkgVersionLabel(it.Pkg.Version), it.Pkg.Author, it.DateText)
	switch {
	case mode == pkgHeroUpdates:
		overline = "UPDATES"
		title = st.model.UpdatesTitle
		meta = st.model.UpdateNames
	case it.HasDate && time.Since(it.Date) < 60*24*time.Hour:
		overline = "NEW"
	}
	y := r.Y + pad
	renderText(renderer, config, ft.overline, overline, glassAccent(), tx, y)
	y += pkgFontH(ft.overline) + 2
	renderText(renderer, config, ft.hero, ellipsize(ft.hero, title, tw), ColorTextPrimary(), tx, y)
	y += pkgFontH(ft.hero)
	if meta != "" {
		renderText(renderer, config, ft.body, ellipsize(ft.body, meta, tw), ColorTextSecondary(), tx, y)
		y += pkgFontH(ft.body) + 6
	}
	if mode == pkgHeroFeatured {
		for _, line := range pkgHeroLines(ft.body, it, tw) {
			if y+pkgFontH(ft.body) > r.Y+r.H-pad {
				break
			}
			renderText(renderer, config, ft.body, line, ColorTextTertiary(), tx, y)
			y += pkgFontH(ft.body) + 1
		}
		// Dots: which featured package this is, and a click target for each.
		n := len(st.model.Featured)
		if n > 1 {
			dx := tx
			dy := r.Y + r.H - 14
			cur := st.heroIndex % n
			for i := 0; i < n; i++ {
				w := int32(8)
				color := glassA(60)
				if i == cur {
					w = 20
					color = glassAccent()
				}
				fillRoundedRect(renderer, dx, dy-3, w, 6, 3, color)
				pkgAddHit(pkgHit{Kind: pkgHitHeroDot, Rect: sdl.Rect{X: dx - 3, Y: dy - 9, W: w + 6, H: 18}, Index: i})
				dx += w + 6
			}
		}
	}
}

// Sheet and hero button styles.
const (
	pkgBtnPrimary = iota
	pkgBtnGlass
	pkgBtnDanger
)

// pkgDrawActionButton paints one button: the accent pill, a glass pill or a
// danger pill, dimmed when it cannot run, ringed when focused, with an
// optional progress fill.
func pkgDrawActionButton(renderer *sdl.Renderer, config *Config, ft *pkgType, r sdl.Rect, label string, kind int, disabled, focused bool, frac float64) {
	radius := r.H / 2
	if focused && !disabled {
		drawGlassFocusGlow(renderer, r)
	}
	ink := ColorTextPrimary()
	switch {
	case disabled:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassA(10))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassA(18))
		ink = WithAlpha(ColorTextTertiary(), 170)
	case kind == pkgBtnPrimary:
		fill := glassAccent()
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, fill)
		ink = contrastText(fill)
	case kind == pkgBtnDanger:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(ColorDanger, 46))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, WithAlpha(ColorDanger, 160))
		ink = DangerInk()
	default:
		st := glassState{Hovered: pointInRect(mouseX, mouseY, r), NoLift: true}
		drawGlassSurface(renderer, r, radius, st)
	}
	if frac >= 0 {
		UIProgressBar(renderer, r, frac, sdl.Color{}, WithAlpha(glassAccent(), 110))
	}
	if focused && !disabled {
		drawGlassFocusRing(renderer, glassLifted(r), radius+glassFocusLift)
	}
	text := ellipsize(ft.name, label, r.W-20)
	tw, th := measureText(ft.name, text)
	renderText(renderer, config, ft.name, text, ink, r.X+(r.W-tw)/2, r.Y+(r.H-th)/2)
}

// ── Grid and list views ──────────────────────────────────────────────────

// pkgEnsureFiltered rebuilds the grid/list content when the chip, the sort or
// the model changed, keeping the focus on the same package.
func pkgEnsureFiltered() {
	st := &pkgUI
	if st.filterValid && st.filterChip == st.chip && st.filterSort == st.sortMode && st.filterGen == st.modelGen {
		return
	}
	if st.model != nil {
		valid := false
		for _, c := range st.model.Chips {
			if c.ID == st.chip {
				valid = true
				break
			}
		}
		if !valid {
			st.chip = "all"
		}
	}
	st.filterValid = true
	st.filterChip, st.filterSort, st.filterGen = st.chip, st.sortMode, st.modelGen
	st.filtered = pkgFilterSort(st.model, st.chip, st.sortMode)
	st.gridIndex = pkgIndexOfKey(st.filtered, st.gridKey, st.gridIndex)
	st.listIndex = pkgIndexOfKey(st.filtered, st.listKey, st.listIndex)
	st.reveal = true
}

// pkgIndexOfKey finds a key in a list, or keeps a clamped fallback index.
func pkgIndexOfKey(items []*pkgItem, key string, fallback int) int {
	if key != "" {
		for i, it := range items {
			if it.Key == key {
				return i
			}
		}
	}
	return clampIndex(fallback, len(items))
}

// pkgSortChipLabels is the sort chip's label for each mode, made once.
var pkgSortChipLabels = [...]string{
	pkgSortName:      "Sort: Name",
	pkgSortUpdated:   "Sort: Updated",
	pkgSortSize:      "Sort: Size",
	pkgSortInstalled: "Sort: Installed first",
}

// pkgChipWidths is the chips row's measured widths, reused every frame.
var pkgChipWidths []int32

// pkgDrawChips paints the filter chips and the sort chip.
func pkgDrawChips(renderer *sdl.Renderer, config *Config, ft *pkgType, bar sdl.Rect) {
	st := &pkgUI
	m := st.model
	if m == nil {
		return
	}
	bodyFocused := pkgBodyFocused()
	h := bar.H
	// Sort chip at the right edge.
	sortLabel := pkgSortChipLabels[0]
	if int(st.sortMode) >= 0 && int(st.sortMode) < len(pkgSortChipLabels) {
		sortLabel = pkgSortChipLabels[st.sortMode]
	}
	sw, sh := measureText(ft.meta, sortLabel)
	g := int32(16)
	sortRect := sdl.Rect{X: bar.X + bar.W - (sw + g + 34), Y: bar.Y, W: sw + g + 34, H: h}
	sortFocused := bodyFocused && st.chipFocus && st.chipCursor == len(m.Chips)
	pkgDrawChip(renderer, sortRect, false, sortFocused)
	DrawHomeIcon(renderer, "sort", sdl.Rect{X: sortRect.X + 12, Y: sortRect.Y + (h-g)/2, W: g, H: g}, false)
	renderText(renderer, config, ft.meta, sortLabel, ColorTextSecondary(), sortRect.X+12+g+8, sortRect.Y+(h-sh)/2)
	pkgAddHit(pkgHit{Kind: pkgHitSort, Rect: sortRect})

	// Filter chips, scrolled so the focused (or selected) chip is in view.
	area := sdl.Rect{X: bar.X, Y: bar.Y, W: sortRect.X - 12 - bar.X, H: h}
	widths := pkgChipWidths[:0]
	total := int32(0)
	for _, c := range m.Chips {
		w, _ := measureText(ft.meta, c.Text)
		widths = append(widths, w+28)
		total += w + 28 + 8
	}
	pkgChipWidths = widths
	want := 0
	for i, c := range m.Chips {
		if c.ID == st.chip {
			want = i
		}
	}
	if st.chipFocus && st.chipCursor < len(m.Chips) {
		want = st.chipCursor
	}
	offset := int32(0)
	if total > area.W {
		x := int32(0)
		for i := 0; i < want; i++ {
			x += widths[i] + 8
		}
		if x+widths[want] > area.W-24 {
			offset = x + widths[want] - area.W + 24
		}
	}
	pkgClip(renderer, area, func() {
		x := area.X - offset
		for i, c := range m.Chips {
			r := sdl.Rect{X: x, Y: bar.Y, W: widths[i], H: h}
			selected := c.ID == st.chip
			focused := bodyFocused && st.chipFocus && st.chipCursor == i
			pkgDrawChip(renderer, r, selected, focused)
			label := c.Text
			tw, th := measureText(ft.meta, label)
			ink := ColorTextSecondary()
			if selected {
				ink = contrastText(glassAccent())
			}
			renderText(renderer, config, ft.meta, label, ink, r.X+(r.W-tw)/2, r.Y+(h-th)/2)
			pkgAddHit(pkgHit{Kind: pkgHitChip, Rect: glassIntersect(r, area), Index: i})
			x += widths[i] + 8
		}
	})
}

// pkgDrawChip paints one chip: accent when selected, ringed when focused.
func pkgDrawChip(renderer *sdl.Renderer, r sdl.Rect, selected, focused bool) {
	radius := r.H / 2
	switch {
	case selected:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassAccent())
	case pointInRect(mouseX, mouseY, r):
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassFillHover())
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassEdgeHover())
	default:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassFill())
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassEdge())
	}
	if focused {
		drawGlassFocusRing(renderer, glassLifted(r), radius+glassFocusLift)
	}
}

// pkgGridGeometry is the grid's column count and cell size for a width.
func pkgGridGeometry(w int32) (cols int, cellW, cellH, pitchY int32) {
	cols = UIGridColumns(w, pkgCardW, pkgGap)
	cellW = UIGridCellWidth(w, cols, pkgGap)
	cellH = cellW * 3 / 4
	pitchY = cellH + pkgGap + 4
	return cols, cellW, cellH, pitchY
}

// pkgDrawGridOrList paints the chips and the grid or the list under them.
func pkgDrawGridOrList(renderer *sdl.Renderer, config *Config, ft *pkgType, body, c sdl.Rect, live pkgLive, dt float64, motion bool, now uint64) {
	st := &pkgUI
	pkgEnsureFiltered()
	bar := sdl.Rect{X: c.X, Y: c.Y, W: c.W, H: pkgChipsH}
	pkgDrawChips(renderer, config, ft, bar)
	area := sdl.Rect{X: c.X, Y: c.Y + pkgChipsH + 10, W: c.W, H: c.H - pkgChipsH - 10}
	if len(st.filtered) == 0 {
		UIEmptyState(renderer, config, area, "@filter", "Nothing here", "No package matches this filter.")
		return
	}
	if st.view == pkgViewGrid {
		pkgDrawGrid(renderer, config, ft, body, area, live, dt, motion, now)
	} else {
		pkgDrawList(renderer, config, ft, body, area, live, dt, motion, now)
	}
}

// pkgDrawGrid paints every package of the filter as a poster card.
func pkgDrawGrid(renderer *sdl.Renderer, config *Config, ft *pkgType, body, area sdl.Rect, live pkgLive, dt float64, motion bool, now uint64) {
	st := &pkgUI
	n := len(st.filtered)
	cols, cellW, cellH, pitchY := pkgGridGeometry(area.W)
	st.gridCols = cols
	st.gridIndex = clampIndex(st.gridIndex, n)
	rowsN := (n + cols - 1) / cols
	contentH := int32(rowsN)*pitchY + 2*pkgLift
	if st.reveal {
		st.reveal = false
		row := int32(st.gridIndex / cols)
		top := row * pitchY
		bottom := top + cellH + 2*pkgLift
		if float64(top) < st.gridScroll.target {
			st.gridScroll.target = float64(top)
		}
		if float64(bottom) > st.gridScroll.target+float64(area.H) {
			st.gridScroll.target = float64(bottom - area.H)
		}
	}
	st.gridScroll.clamp(float64(contentH - area.H))
	st.gridScroll.step(dt, motion)
	scrollY := int32(math.Round(st.gridScroll.cur))
	bodyFocused := pkgBodyFocused() && !st.chipFocus
	pkgTrackFocus(pkgCurrentFocusID(), now)
	clip := sdl.Rect{X: body.X, Y: area.Y, W: body.W, H: area.H}
	pkgClip(renderer, clip, func() {
		var focusedItem *pkgItem
		var focusedRest sdl.Rect
		var focusID pkgFocusKey
		for i, it := range st.filtered {
			row, col := int32(i/cols), int32(i%cols)
			rest := sdl.Rect{X: area.X + col*(cellW+pkgGap), Y: area.Y + pkgLift + row*pitchY - scrollY, W: cellW, H: cellH}
			if rest.Y+rest.H < area.Y-pkgLift || rest.Y > area.Y+area.H+pkgLift {
				continue
			}
			id := pkgFocusKey{kind: 'g', key: it.Key}
			if bodyFocused && i == st.gridIndex {
				focusedItem, focusedRest, focusID = it, rest, id
			} else {
				pkgDrawCard(renderer, config, ft, it, rest, false, pkgLiftFor(id, false, now, motion), live, now)
			}
			pkgAddHit(pkgHit{Kind: pkgHitCard, Rect: glassIntersect(rest, area), Index: i, Key: it.Key})
		}
		if focusedItem != nil {
			pkgDrawCard(renderer, config, ft, focusedItem, focusedRest, true, pkgLiftFor(focusID, true, now, motion), live, now)
		}
	})
	pkgDrawScrollbar(renderer, sdl.Rect{X: body.X + body.W - 4, Y: area.Y + 4, W: 3, H: area.H - 8}, float64(scrollY), float64(contentH), float64(area.H), false)
}

// pkgDrawMiniTile is the list row's small square tile.
func pkgDrawMiniTile(renderer *sdl.Renderer, config *Config, ft *pkgType, it *pkgItem, r sdl.Rect) {
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, homeIconRadius, it.Tile)
	pkgTileBands(renderer, r, r, homeIconRadius, 0)
	pkgDrawArt(renderer, config, it, r, ft.monoList)
}

// pkgDrawList paints one row per package.
func pkgDrawList(renderer *sdl.Renderer, config *Config, ft *pkgType, body, area sdl.Rect, live pkgLive, dt float64, motion bool, now uint64) {
	st := &pkgUI
	n := len(st.filtered)
	st.listIndex = clampIndex(st.listIndex, n)
	pitch := pkgListRowH + pkgListGap
	contentH := int32(n)*pitch - pkgListGap + 8
	if st.reveal {
		st.reveal = false
		top := int32(st.listIndex) * pitch
		bottom := top + pkgListRowH + 8
		if float64(top) < st.listScroll.target {
			st.listScroll.target = float64(top)
		}
		if float64(bottom) > st.listScroll.target+float64(area.H) {
			st.listScroll.target = float64(bottom - area.H)
		}
	}
	st.listScroll.clamp(float64(contentH - area.H))
	st.listScroll.step(dt, motion)
	scrollY := int32(math.Round(st.listScroll.cur))
	bodyFocused := pkgBodyFocused() && !st.chipFocus
	pkgTrackFocus(pkgCurrentFocusID(), now)
	wide := area.W >= 760
	pkgClip(renderer, sdl.Rect{X: body.X, Y: area.Y, W: body.W, H: area.H}, func() {
		for i, it := range st.filtered {
			r := sdl.Rect{X: area.X, Y: area.Y + 4 + int32(i)*pitch - scrollY, W: area.W - 10, H: pkgListRowH}
			if r.Y+r.H < area.Y || r.Y > area.Y+area.H {
				continue
			}
			focused := bodyFocused && i == st.listIndex
			pkgDrawListRow(renderer, config, ft, it, r, focused, wide, live, now)
			pkgAddHit(pkgHit{Kind: pkgHitCard, Rect: glassIntersect(r, area), Index: i, Key: it.Key})
		}
	})
	pkgDrawScrollbar(renderer, sdl.Rect{X: body.X + body.W - 4, Y: area.Y + 4, W: 3, H: area.H - 8}, float64(scrollY), float64(contentH), float64(area.H), false)
}

// pkgDrawListRow paints one list row: tile, name and summary, then version,
// size and status in columns at the right.
func pkgDrawListRow(renderer *sdl.Renderer, config *Config, ft *pkgType, it *pkgItem, r sdl.Rect, focused, wide bool, live pkgLive, now uint64) {
	radius := glassRowRadius
	if focused {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(glassAccent(), 30))
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, glassFillFocus())
	} else {
		drawGlassSurface(renderer, r, radius, glassState{Hovered: pointInRect(mouseX, mouseY, r), NoLift: true})
	}
	tileSide := r.H - 16
	tile := sdl.Rect{X: r.X + 10, Y: r.Y + 8, W: tileSide, H: tileSide}
	pkgDrawMiniTile(renderer, config, ft, it, tile)

	// Right-hand columns, measured from the right edge.
	status, frac := pkgStatusOf(it, live)
	right := r.X + r.W - 14
	badge := pkgDrawBadge(renderer, config, ft.meta, status, frac, right, r.Y+(r.H-pkgFontH(ft.meta)-6)/2, false, !pkgUI.online)
	colRight := badge.X - 16
	if badge.W < 96 {
		colRight = right - 96 - 16
	}
	if wide {
		sizeText := it.SizeText
		if sizeText == "" {
			sizeText = "—"
		}
		sw, sh := measureText(ft.meta, sizeText)
		renderText(renderer, config, ft.meta, sizeText, ColorTextTertiary(), colRight-sw, r.Y+(r.H-sh)/2)
		colRight -= 90
	}
	version := pkgVersionLabel(it.Pkg.Version)
	if version == "" {
		version = "—"
	}
	version = ellipsize(ft.meta, version, 84)
	vw, vh := measureText(ft.meta, version)
	renderText(renderer, config, ft.meta, version, ColorTextSecondary(), colRight-vw, r.Y+(r.H-vh)/2)
	colRight -= 100

	// Name and summary.
	tx := tile.X + tile.W + 14
	tw := colRight - tx
	if tw > 40 {
		nameH := pkgFontH(ft.name)
		metaH := pkgFontH(ft.meta)
		ty := r.Y + (r.H-nameH-metaH-2)/2
		renderText(renderer, config, ft.name, ellipsize(ft.name, it.Pkg.Name, tw), ColorTextPrimary(), tx, ty)
		summary := it.Summary
		if summary == "" {
			summary = pkgJoinDot(it.CategoryTitle, it.Pkg.Author)
		}
		renderText(renderer, config, ft.meta, ellipsize(ft.meta, summary, tw), ColorTextTertiary(), tx, ty+nameH+2)
	}
	if status == pkgStatusInstalling || status == pkgStatusRemoving {
		pkgDrawProgress(renderer, sdl.Rect{X: tile.X + tile.W + 14, Y: r.Y + r.H - 6, W: r.W - tile.W - 38, H: 3}, frac, now)
	}
	if focused {
		drawGlassFocusRing(renderer, r, radius)
	}
}

// ── Details sheet ────────────────────────────────────────────────────────

// pkgSheetBtn is one button of the details sheet.
type pkgSheetBtn struct {
	ID       string
	Label    string
	Kind     int
	Disabled bool
	Frac     float64
}

// pkgSheetButtonsBuf is reused every frame the sheet is open.
var pkgSheetButtonsBuf []pkgSheetBtn

// pkgSheetButtons is the sheet's buttons for an item in its current state.
func pkgSheetButtons(it *pkgItem, live pkgLive) []pkgSheetBtn {
	st := &pkgUI
	out := pkgSheetButtonsBuf[:0]
	offline := !st.online
	status, frac := pkgStatusOf(it, live)
	armed := st.sheet.armed == "remove" && sdl.GetTicks64()-st.sheet.armedAt < 4000
	removeLabel := "Remove"
	if armed {
		removeLabel = "Press again to remove"
	}
	_, _, hasSource := pkgSourceURL(it)
	switch {
	case it.Kind == pkgKindApp:
		switch status {
		case pkgStatusInstalling:
			out = append(out, pkgSheetBtn{ID: "busy", Label: "Updating " + pkgStatusText(status, frac), Kind: pkgBtnPrimary, Disabled: true, Frac: frac})
		default:
			out = append(out, pkgSheetBtn{ID: "update", Label: "Update JukaHub", Kind: pkgBtnPrimary, Disabled: offline, Frac: -1})
		}
	case status == pkgStatusInstalling:
		out = append(out, pkgSheetBtn{ID: "cancel", Label: "Cancel install", Kind: pkgBtnGlass, Frac: frac})
	case status == pkgStatusQueued:
		out = append(out, pkgSheetBtn{ID: "dequeue", Label: "Queued — cancel", Kind: pkgBtnGlass, Frac: -1})
	case status == pkgStatusRemoving:
		out = append(out, pkgSheetBtn{ID: "busy", Label: "Removing…", Kind: pkgBtnGlass, Disabled: true, Frac: -1})
	case it.Kind == pkgKindLocal:
		out = append(out, pkgSheetBtn{ID: "open", Label: "Open folder", Kind: pkgBtnPrimary, Frac: -1})
		out = append(out, pkgSheetBtn{ID: "remove", Label: removeLabel, Kind: pkgBtnDanger, Frac: -1})
	case status == pkgStatusUpdate:
		out = append(out, pkgSheetBtn{ID: "update", Label: "Update to " + pkgVersionLabel(it.Pkg.Version), Kind: pkgBtnPrimary, Disabled: offline, Frac: -1})
		out = append(out, pkgSheetBtn{ID: "open", Label: "Open folder", Kind: pkgBtnGlass, Frac: -1})
		out = append(out, pkgSheetBtn{ID: "remove", Label: removeLabel, Kind: pkgBtnDanger, Frac: -1})
	case status == pkgStatusInstalled:
		out = append(out, pkgSheetBtn{ID: "open", Label: "Open folder", Kind: pkgBtnPrimary, Frac: -1})
		out = append(out, pkgSheetBtn{ID: "reinstall", Label: "Reinstall", Kind: pkgBtnGlass, Disabled: offline, Frac: -1})
		out = append(out, pkgSheetBtn{ID: "remove", Label: removeLabel, Kind: pkgBtnDanger, Frac: -1})
	default:
		label := "Get"
		if it.Pkg.Download == "" {
			out = append(out, pkgSheetBtn{ID: "get", Label: "No download", Kind: pkgBtnPrimary, Disabled: true, Frac: -1})
			break
		}
		out = append(out, pkgSheetBtn{ID: "get", Label: label, Kind: pkgBtnPrimary, Disabled: offline, Frac: -1})
	}
	if hasSource {
		out = append(out, pkgSheetBtn{ID: "source", Label: "Source page", Kind: pkgBtnGlass, Disabled: offline, Frac: -1})
	}
	pkgSheetButtonsBuf = out
	return out
}

// pkgSheetNeedsNet reports whether a disabled button is waiting on Wi-Fi.
func pkgSheetNeedsNet(btns []pkgSheetBtn) bool {
	if pkgUI.online {
		return false
	}
	for _, b := range btns {
		if b.Disabled && b.ID != "busy" {
			return true
		}
	}
	return false
}

// pkgSheetText builds the sheet's text column for an item at a width.
func pkgSheetText(it *pkgItem, ft *pkgType, width int32) []pkgSheetLine {
	lines := make([]pkgSheetLine, 0, 48)
	add := func(kind int, text string) {
		lines = append(lines, pkgSheetLine{Text: text, Kind: kind})
	}
	para := func(kind int, font *ttf.Font, text string) {
		for _, l := range wrapPackageText(text, font, width) {
			add(kind, l)
		}
	}
	section := func(title string) {
		if len(lines) > 0 {
			add(pkgLineGap, "")
		}
		add(pkgLineHeading, strings.ToUpper(title))
	}
	p := it.Pkg
	section("About")
	if d := strings.TrimSpace(p.Description); d != "" {
		para(pkgLineBody, ft.body, d)
	} else {
		add(pkgLineMeta, "The registry has no description for this package.")
	}
	if it.Kind == pkgKindApp {
		section("What's new")
		add(pkgLineBody, "Release "+p.Version+".")
		add(pkgLineMeta, "Installs over this copy of JukaHub; it restarts when the update is in place.")
		return lines
	}
	section("What's new")
	if cl := strings.TrimSpace(it.Extra.Changelog); cl != "" {
		para(pkgLineBody, ft.body, cl)
	} else {
		release := pkgJoinDot("Version "+strings.TrimSpace(p.Version), it.DateText)
		if strings.TrimSpace(p.Version) == "" {
			release = it.DateText
		}
		if release == "" {
			release = "No release notes."
		}
		add(pkgLineBody, release)
	}
	if it.HasUpdate {
		add(pkgLineMeta, "You have "+pkgVersionLabel(it.InstalledVersion)+" — "+pkgVersionLabel(p.Version)+" is available.")
	}
	section("Compatibility")
	if it.Devices != "" {
		para(pkgLineBody, ft.body, "Made for "+it.Devices+".")
	}
	switch it.DeviceFit {
	case 1:
		add(pkgLineMeta, "✓ Works on this device.")
	case -1:
		para(pkgLineMeta, ft.body, "Not listed for this device ("+pkgDeviceName()+") — it may not work here.")
	default:
		add(pkgLineMeta, "The registry does not say which devices it supports.")
	}
	section("Requirements")
	deps := make([]string, 0, len(p.Dependencies))
	for _, d := range p.Dependencies {
		if d = strings.TrimSpace(d); d != "" {
			deps = append(deps, d)
		}
	}
	if len(deps) > 0 {
		para(pkgLineBody, ft.body, strings.Join(deps, ", "))
	} else {
		add(pkgLineBody, "Nothing else is needed.")
	}
	section("Details")
	if v := strings.TrimSpace(p.Version); v != "" {
		add(pkgLineMeta, "Version: "+v)
	}
	if it.DateText != "" {
		add(pkgLineMeta, "Released: "+it.DateText)
	}
	if it.SizeText != "" {
		add(pkgLineMeta, "Size: "+it.SizeText)
	}
	if a := strings.TrimSpace(p.Author); a != "" {
		add(pkgLineMeta, "Author: "+a)
	}
	if mf := strings.TrimSpace(p.MainFilename); mf != "" {
		add(pkgLineMeta, "Entry file: "+mf)
	}
	if label, _, ok := pkgSourceURL(it); ok {
		para(pkgLineMeta, ft.body, "Source: "+label)
	}
	if it.Installed {
		if it.InstalledVersion != "" {
			add(pkgLineMeta, "Installed version: "+it.InstalledVersion)
		} else {
			add(pkgLineMeta, "Installed version: not recorded (installed before version tracking)")
		}
		add(pkgLineMeta, "Folder: packages/"+it.Key)
	}
	return lines
}

// pkgDeviceName names the device in hand for the compatibility note.
func pkgDeviceName() string {
	d := DetectDevice()
	if d == DeviceDevBuild {
		return "desktop build"
	}
	name := deviceDisplayName(d)
	if i := strings.Index(name, " ("); i > 0 {
		name = name[:i]
	}
	return name
}

// pkgSheetLineFont is the font a sheet line kind is drawn in.
func pkgSheetLineFont(ft *pkgType, kind int) *ttf.Font {
	switch kind {
	case pkgLineHeading:
		return ft.overline
	case pkgLineMeta:
		return ft.meta
	}
	return ft.body
}

// pkgSheetLineH is the pitch of a sheet line kind.
func pkgSheetLineH(ft *pkgType, kind int) int32 {
	switch kind {
	case pkgLineHeading:
		return pkgFontH(ft.overline) + 8
	case pkgLineGap:
		return 14
	case pkgLineMeta:
		return pkgFontH(ft.meta) + 5
	}
	return pkgFontH(ft.body) + 4
}

// pkgDrawSheet paints the details sheet over the body.
func pkgDrawSheet(renderer *sdl.Renderer, config *Config, ft *pkgType, body sdl.Rect, live pkgLive, dt float64, motion bool, now uint64) {
	st := &pkgUI
	it := st.model.ByKey[st.sheet.key]
	if it == nil {
		st.sheet = pkgSheet{}
		return
	}
	p := 1.0
	if motion {
		p = uiEaseOutCubic(float64(now-st.sheet.openedAt) / 200)
	}
	setDrawColor(renderer, glassScrimColor(p))
	_ = renderer.FillRect(&body)

	s := sdl.Rect{X: body.X + 8, Y: body.Y + 4 + int32(math.Round((1-p)*40)), W: body.W - 16, H: body.H - 8}
	drawGlassSheet(renderer, s, glassSheetFill())
	drawOverlayCardAccent(renderer, s.X, s.Y, s.W)
	pkgAddHit(pkgHit{Kind: pkgHitSheet, Rect: s})

	// Close button.
	cr := sdl.Rect{X: s.X + s.W - 52, Y: s.Y + 16, W: 36, H: 36}
	drawGlassSurface(renderer, cr, cr.H/2, glassState{Hovered: pointInRect(mouseX, mouseY, cr), NoLift: true})
	g := int32(16)
	DrawHomeIcon(renderer, "close", sdl.Rect{X: cr.X + (cr.W-g)/2, Y: cr.Y + (cr.H-g)/2, W: g, H: g}, false)
	pkgAddHit(pkgHit{Kind: pkgHitSheetClose, Rect: cr})

	// Left column: the tile and the buttons.
	colW := int32(240)
	if s.W < 900 {
		colW = 200
	}
	colX := s.X + 28
	tile := sdl.Rect{X: colX, Y: s.Y + 28, W: colW, H: colW * 3 / 4}
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 18, it.Tile)
	pkgTileBands(renderer, tile, tile, 18, 0)
	pkgDrawArt(renderer, config, it, tile, ft.monoBig)
	strokeRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 18, 1, sdl.Color{R: 255, G: 255, B: 255, A: 30})
	status, frac := pkgStatusOf(it, live)
	pkgDrawBadge(renderer, config, ft.meta, status, frac, tile.X+tile.W-10, tile.Y+10, true, !st.online)

	btns := pkgSheetButtons(it, live)
	st.sheet.btn = pkgSheetClampButton(btns, st.sheet.btn)
	by := tile.Y + tile.H + 18
	btnH := int32(42)
	for i, b := range btns {
		r := sdl.Rect{X: colX, Y: by, W: colW, H: btnH}
		if r.Y+r.H > s.Y+s.H-16 {
			break
		}
		focused := pkgBodyFocused() && !st.sheet.textFocus && i == st.sheet.btn
		kind := b.Kind
		if b.ID == "remove" && st.sheet.armed == "remove" && now-st.sheet.armedAt < 4000 {
			kind = pkgBtnPrimary
		}
		if kind == pkgBtnPrimary && b.ID == "remove" {
			// Armed: a solid danger pill, so the second press is unmistakable.
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, ColorDanger)
			text := ellipsize(ft.name, b.Label, r.W-20)
			tw, th := measureText(ft.name, text)
			renderText(renderer, config, ft.name, text, contrastText(ColorDanger), r.X+(r.W-tw)/2, r.Y+(r.H-th)/2)
			if focused {
				drawGlassFocusRing(renderer, glassLifted(r), r.H/2+glassFocusLift)
			}
		} else {
			pkgDrawActionButton(renderer, config, ft, r, b.Label, kind, b.Disabled, focused, b.Frac)
		}
		pkgAddHit(pkgHit{Kind: pkgHitSheetButton, Rect: r, Index: i})
		by += btnH + 10
	}
	if pkgSheetNeedsNet(btns) && by+pkgFontH(ft.meta) < s.Y+s.H-12 {
		note := ellipsize(ft.meta, "Needs internet — connect Wi-Fi to download.", colW)
		renderText(renderer, config, ft.meta, note, WarningInk(), colX, by+2)
	}

	// Right column: title, byline, chips, then the scrolling text.
	rx := colX + colW + 32
	rw := s.X + s.W - 28 - rx
	if rw < 120 {
		return
	}
	ty := s.Y + 24
	titleW := cr.X - 16 - rx
	renderText(renderer, config, ft.hero, ellipsize(ft.hero, it.Pkg.Name, titleW), ColorTextPrimary(), rx, ty)
	ty += pkgFontH(ft.hero) + 2
	byline := pkgJoinDot("by "+strings.TrimSpace(it.Pkg.Author), pkgVersionLabel(it.Pkg.Version), it.DateText, it.SizeText)
	if strings.TrimSpace(it.Pkg.Author) == "" {
		byline = pkgJoinDot(pkgVersionLabel(it.Pkg.Version), it.DateText, it.SizeText)
	}
	renderText(renderer, config, ft.body, ellipsize(ft.body, byline, titleW), ColorTextSecondary(), rx, ty)
	ty += pkgFontH(ft.body) + 12

	// Chips: category and device fit.
	cx := rx
	chipH := pkgFontH(ft.meta) + 10
	drawChip := func(label string, tone sdl.Color, glyph string) {
		lw, lh := measureText(ft.meta, label)
		gw := int32(0)
		if glyph != "" {
			gw = 18
		}
		w := lw + 22 + gw
		if cx+w > rx+rw {
			return
		}
		r := sdl.Rect{X: cx, Y: ty, W: w, H: chipH}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, chipH/2, glassA(16))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, chipH/2, 1, glassA(30))
		textX := r.X + 11
		if glyph != "" {
			DrawHomeIcon(renderer, glyph, sdl.Rect{X: r.X + 9, Y: r.Y + (chipH-14)/2, W: 14, H: 14}, false)
			textX += gw
		}
		renderText(renderer, config, ft.meta, label, tone, textX, r.Y+(chipH-lh)/2)
		cx += w + 8
	}
	drawChip(it.CategoryTitle, ColorTextSecondary(), it.CategoryIcon)
	switch it.DeviceFit {
	case 1:
		drawChip("Works on this device", SuccessInk(), "check")
	case -1:
		drawChip("Not listed for this device", WarningInk(), "warning")
	}
	if it.Kind == pkgKindLocal {
		drawChip("Not in the registry", ColorTextTertiary(), "info")
	}
	ty += chipH + 16

	// Text column.
	text := sdl.Rect{X: rx, Y: ty, W: rw, H: s.Y + s.H - 20 - ty}
	if text.H < 40 {
		return
	}
	linesAt := pkgWrapKey{key: it.Key, width: text.W, gen: st.modelGen, font: ft.body}
	if st.sheet.linesAt != linesAt || st.sheet.lines == nil {
		st.sheet.linesAt = linesAt
		st.sheet.lines = pkgSheetText(it, ft, text.W-14)
		h := int32(0)
		for _, l := range st.sheet.lines {
			h += pkgSheetLineH(ft, l.Kind)
		}
		st.sheet.contentH = h
	}
	st.sheet.viewH = text.H
	st.sheet.scroll.clamp(float64(st.sheet.contentH - text.H))
	st.sheet.scroll.step(dt, motion)
	off := int32(math.Round(st.sheet.scroll.cur))
	textFocused := pkgBodyFocused() && st.sheet.textFocus
	if textFocused {
		drawGlassFocusRing(renderer, sdl.Rect{X: text.X - 10, Y: text.Y - 8, W: text.W + 16, H: text.H + 12}, 14)
	}
	pkgAddHit(pkgHit{Kind: pkgHitSheetText, Rect: text})
	pkgClip(renderer, text, func() {
		y := text.Y - off
		for _, l := range st.sheet.lines {
			lh := pkgSheetLineH(ft, l.Kind)
			if y+lh >= text.Y && y <= text.Y+text.H && l.Text != "" {
				font := pkgSheetLineFont(ft, l.Kind)
				color := ColorTextPrimary()
				switch l.Kind {
				case pkgLineHeading:
					color = ColorTextTertiary()
				case pkgLineMeta:
					color = ColorTextSecondary()
				}
				renderText(renderer, config, font, l.Text, color, text.X, y)
			}
			y += lh
		}
	})
	pkgDrawScrollbar(renderer, sdl.Rect{X: text.X + text.W - 3, Y: text.Y, W: 3, H: text.H}, float64(off), float64(st.sheet.contentH), float64(text.H), textFocused)
}

// pkgSheetClampButton keeps the sheet's focus on an enabled button.
func pkgSheetClampButton(btns []pkgSheetBtn, i int) int {
	if len(btns) == 0 {
		return 0
	}
	i = clampIndex(i, len(btns))
	if !btns[i].Disabled {
		return i
	}
	for j := range btns {
		if !btns[j].Disabled {
			return j
		}
	}
	return i
}
