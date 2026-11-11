package main

// bootarrive.go — how the first screen arrives at the end of the boot.
//
// The old splash cross-faded into Home as one picture. An OS does not hand its
// screen over like a photograph: the cards come in. When the hold ends, the
// first screen is drawn once more, offscreen — the warm-up (bootwork.go
// warmSceneCaches) has filled the caches, so this costs a frame, and the
// warm-up's own render is seconds old by then, with Home's backdrop drifted
// on. The arrival cuts that render into its cards and lets them rise into
// place one after another in reading order, over Home's own backdrop, while
// everything between the cards — the header, the greeting, the footer — fades
// in as one piece and the boot's wordmark flies up into the header, where the
// same word lives on Home. The arrival's last frame is that render itself, so
// the first live frame continues it without a seam.
//
// The cut is only trusted where the layout's card rects are what was drawn:
// Home in its own, dashboard or grid arrangement. Any other first screen (a
// config that starts elsewhere, another arrangement) arrives as one picture,
// rising a few pixels as it fades in. Under reduced motion nothing moves: the
// cards fade in turn, and the wordmark fades out where it is.
//
// The cards move by whole canvas pixels and are never scaled. A canvas drawn
// at a logical size (the Brick's 960x720 on its 1024x768 panel, a resized
// window) turns a whole canvas pixel into a fraction of a panel pixel, so the
// arrival's targets are linear-filtered: a rising card glides instead of
// shimmering as its rows snap from one panel row to the next.

import (
	"math"
	"sort"

	"github.com/veandco/go-sdl2/sdl"
)

// bootArriveMargin grows each card's cut by this much (design pixels) so its
// edge light travels with it. Home's gutters are 12–16 px, so two grown cards
// still never overlap.
const bootArriveMargin = 6

// bootArriveFocusMargin is the focused card's growth: its focus ring sits up
// to eight pixels outside it (a pixel of lift, a four-pixel gap, a three-pixel
// stroke), and a ring left behind in the fading rest of the screen while its
// card rises would draw the ring twice. The neighbours give way where the two
// would meet (bootArriveCardRects).
const bootArriveFocusMargin = 10

// bootArriveBeats times one arrival, in milliseconds from its start: the
// progress line going, the logo zooming through, Home's backdrop coming up
// over the boot's, the wordmark's flight to the header and its hand-over to
// the header's own, the rest of the screen fading in, the cards' cascade
// (first card, the gap between two, each card's rise and how far below its
// place it starts, in design pixels), and the single picture's fade and rise.
// wholeOnly arrivals never cut the screen into cards.
type bootArriveBeats struct {
	progressOut    float64
	tileOut        float64
	tileZoom       float64
	baseFrom       float64
	baseTo         float64
	flightMS       float64
	flightFadeFrom float64
	flightFadeTo   float64
	restFrom       float64
	restTo         float64
	cardsFrom      float64
	cardStagger    float64
	cardMS         float64
	cardDY         float64
	wholeFrom      float64
	wholeTo        float64
	wholeDY        float64
	wholeOnly      bool
}

// bootArrivePower is the full boot's arrival: about a second for Home's
// thirteen cards.
var bootArrivePower = bootArriveBeats{
	progressOut:    160,
	tileOut:        380,
	tileZoom:       0.16,
	baseFrom:       80,
	baseTo:         520,
	flightMS:       560,
	flightFadeFrom: 460,
	flightFadeTo:   600,
	restFrom:       300,
	restTo:         620,
	cardsFrom:      220,
	cardStagger:    34,
	cardMS:         420,
	cardDY:         26,
	wholeFrom:      120,
	wholeTo:        560,
	wholeDY:        18,
}

// bootArriveQuick is a launch's (and a power-on skipped with a press): under
// four tenths of a second, starting before the launch's budget runs out, so a
// launch costs about what the old cross-fade did.
var bootArriveQuick = bootArriveBeats{
	progressOut:    80,
	tileOut:        200,
	tileZoom:       0.10,
	baseFrom:       0,
	baseTo:         220,
	flightMS:       300,
	flightFadeFrom: 240,
	flightFadeTo:   330,
	restFrom:       120,
	restTo:         300,
	cardsFrom:      40,
	cardStagger:    10,
	cardMS:         220,
	cardDY:         18,
	wholeFrom:      30,
	wholeTo:        260,
	wholeDY:        12,
}

// bootArriveSkip answers a press during a launch's hello: the person wants in,
// so the first screen fades up as one picture in about a tenth of a second.
// The card fields only keep the struct sane; a wholeOnly arrival never uses
// them.
var bootArriveSkip = bootArriveBeats{
	progressOut:    80,
	tileOut:        120,
	baseTo:         120,
	flightMS:       120,
	flightFadeFrom: 0,
	flightFadeTo:   120,
	restTo:         120,
	cardMS:         120,
	wholeFrom:      0,
	wholeTo:        120,
	wholeOnly:      true,
}

// duration is how long an arrival lasts: until its last part has settled. A
// whole-picture arrival is the picture, the logo and the progress line going;
// a cut one adds Home's backdrop, the rest of the screen, the cards and, when
// the wordmark flies, its hand-over.
func (b *bootArriveBeats) duration(cards int, whole, flight bool) float64 {
	end := math.Max(b.tileOut, b.progressOut)
	if whole {
		return math.Max(end, b.wholeTo)
	}
	end = math.Max(end, math.Max(b.baseTo, b.restTo))
	if flight {
		end = math.Max(end, b.flightFadeTo)
	}
	if cards > 0 {
		end = math.Max(end, b.cardsFrom+b.cardStagger*float64(cards-1)+b.cardMS)
	}
	return end
}

// card is card i's presence at ms, and how far below its place it still is
// (design pixels; zero without motion).
func (b *bootArriveBeats) card(i int, ms float64, motion bool) (alpha, dy float64) {
	p := bootClamp01((ms - b.cardsFrom - b.cardStagger*float64(i)) / b.cardMS)
	alpha = bootSmoothstep(0, 0.6, p)
	if motion {
		dy = b.cardDY * (1 - uiEaseOutCubic(p))
	}
	return alpha, dy
}

// whole is the single picture's presence at ms and how far below its place it
// still is (design pixels; zero without motion).
func (b *bootArriveBeats) whole(ms float64, motion bool) (alpha, dy float64) {
	p := bootClamp01((ms - b.wholeFrom) / (b.wholeTo - b.wholeFrom))
	alpha = bootSmoothstep(0, 1, p)
	if motion {
		dy = b.wholeDY * (1 - uiEaseOutCubic(p))
	}
	return alpha, dy
}

// bootArrivePlan is how the first screen comes in: as cards cut out of its
// render over its backdrop, with the rest of the screen fading in round them,
// or as one picture. brand is where the header draws the wordmark the boot's
// own flies to (zero size: no flight).
type bootArrivePlan struct {
	whole bool
	cards []sdl.Rect
	rest  []sdl.Rect
	brand sdl.Rect
}

// bootArriveState is one arrival in progress: its beats, its plan, the picture
// it is cut from (tex: its own fresh render, own, or else the warm-up's, which
// bootHandoff owns and the splash releases) and the backdrop it created.
type bootArriveState struct {
	beats *bootArriveBeats
	plan  bootArrivePlan
	tex   *sdl.Texture
	own   *sdl.Texture
	base  *sdl.Texture
	dur   float64
}

// begin prepares the arrival for the screen as it is now: the first screen
// drawn afresh and, when it is cut into cards, Home's backdrop beside it, in
// the same moment so the two agree.
func (a *bootArriveState) begin(renderer *sdl.Renderer, config *Config, beats *bootArriveBeats, motion bool) {
	a.release()
	if beats == nil {
		beats = &bootArriveQuick
	}
	a.beats = beats
	a.plan = bootArrivePlan{whole: true}
	a.own = bootArriveRenderScene(renderer, config)
	switch {
	case a.own != nil:
		a.tex = a.own
	case bootHandoff.tex != nil && bootHandoff.renderer == renderer:
		// The fresh render was refused: the warm-up's is older, not wrong.
		a.tex = bootHandoff.tex
	}
	if a.tex != nil && !beats.wholeOnly {
		a.plan = bootArrivePlanFor(config, screenWidth, screenHeight)
		if !a.plan.whole {
			// The cards rise over Home's own backdrop; without one to put
			// under them they arrive as one picture instead.
			if a.base = bootArriveBaseTexture(renderer); a.base == nil {
				a.plan = bootArrivePlan{whole: true}
			}
		}
	}
	if !motion {
		a.plan.brand = sdl.Rect{}
	}
	a.dur = a.beats.duration(len(a.plan.cards), a.plan.whole, a.plan.brand.W > 0 && bootLiquid.word != nil)
}

// release destroys what the arrival created. The warm-up's render stays the
// splash's to release (bootHandoffRelease).
func (a *bootArriveState) release() {
	if a.base != nil {
		_ = a.base.Destroy()
	}
	if a.own != nil {
		_ = a.own.Destroy()
	}
	*a = bootArriveState{}
}

// bootArrivePlanFor cuts the first screen into its arrival. Only Home in the
// arrangements whose card rects are the ones drawn is cut; anything else comes
// in whole.
func bootArrivePlanFor(config *Config, w, h int32) bootArrivePlan {
	whole := bootArrivePlan{whole: true}
	if config == nil || homeLayout == nil || homeLayout.Focus == nil || w <= 0 || h <= 0 {
		return whole
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) || config.Scenes[currentSceneIndex].Layout != "home" {
		return whole
	}
	switch homeArrangement(config) {
	case homeArrangementCustom, homeArrangementDashboard, homeArrangementGrid:
	default:
		return whole
	}
	cards := bootArriveCardRects(homeLayout.Items, homeLayout.Order, homeLayout.Focus.IsEnabled, homeLayout.Focus.Current(),
		headerBarHeight(), w, h, scaleX(bootArriveMargin, w), scaleX(bootArriveFocusMargin, w))
	if len(cards) == 0 {
		return whole
	}
	return bootArrivePlan{
		cards: cards,
		rest:  bootArriveRest(sdl.Rect{X: 0, Y: 0, W: w, H: h}, cards),
		brand: bootArriveBrandBox(),
	}
}

// bootArriveIsHeaderChip reports whether a Home item is one the header draws:
// those arrive with the header, not as cards.
func bootArriveIsHeaderChip(id HomeFocusID) bool {
	switch id {
	case HomeFocusClock, HomeFocusWeather, HomeFocusUpdate, HomeFocusPS5, HomeFocusPS4, HomeFocusNowPlaying,
		HomeFocusBell, HomeFocusWiFi, HomeFocusVolume, HomeFocusQR, HomeFocusTrophy:
		return true
	}
	return false
}

// bootArriveCardRects is a Home layout's cards as the arrival cuts them: every
// item the layout draws as a card — not a header chip or anything else that
// starts inside the header band, not one the layout has switched off (the
// hidden power button, a control it does not draw), not empty, on screen —
// grown by margin pixels, clipped to the screen, in reading order. Without an
// enabled function everything but the power button counts. The focused card
// grows by focusMargin instead, so its focus ring rises with it; that growth
// stops short of every other card, and a neighbour's margin gives way where it
// would reach into it, so no two cuts overlap and no card is cut into.
func bootArriveCardRects(items map[HomeFocusID]HomeItem, order []HomeFocusID, enabled func(HomeFocusID) bool,
	focused HomeFocusID, barH, w, h, margin, focusMargin int32) []sdl.Rect {
	screen := sdl.Rect{X: 0, Y: 0, W: w, H: h}
	picked := make([]sdl.Rect, 0, len(order))
	focus := -1
	seen := make(map[HomeFocusID]bool, len(order))
	for _, id := range order {
		if seen[id] || bootArriveIsHeaderChip(id) {
			continue
		}
		seen[id] = true
		if (enabled == nil && id == HomeFocusExit) || (enabled != nil && !enabled(id)) {
			continue
		}
		item, ok := items[id]
		if !ok || item.Rect.W <= 0 || item.Rect.H <= 0 || item.Rect.Y < barH {
			continue
		}
		if r := glassIntersect(bootArriveGrow(item.Rect, margin), screen); r.W <= 0 || r.H <= 0 {
			continue
		}
		if id == focused && focused != "" {
			focus = len(picked)
		}
		picked = append(picked, item.Rect)
	}
	var claim sdl.Rect
	if focus >= 0 {
		claim = bootArriveGrowClear(picked, focus, focusMargin)
	}
	cards := make([]sdl.Rect, 0, len(picked))
	for i, rect := range picked {
		g := bootArriveGrow(rect, margin)
		switch {
		case i == focus:
			g = claim
		case focus >= 0:
			g = bootArriveGiveWay(g, rect, claim, picked[focus])
		}
		if r := glassIntersect(g, screen); r.W > 0 && r.H > 0 {
			cards = append(cards, r)
		}
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Y != cards[j].Y {
			return cards[i].Y < cards[j].Y
		}
		return cards[i].X < cards[j].X
	})
	return cards
}

// bootArriveGrow is r grown by m on every side.
func bootArriveGrow(r sdl.Rect, m int32) sdl.Rect {
	return sdl.Rect{X: r.X - m, Y: r.Y - m, W: r.W + 2*m, H: r.H + 2*m}
}

// bootArriveGrowClear grows card self of cards by up to grow on each side,
// but on each side only as far as the nearest other card facing it — one
// whose span, along that side, meets the grown span — so the growth never
// reaches another card, a diagonal neighbour within reach included.
func bootArriveGrowClear(cards []sdl.Rect, self int, grow int32) sdl.Rect {
	r := cards[self]
	left, top, right, bottom := grow, grow, grow, grow
	for i, o := range cards {
		if i == self {
			continue
		}
		acrossX := o.X < r.X+r.W+grow && r.X-grow < o.X+o.W
		acrossY := o.Y < r.Y+r.H+grow && r.Y-grow < o.Y+o.H
		if acrossX && o.Y+o.H <= r.Y {
			top = min32(top, r.Y-(o.Y+o.H))
		}
		if acrossX && o.Y >= r.Y+r.H {
			bottom = min32(bottom, o.Y-(r.Y+r.H))
		}
		if acrossY && o.X+o.W <= r.X {
			left = min32(left, r.X-(o.X+o.W))
		}
		if acrossY && o.X >= r.X+r.W {
			right = min32(right, o.X-(r.X+r.W))
		}
	}
	return sdl.Rect{X: r.X - left, Y: r.Y - top, W: r.W + left + right, H: r.H + top + bottom}
}

// bootArriveGiveWay trims grown, a neighbour's cut around its card own, off
// the focused card's claim: on the side that faces the focused card, by as
// little as clears it. The claim stops short of own, so the card itself stays
// whole; a neighbour that cannot clear it (a layout whose cards overlap) keeps
// just its own rect.
func bootArriveGiveWay(grown, own, claim, focused sdl.Rect) sdl.Rect {
	if !homeRectsOverlap(grown, claim) {
		return grown
	}
	best, bestArea := own, int32(-1)
	try := func(t sdl.Rect) {
		if t.W <= 0 || t.H <= 0 || homeRectsOverlap(t, claim) {
			return
		}
		if area := t.W * t.H; area > bestArea {
			best, bestArea = t, area
		}
	}
	if own.Y+own.H <= focused.Y {
		t := grown
		t.H = claim.Y - t.Y
		try(t)
	}
	if own.Y >= focused.Y+focused.H {
		t := grown
		t.H = t.Y + t.H - (claim.Y + claim.H)
		t.Y = claim.Y + claim.H
		try(t)
	}
	if own.X+own.W <= focused.X {
		t := grown
		t.W = claim.X - t.X
		try(t)
	}
	if own.X >= focused.X+focused.W {
		t := grown
		t.W = t.X + t.W - (claim.X + claim.W)
		t.X = claim.X + claim.W
		try(t)
	}
	return best
}

// bootArriveRest is screen minus the holes, as disjoint rects: the part of the
// first screen that fades in round the cards. The holes' edges cut the screen
// into a grid; the free cells of each grid row merge into runs, and runs that
// match the row above extend it, so a row without cards is one rect.
func bootArriveRest(screen sdl.Rect, holes []sdl.Rect) []sdl.Rect {
	if screen.W <= 0 || screen.H <= 0 {
		return nil
	}
	xs := []int32{screen.X, screen.X + screen.W}
	ys := []int32{screen.Y, screen.Y + screen.H}
	clipped := make([]sdl.Rect, 0, len(holes))
	for _, hole := range holes {
		c := glassIntersect(hole, screen)
		if c.W <= 0 || c.H <= 0 {
			continue
		}
		clipped = append(clipped, c)
		xs = append(xs, c.X, c.X+c.W)
		ys = append(ys, c.Y, c.Y+c.H)
	}
	xs = bootArriveUniq(xs)
	ys = bootArriveUniq(ys)
	covered := func(x0, y0, x1, y1 int32) bool {
		for _, c := range clipped {
			if c.X <= x0 && x1 <= c.X+c.W && c.Y <= y0 && y1 <= c.Y+c.H {
				return true
			}
		}
		return false
	}
	var out []sdl.Rect
	for j := 0; j+1 < len(ys); j++ {
		y0, y1 := ys[j], ys[j+1]
		run := -1
		for i := 0; i+1 < len(xs); i++ {
			free := !covered(xs[i], y0, xs[i+1], y1)
			if free && run < 0 {
				run = i
			}
			if !free && run >= 0 {
				out = bootArriveAddRun(out, sdl.Rect{X: xs[run], Y: y0, W: xs[i] - xs[run], H: y1 - y0})
				run = -1
			}
		}
		if run >= 0 {
			last := xs[len(xs)-1]
			out = bootArriveAddRun(out, sdl.Rect{X: xs[run], Y: y0, W: last - xs[run], H: y1 - y0})
		}
	}
	return out
}

// bootArriveAddRun appends a free run, extending the rect directly above it
// when that one spans exactly the same columns.
func bootArriveAddRun(out []sdl.Rect, run sdl.Rect) []sdl.Rect {
	for i := len(out) - 1; i >= 0; i-- {
		r := &out[i]
		if r.X == run.X && r.W == run.W && r.Y+r.H == run.Y {
			r.H += run.H
			return out
		}
	}
	return append(out, run)
}

// bootArriveUniq sorts v and drops repeats.
func bootArriveUniq(v []int32) []int32 {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	out := v[:0]
	for i, x := range v {
		if i == 0 || x != v[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// bootArriveBrandBox is where Home's header draws its wordmark (header.go
// drawHeaderBrand, with the config drawHeader uses): the boot's wordmark flies
// there. Zero size when there is no font to measure it with.
func bootArriveBrandBox() sdl.Rect {
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	font := headerBrandFont(cfg)
	if font == nil {
		return sdl.Rect{}
	}
	bw, _ := measureText(font, headerBrandText)
	bh := int32(font.Height())
	if bw <= 0 || bh <= 0 {
		return sdl.Rect{}
	}
	return sdl.Rect{X: StatusBarMargin, Y: (headerBarHeight() - bh) / 2, W: bw, H: bh}
}

// bootArriveTarget is a screen-sized render target with linear filtering (see
// the top of this file), or nil when the driver refuses one. The filter is a
// hint read when the texture is created, so it is set around the creation and
// put back for everyone else.
func bootArriveTarget(renderer *sdl.Renderer) *sdl.Texture {
	if renderer == nil || screenWidth <= 0 || screenHeight <= 0 {
		return nil
	}
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return nil
	}
	return tex
}

// bootArriveDrawInto runs draw with tex as the render target and reports
// whether it finished. Drawing is never a boot dependency: a panic on a
// half-initialised boot costs the cascade, not the launch, and the target is
// released outside the guard (as in warmSceneCaches) so the renderer is never
// left bound to a texture that is about to be destroyed. The clip and blend
// state the drawing leaves behind are put back.
func bootArriveDrawInto(renderer *sdl.Renderer, tex *sdl.Texture, what string, draw func()) bool {
	if err := renderer.SetRenderTarget(tex); err != nil {
		return false
	}
	drawn := func() (ok bool) {
		defer func() {
			if rec := recover(); rec != nil {
				logInfo("startup", "arrival %s skipped: %v", what, rec)
				ok = false
			}
		}()
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.Clear()
		draw()
		return true
	}()
	if err := renderer.SetRenderTarget(nil); err != nil {
		logWarn("startup", "arrival %s could not release its target: %v", what, err)
		drawn = false
	}
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	return drawn
}

// bootArriveRenderScene draws the current scene as it is now into a texture of
// its own: the picture the arrival cuts. Nil when there is no scene to draw or
// the driver refuses the target.
func bootArriveRenderScene(renderer *sdl.Renderer, config *Config) *sdl.Texture {
	if renderer == nil || config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return nil
	}
	tex := bootArriveTarget(renderer)
	if tex == nil {
		return nil
	}
	if !bootArriveDrawInto(renderer, tex, "render", func() { renderScene(renderer, config, config.Scenes[currentSceneIndex]) }) {
		_ = tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// bootArriveBaseTexture renders Home's backdrop alone into a texture: what the
// cards rise over. Nil when the layout or the driver cannot provide it.
func bootArriveBaseTexture(renderer *sdl.Renderer) *sdl.Texture {
	if renderer == nil || homeLayout == nil {
		return nil
	}
	tex := bootArriveTarget(renderer)
	if tex == nil {
		return nil
	}
	if !bootArriveDrawInto(renderer, tex, "backdrop", func() { homeLayout.drawBackdrop(renderer) }) {
		_ = tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// bootArriveAlpha is a presence as an alpha modulation.
func bootArriveAlpha(a float64) uint8 {
	return uint8(math.Round(255 * bootClamp01(a)))
}

// drawArrival paints arrival frame am (milliseconds since the arrival began;
// ms, since the boot began, keeps the backdrop's own motion going).
func (r *bootOSRun) drawArrival(ms, am float64) {
	a := &r.arrive
	b := a.beats
	if b == nil {
		b = &bootArriveQuick
	}
	f := bootOSTimeline(ms, r.powerOn, r.motion, r.continuity)
	// The boot's backdrop is only drawn while something of it still shows:
	// once Home's opaque backdrop (or the whole first screen, in place) covers
	// it, it is several screens of fill nobody sees.
	covered := false
	if a.tex != nil {
		if a.plan.whole {
			alpha, dy := b.whole(am, r.motion)
			covered = alpha >= 1 && dy == 0
		} else {
			covered = a.base != nil && bootSmoothstep(b.baseFrom, b.baseTo, am) >= 1
		}
	}
	if !covered {
		r.drawBackdrop(ms, &f)
	}
	ys := float64(screenHeight) / float64(HomeBaseHeight)
	if tex := a.tex; tex != nil {
		if a.plan.whole {
			alpha, dy := b.whole(am, r.motion)
			bootArriveF = sdl.FRect{X: 0, Y: float32(math.Round(dy * ys)), W: float32(screenWidth), H: float32(screenHeight)}
			_ = tex.SetAlphaMod(bootArriveAlpha(alpha))
			_ = r.renderer.CopyF(tex, nil, &bootArriveF)
		} else {
			if a.base != nil {
				bootArriveR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
				_ = a.base.SetAlphaMod(bootArriveAlpha(bootSmoothstep(b.baseFrom, b.baseTo, am)))
				_ = r.renderer.Copy(a.base, nil, &bootArriveR)
				_ = a.base.SetAlphaMod(255)
			}
			_ = tex.SetAlphaMod(bootArriveAlpha(bootSmoothstep(b.restFrom, b.restTo, am)))
			for i := range a.plan.rest {
				bootArriveR = a.plan.rest[i]
				_ = r.renderer.Copy(tex, &bootArriveR, &bootArriveR)
			}
			for i := range a.plan.cards {
				alpha, dy := b.card(i, am, r.motion)
				if alpha <= 0 {
					continue
				}
				bootArriveR = a.plan.cards[i]
				bootArriveF = sdl.FRect{
					X: float32(bootArriveR.X),
					Y: float32(float64(bootArriveR.Y) + math.Round(dy*ys)),
					W: float32(bootArriveR.W),
					H: float32(bootArriveR.H),
				}
				_ = tex.SetAlphaMod(bootArriveAlpha(alpha))
				_ = r.renderer.CopyF(tex, &bootArriveR, &bootArriveF)
			}
		}
		_ = tex.SetAlphaMod(255)
	}
	// The boot's own layer leaves on top: the logo zooming through, the
	// wordmark flying to the header (or fading where it is), the progress line.
	out := 1 - bootSmoothstep(0, b.tileOut, am)
	zoom := 1.0
	if r.motion {
		zoom = 1 + b.tileZoom*uiEaseOutCubic(am/b.tileOut)
	}
	r.drawLayer(&f, out, zoom)
	if a.plan.brand.W > 0 && bootLiquid.word != nil {
		// The flight draws the sharp word: a press that ends the hold before the
		// word has come into focus must not pop it in sharp, so it carries the
		// focus the hold had reached.
		alpha := (1 - bootSmoothstep(b.flightFadeFrom, b.flightFadeTo, am)) * bootSmoothstep(0.3, 0.95, f.word)
		r.drawFlight(&f, b, am, alpha)
	} else {
		r.drawWord(&f, out)
	}
	r.drawProgress(f.track*(1-bootSmoothstep(0, b.progressOut, am)), f.dawn)
}

// drawFlight paints the wordmark am milliseconds into arrival b: on its way
// (eased) from its place under the logo to the header's brand, at alpha. It
// lands on the header's own word just as the header fades in, and hands over
// to it.
func (r *bootOSRun) drawFlight(f *bootOSFrame, b *bootArriveBeats, am, alpha float64) {
	k := &bootLiquid
	to := r.arrive.plan.brand
	if k.word == nil || b == nil || b.flightMS <= 0 || alpha <= 0 || to.H <= 0 {
		return
	}
	glyphW := float64(k.wordW - 2*k.wordPad)
	glyphH := float64(k.wordH - 2*k.wordPad)
	if glyphW <= 0 || glyphH <= 0 {
		return
	}
	p := bootOSEaseInOut(am / b.flightMS)
	// Where the hold left the glyphs: on the whole pixel bootLiquidDrawWordmark
	// puts them at rest, so the first frame of the flight does not step.
	fromX := math.Floor(r.cx-float64(k.wordW)/2) + float64(k.wordPad)
	fromY := float64(r.layout.WordTop)
	s := 1 + (float64(to.H)/glyphH-1)*p
	x := fromX + (float64(to.X)-fromX)*p
	y := fromY + (float64(to.Y)-fromY)*p
	pad := float64(k.wordPad) * s
	bootArriveF = sdl.FRect{X: float32(x - pad), Y: float32(y - pad), W: float32(float64(k.wordW) * s), H: float32(float64(k.wordH) * s)}
	// Under the word: the boot's dawning backdrop with Home's own coming up
	// over it.
	bg := glassMix(bootOSDawnColour(f.dawn), ColorBackground, bootSmoothstep(b.baseFrom, b.baseTo, am))
	ink := bootOSInkOn(bootLogoInk, ColorTextPrimary(), bg, f.dawn, glassLightTheme())
	_ = k.word.SetColorMod(ink.R, ink.G, ink.B)
	_ = k.word.SetAlphaMod(bootArriveAlpha(alpha))
	_ = r.renderer.CopyF(k.word, nil, &bootArriveF)
	_ = k.word.SetColorMod(255, 255, 255)
	_ = k.word.SetAlphaMod(255)
}
